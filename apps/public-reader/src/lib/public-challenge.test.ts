// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { requestChallenge } from "./public-challenge";

type WidgetOptions = { action: string; callback(token: string): void; "expired-callback"(): void; "error-callback"(): void };
let options: WidgetOptions;
const remove = vi.fn();

beforeEach(() => {
  document.body.innerHTML = "";
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ siteKey: "qa-site" }))));
  Object.defineProperty(window, "turnstile", { configurable: true, value: {
    render: vi.fn((_root: HTMLElement, value: WidgetOptions) => { options = value; return "qa-widget"; }), remove
  }});
  vi.spyOn(HTMLDialogElement.prototype, "showModal").mockImplementation(function (this: HTMLDialogElement) { this.open = true; });
  vi.spyOn(HTMLDialogElement.prototype, "close").mockImplementation(function (this: HTMLDialogElement) { this.open = false; });
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); remove.mockClear(); });

async function widgetReady() { await vi.waitFor(() => expect(document.querySelector("dialog")).not.toBeNull()); }

describe("public challenge lifecycle", () => {
  it("returns a fresh token for the requested action and removes the widget", async () => {
    const first = requestChallenge("comment_upload"); await widgetReady();
    expect(options.action).toBe("comment_upload"); options.callback("first-token");
    await expect(first).resolves.toBe("first-token");
    expect(document.querySelector("dialog")).toBeNull(); expect(remove).toHaveBeenCalledWith("qa-widget");
    const next = requestChallenge("order_create"); await widgetReady();
    expect(options.action).toBe("order_create"); options.callback("second-token");
    await expect(next).resolves.toBe("second-token");
    expect(localStorage.length).toBe(0);
  });
  it("aborts a pending challenge without leaving a modal or accepting a late token", async () => {
    const controller = new AbortController();
    const pending = requestChallenge("comment_upload", { signal: controller.signal });
    const rejected = expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await widgetReady(); controller.abort(); options.callback("late"); await rejected;
    expect(document.querySelector("dialog")).toBeNull(); expect(remove).toHaveBeenCalledTimes(1);
  });
  it("fails closed on unavailable config and expired tokens", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response("", { status: 503 }));
    await expect(requestChallenge("order_create")).rejects.toThrow("暂不可用");
    expect(document.querySelector("dialog")).toBeNull();
    const pending = requestChallenge("order_create");
    const rejected = expect(pending).rejects.toThrow("过期");
    await widgetReady(); options["expired-callback"](); await rejected;
    expect(document.querySelector("dialog")).toBeNull();
  });
});
