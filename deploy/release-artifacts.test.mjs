import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

const remote = readFileSync(new URL('./remote-deploy.sh', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const extraction = remote.slice(remote.indexOf('echo "=== Extracting deploy bundle'), remote.indexOf('# This hostname is a production contract'));
const bash = process.platform === 'win32'
  ? path.resolve(execFileSync('git', ['--exec-path'], { encoding: 'utf8' }).trim(), '../../../bin/bash.exe')
  : 'bash';

function put(root, relative, contents) {
  const target = path.join(root, relative);
  mkdirSync(path.dirname(target), { recursive: true });
  writeFileSync(target, contents);
}

function upgrade({ missingAdmin = false, missingReaderShell = false, brokenArchive = false } = {}) {
  const dir = mkdtempSync(path.join(tmpdir(), 'freedompost-release-upgrade-'));
  const deployed = path.join(dir, 'deployed');
  const candidate = path.join(dir, 'candidate');
  const rollback = path.join(dir, 'rollback');
  const archive = path.join(dir, 'bundle.tar.gz');
  put(deployed, 'apps/public-reader/dist/p/welcome/index.html', 'old demo');
  put(deployed, 'apps/public-reader/dist/reader/index.html', 'old reader');
  put(deployed, 'apps/admin/dist/index.html', 'old admin');
  put(deployed, 'apps/admin/dist/assets/removed.js', 'old asset');
  put(deployed, '.env', 'UNCHANGED=fixture');
  put(deployed, 'runtime/local-storage/user-file', 'persistent upload');
  put(rollback, 'config.tar.gz', 'existing rollback snapshot');
  put(candidate, 'apps/public-reader/dist/reader/index.html', 'new reader');
  if (!missingReaderShell) put(candidate, 'apps/public-reader/dist/reader-shell/index.html', 'new article route');
  put(candidate, 'apps/public-reader/dist/search/post-list.json', '[]');
  if (!missingAdmin) put(candidate, 'apps/admin/dist/index.html', 'new admin');
  execFileSync('tar', ['-czf', archive, '-C', candidate, '.']);
  if (brokenArchive) writeFileSync(archive, 'invalid archive');
  try {
    const result = spawnSync(bash, ['--noprofile', '--norc', '-s'], {
      encoding: 'utf8',
      env: { ...process.env, FIXTURE_DEPLOY: deployed, FIXTURE_ROLLBACK: rollback, FIXTURE_BUNDLE: archive },
      input: `set -euo pipefail
SUDO=''
DEPLOY_PATH="$FIXTURE_DEPLOY"
ROLLBACK_DIR="$FIXTURE_ROLLBACK"
if command -v cygpath >/dev/null 2>&1; then
  DEPLOY_PATH=$(cygpath -u "$DEPLOY_PATH")
  ROLLBACK_DIR=$(cygpath -u "$ROLLBACK_DIR")
  FIXTURE_BUNDLE=$(cygpath -u "$FIXTURE_BUNDLE")
fi
${extraction.replaceAll('/tmp/freedompost-deploy.tar.gz', '$FIXTURE_BUNDLE')}
`,
    });
    const read = (relative) => readFileSync(path.join(deployed, relative), 'utf8');
    return {
      result,
      reader: read('apps/public-reader/dist/reader/index.html'),
      admin: read('apps/admin/dist/index.html'),
      hasDemo: existsSync(path.join(deployed, 'apps/public-reader/dist/p/welcome/index.html')),
      hasRemovedAsset: existsSync(path.join(deployed, 'apps/admin/dist/assets/removed.js')),
      oldReaderBackup: existsSync(path.join(rollback, 'static/public-reader/previous/reader/index.html')),
      env: read('.env'),
      upload: read('runtime/local-storage/user-file'),
      existingBackup: readFileSync(path.join(rollback, 'config.tar.gz'), 'utf8'),
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test('upgrading replaces generated static trees without retaining removed pages or touching persistent data', () => {
  const actual = upgrade();
  assert.equal(actual.result.status, 0, actual.result.stderr);
  assert.equal(actual.reader, 'new reader');
  assert.equal(actual.admin, 'new admin');
  assert.equal(actual.hasDemo, false, 'removed demo must not survive the upgrade');
  assert.equal(actual.hasRemovedAsset, false, 'obsolete admin assets must not survive the upgrade');
  assert.equal(actual.oldReaderBackup, true, 'previous static output must remain recoverable');
  assert.equal(actual.env, 'UNCHANGED=fixture');
  assert.equal(actual.upload, 'persistent upload');
  assert.equal(actual.existingBackup, 'existing rollback snapshot');
});

for (const scenario of [{ missingAdmin: true }, { missingReaderShell: true }, { brokenArchive: true }]) {
  test(`failed static upgrade restores both previous trees: ${JSON.stringify(scenario)}`, () => {
    const actual = upgrade(scenario);
    assert.notEqual(actual.result.status, 0);
    assert.equal(actual.reader, 'old reader');
    assert.equal(actual.admin, 'old admin');
    assert.equal(actual.hasDemo, true);
    assert.equal(actual.hasRemovedAsset, true);
    assert.equal(actual.env, 'UNCHANGED=fixture');
    assert.equal(actual.upload, 'persistent upload');
    assert.equal(actual.existingBackup, 'existing rollback snapshot');
  });
}

test('the exact release builds and ships a versioned media migration CLI without running a migration', () => {
  const workflow = readFileSync(new URL('../.github/workflows/deploy.yml', import.meta.url), 'utf8');
  const dockerfile = readFileSync(new URL('../services/api/Dockerfile', import.meta.url), 'utf8');
  assert.match(workflow, /ref: \$\{\{ inputs.release_sha \}\}/);
  assert.match(workflow, /go build[^\n]*-X main\.version=\$\{\{ inputs\.release_sha \}\}[^\n]*-o bin\/media-migrate \.\/cmd\/media-migrate/);
  assert.match(workflow, /test -s \/tmp\/freedompost-deploy\/services\/api\/bin\/media-migrate/);
  assert.match(workflow, /test -s \/tmp\/freedompost-deploy\/services\/api\/bin\/fp-api/);
  assert.match(dockerfile, /^COPY services\/api\/bin\/media-migrate \/media-migrate$/m);
  assert.match(dockerfile, /^COPY services\/api\/bin\/fp-api \/fp-api$/m);
  assert.doesNotMatch(remote, /(?:exec|run)[^\n]*media-migrate[^\n]*(?:--apply|--retire-public-originals|--confirm-cache-purge)/);
});

test('server, CLI, PR checks, and the local build use the same explicit version', () => {
  const workflow = readFileSync(new URL('../.github/workflows/deploy.yml', import.meta.url), 'utf8');
  const ci = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const makefile = readFileSync(new URL('../services/api/Makefile', import.meta.url), 'utf8');
  assert.match(workflow, /go build[^\n]*-X main\.version=\$\{\{ inputs\.release_sha \}\}[^\n]*-o bin\/fp-api \.\/cmd\/server/);
  assert.match(ci, /-o \/tmp\/media-migrate[\s\S]*?\.\/cmd\/media-migrate/);
  assert.match(ci, /test "\$\(\/tmp\/media-migrate -version\)" = "\$\{\{ github\.sha \}\}"/);
  const build = makefile.split(/^build:.*$/m)[1]?.split(/^fmt:/m)[0] ?? '';
  assert.match(build, /-o bin\/fp-api/);
  assert.match(build, /-o bin\/media-migrate[\s\S]*\.\/cmd\/media-migrate/);
  assert.equal((build.match(/main\.version=\$\(VERSION\)/g) ?? []).length, 2);
});

test('prepares a private, deployment-user-owned rollback directory using privileges before taking any snapshot', () => {
  const helpers = remote.split('# BEGIN rollback directory guard functions\n')[1]?.split('# END rollback directory guard functions')[0];
  assert.ok(helpers, 'execute the real rollback preparation function');
  const dir = mkdtempSync(path.join(tmpdir(), 'freedompost-rollback-permissions-'));
  try {
    const result = spawnSync(bash, ['--noprofile', '--norc', '-s'], {
      encoding: 'utf8', env: { ...process.env, FIXTURE_ROOT: dir },
      input: `set -euo pipefail
if command -v cygpath >/dev/null 2>&1; then FIXTURE_ROOT=$(cygpath -u "$FIXTURE_ROOT"); fi
DEPLOY_PATH="$FIXTURE_ROOT/parent/deployed"
ROLLBACK_DIR="\${DEPLOY_PATH}.rollback/release"
SUDO=privileged
privileged() { printf '%s\\n' "$1" >> "$FIXTURE_ROOT/privileged-commands"; "$@"; }
${helpers}
prepare_rollback_directory
test -d "$ROLLBACK_DIR"
printf 'private snapshot' > "$ROLLBACK_DIR/snapshot"
`,
    });
    assert.equal(result.status, 0, result.stderr);
    assert.match(readFileSync(path.join(dir, 'privileged-commands'), 'utf8'), /^mkdir\nchown\nchmod\n/);
    assert.match(remote, /ROLLBACK_DIR=[^\n]*\nprepare_rollback_directory\nif \[ -f "\$DEPLOY_PATH\/\.env" \]/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
