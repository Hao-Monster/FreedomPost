// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import earn from "../pages/earn.astro?raw";
import { requestChallenge } from "./public-challenge.js";

vi.mock("lucide", () => ({ createIcons: vi.fn(), icons: {} }));
vi.mock("./public-challenge.js", () => ({ requestChallenge: vi.fn(async () => "qa-affiliate-token") }));

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

async function mount() {
  document.body.innerHTML = earn.slice(earn.indexOf("  <section"), earn.lastIndexOf("</PortalShell>"));
  const fetcher = vi.fn<typeof fetch>(async (rawPath) => {
    const path = String(rawPath);
    if (path === "/api/affiliate/dashboard") return json({ error: { message: "not signed in" } }, 401);
    if (path === "/api/affiliate/access") return json({
      shareUrl: "https://shop.example/market/?ref=QA_User",
      generatedPassword: "QA_PASSWORD1",
      dashboard: { affiliate: { wechatId: "QA_User" }, totalClicks: 0, uniqueClicks: 0, completedOrders: 0, pendingCommissionCents: 0, paidCommissionCents: 0, orders: [] }
    });
    return json({ items: [] });
  });
  vi.stubGlobal("fetch", fetcher);
  vi.resetModules();
  await import("../scripts/portal.js");
  await vi.advanceTimersByTimeAsync(0);
  const form = document.querySelector<HTMLFormElement>("#affiliateAccessForm")!;
  form.querySelector<HTMLInputElement>('[name="wechatId"]')!.value = "QA_User";
  const submit = (intent: string) => form.dispatchEvent(new SubmitEvent("submit", { bubbles: true, cancelable: true, submitter: form.querySelector<HTMLButtonElement>(`button[value="${intent}"]`)! }));
  return { form, fetcher, submit };
}

describe("explicit affiliate registration and login", () => {
  beforeEach(() => { localStorage.clear(); vi.useFakeTimers(); vi.mocked(requestChallenge).mockReset().mockResolvedValue("qa-affiliate-token"); });
  afterEach(() => { window.dispatchEvent(new Event("pagehide")); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

  it("registers explicitly without a password and preserves the one-time generated password display", async () => {
    const { submit, fetcher, form } = await mount();
    submit("register");
    await vi.advanceTimersByTimeAsync(0);
    expect(requestChallenge).toHaveBeenCalledWith("affiliate_register");
    const request = fetcher.mock.calls.find(([path]) => path === "/api/affiliate/access")?.[1];
    expect(JSON.parse(String(request?.body))).toEqual({ intent: "register", wechatId: "QA_User", password: "", turnstileToken: "qa-affiliate-token" });
    expect(form.hidden).toBe(true);
    expect(document.querySelector("#generatedPasswordNotice")?.textContent).toContain("QA_PASSWORD1");
    expect(JSON.stringify(localStorage)).not.toContain("qa-affiliate-token");
  });

  it("does not turn a passwordless login into registration", async () => {
    const { submit, fetcher } = await mount();
    submit("login");
    await vi.advanceTimersByTimeAsync(0);
    expect(requestChallenge).not.toHaveBeenCalled();
    expect(fetcher.mock.calls.filter(([path]) => path === "/api/affiliate/access")).toHaveLength(0);
    expect(document.querySelector("#affiliateAccessError")?.textContent).toContain("请输入查询密码");
  });

  it("uses the login action and coalesces submissions while verification is pending", async () => {
    const { form, submit, fetcher } = await mount();
    form.querySelector<HTMLInputElement>('[name="password"]')!.value = "qa-existing-password";
    let resolve!: (token: string) => void;
    vi.mocked(requestChallenge).mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
    submit("login"); submit("register");
    expect(requestChallenge).toHaveBeenCalledExactlyOnceWith("affiliate_login");
    expect([...form.querySelectorAll<HTMLButtonElement>('button[type="submit"]')].every((button) => button.disabled)).toBe(true);
    resolve("qa-login-token");
    await vi.advanceTimersByTimeAsync(0);
    const requests = fetcher.mock.calls.filter(([path]) => path === "/api/affiliate/access");
    expect(requests).toHaveLength(1);
    expect(JSON.parse(String(requests[0][1]?.body))).toMatchObject({ intent: "login", password: "qa-existing-password", turnstileToken: "qa-login-token" });
  });
});
