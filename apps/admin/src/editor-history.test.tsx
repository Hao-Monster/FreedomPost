// @vitest-environment happy-dom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { App, editorHtmlToMarkdown } from "./main.js";
import { editorCalloutHtml } from "./editor-callout.js";

const postID = "00000000-0000-4000-8000-000000000001";
const attachmentID = "00000000-0000-4000-8000-000000000002";
const mediaURL = `/api/post-media/${postID}/${attachmentID}`;
const originalExecCommand = document.execCommand;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
let root: Root | undefined;
let container: HTMLDivElement;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  container?.remove();
  window.getSelection()?.removeAllRanges();
  document.execCommand = originalExecCommand;
  vi.unstubAllGlobals();
});

async function openEditor(options: { markdown?: string; autoImport?: boolean } = {}) {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  const post = { id: postID, slug: "qa-history", title: "QA history", markdown: options.markdown ?? "前文原文后文", visibility: "private", priceCents: 0, currency: "CNY", createdAt: "2026-10-24", updatedAt: "2026-10-24" };
  const other = { ...post, id: "00000000-0000-4000-8000-000000000003", title: "QA other article", slug: "qa-other", markdown: "另一篇文章" };
  const saves: Array<{ markdown: string; importedImages: unknown[] }> = [];
  const finishUploads: Array<(ok?: boolean) => void> = [];
  let finishImport!: (ok?: boolean) => void;
  let importAttempts = 0;
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/session")) return Response.json({ session: { username: "qa" } });
    if (url.endsWith("/attachments") && init?.method === "POST") {
      const id = finishUploads.length === 0 ? attachmentID : "00000000-0000-4000-8000-000000000004";
      return new Promise<Response>((resolve) => {
        finishUploads.push((ok = true) => resolve(ok
          ? Response.json({ file: { id, url: `/api/post-media/${postID}/${id}`, name: "qa.png", mimeType: "image/png", claimToken: "qa-claim" } })
          : Response.json({ error: { message: "QA upload failed" } }, { status: 500 })));
      });
    }
    if (url.endsWith("/image-imports") && init?.method === "POST") {
      const { items } = JSON.parse(String(init.body)) as { items: Array<{ clientId: string }> };
      const failure = { code: "IMPORT_UNAVAILABLE", message: "QA import failed", resolution: "重试" };
      if (importAttempts++ === 0 && !options.autoImport) return Response.json({ items: items.map(({ clientId }) => ({ clientId, error: failure })) });
      return new Promise<Response>((resolve) => {
        finishImport = (ok = true) => resolve(Response.json({ items: items.map(({ clientId }, index) => {
          const id = index === 0 ? attachmentID : "00000000-0000-4000-8000-000000000004";
          return { clientId, ...(ok
            ? { file: { id, url: `/api/post-media/${postID}/${id}`, name: "qa.png", claimToken: "qa-claim" } }
            : { error: failure }) };
        }) }));
      });
    }
    if (init?.method === "PUT") {
      const body = JSON.parse(String(init.body)) as { markdown: string; importedImages: unknown[] };
      saves.push(body);
      if (options.autoImport && body.markdown.includes("https://example.com/")) {
        return Response.json({ error: { code: "EXTERNAL_IMAGE_NOT_IMPORTED", message: "QA external image" } }, { status: 400 });
      }
      return Response.json({ ...(url.endsWith(other.id) ? other : post), ...body });
    }
    return Response.json({ items: url.endsWith("/posts") ? [post, other] : [] });
  }));
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root!.render(<App />); await tick(); });
  const editor = container.querySelector<HTMLElement>('[aria-label="文章正文"]')!;
  return {
    editor, saves,
    completeUpload: async (ok = true, index = 0) => { await act(async () => { finishUploads[index]!(ok); await tick(); }); },
    completeImport: async (ok = true) => { await act(async () => { finishImport(ok); await tick(); }); },
    save: async () => {
      await act(async () => {
        [...container.querySelectorAll("button")].find((button) => button.textContent === "保存")!.click();
        await tick();
      });
    }
  };
}

function selectText(editor: HTMLElement, node: Node, start: number, end = start) {
  editor.focus();
  const range = document.createRange();
  range.setStart(node, start);
  range.setEnd(node, end);
  const selection = window.getSelection()!;
  selection.removeAllRanges();
  selection.addRange(range);
  document.dispatchEvent(new Event("selectionchange"));
}

async function startUpload(editor: HTMLElement) {
  selectText(editor, editor.querySelector("p")!.firstChild!, 2);
  const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
  Object.defineProperty(input, "files", { configurable: true, value: [new File(["QA"], "qa.png", { type: "image/png" })] });
  await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  expect(editor.querySelector('[data-fp-type="pending-media"]')).not.toBeNull();
}

async function typeText(editor: HTMLElement, text: string) {
  const paragraph = [...editor.querySelectorAll("p")].at(-1)!;
  selectText(editor, paragraph.firstChild!, 0);
  await act(async () => {
    editor.dispatchEvent(new InputEvent("beforeinput", { bubbles: true, cancelable: true, inputType: "insertText", data: text }));
    const range = window.getSelection()!.getRangeAt(0);
    const node = document.createTextNode(text);
    range.insertNode(node);
    range.setStartAfter(node);
    range.collapse(true);
    editor.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText", data: text }));
    await tick();
  });
}

async function history(command: "undo" | "redo") {
  await act(async () => { document.execCommand(command); await tick(); });
}

it("preserves an in-flight upload through typing undo/redo and never restores completed placeholders", async () => {
  const { editor, completeUpload, save, saves } = await openEditor();
  await startUpload(editor);
  await typeText(editor, "追加");
  await history("undo");
  expect(editor.querySelector('[data-fp-type="pending-media"]')).not.toBeNull();
  expect(editor.textContent).not.toContain("追加");
  await history("redo");
  await completeUpload();
  expect(editor.querySelector("img")?.getAttribute("src")).toBe(mediaURL);
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
  await history("undo");
  expect(editor.querySelector("img")).not.toBeNull();
  expect(editor.textContent).not.toContain("追加");
  await history("undo");
  expect(editor.innerHTML).toBe("<p>前文原文后文</p>");
  await history("redo");
  await history("redo");
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
  expect(editor.textContent).toContain("追加");
  expect(window.getSelection()?.anchorNode?.parentElement?.textContent).toContain("追加原文后文");
  await save();
  expect(saves).toHaveLength(1);
  expect(saves[0]?.markdown).toContain(mediaURL);
  expect(saves[0]?.importedImages).toEqual([{ id: attachmentID, claimToken: "qa-claim" }]);
});

it("keeps an undone upload absent on completion and restores the finished image on redo", async () => {
  const { editor, completeUpload, save, saves } = await openEditor();
  await startUpload(editor);
  await history("undo");
  expect(editor.innerHTML).toBe("<p>前文原文后文</p>");
  const titleInput = container.querySelector<HTMLInputElement>(".post-title-field input") ?? [...container.querySelectorAll<HTMLInputElement>("input")].find((input) => input.value === "QA history")!;
  titleInput.focus();
  await completeUpload();
  expect(document.activeElement).toBe(titleInput);
  expect(editor.querySelector("img")).toBeNull();
  await save();
  expect(saves[0]?.markdown).toBe("前文原文后文");
  await history("redo");
  expect(editor.querySelector("img")?.getAttribute("src")).toBe(mediaURL);
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
  await save();
  expect(saves[1]?.markdown).toContain(mediaURL);
});

it("settles concurrent uploads out of order without moving or reviving either placeholder", async () => {
  const { editor, completeUpload, save, saves } = await openEditor();
  await startUpload(editor);
  await startUpload(editor);
  await history("undo");
  await history("redo");
  await completeUpload(true, 1);
  await completeUpload(true, 0);
  const expected = [`/api/post-media/${postID}/00000000-0000-4000-8000-000000000004`, mediaURL];
  expect([...editor.querySelectorAll("img")].map((img) => img.getAttribute("src"))).toEqual(expected);
  await history("undo");
  expect([...editor.querySelectorAll("img")].map((img) => img.getAttribute("src"))).toEqual([mediaURL]);
  await history("redo");
  expect([...editor.querySelectorAll("img")].map((img) => img.getAttribute("src"))).toEqual(expected);
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
  await save();
  expect(saves[0]?.importedImages).toHaveLength(2);
});

it.each([false, true])("does not revive a failed upload after undo/redo (initially undone: %s)", async (undoFirst) => {
  const { editor, completeUpload, save, saves } = await openEditor();
  await startUpload(editor);
  if (undoFirst) await history("undo");
  await completeUpload(false);
  if (!undoFirst) await history("undo");
  await history("redo");
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
  expect(editor.querySelector("img")).toBeNull();
  await save();
  await save();
  expect(saves).toHaveLength(1);
  expect(saves[0]?.markdown).toBe("前文原文后文");
});

async function pasteFailedImage(editor: HTMLElement) {
  selectText(editor, editor.querySelector("p")!.firstChild!, 2);
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", { value: { items: [], getData: (type: string) => type === "text/html" ? '<img src="https://example.com/qa.png">' : "" } });
  await act(async () => { editor.dispatchEvent(event); await tick(); });
  expect(editor.querySelector('[data-fp-type="image-import-error"]')).not.toBeNull();
}

async function clickImageAction(editor: HTMLElement, action: string) {
  await act(async () => { editor.querySelector<HTMLButtonElement>(`[data-image-import-action="${action}"]`)!.click(); await tick(); });
}

it("preserves text redo when a retry completes without further typing and has no empty undo step", async () => {
  const { editor, completeImport } = await openEditor();
  await pasteFailedImage(editor);
  await typeText(editor, "追加");
  await history("undo");
  await clickImageAction(editor, "retry");
  await completeImport();
  await history("redo");
  expect(editor.textContent).toContain("追加原文后文");
  await history("undo");
  expect(editor.textContent).not.toContain("追加");
  await history("undo");
  expect(editor.querySelector("img")).toBeNull();
});

it("undoes the image insertion in one step while a retry is pending", async () => {
  const { editor, completeImport } = await openEditor();
  await pasteFailedImage(editor);
  await clickImageAction(editor, "retry");
  await history("undo");
  expect(editor.innerHTML).toBe("<p>前文原文后文</p>");
  await completeImport();
  expect(editor.querySelector("img")).toBeNull();
  await history("redo");
  expect(editor.querySelector("img")?.getAttribute("src")).toBe(mediaURL);
});

it.each(["retry", "upload-local"])("blocks concurrent card actions throughout %s and undo/redo", async (action) => {
  const { editor, completeImport, completeUpload } = await openEditor();
  await pasteFailedImage(editor);
  await clickImageAction(editor, action);
  if (action === "upload-local") {
    const input = container.querySelector<HTMLInputElement>('input[type="file"]:not([multiple])')!;
    Object.defineProperty(input, "files", { configurable: true, value: [new File(["QA"], "qa.png", { type: "image/png" })] });
    await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  }
  await typeText(editor, "追加");
  await history("undo");
  await history("redo");
  const count = vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith("/image-imports")).length;
  await clickImageAction(editor, "retry");
  await clickImageAction(editor, "remove");
  expect(vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith("/image-imports"))).toHaveLength(count);
  expect(editor.querySelector('[data-fp-type="image-import-error"]')).not.toBeNull();
  if (action === "retry") await completeImport(); else await completeUpload();
});

it("does not count an emoji picker as an edit, and records emoji changes before the next insertion", async () => {
  const { editor } = await openEditor({ markdown: ':::callout{emoji="💡"}\n提示\n:::\n\n尾文' });
  await typeText(editor, "追加");
  await history("undo");
  await act(async () => { editor.querySelector<HTMLButtonElement>("[data-callout-emoji-trigger]")!.click(); await tick(); });
  await history("redo");
  expect(editor.textContent).toContain("追加尾文");
  await act(async () => { editor.querySelector<HTMLButtonElement>('[data-callout-emoji-option="⚠️"]')!.click(); await tick(); });
  await startUpload(editor);
  await history("undo");
  expect(editor.querySelector('[data-fp-type="callout"]')?.getAttribute("data-emoji")).toBe("⚠️");
  await history("undo");
  expect(editor.querySelector('[data-fp-type="callout"]')?.getAttribute("data-emoji")).toBe("💡");
  await history("redo");
  expect(editor.querySelector('[data-fp-type="callout"]')?.getAttribute("data-emoji")).toBe("⚠️");
});

it("restores a failed paragraph split while preserving text typed during the upload", async () => {
  const { editor, completeUpload, save, saves } = await openEditor();
  await startUpload(editor);
  await typeText(editor, "追加");
  await completeUpload(false);
  expect(editorHtmlToMarkdown(editor)).toBe("前文追加原文后文");
  await history("undo");
  expect(editorHtmlToMarkdown(editor)).toBe("前文原文后文");
  await history("redo");
  expect(editorHtmlToMarkdown(editor)).toBe("前文追加原文后文");
  await save(); await save();
  expect(saves.at(-1)?.markdown).toBe("前文追加原文后文");
});

it("retains the caret after a failed split without taking focus from the title", async () => {
  const { editor, completeUpload } = await openEditor();
  await startUpload(editor);
  await typeText(editor, "追加");
  const title = [...container.querySelectorAll<HTMLInputElement>("input")].find((input) => input.value === "QA history")!;
  title.focus();
  await completeUpload(false);
  expect(document.activeElement).toBe(title);
  await act(async () => { container.querySelector<HTMLButtonElement>('button[title="加粗"]')!.click(); await tick(); });
  const caret = window.getSelection()!.getRangeAt(0);
  const prefix = document.createRange();
  prefix.selectNodeContents(editor);
  prefix.setEnd(caret.startContainer, caret.startOffset);
  expect(prefix.toString()).toBe("前文追加");
});

it.each(["前文**原文**后文", "- 前文原文后文"])("restores formatted split boundaries after upload failure: %s", async (markdown) => {
  const { editor, completeUpload, save, saves } = await openEditor({ markdown });
  const text = editor.querySelector("strong,li")!.firstChild!;
  selectText(editor, text, 1);
  const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
  Object.defineProperty(input, "files", { configurable: true, value: [new File(["QA"], "qa.png", { type: "image/png" })] });
  await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  await completeUpload(false);
  expect(editorHtmlToMarkdown(editor)).toBe(markdown);
  await history("undo"); await history("redo");
  expect(editorHtmlToMarkdown(editor)).toBe(markdown);
  await save(); await save();
  expect(saves.at(-1)?.markdown).toBe(markdown);
});

it("restores text selected for replacement when its upload fails", async () => {
  const { editor, completeUpload } = await openEditor({ markdown: "前文**原文**后文" });
  const text = editor.querySelector("strong")!.firstChild!;
  selectText(editor, text, 0, 2);
  const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
  Object.defineProperty(input, "files", { configurable: true, value: [new File(["QA"], "qa.png", { type: "image/png" })] });
  await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  await completeUpload(false);
  expect(editorHtmlToMarkdown(editor)).toBe("前文**原文**后文");
});

it("restores a multi-paragraph replacement after its upload fails", async () => {
  const markdown = "前文原文\n\n后文尾文";
  const { editor, completeUpload } = await openEditor({ markdown });
  const paragraphs = editor.querySelectorAll("p");
  selectText(editor, paragraphs[0]!.firstChild!, 2);
  window.getSelection()!.getRangeAt(0).setEnd(paragraphs[1]!.firstChild!, 2);
  const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
  Object.defineProperty(input, "files", { configurable: true, value: [new File(["QA"], "qa.png", { type: "image/png" })] });
  await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  await completeUpload(false);
  expect(editorHtmlToMarkdown(editor)).toBe(markdown);
});

it.each([0, 1])("rejoins concurrent failed upload splits in either completion order: %s", async (first) => {
  const { editor, completeUpload } = await openEditor();
  await startUpload(editor);
  const paragraph = [...editor.querySelectorAll("p")].at(-1)!;
  selectText(editor, paragraph.firstChild!, 1);
  const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
  await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  await completeUpload(false, first);
  await completeUpload(false, 1 - first);
  expect(editor.innerHTML).toBe("<p>前文原文后文</p>");
  await history("undo"); await history("redo");
  expect(editor.innerHTML).toBe("<p>前文原文后文</p>");
});

it("repairs missing and cloned media IDs before automatic import", async () => {
  const { editor, save, completeImport, saves } = await openEditor({ markdown: '![第一张](https://example.com/qa.png)\n\n尾文', autoImport: true });
  const original = editor.querySelector("figure")!;
  const originalID = original.getAttribute("data-editor-media-id");
  original.before(original.cloneNode(true));
  const third = original.cloneNode(true) as HTMLElement;
  third.removeAttribute("data-editor-media-id");
  original.after(third);
  await act(async () => { editor.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertFromDrop" })); await tick(); });
  await save();
  const ids = [...editor.querySelectorAll<HTMLElement>("figure")].map((node) => node.dataset.editorMediaId);
  expect(ids.every(Boolean)).toBe(true);
  expect(new Set(ids).size).toBe(3);
  expect(original.getAttribute("data-editor-media-id")).toBe(originalID);
  await completeImport();
  expect(saves.at(-1)?.markdown).not.toContain("https://example.com/");
  expect(editor.querySelectorAll('img[src^="/api/post-media/"]')).toHaveLength(3);
});

it.each([false, true])("does not reuse a settled split while another upload is pending or undone (undone: %s)", async (undoSecond) => {
  const { editor, completeUpload } = await openEditor({ markdown: "abcdef\n\nuvwxyz" });
  const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
  const upload = async () => {
    Object.defineProperty(input, "files", { configurable: true, value: [new File(["QA"], "qa.png", { type: "image/png" })] });
    await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  };
  selectText(editor, editor.querySelector("p")!.firstChild!, 2);
  await upload();
  selectText(editor, [...editor.querySelectorAll("p")].at(-1)!.firstChild!, 2);
  await upload();
  await completeUpload(true, 0);
  if (undoSecond) {
    await history("undo");
    await completeUpload(true, 1);
  }
  await act(async () => {
    const image = editor.querySelector("figure")!;
    const blank = image.nextElementSibling;
    if (blank?.tagName === "P" && !blank.textContent?.trim()) blank.remove();
    image.remove();
    editor.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "deleteContentBackward" }));
    await tick();
  });
  const before = editorHtmlToMarkdown(editor);
  const first = editor.querySelector("p")!;
  selectText(editor, first.firstChild!, first.textContent!.length);
  await upload();
  await completeUpload(false, 2);
  expect(editorHtmlToMarkdown(editor)).toBe(before);
  if (!undoSecond) await completeUpload(false, 1);
  expect(editorHtmlToMarkdown(editor)).toBe("ab\n\ncdef\n\nuvwxyz");
  await history("undo");
  await history("redo");
  expect(editorHtmlToMarkdown(editor)).toBe("ab\n\ncdef\n\nuvwxyz");
  expect(editor.querySelector("[data-editor-split-id]")).toBeNull();
});

it.each([[true, 1], [false, 1], [true, 2], [false, 2]] as const)("settles an upload hidden inside a later upload's restoration (success: %s, depth: %s)", async (success, depth) => {
  const { editor, completeUpload, save, saves } = await openEditor({ markdown: "abcdef" });
  await startUpload(editor);
  const pending = editor.querySelector<HTMLElement>('[data-fp-type="pending-media"]')!;
  const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
  for (let i = 0; i < depth; i++) {
    const range = document.createRange();
    range.selectNode(editor.querySelector('[data-fp-type="pending-media"]')!);
    window.getSelection()!.removeAllRanges();
    window.getSelection()!.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
    await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  }
  expect(editor.querySelectorAll('[data-fp-type="pending-media"]')).toHaveLength(1);
  expect(editor.querySelector<HTMLElement>('[data-pending-restoration]')!.dataset.pendingRestoration).toContain(pending.dataset.pendingMediaId);
  await completeUpload(success, 0);
  for (let i = 1; i <= depth; i++) await completeUpload(false, i);
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
  expect(editor.querySelector('[data-editor-split-id]')).toBeNull();
  if (success) expect(editor.querySelector("img")?.getAttribute("src")).toBe(mediaURL);
  else expect(editor.innerHTML).toBe("<p>abcdef</p>");
  await history("undo");
  await history("redo");
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
  await save();
  await save();
  expect(saves).toHaveLength(1);
  if (success) expect(saves[0]?.markdown).toContain(mediaURL);
  else expect(saves[0]?.markdown).toBe("abcdef");
});

it.each([[0, 1], [1, 0]])("preserves only the successful split of concurrent uploads (success order: %s, %s)", async (successIndex, failureIndex) => {
  const { editor, completeUpload } = await openEditor({ markdown: "abcdefghi" });
  const input = container.querySelector<HTMLInputElement>('input[type="file"][multiple]')!;
  Object.defineProperty(input, "files", { configurable: true, value: [new File(["QA"], "qa.png", { type: "image/png" })] });
  for (let i = 0; i < 2; i++) {
    selectText(editor, [...editor.querySelectorAll("p")].at(-1)!.firstChild!, 3);
    await act(async () => { input.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  }
  await completeUpload(true, successIndex);
  await completeUpload(false, failureIndex);
  expect([...editor.querySelectorAll("p")].map((p) => p.textContent).filter(Boolean)).toEqual(successIndex === 0 ? ["abc", "defghi"] : ["abcdef", "ghi"]);
  expect(editor.querySelectorAll("img")).toHaveLength(1);
  expect(editor.querySelector('[data-editor-split-id]')).toBeNull();
  await history("undo");
  await history("redo");
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
  expect([...editor.querySelectorAll("p")].map((p) => p.textContent).filter(Boolean)).toEqual(successIndex === 0 ? ["abc", "defghi"] : ["abcdef", "ghi"]);
});

it.each(["undo", "redo", "settle"])("keeps the original image identity when a drag-copy precedes a node recreated by %s", async (path) => {
  const { editor, save, completeImport, completeUpload } = await openEditor({
    markdown: path === "settle" ? "前文原文后文" : "![original](https://example.com/qa.png)\n\n尾文",
    autoImport: true
  });
  if (path === "settle") {
    await startUpload(editor);
    await completeUpload();
  } else {
    await save();
    await typeText(editor, "输入");
    await history("undo");
    if (path === "redo") await history("redo");
  }
  const original = editor.querySelector<HTMLElement>("figure.editor-image")!;
  const originalID = original.dataset.editorMediaId;
  const copy = original.cloneNode(true) as HTMLElement;
  await act(async () => {
    original.before(copy);
    editor.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertFromDrop" }));
    await tick();
  });
  expect(original.dataset.editorMediaId).toBe(originalID);
  expect(copy.dataset.editorMediaId).not.toBe(originalID);
  if (path !== "settle") {
    await completeImport();
    const currentOriginal = editor.querySelector<HTMLElement>(`[data-editor-media-id="${originalID}"]`)!;
    expect(currentOriginal.querySelector("img")?.getAttribute("src")).toBe(mediaURL);
    expect(editor.querySelector<HTMLElement>(`[data-editor-media-id="${copy.dataset.editorMediaId}"]`)?.querySelector("img")?.getAttribute("src")).toBe("https://example.com/qa.png");
  }
});

it("replaces unsafe media IDs before selecting an automatically imported image", async () => {
  const { editor, save, completeImport, saves } = await openEditor({ markdown: '![图片](https://example.com/qa.png)\n\n尾文', autoImport: true });
  const original = editor.querySelector<HTMLElement>("figure")!;
  const figure = original.cloneNode(true) as HTMLElement;
  figure.dataset.editorMediaId = 'qa"\\image]';
  original.replaceWith(figure);
  await save();
  await completeImport();
  expect(editor.querySelector("img")?.getAttribute("src")).toBe(mediaURL);
  expect(saves.at(-1)?.markdown).toContain(mediaURL);
});

it("does not insert a completed upload into another article after switching", async () => {
  const { editor, completeUpload } = await openEditor();
  await startUpload(editor);
  await act(async () => {
    [...container.querySelectorAll("button")].find((button) => button.querySelector("span")?.textContent === "QA other article")!.click();
    await tick();
  });
  await completeUpload();
  await history("redo");
  expect(editor.innerHTML).toBe("<p>另一篇文章</p>");
  expect(editor.querySelector('[data-fp-type="pending-media"]')).toBeNull();
});

it("refuses to save an unresolved upload placeholder even after repeated save attempts", async () => {
  const { editor, save, saves } = await openEditor();
  editor.insertAdjacentHTML("beforeend", '<div data-fp-type="pending-media" contenteditable="false">图片上传中…</div>');
  await save();
  await save();
  expect(saves).toHaveLength(0);
  expect(container.querySelector(".toast")?.textContent).toContain("上传");
});

it("undoes a selected-text callout back to the original paragraph and saves that structure", async () => {
  const { editor, save, saves } = await openEditor();
  selectText(editor, editor.querySelector("p")!.firstChild!, 2, 4);
  const block = container.querySelector<HTMLSelectElement>('select[aria-label="块类型"]')!;
  await act(async () => { block.value = "callout"; block.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  expect(editor.querySelector(".editor-callout-content")?.textContent).toBe("原文");
  await history("undo");
  expect(editor.innerHTML).toBe("<p>前文原文后文</p>");
  expect(window.getSelection()?.toString()).toBe("原文");
  await save();
  expect(saves[0]?.markdown).toBe("前文原文后文");
  await history("redo");
  expect(editorHtmlToMarkdown(editor)).toContain(':::callout{emoji="⛱️"}\n原文\n:::');
});

it("rejects a selection spanning an existing callout without normalizing other paragraphs", async () => {
  const { editor } = await openEditor();
  editor.innerHTML = `<div><p>前文</p></div>${editorCalloutHtml("已有提示", "💡")}<div><p>后文</p></div>`;
  const original = editor.innerHTML;
  const first = editor.firstElementChild!.querySelector("p")!.firstChild!;
  const last = editor.lastElementChild!.querySelector("p")!.firstChild!;
  selectText(editor, first, 1);
  window.getSelection()!.getRangeAt(0).setEnd(last, 1);
  document.dispatchEvent(new Event("selectionchange"));
  const block = container.querySelector<HTMLSelectElement>('select[aria-label="块类型"]')!;
  await act(async () => { block.value = "callout"; block.dispatchEvent(new Event("change", { bubbles: true })); await tick(); });
  expect(editor.innerHTML).toBe(original);
  expect(container.querySelector(".toast")?.textContent).toBe("高亮块不能嵌套");
});

it.each(["retry", "upload-local"])("settles a failed image card after undo/redo during %s", async (action) => {
  const { editor, completeImport, completeUpload, save, saves } = await openEditor();
  selectText(editor, editor.querySelector("p")!.firstChild!, 2);
  const paste = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(paste, "clipboardData", { value: { items: [], getData: (type: string) => type === "text/html" ? '<img src="https://example.com/qa.png">' : "" } });
  await act(async () => { editor.dispatchEvent(paste); await tick(); });
  expect(editor.querySelector('[data-fp-type="image-import-error"]')).not.toBeNull();
  await act(async () => {
    editor.querySelector<HTMLButtonElement>(`[data-image-import-action="${action}"]`)!.click();
    if (action === "upload-local") {
      const input = container.querySelector<HTMLInputElement>('input[type="file"]:not([multiple])')!;
      Object.defineProperty(input, "files", { configurable: true, value: [new File(["QA"], "qa.png", { type: "image/png" })] });
      input.dispatchEvent(new Event("change", { bubbles: true }));
    }
    await tick();
  });
  await typeText(editor, "追加");
  await history("undo");
  await history("redo");
  if (action === "retry") await completeImport();
  else await completeUpload();
  expect(editor.querySelector('[data-fp-type="image-import-error"]')).toBeNull();
  expect(editor.querySelector("img")?.getAttribute("src")).toBe(mediaURL);
  await history("undo");
  await history("redo");
  expect(editor.querySelector('[data-fp-type="image-import-error"]')).toBeNull();
  await save();
  expect(saves[0]?.markdown).toContain(mediaURL);
});

it("retains the failed image source when its deletion is undone", async () => {
  const { editor, completeImport } = await openEditor();
  selectText(editor, editor.querySelector("p")!.firstChild!, 2);
  const paste = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(paste, "clipboardData", { value: { items: [], getData: (type: string) => type === "text/html" ? '<img src="https://example.com/qa.png">' : "" } });
  await act(async () => { editor.dispatchEvent(paste); await tick(); });
  await act(async () => { editor.querySelector<HTMLButtonElement>('[data-image-import-action="remove"]')!.click(); await tick(); });
  expect(editor.querySelector('[data-fp-type="image-import-error"]')).toBeNull();
  await history("undo");
  await act(async () => { editor.querySelector<HTMLButtonElement>('[data-image-import-action="retry"]')!.click(); await tick(); });
  expect(vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith("/image-imports"))).toHaveLength(2);
  await completeImport();
  expect(editor.querySelector("img")?.getAttribute("src")).toBe(mediaURL);
});

it.each([true, false])("settles automatic imports in existing history and keeps duplicate source images independent (success: %s)", async (success) => {
  const { editor, completeImport, save, saves } = await openEditor({
    markdown: "![first](https://example.com/old.png)\n\n![second](https://example.com/old.png)\n\n尾部原文",
    autoImport: true
  });
  await typeText(editor, "保存前");
  await save();
  await typeText(editor, "上传时");
  await history("undo");
  await history("redo");
  await completeImport(success);
  await history("undo");
  await history("undo");
  await history("redo");
  await history("redo");
  expect(editor.innerHTML).not.toContain("https://example.com/");
  if (success) {
    expect([...editor.querySelectorAll("img")].map((image) => [image.alt, image.getAttribute("src")])).toEqual([
      ["first", mediaURL],
      ["second", `/api/post-media/${postID}/00000000-0000-4000-8000-000000000004`]
    ]);
    expect(saves).toHaveLength(2);
    expect(saves[1]?.importedImages).toHaveLength(2);
    await save();
    expect(saves).toHaveLength(3);
    expect(saves[2]?.markdown).not.toContain("https://example.com/");
    expect(saves[2]?.importedImages).toEqual([]);
  } else {
    expect(editor.querySelectorAll('[data-fp-type="image-import-error"]')).toHaveLength(2);
    await save();
    expect(saves).toHaveLength(1);
  }
});
