import assert from "node:assert/strict";
import test from "node:test";
import * as preflight from "./chatwoot-preflight.mjs";

const environment = { CHATWOOT_BASE_URL: "https://chat.example.test" };
const supportedRelease = {
  revision: "a".repeat(40),
  upstreamVersion: "4.17.1",
  supportHandoffProtocol: 1
};

test("requires the handoff capability before a website release can enable automatic recovery", async () => {
  assert.equal(typeof preflight.probeChatwootHandoff, "function", "the current gate only checks an inbox, not reliable delivery support");
  await preflight.probeChatwootHandoff(environment, async (url, options) => {
    assert.equal(url.href, "https://chat.example.test/freedompost-widget-release.json");
    assert.equal(options.redirect, "manual");
    assert.equal(options.headers.accept, "application/json");
    assert.ok(options.signal instanceof AbortSignal);
    return Response.json(supportedRelease);
  });
});

test("rejects old, partial, or malformed support releases", async () => {
  for (const metadata of [
    {}, null, [],
    { ...supportedRelease, supportHandoffProtocol: undefined },
    { ...supportedRelease, supportHandoffProtocol: "1" },
    { ...supportedRelease, supportHandoffProtocol: true },
    { ...supportedRelease, supportHandoffProtocol: 2 },
    { ...supportedRelease, revision: "main" },
    { ...supportedRelease, upstreamVersion: "different" }
  ]) {
    await assert.rejects(preflight.probeChatwootHandoff(environment, async () => Response.json(metadata)),
      /does not support order handoff protocol 1/);
  }
});

test("never follows a metadata redirect or treats a proxy success page as protocol support", async () => {
  for (const status of [301, 302, 401, 404, 503]) {
    await assert.rejects(preflight.probeChatwootHandoff(environment,
      async () => new Response("private-diagnostic", { status })),
    { message: `Chatwoot handoff probe failed (HTTP ${status})` });
  }
  await assert.rejects(preflight.probeChatwootHandoff(environment,
    async () => new Response("<html>Login</html>")),
  { message: "Chatwoot release does not support order handoff protocol 1" });
});

test("bounds metadata and keeps transport and stream diagnostics private", async () => {
  await assert.rejects(preflight.probeChatwootHandoff(environment,
    async () => new Response("x".repeat(16 * 1024 + 1))),
  { message: "Chatwoot handoff probe exceeded response limit" });
  await assert.rejects(preflight.probeChatwootHandoff(environment,
    async () => { throw new Error("private-network-diagnostic"); }),
  { message: "Chatwoot handoff probe failed (network or timeout)" });
  const broken = new ReadableStream({ start(controller) { controller.error(new Error("private-body")); } });
  await assert.rejects(preflight.probeChatwootHandoff(environment, async () => new Response(broken)),
    { message: "Chatwoot handoff probe response read failed" });
});
