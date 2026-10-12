// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrderHandoffController, orderHandoffStoragePrefix, type OrderHandoffRecord } from "./order-handoff.js";
import { mountOrderHandoffView, orderHandoffDescription } from "./order-handoff-view.js";

const stored: OrderHandoffRecord = {
  version: 1, requestKey: "95fc9bf1-b338-41c2-9a5e-f8ba8ad16c8c", input: { productSlug: "qa-product", recommenderWechatId: "QA_Ref" }, createdAt: 1, creationAttempted: true,
  order: { id: "qa-id", orderCode: "QA-001", productTitle: '<img src=x onerror="alert(1)">' },
  supportDelivery: { status: "sent", messageId: 42, conversationId: 7, bindingId: "qa-binding" }, phase: "action_required"
};

describe("order handoff visible recovery panel", () => {
  beforeEach(() => { localStorage.clear(); document.body.replaceChildren(); });
  afterEach(() => vi.restoreAllMocks());

  it("keeps saved-but-not-visible distinct from success and inserts server titles only as text", () => {
    localStorage.setItem(orderHandoffStoragePrefix + stored.requestKey, JSON.stringify(stored));
    const controller = new OrderHandoffController({ storage: localStorage, fetch: vi.fn(), waitForWidget: vi.fn() });
    const panel = document.createElement("section");
    document.body.append(panel);
    const cleanup = mountOrderHandoffView(panel, controller);
    expect(panel.hidden).toBe(false);
    expect(panel.querySelector("img")).toBeNull();
    expect(panel.querySelector("h3")?.textContent).toBe(stored.order?.productTitle);
    expect(panel.querySelector('[role="status"]')?.textContent).toContain("尚未确认在当前会话显示");
    expect(panel.querySelector("button")?.textContent).toBe("同步查看订单消息");
    expect(panel.closest("dialog")).toBeNull();
    cleanup();
  });

  it("runs the original request when recovery is clicked and cleans up listeners on unmount", () => {
    localStorage.setItem(orderHandoffStoragePrefix + stored.requestKey, JSON.stringify(stored));
    const controller = new OrderHandoffController({ storage: localStorage, fetch: vi.fn(), waitForWidget: vi.fn() });
    const run = vi.spyOn(controller, "run").mockResolvedValue(stored);
    const panel = document.createElement("section");
    document.body.append(panel);
    const cleanup = mountOrderHandoffView(panel, controller);
    const button = panel.querySelector<HTMLButtonElement>("button")!;
    button.click();
    expect(run).toHaveBeenCalledExactlyOnceWith(stored.requestKey);
    cleanup();
    button.click();
    expect(run).toHaveBeenCalledOnce();
  });

  it("reports invalid stored recovery data visibly instead of dropping it", () => {
    localStorage.setItem(orderHandoffStoragePrefix + stored.requestKey, "invalid-json");
    const controller = new OrderHandoffController({ storage: localStorage, fetch: vi.fn(), waitForWidget: vi.fn() });
    const panel = document.createElement("section");
    const cleanup = mountOrderHandoffView(panel, controller);
    expect(panel.hidden).toBe(false);
    expect(panel.querySelector('[role="alert"]')?.textContent).toContain("尚未创建新订单");
    expect(localStorage.getItem(orderHandoffStoragePrefix + stored.requestKey)).toBe("invalid-json");
    cleanup();
  });

  it("does not describe an unauthenticated restoration or a wrong conversation as delivery success", () => {
    expect(orderHandoffDescription({ ...stored, errorCode: "ORDER_SESSION_LOST" })).toContain("会话已失效");
    expect(orderHandoffDescription({ ...stored, errorCode: "SUPPORT_BINDING_MISMATCH" })).toContain("已停止发送");
    expect(orderHandoffDescription({ ...stored, phase: "delivered" })).toContain("已在当前会话中显示");
  });
});
