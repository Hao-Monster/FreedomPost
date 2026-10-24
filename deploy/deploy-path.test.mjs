import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { validateProductionEnvironment } from './production-preflight.mjs';

const remote = readFileSync(new URL('./remote-deploy.sh', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const entry = remote.slice(0, remote.indexOf('# BEGIN admin credential guard functions'));
const bash = process.platform === 'win32'
  ? path.resolve(execFileSync('git', ['--exec-path'], { encoding: 'utf8' }).trim(), '../../../bin/bash.exe')
  : 'bash';
const invalidPaths = [
  '/srv/freedompost/', '', '/', '//', '/srv//freedompost', 'srv/freedompost',
  './freedompost', '/srv/.', '/srv/..', '/srv/./freedompost', '/srv/../freedompost',
  '/srv/site/../', '/srv/site\nother', '/srv/site\r', '/srv/site\t', '/srv/site name',
  '/srv/site\\other', '/srv/site;echo', '/srv/$(id)', '/srv/`id`', "/srv/site'", '/srv/site"',
];

function runEntry(deployPath, canonicalPath) {
  return spawnSync(bash, ['--noprofile', '--norc', '-s'], {
    encoding: 'utf8',
    env: { ...process.env, DEPLOY_PATH: deployPath, FIXTURE_CANONICAL: canonicalPath ?? '' },
    input: `id() { printf 'ID_CALLED\\n' >&2; printf '1001\\n'; }
${canonicalPath === undefined ? '' : 'realpath() { printf \'%s\\n\' "$FIXTURE_CANONICAL"; }'}
${entry}
printf '%s\\n' "$DEPLOY_PATH"
printf '%s\\n' "\${DEPLOY_PATH}.rollback/release"
`,
  });
}

test('production preflight rejects root, noncanonical, and shell-unsafe deployment paths', () => {
  for (const deployPath of invalidPaths) {
    const errors = validateProductionEnvironment({ DEPLOY_PATH: deployPath });
    assert.ok(errors.some((error) => error.name === 'DEPLOY_PATH'), JSON.stringify(deployPath));
  }
});

test('production preflight accepts canonical absolute deployment paths', () => {
  for (const deployPath of ['/srv/freedompost', '/opt/freedompost-prod', '/srv/site_2/release.v1']) {
    const errors = validateProductionEnvironment({ DEPLOY_PATH: deployPath });
    assert.equal(errors.some((error) => error.name === 'DEPLOY_PATH'), false, deployPath);
  }
});

test('remote entry rejects unsafe paths before any existing deployment command runs', () => {
  for (const deployPath of invalidPaths) {
    const result = runEntry(deployPath);
    assert.notEqual(result.status, 0, JSON.stringify(deployPath));
    assert.match(result.stderr, /DEPLOY_PATH/);
    assert.doesNotMatch(result.stderr, /ID_CALLED/);
    assert.equal(result.stdout, '');
  }
});

test('remote entry accepts an absent canonical target and keeps rollback outside its build context', () => {
  const deployPath = '/srv/freedompost-path-fixture-not-created';
  const result = runEntry(deployPath);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, `${deployPath}\n${deployPath}.rollback/release\n`);
  assert.equal(path.posix.dirname(`${deployPath}.rollback`), path.posix.dirname(deployPath));
  assert.equal(`${deployPath}.rollback`.startsWith(`${deployPath}/`), false);
});

test('remote entry rejects filesystem aliases, including one resolving to root', () => {
  for (const canonical of ['/', '/srv/elsewhere']) {
    const result = runEntry('/srv/freedompost', canonical);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /DEPLOY_PATH/);
    assert.doesNotMatch(result.stderr, /ID_CALLED/);
  }
});

test('release validates its path before upload and excludes legacy rollback snapshots from Docker context', () => {
  const workflow = readFileSync(new URL('../.github/workflows/deploy.yml', import.meta.url), 'utf8');
  const ignore = readFileSync(new URL('../.dockerignore', import.meta.url), 'utf8');
  const preflight = workflow.indexOf('node deploy/production-preflight.mjs');
  assert.ok(preflight >= 0);
  assert.ok(preflight < workflow.indexOf('- name: Copy bundle to server'));
  assert.ok(preflight < workflow.indexOf('- name: Deploy over SSH'));
  assert.equal((workflow.match(/\$\{\{ secrets\.DEPLOY_PATH \}\}/g) ?? []).length, 2);
  assert.doesNotMatch(workflow, /\$DEPLOY_PATH|\$\{DEPLOY_PATH\}/);
  assert.match(workflow, /target: "\/tmp"/);
  assert.match(ignore, /^\.rollback\s*$/m);
});
