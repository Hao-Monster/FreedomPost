// @vitest-environment happy-dom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OrderHandoffController, orderHandoffStoragePrefix, type OrderHandoffInput, type OrderHandoffWidget, type SupportDelivery } from "./order-handoff.js";

const input: OrderHandoffInput = { productSlug: "qa-digital-service", recommenderWechatId: "QA_Recommender" };
// Deliberately synthetic valid UUIDs: request identifiers, not credentials.
const requestKey = "11111111-1111-4111-8111-111111111111";
const secondKey = "22222222-2222-4222-8222-222222222222";
const order = { id: "qa-order-id", orderCode: "QA-ORDER-001", productTitle: "QA digital service" };
const awaiting: SupportDelivery = { status: "awaiting_session" };
const receipt: SupportDelivery = { status: "sent", messageId: 117, conversationId: 11, bindingId: "qa-binding" };
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const apiError = (code: string, status: number) => response({ error: { code, message: "redacted" } }, status);

function fixture() {
  const events: string[] = [];
  let delivered = false;
  const fetcher = vi.fn<typeof fetch>(async (path, options) => {
    const url = String(path);
    events.push(`${options?.method ?? "GET"} ${url}`);
    if (url === "/api/orders/session") return response({ ready: true, csrfToken: "qa-memory-only-csrf" });
    if (url === "/api/orders") return response({ order, supportDelivery: awaiting }, 201);
    if (url === "/api/orders/recover") return response({ order, supportDelivery: delivered ? receipt : awaiting });
    if (url === `/api/orders/${order.id}/support-delivery`) {
      if (options?.method === "POST") delivered = true;
      return response({ supportDelivery: delivered ? receipt : awaiting });
    }
    throw new Error(`unexpected test URL: ${url}`);
  });
  const widget: OrderHandoffWidget = {
    ensureConversation: vi.fn(async () => { events.push("ensureConversation"); return { generation: "qa-generation", bindingId: "qa-binding" }; }),
    confirmMessage: vi.fn(async () => { events.push("confirmMessage"); })
  };
  const waitForWidget = vi.fn(async () => widget);
  const controller = new OrderHandoffController({ storage: localStorage, fetch: fetcher, waitForWidget, requestKey: () => requestKey });
  return { controller, fetcher, widget, waitForWidget, events };
}

async function finish<T>(promise: Promise<T>): Promise<T> {
  await vi.runAllTimersAsync();
  return promise;
}

describe("durable automatic order handoff", () => {
  beforeEach(() => { localStorage.clear(); vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

  it("persists the original input before any request, then opens, sends and confirms the same conversation", async () => {
    const { controller, fetcher, widget, events } = fixture();
    controller.createRequest(input);
    expect(fetcher).not.toHaveBeenCalled();
    expect(JSON.parse(localStorage.getItem(orderHandoffStoragePrefix + requestKey)!)).toMatchObject({ input, requestKey, creationAttempted: false });
    const result = await finish(controller.run(requestKey));

    expect(result.phase).toBe("delivered");
    expect(events).toEqual(["GET /api/orders/session", "POST /api/orders", "ensureConversation", `GET /api/orders/${order.id}/support-delivery`, `POST /api/orders/${order.id}/support-delivery`, "confirmMessage"]);
    expect(fetcher.mock.calls.find(([path]) => path === "/api/orders")?.[1]).toMatchObject({ credentials: "same-origin", cache: "no-store", headers: { "Idempotency-Key": requestKey, "X-Order-CSRF": "qa-memory-only-csrf" }, body: JSON.stringify(input) });
    expect(fetcher.mock.calls.find(([path, init]) => String(path).endsWith("support-delivery") && init?.method === "POST")?.[1]?.body).toBe(JSON.stringify({ expectedBindingId: "qa-binding" }));
    expect(widget.confirmMessage).toHaveBeenCalledWith(expect.objectContaining({ messageId: 117, conversationId: 11, bindingId: "qa-binding", generation: "qa-generation" }));
    expect(localStorage.getItem(orderHandoffStoragePrefix + requestKey)).not.toContain("qa-memory-only-csrf");
  });

  it("requires the owner's new cookie to be acknowledged before order creation", async () => {
    const { controller, fetcher } = fixture();
    fetcher.mockImplementationOnce(async () => response({ ready: false }));
    controller.createRequest(input);
    await finish(controller.run(requestKey));
    expect(fetcher.mock.calls.slice(0, 3).map(([path]) => path)).toEqual(["/api/orders/session", "/api/orders/session", "/api/orders"]);
  });

  it("does not create an order when the browser cannot return the owner cookie", async () => {
    const { controller, fetcher, waitForWidget } = fixture();
    fetcher.mockImplementation(async () => response({ ready: false }));
    controller.createRequest(input);
    const result = await finish(controller.run(requestKey));
    expect(result).toMatchObject({ phase: "action_required", errorCode: "ORDER_OWNER_REQUIRED", creationAttempted: false });
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(waitForWidget).not.toHaveBeenCalled();
  });

  it("coalesces duplicate clicks on one request while a distinct explicit request gets a new key", async () => {
    const { controller, fetcher, waitForWidget } = fixture();
    controller.createRequest(input);
    const first = controller.run(requestKey);
    const duplicate = controller.run(requestKey);
    expect(duplicate).toBe(first);
    await finish(first);
    const another = new OrderHandoffController({ storage: localStorage, fetch: fetcher, waitForWidget, requestKey: () => secondKey });
    another.createRequest(input);
    await finish(another.run(secondKey));
    const creates = fetcher.mock.calls.filter(([path]) => path === "/api/orders");
    expect(creates).toHaveLength(2);
    expect(creates.map(([, init]) => (init?.headers as Record<string, string>)["Idempotency-Key"])).toEqual([requestKey, secondKey]);
  });

  it("recovers an order after a lost create response without creating another order", async () => {
    const { controller, fetcher } = fixture();
    const normal = fetcher.getMockImplementation()!;
    fetcher.mockImplementation((path, init) => path === "/api/orders" ? Promise.reject(new TypeError("response lost")) : normal(path, init));
    controller.createRequest(input);
    const result = await finish(controller.run(requestKey));
    expect(result.phase).toBe("delivered");
    expect(fetcher.mock.calls.filter(([path]) => path === "/api/orders")).toHaveLength(1);
    expect(fetcher.mock.calls.filter(([path]) => path === "/api/orders/recover")).toHaveLength(1);
  });

  it("may repeat the same create only after a same-run authoritative recovery miss", async () => {
    const { controller, fetcher } = fixture();
    const normal = fetcher.getMockImplementation()!;
    let creates = 0;
    fetcher.mockImplementation((path, init) => {
      if (path === "/api/orders" && creates++ === 0) return Promise.reject(new TypeError("never reached server"));
      if (path === "/api/orders/recover") return Promise.resolve(apiError("ORDER_NOT_FOUND", 404));
      return normal(path, init);
    });
    controller.createRequest(input);
    expect((await finish(controller.run(requestKey))).phase).toBe("delivered");
    expect(fetcher.mock.calls.filter(([path]) => path === "/api/orders").map(([, init]) => (init?.headers as Record<string, string>)["Idempotency-Key"])).toEqual([requestKey, requestKey]);
  });

  it("restores a request with unknown order ID after refresh and never uses new input", async () => {
    const { controller, fetcher, waitForWidget } = fixture();
    controller.createRequest(input);
    const stored = JSON.parse(localStorage.getItem(orderHandoffStoragePrefix + requestKey)!);
    localStorage.setItem(orderHandoffStoragePrefix + requestKey, JSON.stringify({ ...stored, creationAttempted: true, phase: "creating" }));
    const restored = new OrderHandoffController({ storage: localStorage, fetch: fetcher, waitForWidget });
    await finish(restored.resumePending());
    expect(restored.get(requestKey)?.phase).toBe("delivered");
    expect(fetcher.mock.calls.filter(([path]) => path === "/api/orders")).toHaveLength(0);
    expect(fetcher.mock.calls.find(([path]) => path === "/api/orders/recover")?.[1]?.body).toBe(JSON.stringify({ requestKey }));
  });

  it("does not recreate a missing recovered order under a possibly replaced owner", async () => {
    const { controller, fetcher, waitForWidget } = fixture();
    controller.createRequest(input);
    const stored = JSON.parse(localStorage.getItem(orderHandoffStoragePrefix + requestKey)!);
    localStorage.setItem(orderHandoffStoragePrefix + requestKey, JSON.stringify({ ...stored, creationAttempted: true }));
    const normal = fetcher.getMockImplementation()!;
    fetcher.mockImplementation((path, init) => path === "/api/orders/recover" ? Promise.resolve(apiError("ORDER_NOT_FOUND", 404)) : normal(path, init));
    const restored = new OrderHandoffController({ storage: localStorage, fetch: fetcher, waitForWidget });
    expect(await finish(restored.run(requestKey))).toMatchObject({ errorCode: "ORDER_RECOVERY_NOT_FOUND", phase: "action_required" });
    expect(fetcher.mock.calls.some(([path]) => path === "/api/orders")).toBe(false);
  });

  it("stops recovery if the previous owner's cookie has been lost", async () => {
    const { controller, fetcher, waitForWidget } = fixture();
    controller.createRequest(input);
    await finish(controller.run(requestKey));
    fetcher.mockClear();
    fetcher.mockImplementationOnce(async () => response({ ready: false }));
    const restored = new OrderHandoffController({ storage: localStorage, fetch: fetcher, waitForWidget });
    expect(await finish(restored.run(requestKey))).toMatchObject({ errorCode: "ORDER_SESSION_LOST" });
    expect(fetcher.mock.calls.every(([path]) => path === "/api/orders/session")).toBe(true);
  });

  it("waits beyond five seconds for the widget and still delivers the original order", async () => {
    const { controller, waitForWidget, widget } = fixture();
    waitForWidget.mockImplementation(() => new Promise((resolve) => setTimeout(() => resolve(widget), 6500)));
    controller.createRequest(input);
    expect((await finish(controller.run(requestKey))).phase).toBe("delivered");
    expect(waitForWidget).toHaveBeenCalledOnce();
  });

  it("bounds unavailable widget attempts to three and the foreground budget to thirty seconds", async () => {
    const { controller, waitForWidget, fetcher } = fixture();
    waitForWidget.mockImplementation(() => new Promise(() => {}));
    controller.createRequest(input);
    const start = Date.now();
    const result = await finish(controller.run(requestKey));
    expect(result).toMatchObject({ order, phase: "action_required", errorCode: "HANDOFF_TIMEOUT" });
    expect(waitForWidget).toHaveBeenCalledTimes(3);
    expect(Date.now() - start).toBeLessThanOrEqual(30000);
    expect(fetcher.mock.calls.some(([path]) => String(path).endsWith("support-delivery"))).toBe(false);
  });

  it("does not resend a saved message when visibility confirmation fails or is retried manually", async () => {
    const { controller, widget, fetcher } = fixture();
    vi.mocked(widget.confirmMessage).mockRejectedValue(new Error("display unavailable"));
    controller.createRequest(input);
    const failed = await finish(controller.run(requestKey));
    expect(failed).toMatchObject({ phase: "action_required", supportDelivery: receipt });
    vi.mocked(widget.confirmMessage).mockResolvedValue(undefined);
    expect((await finish(controller.run(requestKey))).phase).toBe("delivered");
    expect(fetcher.mock.calls.filter(([path, init]) => String(path).endsWith("support-delivery") && init?.method === "POST")).toHaveLength(1);
    expect(widget.confirmMessage).toHaveBeenCalledTimes(4);
  });

  it("reconciles a lost delivery response by reading the receipt before any further send", async () => {
    const { controller, fetcher } = fixture();
    const normal = fetcher.getMockImplementation()!;
    fetcher.mockImplementation(async (path, init) => {
      const result = await normal(path, init);
      if (String(path).endsWith("support-delivery") && init?.method === "POST") throw new TypeError("saved but response lost");
      return result;
    });
    controller.createRequest(input);
    expect((await finish(controller.run(requestKey))).phase).toBe("delivered");
    expect(fetcher.mock.calls.filter(([path, init]) => String(path).endsWith("support-delivery") && init?.method === "POST")).toHaveLength(1);
  });

  it.each(["ORDER_CSRF_INVALID", "SUPPORT_BINDING_MISMATCH", "IDEMPOTENCY_CONFLICT"])("stops unsafe auto-retry on %s", async (code) => {
    const { controller, fetcher } = fixture();
    const normal = fetcher.getMockImplementation()!;
    fetcher.mockImplementation((path, init) => String(path).endsWith("support-delivery") && init?.method === "POST" ? Promise.resolve(apiError(code, 409)) : normal(path, init));
    controller.createRequest(input);
    expect(await finish(controller.run(requestKey))).toMatchObject({ phase: "action_required", errorCode: code });
    expect(fetcher.mock.calls.filter(([path, init]) => String(path).endsWith("support-delivery") && init?.method === "POST")).toHaveLength(1);
  });

  it("does not reopen the widget after the customer closes the handoff", async () => {
    const { controller, widget } = fixture();
    vi.mocked(widget.ensureConversation).mockRejectedValue(Object.assign(new Error("closed"), { code: "HANDOFF_CLOSED" }));
    controller.createRequest(input);
    expect(await finish(controller.run(requestKey))).toMatchObject({ phase: "action_required", errorCode: "HANDOFF_CLOSED" });
    await finish(controller.resumePending());
    expect(widget.ensureConversation).toHaveBeenCalledOnce();
  });

  it.each(["delivery-query", "delivery-send", "retry-backoff"])("keeps a user close during %s paused across reload until manual recovery of the same order", async (boundary) => {
    const { controller, fetcher, widget, waitForWidget } = fixture();
    const normal = fetcher.getMockImplementation()!;
    let closed = false;
    fetcher.mockImplementation(async (path, init) => {
      const delivery = String(path).endsWith("support-delivery");
      const isPost = init?.method === "POST";
      const atBoundary = boundary === "delivery-query" ? !isPost : isPost;
      if (delivery && atBoundary && !closed) {
        closed = true;
        if (isPost) await normal(path, init);
        if (boundary === "retry-backoff") setTimeout(() => controller.pause("HANDOFF_CLOSED"), 100);
        else controller.pause("HANDOFF_CLOSED");
        throw new TypeError("response lost around customer close");
      }
      return normal(path, init);
    });
    controller.createRequest(input);
    expect(await finish(controller.run(requestKey))).toMatchObject({ phase: "action_required", errorCode: "HANDOFF_CLOSED", order });
    expect(widget.ensureConversation).toHaveBeenCalledOnce();
    const callsBeforeReload = fetcher.mock.calls.length;
    const restored = new OrderHandoffController({ storage: localStorage, fetch: fetcher, waitForWidget });
    await finish(restored.resumePending());
    expect(fetcher).toHaveBeenCalledTimes(callsBeforeReload);
    expect(widget.ensureConversation).toHaveBeenCalledOnce();

    expect((await finish(restored.run(requestKey))).phase).toBe("delivered");
    expect(widget.ensureConversation).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls.filter(([path]) => path === "/api/orders")).toHaveLength(1);
    expect(fetcher.mock.calls.filter(([path, init]) => String(path).endsWith("support-delivery") && init?.method === "POST")).toHaveLength(1);
    expect(widget.confirmMessage).toHaveBeenCalledOnce();
  });

  it("cancels pending work on navigation without a late send or widget reopen", async () => {
    const { controller, waitForWidget, fetcher } = fixture();
    waitForWidget.mockImplementation(() => new Promise(() => {}));
    controller.createRequest(input);
    const running = controller.run(requestKey);
    await vi.advanceTimersByTimeAsync(0);
    controller.pause();
    const result = await finish(running);
    expect(result).toMatchObject({ phase: "action_required", errorCode: "HANDOFF_CANCELLED" });
    expect(waitForWidget).toHaveBeenCalledOnce();
    expect(fetcher.mock.calls.some(([path]) => String(path).endsWith("support-delivery"))).toBe(false);
  });

  it("requires the new acknowledged SDK and never falls back to toggle or setUser", async () => {
    const { controller, waitForWidget, fetcher } = fixture();
    const legacy = { toggle: vi.fn(), setUser: vi.fn() };
    waitForWidget.mockResolvedValue(legacy as unknown as OrderHandoffWidget);
    controller.createRequest(input);
    expect(await finish(controller.run(requestKey))).toMatchObject({ phase: "action_required", errorCode: "SDK_UPGRADE_REQUIRED" });
    expect(legacy.toggle).not.toHaveBeenCalled();
    expect(legacy.setUser).not.toHaveBeenCalled();
    expect(fetcher.mock.calls.some(([path]) => String(path).endsWith("support-delivery"))).toBe(false);
  });

  it("resumes navigation-interrupted records after reload but does not recreate their orders", async () => {
    const { controller, fetcher, waitForWidget, widget } = fixture();
    waitForWidget.mockImplementation(() => new Promise(() => {}));
    controller.createRequest(input);
    const running = controller.run(requestKey);
    await vi.advanceTimersByTimeAsync(0);
    controller.pause();
    await finish(running);
    waitForWidget.mockResolvedValue(widget);
    const restored = new OrderHandoffController({ storage: localStorage, fetch: fetcher, waitForWidget });
    await finish(restored.resumePending());
    expect(restored.get(requestKey)?.phase).toBe("delivered");
    expect(fetcher.mock.calls.filter(([path]) => path === "/api/orders")).toHaveLength(1);
  });

  it("never resends when recovery contradicts an already observed message receipt", async () => {
    const { controller, fetcher } = fixture();
    controller.createRequest(input);
    await finish(controller.run(requestKey));
    const normal = fetcher.getMockImplementation()!;
    fetcher.mockImplementation((path, init) => path === "/api/orders/recover" ? Promise.resolve(response({ order, supportDelivery: awaiting })) : normal(path, init));
    expect(await finish(controller.run(requestKey))).toMatchObject({ phase: "action_required", errorCode: "INVALID_RESPONSE", supportDelivery: receipt });
    expect(fetcher.mock.calls.filter(([path, init]) => String(path).endsWith("support-delivery") && init?.method === "POST")).toHaveLength(1);
  });

  it("includes time queued behind another handoff in each request's thirty second budget", async () => {
    const { fetcher, waitForWidget } = fixture();
    waitForWidget.mockImplementation(() => new Promise(() => {}));
    const keys = [requestKey, secondKey];
    const controller = new OrderHandoffController({ storage: localStorage, fetch: fetcher, waitForWidget, requestKey: () => keys.shift()! });
    controller.createRequest(input);
    controller.createRequest(input);
    const start = Date.now();
    const requests = [controller.run(requestKey), controller.run(secondKey)];
    await finish(Promise.all(requests));
    expect(Date.now() - start).toBeLessThanOrEqual(30000);
    expect(controller.get(requestKey)?.phase).toBe("action_required");
    expect(controller.get(secondKey)?.phase).toBe("action_required");
  });

  it("does not call an API if durable intent storage is unavailable", () => {
    const { fetcher, waitForWidget } = fixture();
    const brokenStorage = { getItem: () => null, key: () => null, removeItem: () => {}, length: 0, setItem: () => { throw new Error("quota exceeded"); } };
    const controller = new OrderHandoffController({ storage: brokenStorage, fetch: fetcher, waitForWidget });
    expect(() => controller.createRequest(input)).toThrow("RECOVERY_STORAGE_UNAVAILABLE");
    expect(fetcher).not.toHaveBeenCalled();
  });
});
