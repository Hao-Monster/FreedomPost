// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { App } from "./main";

it.each(["uploads", "uploads(archive)", "uploads(archive(2026))", "uploads%28archive%29"])(
  "preserves an uploaded attachment from %s after saving and reopening",
  async (prefix) => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    const base = { visibility: "private", priceCents: 0, currency: "CNY", createdAt: "2026-09-09", updatedAt: "2026-09-09", markdown: "text" };
    const posts = [
      { ...base, id: "post-a", slug: "a", title: "Article A" },
      { ...base, id: "post-b", slug: "b", title: "Article B" }
    ];
    const url = `https://cdn.example.test/${prefix}/00000000-0000-4000-8000-000000000001.txt`;
    const name = "report [一] (final).txt";
    const fetchMock = vi.fn(async (requestURL: string, init?: RequestInit) => {
      if (requestURL.endsWith("/session")) return Response.json({ session: { username: "admin" } });
      if (requestURL.endsWith("/attachments") && init?.method === "POST") {
        return Response.json({ file: { id: "file-a", url, name, mimeType: "text/plain", sizeBytes: 5 } });
      }
      if (requestURL.endsWith("/post-a") && init?.method === "PUT") {
        Object.assign(posts[0]!, JSON.parse(String(init.body)));
        return Response.json(posts[0]);
      }
      return Response.json({ items: requestURL.endsWith("/posts") ? posts : [] });
    });
    vi.stubGlobal("fetch", fetchMock);
    const container = document.createElement("div");
    document.body.append(container);
    const root = createRoot(container);
    const selectArticle = (title: string) => {
      [...container.querySelectorAll<HTMLButtonElement>(".post-list button")]
        .find((button) => button.textContent?.includes(title))!.click();
    };
    try {
      await act(async () => { root.render(<App />); });
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
      const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
      Object.defineProperty(input, "files", { value: [new File(["hello"], name, { type: "text/plain" })], configurable: true });
      await act(async () => {
        input.dispatchEvent(new Event("change", { bubbles: true }));
        await new Promise((resolve) => setTimeout(resolve, 0));
      });
      expect(container.querySelector(".editor-attachment a")?.getAttribute("href")).toBe(url);
      await act(async () => {
        [...container.querySelectorAll("button")].find((button) => button.textContent === "保存")!.click();
        await new Promise((resolve) => setTimeout(resolve, 0));
      });
      const save = fetchMock.mock.calls.find(([requestURL, init]) => requestURL.endsWith("/post-a") && init?.method === "PUT");
      expect(JSON.parse(String(save?.[1]?.body)).markdown).toContain(`[附件: report \\[一\\] \\(final\\).txt](${url})`);
      await act(async () => { selectArticle("Article B"); });
      await act(async () => { selectArticle("Article A"); });
      const attachment = container.querySelector(".editor-attachment");
      expect(attachment?.querySelector("a")?.getAttribute("href")).toBe(url);
      expect(attachment?.getAttribute("data-name")).toBe(name);
    } finally {
      await act(async () => root.unmount());
      container.remove();
      vi.unstubAllGlobals();
    }
  }
);

it("keeps links and images following an inline attachment outside its destination", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const markdown = "[附件: a](https://cdn.example.test/a.txt)[尾注](https://example.test/note)\n\n[附件: b](https://cdn.example.test/b.txt)![图片](/api/uploads/image.png)";
  const post = { id: "post-a", slug: "a", title: "Article A", markdown, visibility: "private", createdAt: "2026-09-09", updatedAt: "2026-09-09" };
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
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    const editor = container.querySelector<HTMLElement>('[contenteditable="true"]')!;
    expect(editor.querySelector(".editor-attachment")).toBeNull();
    expect([...editor.querySelectorAll("a")].map((link) => link.getAttribute("href"))).toEqual([
      "https://cdn.example.test/a.txt", "https://example.test/note", "https://cdn.example.test/b.txt"
    ]);
    expect(editor.querySelector("img")?.getAttribute("src")).toBe("/api/uploads/image.png");
  } finally {
    await act(async () => root.unmount());
    container.remove();
    vi.unstubAllGlobals();
  }
});
