// @vitest-environment happy-dom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./main";

let root: Root;
let container: HTMLDivElement;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

async function submitLogin(loginResponse: () => Promise<Response>) {
  const fetchMock = vi.fn(async (url: string, _init?: RequestInit) => {
    if (url === "/api/admin/session") return new Response("{}", { status: 401 });
    if (url === "/api/admin/login") return loginResponse();
    return Response.json({ items: [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  await act(async () => root.render(<App />));
  await act(async () => {
    container.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
  return fetchMock;
}

describe("administrator login feedback", () => {
  it.each([
    { status: 401, code: "LOGIN_FAILED", expected: "用户名或密码不正确" },
    { status: 429, code: "RATE_LIMITED", expected: "登录尝试过多，请稍后再试" },
    { status: 500, code: "INTERNAL_ERROR", expected: "登录服务暂时不可用，请稍后再试" },
    { status: 502, code: null, expected: "登录服务暂时不可用，请稍后再试" }
  ])("shows safe feedback for HTTP $status without entering the admin workspace", async ({ status, code, expected }) => {
    const internalDetail = "backend password=server-secret at redis.internal";
    const fetchMock = await submitLogin(async () => new Response(
      code ? JSON.stringify({ error: { code, message: internalDetail } }) : `<html>${internalDetail}</html>`,
      { status, headers: { "Content-Type": code ? "application/json" : "text/html" } }
    ));

    expect(container.querySelector(".toast")?.textContent).toBe(expected);
    expect(container.textContent).not.toContain(internalDetail);
    expect(container.querySelector(".login-screen")).not.toBeNull();
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(["/api/admin/session", "/api/admin/login"]);
  });

  it("shows a safe network error when the login request cannot connect", async () => {
    const internalDetail = "network credentials=client-secret";
    const fetchMock = await submitLogin(async () => { throw new TypeError(internalDetail); });

    expect(container.querySelector(".toast")?.textContent).toBe("无法连接登录服务，请检查网络后重试");
    expect(container.textContent).not.toContain(internalDetail);
    expect(container.querySelector(".login-screen")).not.toBeNull();
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(["/api/admin/session", "/api/admin/login"]);
  });

  it("loads the admin workspace after a successful login", async () => {
    const fetchMock = await submitLogin(async () => Response.json({ session: { username: "admin" } }));

    expect(container.querySelector(".login-screen")).toBeNull();
    expect(container.querySelector(".admin-shell")).not.toBeNull();
    expect(container.querySelector(".toast")?.textContent).toBe("已登录");
    expect(fetchMock.mock.calls.some(([url]) => url === "/api/admin/posts")).toBe(true);
  });
});
