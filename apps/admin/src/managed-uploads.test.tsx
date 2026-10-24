// @vitest-environment happy-dom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { App } from "./main";
import { localizePastedImages } from "./editor-image-import";

it("uploads article attachments in their article context and sends the one-use claim on save", async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const postID = "00000000-0000-4000-8000-000000000001";
  const attachmentID = "00000000-0000-4000-8000-000000000002";
  const url = `/api/post-media/${postID}/${attachmentID}`;
  const post = { id: postID, slug: "qa", title: "QA media", markdown: "", visibility: "private", priceCents: 0, currency: "CNY", createdAt: "2026-10-19", updatedAt: "2026-10-19" };
  const fetchMock = vi.fn(async (requestURL: string, init?: RequestInit) => {
    if (requestURL.endsWith("/session")) return Response.json({ session: { username: "qa" } });
    if (init?.method === "POST" && requestURL.endsWith("/attachments")) return Response.json({ file: { id: attachmentID, url, name: "report.txt", mimeType: "text/plain", claimToken: "qa-claim" } });
    if (init?.method === "PUT") { Object.assign(post, JSON.parse(String(init.body))); return Response.json(post); }
    return Response.json({ items: requestURL.endsWith("/posts") ? [post] : [] });
  });
  vi.stubGlobal("fetch", fetchMock);
  const container = document.createElement("div"); document.body.append(container);
  const root = createRoot(container);
  const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
  try {
    await act(async () => { root.render(<App />); await tick(); });
    const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
    Object.defineProperty(input, "files", { value: [new File(["qa"], "report.txt", { type: "text/plain" })], configurable: true });
    await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
    expect(fetchMock.mock.calls.find(([, init]) => init?.method === "POST")?.[0]).toBe(`/api/admin/posts/${postID}/attachments`);
    const save = () => [...container.querySelectorAll("button")].find((button) => button.textContent === "保存")!.click();
    await act(async () => { save(); await tick(); });
    await act(async () => { save(); await tick(); });
    const saves = fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT").map(([, init]) => JSON.parse(String(init?.body)));
    expect(saves[0].importedImages).toEqual([{ id: attachmentID, claimToken: "qa-claim" }]);
    expect(saves[0].markdown).toContain(url);
    expect(saves[1].importedImages).toEqual([]);
  } finally { await act(async () => root.unmount()); container.remove(); vi.unstubAllGlobals(); }
});

it("keeps same-origin managed media reusable without sending an authenticated URL to the remote importer", async () => {
  const path = "/api/post-media/00000000-0000-4000-8000-000000000001/00000000-0000-4000-8000-000000000002";
  const importRemoteImages = vi.fn();
  const uploadEmbeddedImage = vi.fn();
  const result = await localizePastedImages(`<img src="https://admin.example${path}" alt="QA">`, "https://admin.example/", { importRemoteImages, uploadEmbeddedImage });
  expect(result?.html).toContain(`src="${path}"`);
  expect(result?.failedImages).toEqual([]);
  expect(importRemoteImages).not.toHaveBeenCalled();
  expect(uploadEmbeddedImage).not.toHaveBeenCalled();
});
