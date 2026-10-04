import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Inspect our small, checked-in loader without executing JavaScript. Fail closed
// if its configuration format changes so releases cannot silently skip this gate.
export function validateChatwootBinding(source, environment) {
  const baseUrl = source.match(/var baseUrl = "(https:\/\/[^"\s]+)";/)?.[1];
  const encodedToken = source.match(/var websiteToken = atob\("([A-Za-z0-9+/=]+)"\);/)?.[1];
  const websiteToken = encodedToken && Buffer.from(encodedToken, "base64").toString("utf8");
  if (!baseUrl || !websiteToken || !/^[A-Za-z0-9_-]{10,256}$/.test(websiteToken)) {
    return ["Public Chatwoot loader configuration is missing or invalid"];
  }
  const errors = [];
  if (baseUrl !== environment.CHATWOOT_BASE_URL) errors.push("CHATWOOT_BASE_URL does not match the public widget");
  if (websiteToken !== environment.CHATWOOT_WEBSITE_TOKEN) errors.push("CHATWOOT_WEBSITE_TOKEN does not match the public widget");
  return errors;
}

export async function probeChatwootInbox(environment, request = fetch) {
  const url = new URL("/widget", environment.CHATWOOT_BASE_URL);
  url.searchParams.set("website_token", environment.CHATWOOT_WEBSITE_TOKEN);
  let response;
  try {
    response = await request(url, { redirect: "manual", signal: AbortSignal.timeout(10000) });
  } catch {
    throw new Error("Chatwoot inbox probe failed (network or timeout)");
  }
  if (response.status !== 200) {
    await response.body?.cancel().catch(() => {});
    throw new Error(`Chatwoot inbox probe failed (HTTP ${response.status})`);
  }
  const reader = response.body?.getReader();
  if (!reader) throw new Error("Chatwoot inbox probe returned no widget");
  const chunks = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > 256 * 1024) throw new Error("Chatwoot inbox probe exceeded response limit");
      chunks.push(Buffer.from(value));
    }
  } catch {
    throw new Error(size > 256 * 1024
      ? "Chatwoot inbox probe exceeded response limit"
      : "Chatwoot inbox probe response read failed");
  } finally {
    // Cleanup failures must not expose transport diagnostics or replace the
    // bounded, sanitized error above.
    await reader.cancel().catch(() => {});
  }
  if (!Buffer.concat(chunks).toString("utf8").includes("window.chatwootWebChannel")) {
    throw new Error("Chatwoot inbox probe returned no widget");
  }
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  try {
    const source = readFileSync(process.argv[2] || "apps/public-reader/dist/chatwoot.js", "utf8");
    const errors = validateChatwootBinding(source, process.env);
    if (errors.length) throw new Error(errors.join("; "));
    await probeChatwootInbox(process.env);
    console.log("OK Chatwoot public widget and order sender use the same reachable inbox");
  } catch (error) {
    // File paths and probe diagnostics are local; no upstream response or token
    // is ever included in the error message.
    console.error(error instanceof Error ? error.message : "Chatwoot preflight failed");
    process.exitCode = 1;
  }
}
