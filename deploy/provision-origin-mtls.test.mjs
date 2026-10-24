import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const script = readFileSync(new URL('./provision-origin-mtls.sh', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const workflow = readFileSync(new URL('../.github/workflows/provision-origin-mtls.yml', import.meta.url), 'utf8');
const bash = process.platform === 'win32' ? 'C:/cygwin64/bin/bash.exe' : 'bash';
const openssl = process.platform === 'win32' ? 'C:/cygwin64/bin/openssl.exe' : 'openssl';
const posix = (value) => process.platform === 'win32'
  ? execFileSync('C:/cygwin64/bin/cygpath.exe', ['-u', value], { encoding: 'utf8' }).trim() : value;

function fixture() {
  const dir = mkdtempSync(path.join(tmpdir(), 'freedompost-mtls-provision-'));
  execFileSync('go', ['run', fileURLToPath(new URL('./fixtures/origin-cert-fixture.go', import.meta.url)), dir]);
  const stage = path.join(dir, 'etc/freedompost/.origin-mtls-provision');
  mkdirSync(path.join(dir, 'etc/caddy/certs'), { recursive: true });
  mkdirSync(path.join(dir, 'etc/freedompost'), { recursive: true });
  const ca = path.join(dir, 'etc/caddy/certs/origin-pull-client-ca.crt');
  const cert = path.join(dir, 'etc/freedompost/origin-health-client.crt');
  const key = path.join(dir, 'etc/freedompost/origin-health-client.key');
  const encoded = (name) => readFileSync(path.join(dir, name)).toString('base64');
  const run = (operation, options = {}) => {
    const result = spawnSync(bash, ['--noprofile', '--norc', '-s'], {
      encoding: 'utf8',
      env: { ...process.env, RELEASE_SHA: 'a'.repeat(40), MTLS_OPERATION: operation,
        CA_BASE64: operation === 'install-health-material' ? encoded('ca.crt') : '',
        HEALTH_CERT_BASE64: operation === 'install-health-material' ? encoded('health.crt') : '', ...options.env },
      input: `# Root ownership is not representable on Windows. Files, OpenSSL, links and rollback are real.
id() { printf '0\\n'; }
stat() { case "$2" in %u) printf '0\\n' ;; %a) printf '755\\n' ;; %u:%a) printf '%s\\n' "\${FIXTURE_KEY_MODE:-0:600}" ;; *) command stat "$@" ;; esac; }
systemctl() { printf 'inactive\\n'; }
${options.failInstall ? 'ln() { case "$2" in */origin-pull-client-ca.crt) return 42 ;; *) command ln "$@" ;; esac; }; export -f ln' : ''}
export -f id stat systemctl
${script.replaceAll('/etc', `${posix(dir)}/etc`)}
`,
    });
    assert.doesNotMatch(result.stdout + result.stderr, /BEGIN (?:RSA |EC )?PRIVATE KEY/);
    return result;
  };
  const seedKey = (name = 'health.key') => {
    mkdirSync(stage, { recursive: true });
    copyFileSync(path.join(dir, name), path.join(stage, 'health.key'));
  };
  return { dir, stage, ca, cert, key, encoded, run, seedKey, close: () => rmSync(dir, { recursive: true, force: true }) };
}

test('provision workflow binds production SSH to current main and transports only validated public input', () => {
  assert.match(workflow, /if: github.ref == 'refs\/heads\/main'/);
  assert.match(workflow, /environment: production/);
  assert.match(workflow, /group: freedompost-production-deploy/);
  assert.match(workflow, /ref: \$\{\{ inputs.release_sha \}\}/);
  assert.equal((workflow.match(/git\/ref\/heads\/main/g) ?? []).length, 2);
  assert.match(workflow, /appleboy\/ssh-action@2ead5e36573f08b82fbfce1504f1a4b05a647c6f/);
  assert.match(workflow, /script_path: deploy\/provision-origin-mtls.sh/);
  const publicGuard = workflow.slice(workflow.indexOf('case "$MTLS_OPERATION"'), workflow.indexOf('- name: Checkout'));
  assert.match(publicGuard, /base64 --decode/);
  assert.match(publicGuard, /BEGIN CERTIFICATE/);
  assert.match(publicGuard, /! grep -q 'PRIVATE KEY'/);
  assert.match(publicGuard, /openssl x509 .* -noout >\/dev\/null 2>&1/);
  for (const run of workflow.split('run: |').slice(1)) assert.doesNotMatch(run.split(/^      - name:/m)[0], /\$\{\{/);
  assert.doesNotMatch(script, /curl|docker (?:compose|restart)|systemctl (?:restart|reload)|\.env|set -x/);
  assert.match(script, /CA=\/etc\/caddy\/certs\/origin-pull-client-ca.crt/);
  assert.match(script, /KEY=\/etc\/freedompost\/origin-health-client.key/);
});

test('inspect reads only fixed metadata and creates no files', () => {
  const f = fixture();
  try {
    const before = readdirSync(f.dir, { recursive: true });
    const result = f.run('inspect');
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /certificate_status=absent/);
    assert.match(result.stdout, /service=docker state=inactive/);
    assert.deepEqual(readdirSync(f.dir, { recursive: true }), before);
  } finally { f.close(); }
});

test('CSR creation keeps one server key on retry and prints only a public CSR', () => {
  const f = fixture();
  try {
    const first = f.run('create-health-csr');
    assert.equal(first.status, 0, first.stderr);
    assert.match(first.stdout, /^-----BEGIN CERTIFICATE REQUEST-----\n[\s\S]*-----END CERTIFICATE REQUEST-----\n$/);
    const key = readFileSync(path.join(f.stage, 'health.key'));
    const second = f.run('create-health-csr');
    assert.equal(second.status, 0, second.stderr);
    assert.deepEqual(readFileSync(path.join(f.stage, 'health.key')), key);
    assert.equal(second.stdout, first.stdout);
    assert.equal(existsSync(f.key), false);
    execFileSync(openssl, ['req', '-in', posix(path.join(f.stage, 'health.csr')), '-verify', '-noout'], { stdio: 'pipe' });
  } finally { f.close(); }
});

test('valid signed material installs idempotently without replacing existing different certificates', () => {
  const f = fixture();
  try {
    f.seedKey();
    const first = f.run('install-health-material');
    assert.equal(first.status, 0, first.stderr);
    const material = [f.ca, f.cert, f.key].map((file) => readFileSync(file));
    assert.equal(f.run('install-health-material').status, 0);
    assert.deepEqual([f.ca, f.cert, f.key].map((file) => readFileSync(file)), material);
    const different = f.run('install-health-material', { env: { CA_BASE64: f.encoded('wrong-ca.crt'), HEALTH_CERT_BASE64: f.encoded('wrong-client.crt') } });
    assert.notEqual(different.status, 0);
    assert.deepEqual([f.ca, f.cert, f.key].map((file) => readFileSync(file)), material);
    copyFileSync(path.join(f.dir, 'wrong-ca.crt'), f.ca);
    const existing = readFileSync(f.ca);
    const conflict = f.run('install-health-material');
    assert.notEqual(conflict.status, 0);
    assert.match(conflict.stderr, /refusing to replace existing different certificate material/);
    assert.deepEqual(readFileSync(f.ca), existing);
    assert.deepEqual([f.cert, f.key].map((file) => readFileSync(file)), material.slice(1));
    assert.match(readFileSync(path.join(f.stage, 'install.state'), 'utf8'), /complete/);
  } finally { f.close(); }
});

test('invalid chain, purpose, validity, key match and ownership fail before formal installation', () => {
  const f = fixture();
  try {
    for (const scenario of [
      { cert: 'wrong-client.crt' }, { cert: 'server.crt' }, { cert: 'expired.crt' }, { cert: 'soon.crt' },
      { key: 'aop.key' }, { mode: '0:644' }, { mode: '1000:600' }, { certData: Buffer.from('invalid').toString('base64') },
    ]) {
      f.seedKey(scenario.key);
      const result = f.run('install-health-material', { env: {
        HEALTH_CERT_BASE64: scenario.certData ?? f.encoded(scenario.cert ?? 'health.crt'),
        FIXTURE_KEY_MODE: scenario.mode ?? '0:600',
      } });
      assert.notEqual(result.status, 0, JSON.stringify(scenario));
      assert.match(result.stderr, /Origin mTLS provisioning:/);
      for (const file of [f.ca, f.cert, f.key]) assert.equal(existsSync(file), false);
    }
  } finally { f.close(); }
});

test('partial installation failure rolls back only newly created formal files and retains the key for retry', () => {
  const f = fixture();
  try {
    f.seedKey();
    const result = f.run('install-health-material', { failInstall: true });
    assert.notEqual(result.status, 0);
    for (const file of [f.ca, f.cert, f.key]) assert.equal(existsSync(file), false);
    assert.ok(existsSync(path.join(f.stage, 'health.key')));
    assert.match(readFileSync(path.join(f.stage, 'install.state'), 'utf8'), /rolled-back/);
    assert.equal(f.run('install-health-material').status, 0);
  } finally { f.close(); }
});

test('shell syntax in public input and unsupported operations fail without touching host paths', () => {
  const f = fixture();
  try {
    for (const [operation, env] of [['install-health-material', { CA_BASE64: '$(touch injected)' }], ['other', {}], ['inspect', { CA_BASE64: 'YQ==' }]]) {
      assert.notEqual(f.run(operation, { env }).status, 0);
      assert.equal(existsSync(f.stage), false);
    }
  } finally { f.close(); }
});
