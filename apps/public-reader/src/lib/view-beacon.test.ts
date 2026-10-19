import { afterEach, expect, it, vi } from "vitest";
import { sendViewBeacon } from "./view-beacon";

afterEach(() => vi.unstubAllGlobals());
it("sends view counts as JSON through the strict browser-write boundary", async () => {
  const sendBeacon = vi.fn((_url: string, _body: Blob) => true);
  vi.stubGlobal("navigator", { sendBeacon });
  sendViewBeacon("qa slug", "qa-reader");
  const [url, payload] = sendBeacon.mock.calls[0]!;
  expect(url).toBe("/api/posts/qa%20slug/view");
  expect(payload.type).toBe("application/json");
  expect(JSON.parse(await payload.text())).toEqual({ localId: "qa-reader" });
});
