// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import { editorHtmlToMarkdown } from "./main";

describe("editor save conversion", () => {
  it("keeps a newly inserted heading", () => {
    const editor = document.createElement("div");
    editor.contentEditable = "true";
    editor.innerHTML = `<p>原文</p><h1><span class="fp-color-red">回到首页，点开按钮即可</span></h1><p>后文</p>`;
    expect(editorHtmlToMarkdown(editor)).toContain("# <span class=\"fp-color-red\">回到首页，点开按钮即可</span>");
  });
});
