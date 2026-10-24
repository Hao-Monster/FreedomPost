import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { createServer } from 'node:http';
import path from 'node:path';
import test from 'node:test';

const remote = readFileSync(new URL('./remote-deploy.sh', import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const request = remote.indexOf('"https://$PREVIEW_DOMAIN/api/benefits/webmaster"');
const start = remote.lastIndexOf('\n    && ', request);
const end = remote.indexOf('\n    && verify_origin_rejects_anonymous', request);
const probe = remote.slice(start, end).replace(/\\\n/g, ' ').replace(/\\\s*$/, '').trim();
const bash = process.platform === 'win32'
  ? path.resolve(execFileSync('git', ['--exec-path'], { encoding: 'utf8' }).trim(), '../../../bin/bash.exe')
  : 'bash';

test('benefit health gate consumes complete headers without a shared file or curl-to-grep pipe', async () => {
  assert.ok(start > 0 && end > start);
  assert.doesNotMatch(probe, /\/tmp\/|\|\s*grep/);
  assert.match(probe, /benefit_headers=\$\(origin_curl .* -D - -o \/dev\/null /);
  assert.match(probe, /grep -qi '\^cache-control:\.\*no-store' <<< "\$benefit_headers"/);
  const server = createServer((request, response) => {
    response.statusCode = request.url === '/http-error' ? 500 : 200;
    response.setHeader('Cache-Control', request.url === '/missing' ? 'public, max-age=60' : 'private, no-store');
    // A large trailing header catches attempts to grep -q directly from curl.
    response.setHeader('X-Trailing-Headers', 'x'.repeat(60000));
    response.end('body is discarded');
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  try {
    const { port } = server.address();
    for (const [scenario, passes] of [['success', true], ['missing', false], ['http-error', false]]) {
      const result = await new Promise((resolve, reject) => {
        const child = spawn(bash, ['--noprofile', '--norc', '-s'], {
          env: { ...process.env, PREVIEW_DOMAIN: 'fixture.test', FIXTURE_URL: `http://127.0.0.1:${port}/${scenario}` },
        });
        let stdout = '', stderr = '';
        child.stdout.on('data', (chunk) => { stdout += chunk; });
        child.stderr.on('data', (chunk) => { stderr += chunk; });
        child.on('error', reject);
        child.on('close', (status) => resolve({ status, stdout, stderr }));
        child.stdin.end(`set -euo pipefail
origin_curl() {
  local args=()
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --resolve) shift 2 ;;
      https://*) args+=("$FIXTURE_URL"); shift ;;
      *) args+=("$1"); shift ;;
    esac
  done
  command curl --noproxy '*' "\${args[@]}"
}
if true ${probe}; then printf 'accepted\\n'; else printf 'rejected\\n'; fi
test "\${#benefit_headers}" -gt 60000
`);
      });
      assert.equal(result.status, 0, `${scenario}: ${result.stderr}`);
      assert.equal(result.stdout.trim(), passes ? 'accepted' : 'rejected', scenario);
    }
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});
