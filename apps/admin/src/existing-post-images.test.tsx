// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { App } from "./main";

it("opens and saves existing managed images even when image import is unavailable", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const src = "https://r2pic.openal.uk/freedompost/uploads/admin/original.webp";
  const post = { id: "existing-post", title: "Existing article", slug: "existing", markdown: `![图片](${src})`, visibility: "private", createdAt: "2026-09-09", updatedAt: "2026-09-09" };
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("image-imports")) return new Response("{}", { status: 503 });
    if (url.endsWith("/session")) return Response.json({ session: { username: "admin" } });
    if (init?.method === "PUT") return Response.json({ ...post, ...JSON.parse(String(init.body)) });
    return Response.json({ items: url.endsWith("/posts") ? [post] : [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  try {
    await act(async () => { root.render(<App />); });
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
    const editor = container.querySelector<HTMLElement>('[contenteditable="true"]')!;
    expect(editor.querySelector("img")?.getAttribute("src")).toBe(src);
    expect(editor.querySelector('[data-fp-type="image-import-error"]')).toBeNull();
    expect(fetchMock.mock.calls.some(([url]) => url.endsWith("image-imports"))).toBe(false);
    editor.insertAdjacentHTML("afterbegin", "<p>安装的时候选English，之后就都是中文了</p>");
    await act(async () => { [...container.querySelectorAll("button")].find(b => b.textContent === "保存")!.click(); });
    const saved = fetchMock.mock.calls.find(([,init]) => init?.method === "PUT");
    expect(saved).toBeDefined();
    const body = JSON.parse(String(saved![1]!.body));
    expect(body.markdown).toContain("安装的时候选English，之后就都是中文了");
    expect(body.markdown).toContain(src);
    expect(container.textContent).toContain("保存成功");
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  }
});
