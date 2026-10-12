// @vitest-environment happy-dom

import { afterEach, describe, expect, it, vi } from "vitest";
import { referralStorageKey } from "./article-links.js";
import market from "../pages/market.astro?raw";

vi.mock("lucide", () => ({ createIcons: vi.fn(), icons: {} }));

describe("market form to acknowledged support handoff", () => {
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); window.dispatchEvent(new Event("pagehide")); delete window.$chatwoot; });

  it.each([false, true])("handles the real form with clipboard denied (closed during send: %s)", async (closeDuringSend) => {
    vi.useFakeTimers();
    localStorage.clear();
    localStorage.setItem(referralStorageKey, "QA_Recommender");
    document.body.innerHTML = market.slice(market.indexOf("  <section"), market.lastIndexOf("</PortalShell>"));
    vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => {});
    const clipboard = vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("permission denied"));
    const order = { id: "qa-real-form-order", orderCode: "QA-FORM-001", productTitle: "QA 商品" };
    const calls: string[] = [];
    const confirmMessage = vi.fn(async () => { calls.push("confirmed"); });
    const ensureConversation = vi.fn(async () => { calls.push("opened"); return { generation: "qa-generation", bindingId: "qa-binding" }; });
    const setUser = vi.fn();
    window.$chatwoot = { hasLoaded: true, toggle: vi.fn(), setUser, ensureConversation, confirmMessage };
    const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
    const fetcher = vi.fn<typeof fetch>(async (rawPath, init) => {
      const path = new URL(String(rawPath), location.origin).pathname;
      if (path === "/api/products") return json({ items: [{ id: "qa-product", slug: "qa-product", title: "QA 商品", priceCents: 100, commissionCents: 0, currency: "CNY", stock: -1, soldCount: 0, status: "published" }] });
      if (path === "/api/orders/session") return json({ ready: true, csrfToken: "qa-csrf" });
      if (path === "/api/orders") { calls.push("created"); return json({ order, supportDelivery: { status: "awaiting_session" } }, 201); }
      if (path.endsWith("/support-delivery")) {
        if (init?.method === "POST") {
          calls.push("sent");
          if (closeDuringSend) {
            window.dispatchEvent(new CustomEvent("chatwoot:closed"));
            throw new TypeError("connection lost after the customer closed support");
          }
          return json({ supportDelivery: { status: "sent", messageId: 10, conversationId: 7, bindingId: "qa-binding" } });
        }
        return json({ supportDelivery: { status: "awaiting_session" } });
      }
      return json({});
    });
    vi.stubGlobal("fetch", fetcher);
    vi.resetModules();
    await import("../scripts/portal.js");
    await vi.advanceTimersByTimeAsync(0);
    const orderButton = document.querySelector<HTMLButtonElement>('[data-market-action="order"]')!;
    expect(orderButton).not.toBeNull();
    orderButton.click();
    const dialog = document.querySelector<HTMLDialogElement>("#orderDialog")!;
    const form = document.querySelector<HTMLFormElement>("#affiliateOrderForm")!;
    expect(dialog.open).toBe(true);
    expect(new FormData(form).get("recommenderWechatId")).toBe("QA_Recommender");
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await vi.runAllTimersAsync();

    expect(calls).toEqual(closeDuringSend ? ["created", "opened", "sent"] : ["created", "opened", "sent", "confirmed"]);
    expect(dialog.open).toBe(false);
    const panel = document.querySelector<HTMLElement>("#marketOrderHandoffs")!;
    expect(panel.hidden).toBe(false);
    expect(panel.closest("dialog")).toBeNull();
    expect(panel.textContent).toContain("QA-FORM-001");
    expect(panel.textContent).toContain(closeDuringSend ? "客服交接已暂停" : "已在当前会话中显示");
    expect(clipboard).not.toHaveBeenCalled();
    expect(setUser).not.toHaveBeenCalled();
    expect(fetcher.mock.calls.filter(([path]) => path === "/api/orders")).toHaveLength(1);
    expect(ensureConversation).toHaveBeenCalledOnce();
    expect(confirmMessage).toHaveBeenCalledTimes(closeDuringSend ? 0 : 1);
    if (closeDuringSend) {
      const saved = Object.keys(localStorage).find((key) => key.startsWith("fp_order_handoff_v2:"));
      expect(saved).toBeTruthy();
      expect(JSON.parse(localStorage.getItem(saved!)!)).toMatchObject({ errorCode: "HANDOFF_CLOSED" });
    }
  });
});
