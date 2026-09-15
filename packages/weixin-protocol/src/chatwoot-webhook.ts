import { createHmac, timingSafeEqual } from "node:crypto";

export interface ChatwootWebhookVerificationOptions {
  rawBody: string;
  timestamp: string;
  signature: string;
  secret: string;
  nowMs?: number;
  toleranceSeconds?: number;
}

/**
 * Verify Chatwoot's `sha256={HMAC(timestamp.rawBody)}` signature.
 * The timestamp check prevents replaying a valid delivery indefinitely.
 */
export function verifyChatwootWebhookSignature(options: ChatwootWebhookVerificationOptions): boolean {
  const secret = options.secret.trim();
  if (!secret || !/^\d+$/.test(options.timestamp)) return false;

  const timestampSeconds = Number(options.timestamp);
  const toleranceSeconds = options.toleranceSeconds ?? 300;
  if (!Number.isSafeInteger(timestampSeconds) || !Number.isInteger(toleranceSeconds) || toleranceSeconds < 0) {
    return false;
  }
  const nowSeconds = Math.floor((options.nowMs ?? Date.now()) / 1000);
  if (Math.abs(nowSeconds - timestampSeconds) > toleranceSeconds) return false;

  const match = options.signature.match(/^sha256=([a-f0-9]{64})$/i);
  if (!match?.[1]) return false;
  const expected = createHmac("sha256", secret)
    .update(`${options.timestamp}.${options.rawBody}`, "utf8")
    .digest();
  const received = Buffer.from(match[1], "hex");
  return received.length === expected.length && timingSafeEqual(received, expected);
}

/** Parse a verified webhook body and reject arrays/scalars at the trust boundary. */
export function parseChatwootWebhookBody(rawBody: string): Record<string, unknown> {
  let value: unknown;
  try {
    value = JSON.parse(rawBody) as unknown;
  } catch {
    throw new Error("Chatwoot webhook body is not valid JSON");
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("Chatwoot webhook body must be a JSON object");
  }
  return value as Record<string, unknown>;
}

/** Generate the exact header value used by tests and local webhook tooling. */
export function chatwootWebhookSignature(rawBody: string, timestamp: string, secret: string): string {
  if (!secret) throw new Error("secret is required");
  return `sha256=${createHmac("sha256", secret).update(`${timestamp}.${rawBody}`, "utf8").digest("hex")}`;
}
