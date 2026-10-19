import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const listen = (server) => new Promise((resolve, reject) => {
  server.once('error', reject);
  server.listen(0, '127.0.0.1', () => resolve(server.address().port));
});
const close = (server) => new Promise((resolve) => server.close(resolve));
async function unusedPort() {
  const server = net.createServer();
  const port = await listen(server);
  await close(server);
  return port;
}

test('real Caddy 2.11.7 enforces the production origin mTLS boundary', {
  skip: !process.env.CADDY_BIN && !process.env.CI ? 'Set CADDY_BIN to the pinned Caddy 2.11.7 executable; CI must run this test' : false,
  timeout: 60000,
}, async (t) => {
  assert.ok(process.env.CADDY_BIN, 'CI must install the pinned Caddy test binary');
  assert.match(execFileSync(process.env.CADDY_BIN, ['version'], { encoding: 'utf8' }), /^v2\.11\.7\s/);
  const dir = mkdtempSync(path.join(tmpdir(), 'freedompost-caddy-mtls-'));
  const pem = (name) => readFileSync(path.join(dir, name));
  const configPath = (name) => JSON.stringify(path.join(dir, name).replaceAll('\\', '/'));
  let caddy;
  let output = '';
  const upstream = http.createServer((request, response) => {
    response.writeHead(request.url === '/api/admin/session' ? 401 : 200, { 'content-type': 'application/json' });
    response.end('{"status":"ok"}');
  });
  try {
    execFileSync('go', ['run', fileURLToPath(new URL('./fixtures/origin-cert-fixture.go', import.meta.url)), dir]);
    for (const site of ['public', 'admin']) {
      mkdirSync(path.join(dir, site));
      writeFileSync(path.join(dir, site, 'index.html'), `<html>${site} fixture</html>`);
    }
    const upstreamPort = await listen(upstream);
    const httpsPort = await unusedPort();
    let httpPort = await unusedPort();
    while (httpPort === httpsPort) httpPort = await unusedPort();
    const serverTLS = `tls ${configPath('server.crt')} ${configPath('server.key')}`;
    // Use the checked-in routing and client_auth configuration. Only replace
    // listener ports, fixture names/paths, server certificates and stub upstreams.
    const source = readFileSync(new URL('./caddy/Caddyfile', import.meta.url), 'utf8');
    const config = source
      .replace('{', `{\n\tadmin off\n\tdefault_bind 127.0.0.1\n\tauto_https disable_redirects\n\thttp_port ${httpPort}\n\thttps_port ${httpsPort}`)
      .replaceAll('admin-freedompost.thinderbox.uk', 'admin.test')
      .replaceAll('{$PREVIEW_DOMAIN}', 'public.test')
      .replaceAll('{$ORIGIN_TEST_HOST}', 'origin.test')
      .replace('tls /etc/caddy/certs/admin-origin.crt /etc/caddy/certs/admin-origin.key', serverTLS)
      .replace('tls internal {', `${serverTLS} {`)
      .replace('\ttls {', `\t${serverTLS} {`)
      .replaceAll('/etc/caddy/certs/origin-pull-client-ca.crt', configPath('ca.crt'))
      .replaceAll('/var/www/freedompost/public', configPath('public'))
      .replaceAll('/var/www/freedompost/admin', configPath('admin'))
      .replaceAll('api-go:3000', `127.0.0.1:${upstreamPort}`)
      .replaceAll('paid-access:8080', `127.0.0.1:${upstreamPort}`);
    writeFileSync(path.join(dir, 'Caddyfile'), config);
    caddy = spawn(process.env.CADDY_BIN, ['run', '--config', path.join(dir, 'Caddyfile'), '--adapter', 'caddyfile'], {
      env: { ...process.env, XDG_DATA_HOME: path.join(dir, 'data'), XDG_CONFIG_HOME: path.join(dir, 'config') },
      windowsHide: true,
    });
    caddy.stdout.on('data', (data) => { output = (output + data).slice(-12000); });
    caddy.stderr.on('data', (data) => { output = (output + data).slice(-12000); });
    caddy.on('error', (error) => { output += error.message; });
    function request(host, route = '/', client = 'health', headerHost = host) {
      return new Promise((resolve, reject) => {
        const req = https.get({
          host: '127.0.0.1', port: httpsPort, servername: host,
          path: route, headers: { host: headerHost }, ca: pem('ca.crt'),
          ...(client ? { cert: pem(`${client}.crt`), key: pem(`${client}.key`) } : {}),
          agent: false, timeout: 3000,
        }, (response) => {
          response.resume();
          response.on('end', () => resolve(response.statusCode));
        });
        req.on('timeout', () => req.destroy(new Error('local TLS request timed out')));
        req.on('error', reject);
      });
    }
    let ready = false;
    for (let attempt = 0; attempt < 40; attempt++) {
      try { ready = await request('public.test', '/health') === 200; } catch { /* Wait only for our isolated Caddy. */ }
      if (ready || caddy.exitCode !== null) break;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    assert.ok(ready, output);
    for (const host of ['admin.test', 'public.test', 'origin.test']) {
      for (const client of [null, 'wrong-client', 'expired', 'server']) {
        await t.test(`${host} rejects ${client ?? 'missing'} client certificate even on loopback`, async () => {
          await assert.rejects(request(host, '/', client), /certificate|unknown ca|EPROTO|ECONNRESET|socket hang up/i);
        });
      }
      await t.test(`${host} accepts distinct AOP and maintenance identities`, async () => {
        const expected = host === 'origin.test' ? 301 : 200;
        assert.equal(await request(host, '/', 'aop'), expected);
        assert.equal(await request(host), expected);
      });
    }
    await t.test('domain fronting cannot select the admin Host through another SNI', async () => {
      assert.equal(await request('public.test', '/', 'health', 'admin.test'), 421);
      assert.equal(await request('origin.test', '/', 'health', 'admin.test'), 421);
    });
    await t.test('mTLS does not replace app authentication or public/admin separation', async () => {
      assert.equal(await request('admin.test', '/api/admin/session'), 401);
      assert.equal(await request('public.test', '/api/admin/session'), 404);
      assert.equal(await request('public.test', '/api/admin/login'), 404);
      assert.equal(await request('public.test', '/admin/'), 404);
      assert.equal(await request('public.test', '/health/ready'), 200);
    });
    await t.test('a missing client trust file prevents loading the actual Caddy configuration', () => {
      const missing = path.join(dir, 'missing-trust.Caddyfile');
      writeFileSync(missing, config.replace(configPath('ca.crt'), configPath('missing-ca.crt')));
      const validation = spawnSync(process.env.CADDY_BIN, ['validate', '--config', missing, '--adapter', 'caddyfile'], {
        encoding: 'utf8',
        env: { ...process.env, XDG_DATA_HOME: path.join(dir, 'data'), XDG_CONFIG_HOME: path.join(dir, 'config') },
        windowsHide: true,
      });
      assert.notEqual(validation.status, 0);
      assert.match(validation.stderr, /missing-ca\.crt/);
    });
  } finally {
    if (caddy && caddy.exitCode === null) {
      const exited = new Promise((resolve) => caddy.once('exit', resolve));
      caddy.kill();
      await exited;
    }
    if (upstream.listening) await close(upstream);
    rmSync(dir, { recursive: true, force: true });
  }
});
