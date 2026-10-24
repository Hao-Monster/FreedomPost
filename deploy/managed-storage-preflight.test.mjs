import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { validateManagedStorageEnvironment } from './production-preflight.mjs';

const names = ['MANAGED_R2_BUCKET', 'MANAGED_R2_PREFIX', 'MANAGED_OSS_BUCKET', 'MANAGED_OSS_ENDPOINT', 'MANAGED_OSS_PREFIX', 'MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED'];
const remote = readFileSync(new URL('./remote-deploy.sh', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const guards = remote.split('# BEGIN managed storage guard functions\n')[1]?.split('# END managed storage guard functions')[0];
assert.ok(guards);
const writers = remote.slice(remote.indexOf('set_env() {'), remote.indexOf('\nset_env "PREVIEW_DOMAIN"'));
const bash = process.platform === 'win32'
  ? path.resolve(execFileSync('git', ['--exec-path'], { encoding: 'utf8' }).trim(), '../../../bin/bash.exe')
  : 'bash';

function run(environment, initial = 'UNRELATED=keep\n') {
  const dir = mkdtempSync(path.join(tmpdir(), 'freedompost-managed-gate-'));
  const file = path.join(dir, '.env');
  writeFileSync(file, initial);
  try {
    const result = spawnSync(bash, ['--noprofile', '--norc', '-s'], {
      encoding: 'utf8',
      env: { ...process.env, ...Object.fromEntries(names.map((name) => [name, ''])), STORAGE_DRIVER: 'local', R2_BUCKET: '', ALIYUN_OSS_BUCKET: '', ...environment, FIXTURE_DIR: dir },
      input: `set -euo pipefail
dir="$FIXTURE_DIR"
if command -v cygpath >/dev/null 2>&1; then dir=$(cygpath -u "$dir"); fi
cd "$dir"
SUDO=''
DEPLOY_PATH="$dir"
${guards}
${writers}
verify_managed_storage_configuration
write_managed_storage_configuration
`,
    });
    return { ...result, contents: readFileSync(file, 'utf8') };
  } finally { rmSync(dir, { recursive: true, force: true }); }
}

test('runner and remote guards reject missing, shared or unconfirmed private buckets before env changes', () => {
  const cases = [
    { STORAGE_DRIVER: 'r2' },
    { STORAGE_DRIVER: 'r2', MANAGED_R2_BUCKET: 'private-media' },
    { STORAGE_DRIVER: 'r2', R2_BUCKET: 'public-media', MANAGED_R2_BUCKET: 'private-media', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'false' },
    { STORAGE_DRIVER: 'r2', MANAGED_R2_BUCKET: 'freedompost', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'true' },
    { STORAGE_DRIVER: 'r2', R2_BUCKET: 'public-media', MANAGED_R2_BUCKET: 'public-media', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'true' },
    { STORAGE_DRIVER: 'oss', ALIYUN_OSS_BUCKET: 'public-media' },
    { STORAGE_DRIVER: 'oss', ALIYUN_OSS_BUCKET: 'public-media', MANAGED_OSS_BUCKET: 'private-media', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'false' },
    { STORAGE_DRIVER: 'oss', MANAGED_OSS_BUCKET: 'private-media', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'true' },
    { STORAGE_DRIVER: 'local', MANAGED_OSS_BUCKET: 'private-media' },
    { STORAGE_DRIVER: 'local', R2_BUCKET: 'public-media', MANAGED_R2_BUCKET: 'private-media', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'false' },
    { STORAGE_DRIVER: 'local', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'yes' },
    { STORAGE_DRIVER: 'r2', MANAGED_R2_BUCKET: ' private-media ', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'true' },
    { STORAGE_DRIVER: 'local', MANAGED_R2_PREFIX: 'bad\nSETTING=value' },
  ];
  const existing = 'UNRELATED=keep\n';
  for (const environment of cases) {
    assert.ok(validateManagedStorageEnvironment(environment).length > 0);
    const result = run(environment, existing);
    assert.notEqual(result.status, 0, JSON.stringify(environment));
    assert.match(result.stderr, /Managed storage gate:/);
    assert.equal(result.contents, existing);
    assert.doesNotMatch(result.stderr, /private-media|previous-private|SETTING=value/);
  }
});

test('valid remote settings are written while omitted optional and historical settings survive', () => {
  const environment = {
    STORAGE_DRIVER: 'r2', R2_BUCKET: 'public-media', MANAGED_R2_BUCKET: 'private-media',
    MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'true',
  };
  assert.deepEqual(validateManagedStorageEnvironment(environment), []);
  const result = run(environment, 'MANAGED_R2_PREFIX=keep-prefix\nMANAGED_OSS_BUCKET=keep-history\nALIYUN_OSS_BUCKET=history-public\nUNRELATED=keep\n');
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.contents, /^MANAGED_R2_BUCKET=private-media$/m);
  assert.match(result.contents, /^MANAGED_R2_PREFIX=keep-prefix$/m);
  assert.match(result.contents, /^MANAGED_OSS_BUCKET=keep-history$/m);
  assert.match(result.contents, /^MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED=true$/m);
  assert.match(result.contents, /^UNRELATED=keep$/m);

  const oss = { STORAGE_DRIVER: 'oss', ALIYUN_OSS_BUCKET: 'public-oss', MANAGED_OSS_BUCKET: 'private-oss', MANAGED_OSS_ENDPOINT: 'https://private-oss.example.com', MANAGED_OSS_PREFIX: 'media', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'true' };
  assert.deepEqual(validateManagedStorageEnvironment(oss), []);
  const written = run(oss);
  assert.equal(written.status, 0, written.stderr);
  for (const name of names.filter((name) => oss[name])) assert.ok(written.contents.includes(`${name}=${oss[name]}\n`));
});

test('local storage does not require remote credentials or erase retained remote configuration', () => {
  assert.deepEqual(validateManagedStorageEnvironment({ STORAGE_DRIVER: 'local' }), []);
  const previous = 'MANAGED_R2_BUCKET=history-private\nMANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED=true\n';
  const result = run({ STORAGE_DRIVER: 'local' }, previous);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.contents, previous);
});

test('local-only storage accepts empty, true and false private-origin confirmation', () => {
  for (const confirmed of ['', 'true', 'false']) {
    const environment = { STORAGE_DRIVER: 'local', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: confirmed };
    assert.deepEqual(validateManagedStorageEnvironment(environment), []);
    const result = run(environment);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.contents, `UNRELATED=keep\n${confirmed ? `MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED=${confirmed}\n` : ''}`);
  }
});

test('remote gate validates retained managed values before any env write', () => {
  const previous = 'R2_BUCKET=public-media\nMANAGED_R2_BUCKET=private-media\nMANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED=true\nUNRELATED=keep\n';
  const rejected = run({ STORAGE_DRIVER: 'local', MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED: 'false' }, previous);
  assert.notEqual(rejected.status, 0);
  assert.match(rejected.stderr, /confirm private origin access/);
  assert.equal(rejected.contents, previous);
  assert.equal(rejected.stdout, '');
  assert.doesNotMatch(rejected.stderr, /public-media|private-media/);

  for (const driver of ['local', 'r2']) {
    const retained = run({ STORAGE_DRIVER: driver, R2_BUCKET: 'public-media' }, previous);
    assert.equal(retained.status, 0, retained.stderr);
    assert.equal(retained.contents, previous);
  }
});

test('private and public buckets are compared using the values each writer will retain or replace', () => {
  for (const driver of ['r2', 'oss']) {
    const publicName = driver === 'r2' ? 'R2_BUCKET' : 'ALIYUN_OSS_BUCKET';
    const privateName = driver === 'r2' ? 'MANAGED_R2_BUCKET' : 'MANAGED_OSS_BUCKET';
    const previous = `${publicName}=old-public\n${privateName}=private-media\nMANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED=true\n`;
    const collision = run({ STORAGE_DRIVER: driver, [publicName]: 'private-media' }, previous);
    assert.notEqual(collision.status, 0);
    assert.match(collision.stderr, /must be distinct/);
    assert.equal(collision.contents, previous);

    const replaced = run({ STORAGE_DRIVER: driver, [publicName]: 'new-public', [privateName]: 'new-private' }, previous);
    assert.equal(replaced.status, 0, replaced.stderr);
    assert.match(replaced.contents, new RegExp(`^${privateName}=new-private$`, 'm'));

    // A local deployment does not write the public remote bucket input.
    const historicalCollision = previous.replace('old-public', 'private-media');
    const retained = run({ STORAGE_DRIVER: 'local', [publicName]: 'different-input' }, historicalCollision);
    assert.notEqual(retained.status, 0);
    assert.match(retained.stderr, /must be distinct/);
    assert.equal(retained.contents, historicalCollision);
  }
});

test('existing configuration parsing accepts literal quotes and refuses ambiguous values without executing them', () => {
  const previous = `R2_BUCKET="public-media"\nMANAGED_R2_BUCKET='private-media'\nMANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED='true'\nMANAGED_R2_PREFIX='$(printf unsafe >&2)'\nUNRELATED=$(printf unsafe >&2)\n`;
  const retained = run({ STORAGE_DRIVER: 'local' }, previous);
  assert.equal(retained.status, 0, retained.stderr);
  assert.equal(retained.stdout + retained.stderr, '');
  assert.equal(retained.contents, previous);
  for (const malformed of [previous + 'MANAGED_R2_BUCKET=duplicate\n', previous.replace("'private-media'", "'private-media")]) {
    const rejected = run({ STORAGE_DRIVER: 'local' }, malformed);
    assert.notEqual(rejected.status, 0);
    assert.match(rejected.stderr, /Managed storage gate:/);
    assert.equal(rejected.contents, malformed);
    assert.doesNotMatch(rejected.stderr, /private-media|unsafe/);
  }
});

test('managed configuration gate precedes mutation and all inputs reach runner and SSH preflight', () => {
  const gate = remote.indexOf('\nverify_managed_storage_configuration\n');
  assert.ok(gate > 0);
  for (const mutation of ['\nprepare_rollback_directory\n', 'tar -xzf /tmp/', '\nset_env "ADMIN_PASSWORD_HASH"', 'up -d postgres']) {
    assert.ok(remote.indexOf(mutation) > gate, mutation);
  }
  const workflow = readFileSync(new URL('../.github/workflows/deploy.yml', import.meta.url), 'utf8');
  const ssh = workflow.match(/^\s*envs: (.+)$/m)?.[1].split(',') ?? [];
  for (const name of names) {
    assert.equal(workflow.split(`${name}: \${{ secrets.${name} }}`).length - 1, 2, name);
    assert.ok(ssh.includes(name), name);
  }
});
