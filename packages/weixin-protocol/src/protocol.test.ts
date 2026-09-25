import { describe, expect, it, vi } from "vitest";
import { WeixinClient, WeixinProtocolError } from "./protocol.js";

function response(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" }
  });
}

describe("WeixinClient", () => {
  it("sends protocol headers and a text message without exposing token in errors", async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response({ ret: 0, message_id: "9007199254740993" }));
    const client = new WeixinClient({
      token: "secret-token",
      baseUrl: "https://ilinkai.weixin.qq.com",
      uinHeader: "MTIzNA==",
      fetchImpl
    });

    const result = await client.sendText({
      toUserId: "wx-user-1",
      text: "订单 FP123 已创建",
      contextToken: "ctx-1",
      clientId: "client-1"
    });

    expect(result.message_id).toBe("9007199254740993");
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    const [url, init] = fetchImpl.mock.calls[0] ?? [];
    expect(String(url)).toBe("https://ilinkai.weixin.qq.com/ilink/bot/sendmessage");
    expect(init?.headers).toMatchObject({
      AuthorizationType: "ilink_bot_token",
      Authorization: "Bearer secret-token",
      "X-WECHAT-UIN": "MTIzNA==",
      "iLink-App-Id": "bot"
    });
    const payload = JSON.parse(String(init?.body)) as {
      msg: { to_user_id: string; item_list: unknown; context_token?: string };
    };
    expect(payload.msg.to_user_id).toBe("wx-user-1");
    expect(payload.msg.item_list).toEqual([{ type: 1, text_item: { text: "订单 FP123 已创建" } }]);
    expect(payload.msg.context_token).toBe("ctx-1");
    expect(JSON.stringify(payload)).not.toContain("secret-token");
  });

  it("turns a non-zero protocol return code into a redacted error", async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockResolvedValue(response({ ret: -14, errmsg: "token secret-token expired" }));
    const client = new WeixinClient({ token: "secret-token", fetchImpl, uinHeader: "MTIzNA==" });

    await expect(client.notifyStart()).rejects.toMatchObject({
      name: "WeixinProtocolError",
      kind: "protocol",
      retCode: -14
    });
    await expect(client.notifyStart()).rejects.not.toThrow("secret-token");
  });

  it("maps an aborted request to a timeout error", async () => {
    const fetchImpl = vi.fn<typeof fetch>().mockImplementation((_input, init) =>
      new Promise((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => {
          const error = new Error("aborted");
          error.name = "AbortError";
          reject(error);
        });
      })
    );
    const client = new WeixinClient({
      token: "secret-token",
      fetchImpl,
      uinHeader: "MTIzNA==",
      requestTimeoutMs: 5
    });

    await expect(client.notifyStart()).rejects.toMatchObject({
      name: "WeixinProtocolError",
      kind: "timeout"
    });
  });

  it("rejects non-HTTPS production endpoints and invalid text", () => {
    expect(() => new WeixinClient({ token: "token", baseUrl: "http://example.test" })).toThrow(WeixinProtocolError);
    const client = new WeixinClient({ token: "token", uinHeader: "MTIzNA==" });
    expect(client.sendText({ toUserId: " ", text: "hello" })).rejects.toMatchObject({ kind: "configuration" });
  });
});
