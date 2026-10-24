// @vitest-environment happy-dom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./main.js";

const downloadUrl = "https://github.com/2dust/v2rayN/releases/download/7.18.0/v2rayN-windows-64-desktop.zip";
let root: Root | undefined;
let container: HTMLDivElement;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  container?.remove();
  window.getSelection()?.removeAllRanges();
  vi.unstubAllGlobals();
});

async function openEditor(markdown = "前文官网下载地址后文") {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const post = { id: "qa-link-paste", slug: "qa", title: "QA links", markdown, visibility: "private", priceCents: 0, currency: "CNY", createdAt: "2026-10-23", updatedAt: "2026-10-23" };
  const saved: Array<{ markdown: string }> = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/session")) return Response.json({ session: { username: "qa" } });
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as { markdown: string };
      saved.push(body);
      return Response.json({ ...post, ...body });
    }
    return Response.json({ items: url.endsWith("/posts") ? [post] : [] });
  }));
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root!.render(<App />); await tick(); });
  return { editor: container.querySelector<HTMLElement>('[aria-label="文章正文"]')!, saved };
}

function selectText(node: Node, start: number, end = start) {
  const range = document.createRange();
  range.setStart(node, start);
  range.setEnd(node, end);
  const selection = window.getSelection()!;
  selection.removeAllRanges();
  selection.addRange(range);
  document.dispatchEvent(new Event("selectionchange"));
}

async function paste(editor: HTMLElement, text: string, html = "") {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: { items: [], getData: (type: string) => type === "text/plain" ? text : type === "text/html" ? html : "" }
  });
  await act(async () => { editor.dispatchEvent(event); await tick(); });
  return event;
}

it.each([false, true])("pastes the download URL as a link, preserving selected text (%s), undo/redo, and saved Markdown", async (hasSelection) => {
  const { editor, saved } = await openEditor();
  editor.focus();
  selectText(editor.querySelector("p")!.firstChild!, 2, hasSelection ? 8 : 2);
  const original = editor.innerHTML;
  const event = await paste(editor, downloadUrl, hasSelection ? `<span>${downloadUrl}</span>` : "");
  expect(event.defaultPrevented).toBe(true);
  const label = hasSelection ? "官网下载地址" : downloadUrl;
  expect(editor.querySelector("a")?.getAttribute("href")).toBe(downloadUrl);
  expect(editor.querySelector("a")?.textContent).toBe(label);
  expect(editor.textContent).toBe(hasSelection ? "前文官网下载地址后文" : `前文${downloadUrl}官网下载地址后文`);

  await act(async () => { document.execCommand("undo"); await tick(); });
  expect(editor.innerHTML).toBe(original);
  await act(async () => { document.execCommand("redo"); await tick(); });
  expect(editor.querySelector("a")?.getAttribute("href")).toBe(downloadUrl);

  await act(async () => {
    [...container.querySelectorAll("button")].find((button) => button.textContent === "保存")!.click();
    await tick();
  });
  expect(saved.at(-1)?.markdown).toBe(`前文[${label}](${downloadUrl})${hasSelection ? "" : "官网下载地址"}后文`);
});

it.each(["普通文字", "javascript:alert(1)", "下载 https://example.com/file.zip", "https://example.com/a\nhttps://example.com/b"])("does not turn ordinary pasted content into one automatic link: %s", async (text) => {
  const { editor } = await openEditor();
  editor.focus();
  selectText(editor.querySelector("p")!.firstChild!, 2);
  await paste(editor, text);
  expect(editor.querySelector("a")).toBeNull();
});

it.each(["`code`", "```text\ncode\n```"])("keeps a URL paste inside code as plain text: %s", async (markdown) => {
  const { editor } = await openEditor(markdown);
  editor.focus();
  selectText(editor.querySelector("code")!.firstChild!, 0);
  const event = await paste(editor, downloadUrl);
  expect(event.defaultPrevented).toBe(false);
  expect(editor.querySelector("a")).toBeNull();
});
