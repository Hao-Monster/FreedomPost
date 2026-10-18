// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { editorImagesMarkdown } from "./editor-media";
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
    // Formatting can alter the DOM without dispatching an input event.
    editor.insertAdjacentHTML("afterbegin", "<h2>安装<strong>指南</strong></h2>");
    await act(async () => { [...container.querySelectorAll("button")].find(b => b.textContent === "保存")!.click(); });
    const saved = fetchMock.mock.calls.find(([,init]) => init?.method === "PUT");
    expect(saved).toBeDefined();
    const body = JSON.parse(String(saved![1]!.body));
    expect(body.markdown).toContain("安装的时候选English，之后就都是中文了");
    expect(body.markdown).toContain(src);
    expect(body.markdown).toContain("## 安装**指南**");
    expect(body.markdown.match(/^## /gm)).toHaveLength(1);
    expect(container.textContent).toContain("保存成功");
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  }
});

it("keeps editor-produced image Markdown with bracketed captions when reopened", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const src = "/api/uploads/diagram.png";
  const markdown = editorImagesMarkdown([{ src, alt: "示例 [一]" }]);
  const post = { id: "post-a", title: "Article A", slug: "a", markdown, visibility: "private", priceCents: 0, currency: "CNY", createdAt: "2026-09-09", updatedAt: "2026-09-09" };
  vi.stubGlobal("fetch", vi.fn(async (url: string) => (
    url.endsWith("/session")
      ? Response.json({ session: { username: "admin" } })
      : Response.json({ items: url.endsWith("/posts") ? [post] : [] })
  )));
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  try {
    await act(async () => { root.render(<App />); });
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    const editor = container.querySelector<HTMLElement>('[contenteditable="true"]')!;
    expect(editor.querySelector("img")?.getAttribute("src")).toBe(src);
    expect(editor.querySelector("img")?.getAttribute("alt")).toBe("示例 [一]");
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  }
});

it("retains B image claim when an earlier A save returns after switching articles", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const base = { visibility: "private", priceCents: 0, currency: "CNY", createdAt: "2026-09-09", updatedAt: "2026-09-09", markdown: "text" };
  const posts = [
    { ...base, id: "post-a", slug: "a", title: "Article A" },
    { ...base, id: "post-b", slug: "b", title: "Article B" }
  ];
  let resolveSaveA!: (r: Response) => void;
  const pendingSaveA = new Promise<Response>((r) => {
    resolveSaveA = r;
  });
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/session")) return Response.json({ session: { username: "admin" } });
    if (url.endsWith("image-imports")) {
      const req = JSON.parse(String(init?.body));
      return Response.json({
        items: req.items.map((item: { clientId: string }) => ({
          clientId: item.clientId,
          file: {
            id: "image-b",
            claimToken: "claim-b",
            url: "/api/uploads/import-b.png",
            name: "图片",
            mimeType: "image/png",
            sizeBytes: 10
          }
        }))
      });
    }
    if (init?.method === "PUT") {
      if (url.endsWith("post-a")) return pendingSaveA;
      return Response.json({ ...posts[1], ...JSON.parse(String(init.body)) });
    }
    return Response.json({ items: url.endsWith("/posts") ? posts : [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  const button = (text: string) => [...container.querySelectorAll("button")].find((b) => b.textContent === text)!;
  try {
    await act(async () => { root.render(<App />); });
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    await act(async () => { button("保存").click(); });
    expect(fetchMock.mock.calls.some(([u, i]) => u.endsWith("post-a") && i?.method === "PUT")).toBe(true);
    await act(async () => {
      [...container.querySelectorAll<HTMLButtonElement>(".post-list button")].find((b) => b.textContent?.includes("Article B"))!.click();
    });
    const editor = container.querySelector<HTMLElement>('[contenteditable="true"]')!;
    const paste = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(paste, "clipboardData", {
      value: {
        items: [],
        getData: (type: string) => (type === "text/html" ? '<img src="https://remote.example/image.png" alt="图片">' : "")
      }
    });
    await act(async () => {
      editor.dispatchEvent(paste);
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(editor.querySelector("img")?.getAttribute("src")).toBe("/api/uploads/import-b.png");
    await act(async () => {
      resolveSaveA(Response.json(posts[0]));
      await new Promise((r) => setTimeout(r, 0));
    });
    await act(async () => {
      button("保存").click();
      await new Promise((r) => setTimeout(r, 0));
    });
    const saveB = fetchMock.mock.calls.find(([u, i]) => u.endsWith("post-b") && i?.method === "PUT");
    const saved = JSON.parse(String(saveB?.[1]?.body));
    expect(saved.importedImages).toEqual([{ id: "image-b", claimToken: "claim-b" }]);
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  }
});

it("reopens adjacent inline images without swallowing the second image into the first URL and handles long backslash sequences without ReDoS", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const pathologicalLine = `![${"\\".repeat(50)}x`;
  const post = {
    id: "a",
    slug: "a",
    title: "A",
    visibility: "private",
    priceCents: 0,
    currency: "CNY",
    createdAt: "2026-09-09",
    updatedAt: "2026-09-09",
    markdown: `![a](/api/uploads/a.png)![b](/api/uploads/b.png)\n\n${pathologicalLine}`
  };
  vi.stubGlobal("fetch", vi.fn(async (u: string) => (
    u.endsWith("/session")
      ? Response.json({ session: { username: "admin" } })
      : Response.json({ items: u.endsWith("/posts") ? [post] : [] })
  )));
  const container = document.createElement("div");
  document.body.append(container);
  const root = createRoot(container);
  try {
    const start = performance.now();
    await act(async () => { root.render(<App />); });
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    expect(performance.now() - start).toBeLessThan(1000);
    const editor = container.querySelector<HTMLElement>('[contenteditable="true"]')!;
    expect([...editor.querySelectorAll("img")].map((i) => i.getAttribute("src"))).toEqual([
      "/api/uploads/a.png",
      "/api/uploads/b.png"
    ]);
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  }
});
