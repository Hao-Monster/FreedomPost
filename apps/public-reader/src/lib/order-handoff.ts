import { requestChallenge } from "./public-challenge.js";

export type OrderHandoffInput = {
  productSlug: string;
  recommenderWechatId: string;
};

export type HandoffOrder = { id: string; orderCode: string; productTitle: string };
export type SupportDeliveryStatus = "awaiting_session" | "sending" | "sent" | "retry_wait" | "reconciling" | "action_required";
export type SupportDelivery = {
  status: SupportDeliveryStatus;
  messageId?: number;
  conversationId?: number;
  bindingId?: string;
};
export type OrderHandoffPhase = "saved" | "checking" | "creating" | "connecting" | "sending" | "syncing" | "retry_wait" | "delivered" | "action_required";
export type OrderHandoffRecord = {
  version: 1;
  requestKey: string;
  input: OrderHandoffInput;
  createdAt: number;
  creationAttempted: boolean;
  order?: HandoffOrder;
  supportDelivery?: SupportDelivery;
  phase: OrderHandoffPhase;
  errorCode?: string;
};

export interface OrderHandoffWidget {
  ensureConversation(options: { timeoutMs: number; signal: AbortSignal }): Promise<{ generation: string; bindingId: string }>;
  confirmMessage(options: { messageId: number; generation: string; bindingId: string; conversationId?: number; timeoutMs: number; signal: AbortSignal }): Promise<void>;
}

type HandoffStorage = Pick<Storage, "getItem" | "setItem" | "removeItem" | "key" | "length">;
type HandoffOptions = {
  storage: HandoffStorage;
  fetch: typeof fetch;
  waitForWidget: (options: { timeoutMs: number; signal: AbortSignal }) => Promise<OrderHandoffWidget>;
  requestKey?: () => string;
};

export const orderHandoffStoragePrefix = "fp_order_handoff_v2:";
const maximumRecords = 20;
const maximumAttempts = 3;
const runBudgetMs = 30000;
const sdkStageMs = 8000;
const requestStageMs = 5000;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const deliveryStatuses = new Set<SupportDeliveryStatus>(["awaiting_session", "sending", "sent", "retry_wait", "reconciling", "action_required"]);
const stoppedCodes = new Set(["HANDOFF_CLOSED", "HANDOFF_CANCELLED", "SUPPORT_BINDING_MISMATCH", "ORDER_OWNER_REQUIRED", "ORDER_CSRF_INVALID", "ORDER_SESSION_LOST", "ORDER_RECOVERY_NOT_FOUND", "IDEMPOTENCY_CONFLICT", "SDK_UPGRADE_REQUIRED", "INVALID_RESPONSE", "RECOVERY_STORAGE_UNAVAILABLE"]);

class HandoffError extends Error {
  constructor(readonly code: string, readonly retryable = false) {
    super(code);
  }
}

/** Durable client intent; authentication and both idempotency decisions stay on the servers. */
export class OrderHandoffController {
  private readonly records = new Map<string, OrderHandoffRecord>();
  private readonly listeners = new Set<() => void>();
  private readonly active = new Map<string, { promise: Promise<OrderHandoffRecord>; abort: AbortController }>();
  private queue: Promise<unknown> = Promise.resolve();
  private storageError?: string;

  constructor(private readonly options: HandoffOptions) {
    try {
      for (const record of this.readStoredRecords()) this.records.set(record.requestKey, record);
    } catch (reason) {
      this.storageError = failureCode(reason);
    }
  }

  list(): OrderHandoffRecord[] {
    return [...this.records.values()].sort((a, b) => b.createdAt - a.createdAt).map(copyRecord);
  }

  get(requestKey: string): OrderHandoffRecord | undefined {
    const record = this.records.get(requestKey);
    return record && copyRecord(record);
  }

  getStorageError(): string | undefined { return this.storageError; }
  isRunning(requestKey: string): boolean { return this.active.has(requestKey); }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    listener();
    return () => this.listeners.delete(listener);
  }

  createRequest(input: OrderHandoffInput): string {
    if (this.storageError) throw new HandoffError(this.storageError);
    const normalized = { productSlug: input.productSlug.trim(), recommenderWechatId: input.recommenderWechatId.trim() };
    if (!normalized.productSlug || normalized.productSlug.length > 64 || !normalized.recommenderWechatId || normalized.recommenderWechatId.length > 32) {
      throw new HandoffError("INVALID_ORDER");
    }
    const stored = this.readStoredRecords();
    // Per-request entries keep a different tab's new order from being overwritten by an index update.
    const completed = stored.filter((record) => record.phase === "delivered").sort((a, b) => a.createdAt - b.createdAt);
    let count = stored.length;
    while (count >= maximumRecords && completed.length) {
      const oldest = completed.shift()!;
      this.options.storage.removeItem(orderHandoffStoragePrefix + oldest.requestKey);
      this.records.delete(oldest.requestKey);
      count--;
    }
    if (count >= maximumRecords) throw new HandoffError("TOO_MANY_PENDING_ORDERS");
    const requestKey = this.options.requestKey?.() ?? crypto.randomUUID();
    if (!uuidPattern.test(requestKey) || this.records.has(requestKey) || this.options.storage.getItem(orderHandoffStoragePrefix + requestKey) !== null) {
      throw new HandoffError("INVALID_REQUEST_KEY");
    }
    const record: OrderHandoffRecord = { version: 1, requestKey, input: normalized, createdAt: Date.now(), creationAttempted: false, phase: "saved" };
    this.persist(record);
    this.records.set(requestKey, record);
    this.emit();
    return requestKey;
  }

  run(requestKey: string): Promise<OrderHandoffRecord> {
    const existing = this.active.get(requestKey);
    if (existing) return existing.promise;
    const record = this.records.get(requestKey);
    if (!record) return Promise.reject(new HandoffError("ORDER_RECOVERY_NOT_FOUND"));
    const abort = new AbortController();
    const deadline = Date.now() + runBudgetMs;
    const promise = this.queue.then(() => this.perform(record, abort.signal, deadline)).finally(() => {
      this.active.delete(requestKey);
      this.emit();
    });
    this.active.set(requestKey, { promise, abort });
    this.queue = promise.catch(() => {});
    this.emit();
    return promise;
  }

  async resumePending(): Promise<void> {
    for (const record of this.list().reverse()) {
      if (record.phase === "delivered" || (record.errorCode && record.errorCode !== "HANDOFF_CANCELLED" && stoppedCodes.has(record.errorCode))) continue;
      const result = await this.run(record.requestKey);
      if (result.errorCode === "HANDOFF_CANCELLED" || result.errorCode === "HANDOFF_CLOSED") break;
    }
  }

  pause(reason: "HANDOFF_CANCELLED" | "HANDOFF_CLOSED" = "HANDOFF_CANCELLED"): void {
    for (const task of this.active.values()) task.abort.abort(new HandoffError(reason));
  }

  private async perform(record: OrderHandoffRecord, signal: AbortSignal, deadline: number): Promise<OrderHandoffRecord> {
    const isRecovery = record.creationAttempted;
    let csrfToken: string | undefined;
    let mayRepeatCreate = !isRecovery;
    let mustRecover = isRecovery;
    for (let attempt = 0; attempt < maximumAttempts; attempt++) {
      try {
        if (signal.aborted) throw cancellationError(signal);
        this.change(record, { phase: record.order ? "connecting" : "checking", errorCode: undefined });
        if (!csrfToken) {
          const owner = await this.ensureOwner(deadline, signal);
          if (owner.renewed && isRecovery) throw new HandoffError("ORDER_SESSION_LOST");
          csrfToken = owner.csrfToken;
        }
        if (mustRecover) {
          try {
            const result = await this.request("/api/orders/recover", { method: "POST", headers: this.headers(csrfToken), body: JSON.stringify({ requestKey: record.requestKey }) }, deadline, signal);
            this.adoptOrder(record, result);
          } catch (reason) {
            if (failureCode(reason) !== "ORDER_NOT_FOUND") throw reason;
            // On a reloaded page we cannot prove that a different tab has not replaced the owner cookie.
            // A missing recovery never becomes an order under a newly acquired owner.
            if (!mayRepeatCreate || record.order) throw new HandoffError("ORDER_RECOVERY_NOT_FOUND");
          }
          mustRecover = false;
        }
        if (!record.order) {
          // Recovery runs first: a single-use token is only obtained for a real
          // creation attempt, never for resuming an already persisted order.
          const challengeStarted = Date.now();
          let turnstileToken: string;
          try {
            turnstileToken = await requestChallenge("order_create", { signal });
          } catch {
            // A dismissed or unavailable challenge needs user action, not three
            // automatic dialogs from the support network retry loop.
            throw new HandoffError("CHALLENGE_NOT_COMPLETED");
          } finally {
            // Human verification has its own timeout; retain the existing budget
            // for the order request and support handoff after it completes.
            deadline += Date.now() - challengeStarted;
          }
          const previouslyAttempted = record.creationAttempted;
          this.change(record, { creationAttempted: true, phase: "creating" });
          mayRepeatCreate = true;
          mustRecover = true;
          let result: Record<string, unknown>;
          try {
            result = await this.request("/api/orders", {
              method: "POST", headers: { ...this.headers(csrfToken), "Idempotency-Key": record.requestKey }, body: JSON.stringify({ ...record.input, turnstileToken })
            }, deadline, signal);
          } catch (reason) {
            // These responses are emitted before creation. A first rejected
            // attempt remains retryable with a fresh token after a page reload.
            // Never clear uncertainty left by an earlier lost response.
            if (!previouslyAttempted && reason instanceof HandoffError && ["CHALLENGE_REJECTED", "CHALLENGE_UNAVAILABLE", "RATE_LIMITED"].includes(reason.code)) {
              this.change(record, { creationAttempted: false });
              mustRecover = false;
            }
            throw reason;
          }
          this.adoptOrder(record, result);
          mustRecover = false;
        }
        const order = record.order;
        if (!order) throw new HandoffError("INVALID_RESPONSE");
        this.change(record, { phase: "connecting" });
        const widget = await this.stage((stageSignal, timeoutMs) => this.options.waitForWidget({ timeoutMs, signal: stageSignal }), deadline, signal, sdkStageMs);
        if (typeof widget.ensureConversation !== "function" || typeof widget.confirmMessage !== "function") throw new HandoffError("SDK_UPGRADE_REQUIRED");
        const conversation = await this.stage((stageSignal, timeoutMs) => widget.ensureConversation({ timeoutMs, signal: stageSignal }), deadline, signal, sdkStageMs);
        if (!conversation.generation || !conversation.bindingId) throw new HandoffError("INVALID_RESPONSE");
        const deliveryPath = `/api/orders/${encodeURIComponent(order.id)}/support-delivery`;
        const result = await this.request(deliveryPath, {}, deadline, signal);
        this.adoptDelivery(record, result);
        if (record.supportDelivery?.bindingId && record.supportDelivery.bindingId !== conversation.bindingId) throw new HandoffError("SUPPORT_BINDING_MISMATCH");
        if (record.supportDelivery?.status !== "sent") {
          this.change(record, { phase: "sending" });
          const delivery = await this.request(deliveryPath, {
            method: "POST", headers: this.headers(csrfToken), body: JSON.stringify({ expectedBindingId: conversation.bindingId })
          }, deadline, signal);
          this.adoptDelivery(record, delivery);
        }
        const receipt = record.supportDelivery;
        if (receipt?.status !== "sent") throw new HandoffError(receipt?.status === "action_required" ? "DELIVERY_ACTION_REQUIRED" : "DELIVERY_PENDING", receipt?.status !== "action_required");
        if (receipt.bindingId && receipt.bindingId !== conversation.bindingId) throw new HandoffError("SUPPORT_BINDING_MISMATCH");
        if (!receipt.messageId) throw new HandoffError("INVALID_RESPONSE");
        this.change(record, { phase: "syncing" });
        await this.stage((stageSignal, timeoutMs) => widget.confirmMessage({
          messageId: receipt.messageId!, conversationId: receipt.conversationId,
          generation: conversation.generation, bindingId: conversation.bindingId, timeoutMs, signal: stageSignal
        }), deadline, signal, sdkStageMs);
        this.change(record, { phase: "delivered", errorCode: undefined });
        return copyRecord(record);
      } catch (reason) {
        const code = signal.aborted ? cancellationError(signal).code : failureCode(reason);
        const canRetry = !signal.aborted && isRetryable(reason) && attempt + 1 < maximumAttempts && Date.now() < deadline;
        this.changeAfterFailure(record, canRetry ? "retry_wait" : "action_required", code);
        if (!canRetry) return copyRecord(record);
        try {
          await this.stage((stageSignal) => waitDelay((attempt + 1) * 500, stageSignal), deadline, signal, (attempt + 1) * 500 + 100);
        } catch (delayError) {
          this.changeAfterFailure(record, "action_required", signal.aborted ? cancellationError(signal).code : failureCode(delayError));
          return copyRecord(record);
        }
      }
    }
    return copyRecord(record);
  }

  private async ensureOwner(deadline: number, signal: AbortSignal): Promise<{ csrfToken: string; renewed: boolean }> {
    let renewed = false;
    for (let index = 0; index < 2; index++) {
      const result = await this.request("/api/orders/session", {}, deadline, signal);
      if (result.ready === true && typeof result.csrfToken === "string" && result.csrfToken.length > 0) return { csrfToken: result.csrfToken, renewed };
      if (result.ready !== false) throw new HandoffError("INVALID_RESPONSE");
      renewed = true;
    }
    throw new HandoffError("ORDER_OWNER_REQUIRED");
  }

  private headers(csrfToken: string): Record<string, string> {
    return { "Content-Type": "application/json", "X-Order-CSRF": csrfToken };
  }

  private async request(path: string, init: RequestInit, deadline: number, signal: AbortSignal): Promise<Record<string, unknown>> {
    return this.stage(async (stageSignal) => {
      const response = await this.options.fetch(path, { ...init, credentials: "same-origin", cache: "no-store", signal: stageSignal, headers: { Accept: "application/json", ...init.headers } });
      const data: unknown = await response.json();
      if (!isObject(data)) throw new HandoffError("INVALID_RESPONSE", response.status >= 500);
      if (!response.ok) {
        const code = isObject(data.error) && typeof data.error.code === "string" ? data.error.code : "REQUEST_FAILED";
        throw new HandoffError(code, response.status >= 500 || response.status === 408 || response.status === 429);
      }
      return data;
    }, deadline, signal, requestStageMs);
  }

  private adoptOrder(record: OrderHandoffRecord, data: Record<string, unknown>): void {
    const order = parseOrder(data.order);
    if (record.order && record.order.id !== order.id) throw new HandoffError("INVALID_RESPONSE");
    const supportDelivery = parseDelivery(data.supportDelivery);
    if (record.supportDelivery?.status === "sent" && (supportDelivery.status !== "sent" || supportDelivery.messageId !== record.supportDelivery.messageId)) throw new HandoffError("INVALID_RESPONSE");
    this.change(record, { order, supportDelivery, phase: "connecting" });
  }

  private adoptDelivery(record: OrderHandoffRecord, data: Record<string, unknown>): void {
    const supportDelivery = parseDelivery(data.supportDelivery);
    // Once a real receipt has been observed, a contradictory response must never cause a resend.
    if (record.supportDelivery?.status === "sent" && (supportDelivery.status !== "sent" || supportDelivery.messageId !== record.supportDelivery.messageId)) throw new HandoffError("INVALID_RESPONSE");
    this.change(record, { supportDelivery });
  }

  private stage<T>(operation: (signal: AbortSignal, timeoutMs: number) => Promise<T>, deadline: number, parent: AbortSignal, maximumMs: number): Promise<T> {
    const timeoutMs = Math.min(deadline - Date.now(), maximumMs);
    if (parent.aborted) return Promise.reject(cancellationError(parent));
    if (timeoutMs <= 0) return Promise.reject(new HandoffError("HANDOFF_TIMEOUT", true));
    const abort = new AbortController();
    return new Promise<T>((resolve, reject) => {
      let settled = false;
      const finish = (complete: () => void) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        parent.removeEventListener("abort", onAbort);
        complete();
      };
      const onAbort = () => finish(() => {
        const reason = cancellationError(parent);
        abort.abort(reason);
        reject(reason);
      });
      const timer = setTimeout(() => finish(() => { abort.abort(); reject(new HandoffError("HANDOFF_TIMEOUT", true)); }), timeoutMs);
      parent.addEventListener("abort", onAbort, { once: true });
      Promise.resolve().then(() => operation(abort.signal, timeoutMs)).then((value) => finish(() => resolve(value)), (reason: unknown) => finish(() => reject(reason)));
    });
  }

  private change(record: OrderHandoffRecord, changes: Partial<OrderHandoffRecord>): void {
    Object.assign(record, changes);
    this.persist(record);
    this.emit();
  }

  private changeAfterFailure(record: OrderHandoffRecord, phase: OrderHandoffPhase, errorCode: string): void {
    Object.assign(record, { phase, errorCode });
    try { this.persist(record); } catch { record.errorCode = "RECOVERY_STORAGE_UNAVAILABLE"; }
    this.emit();
  }

  private persist(record: OrderHandoffRecord): void {
    try { this.options.storage.setItem(orderHandoffStoragePrefix + record.requestKey, JSON.stringify(record)); }
    catch { throw new HandoffError("RECOVERY_STORAGE_UNAVAILABLE"); }
  }

  private readStoredRecords(): OrderHandoffRecord[] {
    const records: OrderHandoffRecord[] = [];
    try {
      for (let index = 0; index < this.options.storage.length; index++) {
        const key = this.options.storage.key(index);
        if (!key?.startsWith(orderHandoffStoragePrefix)) continue;
        const value: unknown = JSON.parse(this.options.storage.getItem(key) ?? "null");
        if (!isObject(value) || value.version !== 1 || typeof value.requestKey !== "string" || !uuidPattern.test(value.requestKey) || key !== orderHandoffStoragePrefix + value.requestKey || !isObject(value.input) || typeof value.input.productSlug !== "string" || typeof value.input.recommenderWechatId !== "string" || typeof value.createdAt !== "number" || typeof value.creationAttempted !== "boolean") throw new HandoffError("RECOVERY_DATA_INVALID");
        const phase = value.phase === "delivered" ? "delivered" : value.phase === "action_required" ? "action_required" : "saved";
        records.push({ version: 1, requestKey: value.requestKey, input: { productSlug: value.input.productSlug, recommenderWechatId: value.input.recommenderWechatId }, createdAt: value.createdAt, creationAttempted: value.creationAttempted, phase,
          ...(value.order ? { order: parseOrder(value.order) } : {}), ...(value.supportDelivery ? { supportDelivery: parseDelivery(value.supportDelivery) } : {}), ...(typeof value.errorCode === "string" ? { errorCode: value.errorCode } : {}) });
      }
    } catch (reason) {
      if (reason instanceof HandoffError) throw reason;
      throw new HandoffError("RECOVERY_STORAGE_UNAVAILABLE");
    }
    return records;
  }

  private emit(): void { for (const listener of this.listeners) listener(); }
}

function copyRecord(record: OrderHandoffRecord): OrderHandoffRecord {
  return { ...record, input: { ...record.input }, ...(record.order ? { order: { ...record.order } } : {}), ...(record.supportDelivery ? { supportDelivery: { ...record.supportDelivery } } : {}) };
}
function isObject(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }
function parseOrder(value: unknown): HandoffOrder {
  if (!isObject(value) || typeof value.id !== "string" || !value.id || typeof value.orderCode !== "string" || !value.orderCode || typeof value.productTitle !== "string" || !value.productTitle) throw new HandoffError("INVALID_RESPONSE");
  return { id: value.id, orderCode: value.orderCode, productTitle: value.productTitle };
}
function parseDelivery(value: unknown): SupportDelivery {
  if (!isObject(value) || typeof value.status !== "string" || !deliveryStatuses.has(value.status as SupportDeliveryStatus)) throw new HandoffError("INVALID_RESPONSE");
  const delivery: SupportDelivery = { status: value.status as SupportDeliveryStatus };
  for (const field of ["messageId", "conversationId"] as const) {
    if (value[field] !== undefined && value[field] !== null) {
      if (typeof value[field] !== "number" || !Number.isSafeInteger(value[field]) || value[field] <= 0) throw new HandoffError("INVALID_RESPONSE");
      delivery[field] = value[field];
    }
  }
  if (value.bindingId !== undefined && value.bindingId !== null) {
    if (typeof value.bindingId !== "string" || !value.bindingId) throw new HandoffError("INVALID_RESPONSE");
    delivery.bindingId = value.bindingId;
  }
  if (delivery.status === "sent" && !delivery.messageId) throw new HandoffError("INVALID_RESPONSE");
  return delivery;
}
function failureCode(reason: unknown): string {
  if (isObject(reason) && typeof reason.code === "string") return reason.code;
  if (reason instanceof Error && stoppedCodes.has(reason.message)) return reason.message;
  return "CONNECTION_FAILED";
}
function isRetryable(reason: unknown): boolean {
  const code = failureCode(reason);
  if (stoppedCodes.has(code)) return false;
  return reason instanceof HandoffError ? reason.retryable : true;
}
function cancellationError(signal: AbortSignal): HandoffError {
  return new HandoffError(failureCode(signal.reason) === "HANDOFF_CLOSED" ? "HANDOFF_CLOSED" : "HANDOFF_CANCELLED");
}
function waitDelay(delay: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) { reject(cancellationError(signal)); return; }
    const cleanup = () => signal.removeEventListener("abort", onAbort);
    const onAbort = () => { clearTimeout(timer); cleanup(); reject(cancellationError(signal)); };
    const timer = setTimeout(() => { cleanup(); resolve(); }, delay);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}
