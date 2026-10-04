import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { probeChatwootInbox, validateChatwootBinding } from "./chatwoot-preflight.mjs";

const source = readFileSync(new URL("../apps/public-reader/public/chatwoot.js", import.meta.url), "utf8");
const environment = {
  CHATWOOT_BASE_URL: "https://chat.clawpan.online",
  CHATWOOT_WEBSITE_TOKEN: Buffer.from("V00zMlZvaEpBWWN5NjlSeDZxcjFQMkx0", "base64").toString("utf8")
};

test("accepts the same inbox in the public widget and the order sender", () => {
  assert.deepEqual(validateChatwootBinding(source, environment), []);
});

test("blocks an old but syntactically valid website token before deployment", () => {
  const errors = validateChatwootBinding(source, { ...environment, CHATWOOT_WEBSITE_TOKEN: "old-inbox-token" });
  assert.deepEqual(errors, ["CHATWOOT_WEBSITE_TOKEN does not match the public widget"]);
  assert.ok(!errors.join(" ").includes("old-inbox-token"));
});

test("blocks a different Chatwoot instance", () => {
  assert.deepEqual(validateChatwootBinding(source, { ...environment, CHATWOOT_BASE_URL: "https://wrong.example" }),
    ["CHATWOOT_BASE_URL does not match the public widget"]);
});

test("fails closed if the public loader cannot be inspected", () => {
  assert.deepEqual(validateChatwootBinding("window.chatwootSDK.run({});", environment),
    ["Public Chatwoot loader configuration is missing or invalid"]);
});

test("requires a real widget response and never follows a redirect", async () => {
  await probeChatwootInbox(environment, async (_url, options) => {
    assert.equal(options.redirect, "manual");
    return new Response("<script>window.chatwootWebChannel = {};</script>", { status: 200 });
  });
  for (const status of [302, 404, 500]) {
    await assert.rejects(probeChatwootInbox(environment, async () => new Response("private diagnostic", { status })),
      new RegExp(`HTTP ${status}`));
  }
  await assert.rejects(probeChatwootInbox(environment, async () => new Response("<html>Login</html>")), /returned no widget/);
});

test("bounds probe responses and suppresses network error details", async () => {
  await assert.rejects(probeChatwootInbox(environment, async () => new Response("x".repeat(256 * 1024 + 1))), /response limit/);
  await assert.rejects(probeChatwootInbox(environment, async () => { throw new Error("secret-diagnostic"); }),
    { message: "Chatwoot inbox probe failed (network or timeout)" });
});

test("suppresses body-stream and cancellation error details", async () => {
  const broken = new ReadableStream({ start(controller) { controller.error(new Error("private-body-diagnostic")); } });
  await assert.rejects(probeChatwootInbox(environment, async () => new Response(broken)),
    { message: "Chatwoot inbox probe response read failed" });
  const uncancellable = new ReadableStream({ cancel() { throw new Error("private-cancel-diagnostic"); } });
  await assert.rejects(probeChatwootInbox(environment, async () => new Response(uncancellable, { status: 404 })),
    { message: "Chatwoot inbox probe failed (HTTP 404)" });
});
