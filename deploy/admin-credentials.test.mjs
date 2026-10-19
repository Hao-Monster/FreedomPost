import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

const remote = readFileSync(new URL('./remote-deploy.sh', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const guards = remote.split('# BEGIN admin credential guard functions\n')[1]?.split('# END admin credential guard functions')[0];
assert.ok(guards, 'execute the real deployment credential functions');
const writers = remote.slice(remote.indexOf('set_env() {'), remote.indexOf('set_env_if_present() {'));
const bash = process.platform === 'win32'
  ? path.resolve(execFileSync('git', ['--exec-path'], { encoding: 'utf8' }).trim(), '../../../bin/bash.exe')
  : 'bash';
if (process.platform === 'win32') assert.ok(existsSync(bash), 'Git Bash is required for deployment tests');

// Synthetic, format-valid values only; these are not production credentials.
const oldHash = '$2b$12$' + 'A'.repeat(53);
const newHash = '$2a$12$' + 'B'.repeat(53);
const escaped = (value) => value.replaceAll('$', () => '$$');
const envText = (hash = oldHash, username = 'operator') => `ADMIN_PASSWORD_HASH=${escaped(hash)}\nADMIN_USERNAME=${username}\nUNRELATED=keep\n`;

function run(body, options = {}) {
  const dir = mkdtempSync(path.join(tmpdir(), 'freedompost-credentials-'));
  const file = path.join(dir, '.env');
  if (options.file !== null) writeFileSync(file, options.file ?? envText());
  const result = spawnSync(bash, ['--noprofile', '--norc', '-s'], {
    encoding: 'utf8',
    input: `set -euo pipefail\nSUDO=''\nDEPLOY_PATH="$FIXTURE_DIR"\nif command -v cygpath >/dev/null 2>&1; then DEPLOY_PATH=$(cygpath -u "$FIXTURE_DIR"); fi\n${guards}\n${writers}\n
compose() {
  case " $* " in
    *' ps '*) printf '%s\\n' "$MOCK_CONTAINERS" ;;
    *' run '*) printf 'ADMIN_PASSWORD_HASH=%s\\nADMIN_USERNAME=%s\\n' "$CANDIDATE_HASH" "$RUNTIME_USERNAME" ;;
    *) return 90 ;;
  esac
}
docker_cmd() {
  case " $* " in
    *'com.docker.compose.oneoff'*)
      case " $* " in *' oneoff '*) printf 'True\\n' ;; *) printf 'False\\n' ;; esac ;;
    *' inspect '*) printf 'ADMIN_PASSWORD_HASH=%s\\nADMIN_USERNAME=%s\\n' "$RUNTIME_HASH" "$RUNTIME_USERNAME" ;;
    *' ps '*) printf '%s' "$BOOTSTRAP_CONTAINERS" ;;
    *) return 91 ;;
  esac
}
${body}\n`,
    env: {
      ...process.env, FIXTURE_DIR: dir, ADMIN_PASSWORD_HASH: oldHash,
      ROTATE_ADMIN_PASSWORD: 'false', RUNTIME_HASH: oldHash, RUNTIME_USERNAME: 'operator',
      CANDIDATE_HASH: oldHash, MOCK_CONTAINERS: 'api', BOOTSTRAP_CONTAINERS: '', ...options.env,
    },
  });
  try {
    return { ...result, file: existsSync(file) ? readFileSync(file, 'utf8') : null };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function passed(result) {
  assert.equal(result.status, 0, result.stderr || result.error?.message);
  assert.doesNotMatch(result.stdout + result.stderr, /\$2[ab]\$/);
}

test('ordinary deployment preserves runtime credentials and ignores one-off containers', () => {
  const result = run('prepare_admin_credentials\ntest "$ADMIN_USERNAME" = operator\ntest "$ADMIN_CREDENTIAL_MODE" = preserve', {
    env: { MOCK_CONTAINERS: 'oneoff\napi\n' },
  });
  passed(result);
  assert.equal(result.file, envText());
});

test('credential drift, invalid input and ambiguous containers stop before mutation', () => {
  const cases = [
    { env: { ADMIN_PASSWORD_HASH: newHash } },
    { file: envText(newHash) },
    { file: envText(oldHash, 'different-user') },
    { file: envText() + `ADMIN_PASSWORD_HASH=${oldHash}\n` },
    { file: envText() + 'ADMIN_USERNAME=operator\n' },
    { env: { ADMIN_PASSWORD_HASH: 'invalid' } },
    { env: { ROTATE_ADMIN_PASSWORD: 'TRUE' } },
    { env: { ROTATE_ADMIN_PASSWORD: '' } },
    { env: { MOCK_CONTAINERS: '' } },
    { env: { MOCK_CONTAINERS: 'api\napi2' } },
    { env: { RUNTIME_HASH: '' } },
    { file: null },
  ];
  for (const scenario of cases) {
    const result = run('prepare_admin_credentials\nprintf mutated > "$DEPLOY_PATH/.env"', scenario);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /Admin credential gate:/);
    assert.equal(result.file, scenario.file === null ? null : scenario.file ?? envText());
    assert.doesNotMatch(result.stdout + result.stderr, /\$2[ab]\$[0-9]/);
  }
});

test('explicit rotation changes only the selected hash and preserves the current username', () => {
  const result = run('prepare_admin_credentials\ntest "$ADMIN_CREDENTIAL_MODE" = rotate\ncd "$DEPLOY_PATH"\nset_env ADMIN_PASSWORD_HASH "$ADMIN_PASSWORD_HASH"\nread_admin_env_file .env\ntest "$ADMIN_FILE_HASH" = "$ADMIN_PASSWORD_HASH"\ntest "$ADMIN_FILE_USERNAME" = "$ADMIN_USERNAME"', {
    file: `ADMIN_PASSWORD_HASH=${escaped(oldHash)}\nADMIN_USERNAME='ops #team'\n`,
    env: { ADMIN_PASSWORD_HASH: newHash, ROTATE_ADMIN_PASSWORD: 'true', RUNTIME_USERNAME: 'ops #team' },
  });
  passed(result);
  assert.equal(result.file, `ADMIN_PASSWORD_HASH=${escaped(newHash)}\nADMIN_USERNAME='ops #team'\n`);
  passed(run('prepare_admin_credentials\ntest "$ADMIN_CREDENTIAL_MODE" = bootstrap\ntest "$ADMIN_USERNAME" = admin', {
    file: null, env: { ROTATE_ADMIN_PASSWORD: 'true' },
  }));
});

test('existing bcrypt quoting and Compose dollar escaping are parsed without evaluating shell text', () => {
  for (const value of [oldHash, escaped(oldHash), `'${oldHash}'`, `"${escaped(oldHash)}"`]) {
    passed(run('prepare_admin_credentials', { file: `ADMIN_PASSWORD_HASH=${value}\nADMIN_USERNAME=operator\n` }));
  }
  const result = run('prepare_admin_credentials', { file: 'ADMIN_PASSWORD_HASH=$(printf unsafe >&2)\n' });
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stdout + result.stderr, /unsafe/);
});

test('candidate and actual runtime must both match selected credentials', () => {
  passed(run('prepare_admin_credentials\nverify_candidate_admin_credentials\nverify_running_admin_credentials'));
  for (const body of ['verify_candidate_admin_credentials', 'verify_running_admin_credentials']) {
    const result = run(`ADMIN_USERNAME=operator\n${body}\nprintf accepted`, {
      env: { CANDIDATE_HASH: newHash, RUNTIME_HASH: newHash },
    });
    assert.notEqual(result.status, 0);
    assert.doesNotMatch(result.stdout, /accepted/);
    assert.match(result.stderr, /do not match/);
  }
});

test('atomic env replacement retains other settings and writes Compose-safe bcrypt', () => {
  const result = run('cd "$DEPLOY_PATH"\nset_env ADMIN_PASSWORD_HASH "$ADMIN_PASSWORD_HASH"\nremove_env_key ADMIN_PASSWORD\nread_admin_env_file .env\ntest "$ADMIN_FILE_HASH" = "$ADMIN_PASSWORD_HASH"', {
    file: envText() + 'ADMIN_PASSWORD=legacy\n', env: { ADMIN_PASSWORD_HASH: newHash },
  });
  passed(result);
  assert.equal(result.file, envText(newHash));
});

test('updating whitespace-formatted credential keys does not introduce duplicates', () => {
  const result = run('cd "$DEPLOY_PATH"\nprepare_admin_credentials\nset_env ADMIN_USERNAME "$ADMIN_USERNAME"\nset_env ADMIN_PASSWORD_HASH "$ADMIN_PASSWORD_HASH"\nremove_env_key ADMIN_PASSWORD\nread_admin_env_file .env', {
    file: ` ADMIN_USERNAME = operator\n ADMIN_PASSWORD_HASH = ${escaped(oldHash)}\n ADMIN_PASSWORD = legacy\n`,
  });
  passed(result);
  assert.equal(result.file, `ADMIN_USERNAME=operator\nADMIN_PASSWORD_HASH=${escaped(oldHash)}\n`);
});

test('a failed env transform leaves the existing file untouched', () => {
  for (const operation of ['set_env ADMIN_PASSWORD_HASH "$ADMIN_PASSWORD_HASH"', 'remove_env_key ADMIN_PASSWORD']) {
    const result = run(`cd "$DEPLOY_PATH"\nawk() { return 17; }\n${operation}\nprintf mutated > .env`);
    assert.notEqual(result.status, 0);
    assert.equal(result.file, envText());
  }
});

test('real Docker Compose resolves the written bcrypt without dollar interpolation damage', () => {
  const result = run('cd "$DEPLOY_PATH"\nset_env ADMIN_PASSWORD_HASH "$ADMIN_PASSWORD_HASH"', {
    env: { ADMIN_PASSWORD_HASH: newHash },
  });
  passed(result);
  const dir = mkdtempSync(path.join(tmpdir(), 'freedompost-compose-'));
  try {
    writeFileSync(path.join(dir, '.env'), result.file);
    writeFileSync(path.join(dir, 'compose.yml'), 'services:\n  api-go:\n    image: alpine:3.22\n    env_file: .env\n');
    const config = JSON.parse(execFileSync('docker', [
      'compose', '--project-name', 'credential-fixture', '--env-file', '.env', '-f', 'compose.yml', 'config', '--format', 'json',
    ], { cwd: dir, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }));
    // `compose config` serializes literal dollars as $$ for a reusable Compose file.
    // The deployment additionally checks the candidate container's actual environment.
    assert.equal(config.services['api-go'].environment.ADMIN_PASSWORD_HASH, escaped(newHash));
    assert.equal(config.services['api-go'].environment.ADMIN_USERNAME, 'operator');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('workflow and deploy ordering enforce explicit rotation before writes and check before success', () => {
  const workflow = readFileSync(new URL('../.github/workflows/deploy.yml', import.meta.url), 'utf8');
  assert.match(workflow, /rotate_admin_password:[\s\S]*?type: boolean\s+default: false/);
  assert.match(workflow, /ROTATE_ADMIN_PASSWORD: \$\{\{ inputs.rotate_admin_password && 'true' \|\| 'false' \}\}/);
  assert.match(workflow, /envs: RELEASE_SHA,ROTATE_ADMIN_PASSWORD,/);
  assert.match(workflow, /- name: Test\s+run: npm test\s/);
  assert.ok(remote.indexOf('\nprepare_admin_credentials\n') < remote.indexOf('\nROLLBACK_DIR='));
  assert.ok(remote.indexOf('\nverify_candidate_admin_credentials\n') < remote.indexOf('run --rm api-go -migrate'));
  assert.ok(remote.indexOf('\nverify_running_admin_credentials\n') < remote.indexOf('> .release-record'));
  assert.match(remote, /credential_mode=%s/);
  assert.doesNotMatch(remote, /^set_env "ADMIN_USERNAME"/m, 'preserve existing username quoting');
});
