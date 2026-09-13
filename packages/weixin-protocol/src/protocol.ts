import { randomBytes, randomUUID } from "node:crypto";

export interface BaseInfo {
  channel_version: string;
  bot_agent: string;
}

export interface TextItem {
  text: string;
}

export interface MessageItem {
  type: 1;
  text_item: TextItem;
}

export interface WeixinMessage {
  from_user_id: string;
  to_user_id: string;
  client_id: string;
  message_type: 2;
  message_state: 2;
  item_list: MessageItem[];
  context_token?: string;
  run_id?: string;
}

export interface GetUpdatesResponse {
  ret?: number;
  errcode?: number;
  errmsg?: string;
  msgs?: ReadonlyArray<Record<string, unknown>>;
  get_updates_buf?: string;
  longpolling_timeout_ms?: number;
}

export interface SendMessageResponse {
  ret?: number;
  errmsg?: string;
  message_id?: string;
}

export interface BasicResponse {
  ret?: number;
  errcode?: number;
  errmsg?: string;
}

export interface WeixinClientOptions {
  /** Bot token obtained from the QR-code login flow. It is never logged. */
  token: string;
  /** The iLink API origin. Production must use HTTPS. */
  baseUrl?: string;
  channelVersion?: string;
  botAgent?: string;
  appClientVersion?: number;
  requestTimeoutMs?: number;
  longPollTimeoutMs?: number;
  /** Test-only injection point; production callers should omit it. */
  fetchImpl?: typeof fetch;
  /** Test-only stable UIN header. Production callers should omit it. */
  uinHeader?: string;
}

export interface GetUpdatesOptions {
  getUpdatesBuf?: string;
  signal?: AbortSignal;
}

export interface SendTextOptions {
  toUserId: string;
  text: string;
  contextToken?: string;
  runId?: string;
  clientId?: string;
}

export class WeixinProtocolError extends Error {
  readonly kind: "configuration" | "network" | "http" | "protocol" | "timeout";
  readonly status?: number;
  readonly retCode?: number;

  constructor(
    message: string,
    options: {
      kind: WeixinProtocolError["kind"];
      status?: number;
      retCode?: number;
      cause?: unknown;
    }
  ) {
    super(message, { cause: options.cause });
    this.name = "WeixinProtocolError";
    this.kind = options.kind;
    if (options.status !== undefined) this.status = options.status;
    if (options.retCode !== undefined) this.retCode = options.retCode;
  }
}

const DEFAULT_BASE_URL = "https://ilinkai.weixin.qq.com";
const DEFAULT_CHANNEL_VERSION = "0.1.0";
const DEFAULT_BOT_AGENT = "FreedomPost-WeixinBridge/0.1";
const DEFAULT_APP_CLIENT_VERSION = 0;
const DEFAULT_REQUEST_TIMEOUT_MS = 15_000;
const DEFAULT_LONG_POLL_TIMEOUT_MS = 35_000;
const MAX_RESPONSE_BYTES = 1_048_576;
const MAX_TEXT_LENGTH = 64_000;

/**
 * Build the protocol's X-WECHAT-UIN value. The header is a base64 encoded
 * decimal uint32, as required by the published iLink protocol.
 */
export function createWechatUinHeader(): string {
  return Buffer.from(String(randomBytes(4).readUInt32BE(0)), "utf8").toString("base64");
}

export class WeixinClient {
  private readonly baseUrl: URL;
  private readonly token: string;
  private readonly baseInfo: BaseInfo;
  private readonly appClientVersion: number;
  private readonly requestTimeoutMs: number;
  private readonly longPollTimeoutMs: number;
  private readonly fetchImpl: typeof fetch;
  private readonly uinHeader: string;

  constructor(options: WeixinClientOptions) {
    const token = options.token.trim();
    if (!token) {
      throw new WeixinProtocolError("Weixin token is required", { kind: "configuration" });
    }

    this.baseUrl = validateBaseUrl(options.baseUrl ?? DEFAULT_BASE_URL);
    this.token = token;
    this.baseInfo = {
      channel_version: validateShortText(options.channelVersion ?? DEFAULT_CHANNEL_VERSION, "channelVersion"),
      bot_agent: validateShortText(options.botAgent ?? DEFAULT_BOT_AGENT, "botAgent")
    };
    this.appClientVersion = validateTimeout(options.appClientVersion ?? DEFAULT_APP_CLIENT_VERSION, "appClientVersion", 0);
    this.requestTimeoutMs = validateTimeout(options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS, "requestTimeoutMs", 1);
    this.longPollTimeoutMs = validateTimeout(options.longPollTimeoutMs ?? DEFAULT_LONG_POLL_TIMEOUT_MS, "longPollTimeoutMs", 1);
    this.fetchImpl = options.fetchImpl ?? fetch;
    this.uinHeader = options.uinHeader ?? createWechatUinHeader();
    if (!this.uinHeader.trim()) {
      throw new WeixinProtocolError("uinHeader must not be empty", { kind: "configuration" });
    }
  }

  async getUpdates(options: GetUpdatesOptions = {}): Promise<GetUpdatesResponse> {
    const requestOptions: { timeoutMs: number; signal?: AbortSignal } = {
      timeoutMs: this.longPollTimeoutMs,
      ...(options.signal ? { signal: options.signal } : {})
    };
    const response = await this.request<GetUpdatesResponse>("ilink/bot/getupdates", {
      get_updates_buf: options.getUpdatesBuf ?? "",
      base_info: this.baseInfo
    }, requestOptions);
    return response;
  }

  async sendText(options: SendTextOptions): Promise<SendMessageResponse> {
    const toUserId = requireText(options.toUserId, "toUserId");
    const text = requireText(options.text, "text");
    if (text.length > MAX_TEXT_LENGTH) {
      throw new WeixinProtocolError("text exceeds protocol limit", { kind: "configuration" });
    }

    const msg: WeixinMessage = {
      from_user_id: "",
      to_user_id: toUserId,
      client_id: options.clientId?.trim() || randomUUID(),
      message_type: 2,
      message_state: 2,
      item_list: [{ type: 1, text_item: { text } }],
      ...(options.contextToken?.trim() ? { context_token: options.contextToken.trim() } : {}),
      ...(options.runId?.trim() ? { run_id: options.runId.trim() } : {})
    };

    return this.request<SendMessageResponse>("ilink/bot/sendmessage", {
      msg,
      base_info: this.baseInfo
    });
  }

  async getConfig(ilinkUserId: string, contextToken?: string): Promise<BasicResponse & { typing_ticket?: string }> {
    const userId = requireText(ilinkUserId, "ilinkUserId");
    return this.request<BasicResponse & { typing_ticket?: string }>("ilink/bot/getconfig", {
      ilink_user_id: userId,
      ...(contextToken?.trim() ? { context_token: contextToken.trim() } : {}),
      base_info: this.baseInfo
    });
  }

  async notifyStart(): Promise<BasicResponse> {
    return this.request<BasicResponse>("ilink/bot/msg/notifystart", { base_info: this.baseInfo });
  }

  async notifyStop(): Promise<BasicResponse> {
    return this.request<BasicResponse>("ilink/bot/msg/notifystop", { base_info: this.baseInfo });
  }

  private async request<T extends object>(
    endpoint: string,
    body: Record<string, unknown>,
    options: { timeoutMs?: number; signal?: AbortSignal } = {}
  ): Promise<T> {
    const url = new URL(endpoint, this.baseUrl);
    const timeoutMs = options.timeoutMs ?? this.requestTimeoutMs;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const combined = combineAbortSignals(controller, options.signal);

    try {
      const response = await this.fetchImpl(url, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          AuthorizationType: "ilink_bot_token",
          Authorization: `Bearer ${this.token}`,
          "X-WECHAT-UIN": this.uinHeader,
          "iLink-App-Id": "bot",
          "iLink-App-ClientVersion": String(this.appClientVersion)
        },
        body: JSON.stringify(body),
        signal: combined.signal
      });

      const contentLength = Number(response.headers.get("content-length") ?? "0");
      if (Number.isFinite(contentLength) && contentLength > MAX_RESPONSE_BYTES) {
        throw new WeixinProtocolError("Weixin response exceeds size limit", {
          kind: "protocol",
          status: response.status
        });
      }
      const raw = await readBoundedResponseText(response, MAX_RESPONSE_BYTES);
      if (!response.ok) {
        throw new WeixinProtocolError(`Weixin HTTP request failed (${response.status})`, {
          kind: "http",
          status: response.status
        });
      }

      const parsed = parseObject<T>(raw);
      assertBusinessSuccess(parsed);
      return parsed;
    } catch (error) {
      if (error instanceof WeixinProtocolError) throw error;
      if (error instanceof Error && error.name === "AbortError") {
        throw new WeixinProtocolError("Weixin request timed out or was aborted", {
          kind: "timeout",
          cause: error
        });
      }
      throw new WeixinProtocolError("Weixin network request failed", {
        kind: "network",
        cause: error
      });
    } finally {
      clearTimeout(timer);
      combined.cleanup();
    }
  }
}

function validateBaseUrl(value: string): URL {
  let url: URL;
  try {
    url = new URL(value);
  } catch (error) {
    throw new WeixinProtocolError("baseUrl must be an absolute URL", { kind: "configuration", cause: error });
  }

  const isLoopback = url.hostname === "localhost" || url.hostname === "127.0.0.1" || url.hostname === "[::1]";
  if (url.protocol !== "https:" && !(url.protocol === "http:" && isLoopback)) {
    throw new WeixinProtocolError("baseUrl must use HTTPS (HTTP is allowed only for loopback tests)", { kind: "configuration" });
  }
  if (url.username || url.password || url.search || url.hash || (url.pathname !== "/" && url.pathname !== "")) {
    throw new WeixinProtocolError("baseUrl must contain only an origin", { kind: "configuration" });
  }
  url.pathname = "/";
  return url;
}

function validateShortText(value: string, field: string): string {
  const text = value.trim();
  if (!text || text.length > 256 || /[\u0000-\u001f\u007f]/.test(text)) {
    throw new WeixinProtocolError(`${field} is invalid`, { kind: "configuration" });
  }
  return text;
}

function validateTimeout(value: number, field: string, minimum: number): number {
  if (!Number.isInteger(value) || value < minimum || value > 300_000) {
    throw new WeixinProtocolError(`${field} is invalid`, { kind: "configuration" });
  }
  return value;
}

function requireText(value: string, field: string): string {
  const text = value.trim();
  if (!text) throw new WeixinProtocolError(`${field} is required`, { kind: "configuration" });
  return text;
}

function parseObject<T>(raw: string): T {
  let value: unknown;
  try {
    value = JSON.parse(raw) as unknown;
  } catch (error) {
    throw new WeixinProtocolError("Weixin returned invalid JSON", { kind: "protocol", cause: error });
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new WeixinProtocolError("Weixin returned an invalid response object", { kind: "protocol" });
  }
  return value as T;
}

async function readBoundedResponseText(response: Response, maxBytes: number): Promise<string> {
  if (!response.body) return response.text();

  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let totalBytes = 0;
  try {
    while (true) {
      const chunk = await reader.read();
      if (chunk.done) break;
      totalBytes += chunk.value.byteLength;
      if (totalBytes > maxBytes) {
        await reader.cancel();
        throw new WeixinProtocolError("Weixin response exceeds size limit", { kind: "protocol", status: response.status });
      }
      chunks.push(chunk.value);
    }
  } finally {
    reader.releaseLock();
  }

  const bytes = new Uint8Array(totalBytes);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(bytes);
}

function assertBusinessSuccess(value: object): void {
  const response = value as { ret?: unknown; errcode?: unknown };
  const ret = typeof response.ret === "number" ? response.ret : undefined;
  const errcode = typeof response.errcode === "number" ? response.errcode : undefined;
  const code = ret !== undefined && ret !== 0 ? ret : errcode !== undefined && errcode !== 0 ? errcode : undefined;
  if (code !== undefined) {
    throw new WeixinProtocolError(`Weixin protocol rejected request (code ${code})`, {
      kind: "protocol",
      retCode: code
    });
  }
}

function combineAbortSignals(
  controller: AbortController,
  external?: AbortSignal
): { signal: AbortSignal; cleanup: () => void } {
  if (!external) return { signal: controller.signal, cleanup: () => undefined };
  if (external.aborted) controller.abort();
  const onAbort = () => controller.abort();
  external.addEventListener("abort", onAbort, { once: true });
  return {
    signal: controller.signal,
    cleanup: () => external.removeEventListener("abort", onAbort)
  };
}
