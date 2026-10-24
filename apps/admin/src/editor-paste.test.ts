// @vitest-environment happy-dom

import { describe, expect, it } from "vitest";
import { pastedLinkUrl, sanitizePastedEditorHtml } from "./editor-paste.js";

describe("pastedLinkUrl", () => {
  it.each([
    "https://github.com/2dust/v2rayN/releases/download/7.18.0/v2rayN-windows-64-desktop.zip",
    "http://example.com/download?a=1&b=2#files",
    "mailto:help@example.com",
    "tel:+8613800000000"
  ])("recognizes a complete pasted link: %s", (url) => {
    expect(pastedLinkUrl(` ${url}\n`)).toBe(url);
  });

  it.each([
    "普通文本",
    "下载 https://example.com/file.zip",
    "https://example.com/file.zip 下载",
    "https://example.com/a\nhttps://example.com/b",
    "https://example.com/a https://example.com/b",
    "www.example.com",
    "/api/uploads/file.zip",
    "https://",
    "mailto:",
    "javascript:alert(1)",
    "data:text/html,test",
    "ftp://example.com/file.zip"
  ])("leaves ordinary text and unsupported links to normal paste: %s", (text) => {
    expect(pastedLinkUrl(text)).toBeNull();
  });
});

describe("sanitizePastedEditorHtml", () => {
  it("removes foreign presentation styles so headings look the same before and after publishing", () => {
    const html = sanitizePastedEditorHtml(
      '<h1 class="source-title" style="font-size:17px;font-weight:400">正文外观的标题</h1><p style="font-size:32px">正文</p>',
      "https://admin.example.com/admin/"
    );

    expect(html).toBe("<h1>正文外观的标题</h1><p>正文</p>");
  });

  it("drops executable markup and unsafe link targets while preserving rich-text semantics", () => {
    const html = sanitizePastedEditorHtml(
      '<script>alert(1)</script><p><strong>安全文本</strong> <a href="javascript:alert(1)" onclick="alert(2)">链接</a></p>',
      "https://admin.example.com/admin/"
    );

    expect(html).toBe("<p><strong>安全文本</strong> <a>链接</a></p>");
  });

  it("keeps uploaded editor images but removes unrelated source attributes", () => {
    const html = sanitizePastedEditorHtml(
      '<figure class="editor-image source-card" data-fp-type="image" style="width:10px"><a href="https://pic.example.com/a.png"><img src="https://pic.example.com/a.png" alt="示例" width="10" onerror="alert(1)"></a></figure>',
      "https://admin.example.com/admin/"
    );

    expect(html).toBe(
      '<figure class="editor-image" data-fp-type="image"><a href="https://pic.example.com/a.png" target="_blank" rel="noreferrer noopener"><img src="https://pic.example.com/a.png" alt="示例"></a></figure>'
    );
  });

  it("unwraps nested source wrapper divs and converts leaf divs into independent paragraphs", () => {
    const html = sanitizePastedEditorHtml(
      '<div id="js_content" class="rich_media_content"><section><div><p>第一段</p><p>第二段</p></div><div>第三段</div></section></div>',
      "https://admin.example.com/admin/"
    );

    expect(html).toBe("<p>第一段</p><p>第二段</p><p>第三段</p>");
  });

  it("splits <br>-separated pasted lines into independent paragraphs and prunes empty inline wrappers", () => {
    const html = sanitizePastedEditorHtml(
      "<div><strong>第一段<br></strong><br><strong>第二段</strong><br>第三段</div>",
      "https://admin.example.com/admin/"
    );

    expect(html).toBe("<p><strong>第一段</strong></p><p><strong>第二段</strong></p><p>第三段</p>");
  });

  it("unwraps nested paragraphs and divs inside list items to keep list structure flat", () => {
    const html = sanitizePastedEditorHtml(
      "<ul><li><p>列表项一</p></li><li><div><strong>列表项二</strong></div></li></ul>",
      "https://admin.example.com/admin/"
    );

    expect(html).toBe("<ul><li>列表项一</li><li><strong>列表项二</strong></li></ul>");
  });
});
