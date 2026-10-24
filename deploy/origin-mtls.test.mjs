import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const remote = readFileSync(new URL('./remote-deploy.sh', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const guards = remote.split('# BEGIN origin client certificate guard functions\n')[1]?.split('# END origin client certificate guard functions')[0];
assert.ok(guards, 'execute the real origin preflight functions');
const bash = process.platform === 'win32'
  ? path.resolve(execFileSync('git', ['--exec-path'], { encoding: 'utf8' }).trim(), '../../../bin/bash.exe')
  : 'bash';

test('origin material failures stop before deployment mutation', () => {
  const dir = mkdtempSync(path.join(tmpdir(), 'freedompost-origin-guard-'));
  try {
    execFileSync('go', ['run', fileURLToPath(new URL('./fixtures/origin-cert-fixture.go', import.meta.url)), dir]);
    const scenarios = [
      { name: 'valid', cert: 'health', key: 'health', status: 0 },
      { name: 'missing', cert: 'missing', key: 'health' },
      { name: 'malformed', cert: 'broken', key: 'health' },
      { name: 'wrong issuer', cert: 'wrong-client', key: 'wrong-client' },
      { name: 'wrong purpose', cert: 'server', key: 'server' },
      { name: 'expired', cert: 'expired', key: 'expired' },
      { name: 'expires soon', cert: 'soon', key: 'soon' },
      { name: 'mismatched key', cert: 'health', key: 'aop' },
      { name: 'unsafe permissions', cert: 'health', key: 'health', mode: '0:644' },
      { name: 'untrusted owner', cert: 'health', key: 'health', mode: '1000:600' },
    ];
    writeFileSync(path.join(dir, 'broken.crt'), 'not a certificate');
    for (const scenario of scenarios) {
      const marker = path.join(dir, 'mutated');
      rmSync(marker, { force: true });
      const result = spawnSync(bash, ['--noprofile', '--norc', '-s'], {
        encoding: 'utf8',
        env: { ...process.env, FIXTURE_DIR: dir, CERT_NAME: scenario.cert, KEY_NAME: scenario.key, KEY_MODE: scenario.mode ?? '0:600' },
        input: `set -euo pipefail
SUDO=''
dir="$FIXTURE_DIR"
if command -v cygpath >/dev/null 2>&1; then dir=$(cygpath -u "$dir"); fi
${guards}
# Windows cannot represent Linux ownership; only stat is mocked. OpenSSL is real.
stat() { printf '%s\\n' "$KEY_MODE"; }
verify_origin_client_material "$dir/ca.crt" "$dir/$CERT_NAME.crt" "$dir/$KEY_NAME.key"
printf changed > "$dir/mutated"
`,
      });
      assert.equal(result.status === 0, scenario.status === 0, `${scenario.name}: ${result.stderr}`);
      assert.equal(existsSync(marker), scenario.status === 0, scenario.name);
      if (scenario.status !== 0) assert.match(result.stderr, /Origin mTLS gate:/);
      assert.doesNotMatch(result.stdout + result.stderr, /BEGIN.*PRIVATE KEY/);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('origin guard runs before snapshot, extraction, credentials and service changes', () => {
  const call = remote.indexOf('\nverify_origin_client_material "$ORIGIN_CLIENT_CA"');
  assert.ok(call > 0);
  for (const mutation of ['\nprepare_admin_credentials\n', '\nprepare_rollback_directory\n', 'tar -xzf /tmp/', 'set_env "ADMIN_PASSWORD_HASH"', 'build paid-access', 'up -d postgres']) {
    assert.ok(remote.indexOf(mutation) > call, mutation);
  }
  assert.doesNotMatch(remote, /ufw allow|firewall-cmd --permanent|iptables -[IC] INPUT/);
  assert.match(remote, /origin_auth=custom-aop-required/);
  const probes = remote.slice(remote.indexOf('echo "=== Verifying Go API'));
  assert.equal((probes.match(/origin_curl /g) ?? []).length, 5);
  assert.doesNotMatch(probes, /&& curl /);
});

test('campaign certificate verification precedes its first data mutation', () => {
  const workflow = readFileSync(new URL('../.github/workflows/benefit-campaign.yml', import.meta.url), 'utf8');
  assert.match(workflow, /\$SUDO curl --noproxy '\*' --cacert[\s\S]*--cert \/etc\/freedompost\/origin-health-client.crt --key \/etc\/freedompost\/origin-health-client.key/);
  const health = workflow.indexOf('origin_curl -fsS "https://$PREVIEW_DOMAIN/health"');
  assert.ok(health > 0 && health < workflow.indexOf('UPDATE benefit_campaigns'));
  assert.match(workflow, /RESPONSE=\$\(origin_curl/);
});

test('anonymous origin rejection cannot pass on HTTP errors or network unavailability', () => {
  for (const [code, status, accepted] of [[35, '000', true], [56, '000', true], [0, '200', false], [22, '401', false], [7, '000', false], [28, '000', false]]) {
    const result = spawnSync(bash, ['--noprofile', '--norc', '-s'], {
      encoding: 'utf8',
      input: `set -euo pipefail\nSUDO=''\n${guards}\ncurl() { printf '${status}'; return ${code}; }\nverify_origin_rejects_anonymous admin.test\n`,
    });
    assert.equal(result.status === 0, accepted, `curl code ${code}, HTTP ${status}: ${result.stderr}`);
  }
});

test('CI and release tests install the checksum-pinned Caddy runtime', () => {
  for (const name of ['ci.yml', 'deploy.yml']) {
    const workflow = readFileSync(new URL(`../.github/workflows/${name}`, import.meta.url), 'utf8');
    const install = workflow.indexOf('bash deploy/install-test-caddy.sh');
    const tests = name === 'ci.yml' ? workflow.indexOf('run: npm run check') : workflow.indexOf('run: npm test');
    assert.ok(install > 0 && tests > install, name);
  }
  const docker = readFileSync(new URL('../Dockerfile', import.meta.url), 'utf8');
  assert.match(docker, /FROM caddy:2\.11\.7-alpine AS nginx/);
  const install = readFileSync(new URL('./install-test-caddy.sh', import.meta.url), 'utf8');
  assert.match(install, /sha512sum -c -/);
});
