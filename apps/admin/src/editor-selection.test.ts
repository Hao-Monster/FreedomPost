// @vitest-environment happy-dom

import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { focusEditorStart, insertBlockPlaceholderAtSelection, resolveCalloutTargetBlocks } from "./editor-selection.js";
import { App, editorHtmlToMarkdown } from "./main.js";
import { editorImageHtml } from "./editor-media.js";

afterEach(() => {
  window.getSelection()?.removeAllRanges();
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

describe("insertBlockPlaceholderAtSelection", () => {
  it("inserts beside the selected paragraph inside nested pasted markup", () => {
    const editor = document.createElement("div");
    editor.innerHTML =
      '<div data-source="wechat"><div><p>第一步</p><p>第二步：点击</p><p>第三步</p></div></div>';
    document.body.append(editor);

    const secondStep = editor.querySelectorAll("p")[1]!;
    const range = document.createRange();
    range.selectNodeContents(secondStep);
    range.collapse(false);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const placeholder = document.createElement("div");
    placeholder.dataset.fpType = "pending-media";
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);

    expect(placeholder.previousElementSibling).toBe(secondStep);
    expect(placeholder.nextElementSibling?.textContent).toBe("第三步");
  });

  it("splits a paragraph when the caret is in the middle of its text", () => {
    const editor = document.createElement("div");
    editor.innerHTML = "<div><p>第二步：点击这里继续</p></div>";
    document.body.append(editor);

    const paragraph = editor.querySelector("p")!;
    const range = document.createRange();
    range.setStart(paragraph.firstChild!, "第二步：点击".length);
    range.collapse(true);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const placeholder = document.createElement("div");
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);

    expect(placeholder.previousElementSibling?.textContent).toBe("第二步：点击");
    expect(placeholder.nextElementSibling?.textContent).toBe("这里继续");
  });

  it("replaces an empty caret paragraph without adding an extra blank line", () => {
    const editor = document.createElement("div");
    editor.innerHTML = "<div><p>第二步：点击</p><p><br></p><p>第三步</p></div>";
    document.body.append(editor);

    const emptyParagraph = editor.querySelectorAll("p")[1]!;
    const range = document.createRange();
    range.selectNodeContents(emptyParagraph);
    range.collapse(true);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const placeholder = document.createElement("div");
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);

    expect(emptyParagraph.isConnected).toBe(false);
    expect(placeholder.previousElementSibling?.textContent).toBe("第二步：点击");
    expect(placeholder.nextElementSibling?.textContent).toBe("第三步");
  });

  it("keeps an insertion inside a callout block", () => {
    const editor = document.createElement("div");
    editor.innerHTML =
      '<div data-fp-type="callout"><div class="editor-callout-content"><p>提示正文</p></div></div>';
    document.body.append(editor);

    const paragraph = editor.querySelector("p")!;
    const range = document.createRange();
    range.selectNodeContents(paragraph);
    range.collapse(false);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const placeholder = document.createElement("div");
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);

    expect(placeholder.parentElement).toBe(editor.querySelector(".editor-callout-content"));
  });

  it.each(["ul", "ol"])("preserves pasted paragraphs and links when replacing a selection across %s items", (tag) => {
    const editor = document.createElement("div");
    editor.innerHTML = `<${tag}><li>前文后文</li><li>下一项</li></${tag}>`;
    document.body.append(editor);
    const items = editor.querySelectorAll("li");
    const range = document.createRange();
    range.setStart(items[0]!.firstChild!, 2);
    range.setEnd(items[1]!.firstChild!, 2);
    const selection = window.getSelection()!;
    selection.addRange(range);

    const placeholder = document.createElement("div");
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);
    placeholder.outerHTML = '<p>第一行</p><p><a href="https://example.com/">第二行链接</a></p>';

    const marker = tag === "ol" ? "1." : "-";
    expect(editorHtmlToMarkdown(editor)).toBe(
      `${marker} 前文\n\n第一行\n\n[第二行链接](https://example.com/)\n\n${marker} 项`
    );
  });

  it.each(["前文后文", "<p>前文后文</p>"])("preserves paragraph boundaries when pasting inside a list item containing %s", (content) => {
    const editor = document.createElement("div");
    editor.innerHTML = `<ul><li>${content}</li><li>下一项</li></ul>`;
    document.body.append(editor);
    const item = editor.querySelector("li")!;
    const range = document.createRange();
    range.setStart((item.querySelector("p") ?? item).firstChild!, 2);
    range.collapse(true);
    const selection = window.getSelection()!;
    selection.addRange(range);

    const placeholder = document.createElement("div");
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);
    placeholder.outerHTML = "<p>第一行</p><p>第二行</p>";

    expect(editorHtmlToMarkdown(editor)).toBe("- 前文\n\n第一行\n\n第二行\n\n- 后文\n- 下一项");
  });

  it.each([
    [editorImageHtml("/api/post-media/post/image", "示意图"), "![示意图](/api/post-media/post/image)"],
    ['<div class="editor-attachment" data-name="文档" data-href="/api/post-media/post/file">文档</div>', "[附件: 文档](/api/post-media/post/file)"]
  ])("preserves a completed media insertion across list items", (html, markdown) => {
    const editor = document.createElement("div");
    editor.innerHTML = "<ul><li>前文后文</li><li>下一项</li></ul>";
    document.body.append(editor);
    const items = editor.querySelectorAll("li");
    const range = document.createRange();
    range.setStart(items[0]!.firstChild!, 2);
    range.setEnd(items[1]!.firstChild!, 2);
    const selection = window.getSelection()!;
    selection.addRange(range);

    const placeholder = document.createElement("div");
    placeholder.dataset.fpType = "pending-media";
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);
    placeholder.outerHTML = html;

    expect(editorHtmlToMarkdown(editor)).toBe(`- 前文\n\n${markdown}\n\n- 项`);
  });

  it("keeps pasted blocks within a callout when splitting its list", () => {
    const editor = document.createElement("div");
    editor.innerHTML = '<aside data-fp-type="callout"><div class="editor-callout-content"><ul><li>前文后文</li></ul></div></aside><p>外部段落</p>';
    document.body.append(editor);
    const range = document.createRange();
    range.setStart(editor.querySelector("li")!.firstChild!, 2);
    range.collapse(true);
    const selection = window.getSelection()!;
    selection.addRange(range);

    const placeholder = document.createElement("div");
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);
    placeholder.outerHTML = "<p>新增段落</p>";

    expect(editorHtmlToMarkdown(editor)).toBe(':::callout{emoji="⛱️"}\n- 前文\n\n新增段落\n\n- 后文\n:::\n\n外部段落');
  });
});

describe("resolveCalloutTargetBlocks", () => {
  it("extracts only the active paragraph when the pasted article is wrapped in nested divs", () => {
    const editor = document.createElement("div");
    editor.innerHTML =
      '<div data-source="wechat"><div><p>第一段</p><p>第二段需要高亮</p><p>第三段</p></div></div>';
    document.body.append(editor);

    const targetParagraph = editor.querySelectorAll("p")[1]!;
    const range = document.createRange();
    range.selectNodeContents(targetParagraph);
    range.collapse(false);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const { selectedNodes } = resolveCalloutTargetBlocks(editor, selection);

    expect(selectedNodes).toHaveLength(1);
    expect(selectedNodes[0]?.textContent).toBe("第二段需要高亮");
    expect([...editor.children].map((el) => el.textContent)).toEqual(["第一段", "第二段需要高亮", "第三段"]);
  });

  it("splits <br>-separated lines in a single paragraph so only the target line becomes a callout", () => {
    const editor = document.createElement("div");
    editor.innerHTML = "<p>第一行内容<br>第二行重点<br>第三行内容</p>";
    document.body.append(editor);

    const paragraph = editor.querySelector("p")!;
    const secondLineTextNode = paragraph.childNodes[2]!;
    const range = document.createRange();
    range.setStart(secondLineTextNode, 2);
    range.collapse(true);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const { selectedNodes } = resolveCalloutTargetBlocks(editor, selection);

    expect(selectedNodes).toHaveLength(1);
    expect(selectedNodes[0]?.textContent).toBe("第二行重点");
    expect([...editor.children].map((el) => el.textContent)).toEqual(["第一行内容", "第二行重点", "第三行内容"]);
  });

  it("splits a paragraph when a substring is selected so unselected text stays outside the callout", () => {
    const editor = document.createElement("div");
    editor.innerHTML = "<p>前言内容。需要高亮的核心句。后记内容。</p>";
    document.body.append(editor);

    const textNode = editor.querySelector("p")!.firstChild!;
    const startOffset = "前言内容。".length;
    const endOffset = startOffset + "需要高亮的核心句。".length;
    const range = document.createRange();
    range.setStart(textNode, startOffset);
    range.setEnd(textNode, endOffset);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const { selectedNodes } = resolveCalloutTargetBlocks(editor, selection);

    expect(selectedNodes).toHaveLength(1);
    expect(selectedNodes[0]?.textContent).toBe("需要高亮的核心句。");
    expect([...editor.children].map((el) => el.textContent)).toEqual(["前言内容。", "需要高亮的核心句。", "后记内容。"]);
  });

  it("does not create stray empty paragraphs when selection boundaries sit directly between block elements", () => {
    const editor = document.createElement("div");
    editor.innerHTML = "<p>第一段</p><p>第二段</p><p>第三段</p>";
    document.body.append(editor);

    const targetParagraph = editor.querySelectorAll("p")[1]!;
    const range = document.createRange();
    range.selectNode(targetParagraph);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);

    const { selectedNodes } = resolveCalloutTargetBlocks(editor, selection);

    expect(selectedNodes).toHaveLength(1);
    expect(selectedNodes[0]?.textContent).toBe("第二段");
    expect([...editor.children].map((el) => el.outerHTML)).toEqual([
      "<p>第一段</p>",
      "<p>第二段</p>",
      "<p>第三段</p>"
    ]);
  });

  it("preserves paragraph boundaries when serializing nested wrapper divs without media", () => {
    const editor = document.createElement("div");
    editor.innerHTML = '<div class="pasted-wrapper"><div><p>第一段</p><p>第二段</p></div></div>';
    expect(editorHtmlToMarkdown(editor)).toBe("第一段\n\n第二段");
  });

  it("only wraps the target paragraph into a callout after pasting a full article in the admin editor", async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    const post = {
      id: "post-1",
      slug: "pasted-post",
      title: "复制文章测试",
      markdown: "",
      visibility: "private",
      priceCents: 0,
      currency: "CNY",
      createdAt: "2026-10-19",
      updatedAt: "2026-10-19"
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        url.endsWith("/session")
          ? Response.json({ session: { username: "admin" } })
          : Response.json({ items: url.endsWith("/posts") ? [post] : [] })
      )
    );

    const container = document.createElement("div");
    document.body.append(container);
    const root = createRoot(container);
    const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

    try {
      await act(async () => {
        root.render(createElement(App));
        await tick();
      });

      const editor = container.querySelector<HTMLElement>('[contenteditable="true"]')!;
      focusEditorStart(editor);

      const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
      Object.defineProperty(pasteEvent, "clipboardData", {
        value: {
          items: [],
          getData: (type: string) =>
            type === "text/html"
              ? '<div id="js_content"><section><p>第一段导语</p><p>第二段核心重点</p><p>第三段总结</p></section></div>'
              : "第一段导语\n第二段核心重点\n第三段总结"
        }
      });

      await act(async () => {
        editor.dispatchEvent(pasteEvent);
        await tick();
      });

      const paragraphs = [...editor.querySelectorAll("p")];
      expect(paragraphs.map((p) => p.textContent)).toEqual(["第一段导语", "第二段核心重点", "第三段总结"]);

      const range = document.createRange();
      range.selectNodeContents(paragraphs[1]!);
      range.collapse(false);
      const selection = window.getSelection()!;
      selection.removeAllRanges();
      selection.addRange(range);
      document.dispatchEvent(new Event("selectionchange"));

      const blockSelect = container.querySelector<HTMLSelectElement>('select[aria-label="块类型"]')!;
      await act(async () => {
        blockSelect.value = "callout";
        blockSelect.dispatchEvent(new Event("change", { bubbles: true }));
        await tick();
      });

      const callouts = editor.querySelectorAll('[data-fp-type="callout"]');
      expect(callouts).toHaveLength(1);
      expect(callouts[0]?.querySelector(".editor-callout-content")?.textContent).toBe("第二段核心重点");
      expect(editorHtmlToMarkdown(editor)).toBe(
        '第一段导语\n\n:::callout{emoji="⛱️"}\n第二段核心重点\n:::\n\n第三段总结'
      );
    } finally {
      await act(async () => root.unmount());
      container.remove();
    }
  });

  it("supports undo, redo, and save when multi-paragraph paste replaces a selection or splits a list", async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    const savedBodies: Array<{ markdown: string }> = [];
    const post = {
      id: "00000000-0000-4000-8000-000000000001",
      slug: "qa",
      title: "QA",
      markdown: "前文原文后文",
      visibility: "private",
      priceCents: 0,
      currency: "CNY",
      createdAt: "2026-10-19",
      updatedAt: "2026-10-19"
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith("/session")) {
          return Response.json({ session: { username: "qa" } });
        }
        if (init?.method === "PUT") {
          const parsed = JSON.parse(String(init.body)) as { markdown: string };
          savedBodies.push(parsed);
          return Response.json({ ...post, ...parsed });
        }
        return Response.json({ items: url.endsWith("/posts") ? [post] : [] });
      })
    );

    const container = document.createElement("div");
    document.body.append(container);
    const root = createRoot(container);
    const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

    try {
      await act(async () => {
        root.render(createElement(App));
        await tick();
      });

      const editor = container.querySelector<HTMLElement>('[contenteditable="true"]')!;
      editor.focus();
      const textNode = editor.querySelector("p")!.firstChild!;
      const range = document.createRange();
      range.setStart(textNode, 2);
      range.setEnd(textNode, 4);
      const selection = window.getSelection()!;
      selection.removeAllRanges();
      selection.addRange(range);
      document.dispatchEvent(new Event("selectionchange"));

      const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
      Object.defineProperty(pasteEvent, "clipboardData", {
        value: {
          items: [],
          getData: (type: string) =>
            type === "text/html"
              ? '<p>第一行</p><p><a href="https://example.com/">第二行链接</a></p>'
              : "第一行\n第二行链接"
        }
      });

      await act(async () => {
        editor.dispatchEvent(pasteEvent);
        await tick();
      });

      expect(editor.innerHTML).toBe(
        '<p>前文</p><p>第一行</p><p><a href="https://example.com/" target="_blank" rel="noreferrer noopener">第二行链接</a></p><p>后文</p>'
      );
      expect(editorHtmlToMarkdown(editor)).toBe("前文\n\n第一行\n\n[第二行链接](https://example.com/)\n\n后文");

      let undoResult = false;
      await act(async () => {
        undoResult = document.execCommand("undo");
        await tick();
      });

      expect(undoResult).toBe(true);
      expect(editor.innerHTML).toBe("<p>前文原文后文</p>");
      expect(editorHtmlToMarkdown(editor)).toBe("前文原文后文");
      expect(window.getSelection()?.toString()).toBe("原文");

      const saveButton = [...container.querySelectorAll("button")].find((btn) => btn.textContent?.trim() === "保存")!;
      await act(async () => {
        saveButton.click();
        await tick();
      });
      expect(savedBodies.at(-1)?.markdown).toBe("前文原文后文");

      let redoResult = false;
      await act(async () => {
        redoResult = document.execCommand("redo");
        await tick();
      });

      expect(redoResult).toBe(true);
      expect(editor.innerHTML).toBe(
        '<p>前文</p><p>第一行</p><p><a href="https://example.com/" target="_blank" rel="noreferrer noopener">第二行链接</a></p><p>后文</p>'
      );
      expect(editorHtmlToMarkdown(editor)).toBe("前文\n\n第一行\n\n[第二行链接](https://example.com/)\n\n后文");

      await act(async () => {
        saveButton.click();
        await tick();
      });
      expect(savedBodies.at(-1)?.markdown).toBe("前文\n\n第一行\n\n[第二行链接](https://example.com/)\n\n后文");

      await act(async () => {
        const secondP = editor.querySelectorAll("p")[2]!;
        secondP.append(document.createTextNode("追加输入"));
        editor.dispatchEvent(new Event("input", { bubbles: true }));
        await tick();
      });
      expect(editorHtmlToMarkdown(editor)).toBe("前文\n\n第一行\n\n[第二行链接](https://example.com/)追加输入\n\n后文");

      await act(async () => {
        editor.dispatchEvent(new KeyboardEvent("keydown", { key: "z", ctrlKey: true, bubbles: true, cancelable: true }));
        await tick();
      });
      expect(editorHtmlToMarkdown(editor)).toBe("前文\n\n第一行\n\n[第二行链接](https://example.com/)\n\n后文");

      await act(async () => {
        editor.dispatchEvent(new KeyboardEvent("keydown", { key: "z", ctrlKey: true, bubbles: true, cancelable: true }));
        await tick();
      });
      expect(editorHtmlToMarkdown(editor)).toBe("前文原文后文");
    } finally {
      await act(async () => root.unmount());
      container.remove();
    }
  });
});

describe("focusEditorStart", () => {
  it("places the caret at the beginning of an empty editor paragraph", () => {
    const editor = document.createElement("div");
    editor.contentEditable = "true";
    editor.innerHTML = "<p><br></p>";
    document.body.append(editor);

    focusEditorStart(editor);

    const range = window.getSelection()!.getRangeAt(0);
    expect(document.activeElement).toBe(editor);
    expect(range.startContainer).toBe(editor.firstElementChild);
    expect(range.startOffset).toBe(0);
    expect(range.collapsed).toBe(true);
  });
});
