import { describe, expect, it } from "vitest";
import {
  chatwootWebhookSignature,
  parseChatwootWebhookBody,
  verifyChatwootWebhookSignature
} from "./chatwoot-webhook.js";

describe("Chatwoot webhook verification", () => {
  const secret = "webhook-secret";
  const body = JSON.stringify({ event: "message_created", id: 42 });
  const timestamp = "1789296000";

  it("accepts a valid HMAC signature within the replay window", () => {
    const signature = chatwootWebhookSignature(body, timestamp, secret);
    expect(
      verifyChatwootWebhookSignature({
        rawBody: body,
        timestamp,
        signature,
        secret,
        nowMs: Number(timestamp) * 1000
      })
    ).toBe(true);
  });

  it("rejects tampered, malformed, stale, or wrong-secret deliveries", () => {
    const signature = chatwootWebhookSignature(body, timestamp, secret);
    expect(
      verifyChatwootWebhookSignature({
        rawBody: `${body} `,
        timestamp,
        signature,
        secret,
        nowMs: Number(timestamp) * 1000
      })
    ).toBe(false);
    expect(
      verifyChatwootWebhookSignature({
        rawBody: body,
        timestamp,
        signature,
        secret: "other-secret",
        nowMs: Number(timestamp) * 1000
      })
    ).toBe(false);
    expect(
      verifyChatwootWebhookSignature({
        rawBody: body,
        timestamp,
        signature,
        secret,
        nowMs: (Number(timestamp) + 301) * 1000
      })
    ).toBe(false);
    expect(
      verifyChatwootWebhookSignature({
        rawBody: body,
        timestamp,
        signature: "bad",
        secret,
        nowMs: Number(timestamp) * 1000
      })
    ).toBe(false);
  });

  it("parses only JSON objects at the trust boundary", () => {
    expect(parseChatwootWebhookBody(body)).toEqual({ event: "message_created", id: 42 });
    expect(() => parseChatwootWebhookBody("[]")).toThrow("JSON object");
    expect(() => parseChatwootWebhookBody("invalid")).toThrow("valid JSON");
  });
});
