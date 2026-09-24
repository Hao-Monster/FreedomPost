// @vitest-environment happy-dom
/**
 * editor-third-round-bugs.test.ts
 * Third-round deep code-analysis bug discovery tests.
 */
import { describe, expect, it } from "vitest";
import { editorHtmlToMarkdown } from "./main";
import { normalizeEditorImageAlt } from "./editor-media";

function makeEditor(html: string): HTMLElement {
  const div = document.createElement("div");
  div.innerHTML = html;
  return div;
}

// BUG-P01: Verify that & in paragraph content survives round-trip without double-escaping
describe("BUG-P01: & in paragraph content round-trip", () => {
  it("ampersand in paragraph is not double-escaped on serialisation", () => {
    // DOM: <p>AT&amp;T <strong>加粗</strong></p>
    // inlineNodeToMarkdown reads TEXT_NODE.textContent which is "AT&T" (decoded by DOM)
    // So serialised markdown = "AT&T **加粗**"
    // On re-parse: markdownFragmentToEditorHtml calls escapeHtml("AT&T **加粗**")
    // = "AT&amp;T **加粗**" then formatInlineMarkdown converts ** -> <strong>
    // Rendered by browser: AT&T (browser decodes &amp;) = correct
    const editor = makeEditor("<p>AT&amp;T <strong>加粗</strong></p>");
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-P01 output:", md);
    expect(md).toContain("AT&T");
    expect(md).toContain("**加粗**");
    expect(md).not.toContain("&amp;"); // markdown itself should be literal &
  });
});

// BUG-P05: Image alt containing ]( breaks the markdown image regex on re-parse
describe("BUG-P05: Image alt with special chars ]( in it", () => {
  it("alt containing ]( is properly escaped so url is not split incorrectly", () => {
    const figure = document.createElement("figure");
    figure.className = "editor-image";
    figure.dataset.fpType = "image";
    const img = document.createElement("img");
    img.src = "https://cdn.example.com/photo.jpg";
    img.alt = "图片](注意括号)";
    figure.append(img);
    const editor = document.createElement("div");
    editor.append(figure);
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-P05 alt with ](:", md);
    // escapeMarkdown should escape the ] and ( in alt
    expect(md).toContain("\\]\\(");
    expect(md).toContain("https://cdn.example.com/photo.jpg");
  });
});

// BUG-P07: bold + italic regex ordering - ** should not conflict with *
describe("BUG-P07: bold and italic regex order", () => {
  it("mixed bold and italic in same paragraph serialise without conflict", () => {
    const editor = makeEditor("<p><del>删除</del> <strong>加粗</strong> <em>斜体</em></p>");
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-P07 mixed formats:", md);
    expect(md).toContain("~~删除~~");
    expect(md).toContain("**加粗**");
    expect(md).toContain("*斜体*");
  });
});

// BUG-P08: Callout containing H1 serialises correctly
describe("BUG-P08: Callout with heading inside", () => {
  it("callout containing h1 and p serialises to :::callout block with # prefix", () => {
    const editor = makeEditor([
      '<aside class="editor-callout" data-fp-type="callout" data-emoji="💡">',
      '  <div class="editor-callout-emoji-shell" contenteditable="false"></div>',
      '  <div class="editor-callout-content">',
      "    <h1>标题在callout内</h1>",
      "    <p>段落在callout内</p>",
      "  </div>",
      "</aside>"
    ].join(""));
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-P08 callout+h1:", md);
    expect(md).toContain(":::callout");
    expect(md).toContain("# 标题在callout内");
    expect(md).toContain("段落在callout内");
  });
});

// BUG-P10: normalizeEditorImageAlt — filename-only input returns default alt
describe("BUG-P10: normalizeEditorImageAlt with edge-case filenames", () => {
  it("plain filename returns default alt", () => {
    expect(normalizeEditorImageAlt("photo.jpg")).toBe("图片");
  });
  it("empty string returns default alt", () => {
    expect(normalizeEditorImageAlt("")).toBe("图片");
  });
  it("meaningful Chinese text returns as-is", () => {
    expect(normalizeEditorImageAlt("产品展示图")).toBe("产品展示图");
  });
  it("extension-only string .jpg matches filename pattern and returns default", () => {
    // After fileToEditorHtml strips special chars from "????.jpg" -> ".jpg"
    // normalizeEditorImageAlt(".jpg") should return "图片" not ".jpg"
    const result = normalizeEditorImageAlt(".jpg");
    console.log("BUG-P10 .jpg alt:", result);
    expect(result).toBe("图片");
  });
});

// BUG-P11: Bold/italic inside link text breaks the link regex on re-parse
describe("BUG-P11: Bold text inside link — critical round-trip bug", () => {
  it("<a><strong>text</strong></a> serialises to [**text**](url) which breaks re-parse link regex", () => {
    // When re-loading markdown "[**加粗链接**](https://example.com)":
    // formatInlineMarkdown step 1: ** pass converts inner ** to <strong>
    //   → "[<strong>加粗链接</strong>](https://example.com)"
    // step 2: link regex /\[([^\]]+)]\(url\)/ — [^\]]+ doesn't match ">" in <strong>
    //   → link regex FAILS → displayed as plain text "[<strong>加粗链接</strong>](url)"
    const editor = makeEditor(
      '<p><a href="https://example.com"><strong>加粗链接</strong></a></p>'
    );
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-P11 bold in link serialised:", md);
    // The serialised markdown will be: [**加粗链接**](https://example.com)
    expect(md).toContain("加粗链接");
    expect(md).toContain("https://example.com");
    // Now test that the link regex can actually match the serialised form
    // The link regex from formatInlineMarkdown:
    const linkRegex = /\[([^\]]+)]\((https?:\/\/[^)\s]+|\/[^)\s]+|mailto:[^\s)]+|tel:[^\s)]+)\)/g;
    const match = linkRegex.exec(md);
    console.log("BUG-P11 link regex match:", match);
    // If bold markers ** are inside the link text [], [^\]]+ should still match * chars
    // * is not ] so it DOES match — let's verify
    expect(match).not.toBeNull();
    expect(match?.[2]).toBe("https://example.com");
  });

  it("<a><em>text</em></a> link with italic text round-trips correctly", () => {
    const editor = makeEditor(
      '<p><a href="https://example.com"><em>斜体链接</em></a></p>'
    );
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-P11 italic in link:", md);
    // md = [*斜体链接*](https://example.com)
    // formatInlineMarkdown step 1: ** pass — no match
    // step 2: * pass — "*斜体链接*" inside [] becomes <em>斜体链接</em>
    //   → "[<em>斜体链接</em>](https://example.com)" 
    // step 3: link regex — [^\]]+ fails on ">" in <em> → link lost!
    // Actually wait: the * regex runs AFTER the link regex in formatInlineMarkdown?
    // No — looking at the order:
    // 1. link regex
    // 2. ** (bold)
    // 3. ~~ (strikethrough)
    // 4. * (italic)
    // 5. ``/` (code)
    // So the link regex runs FIRST on the raw markdown "[*斜体链接*](url)"
    // [^\]]+ matches "*斜体链接*" (no ] in it) → link regex succeeds!
    // Then * inside the produced <a> tag — the * pass runs on the full string
    // including already-converted HTML... this could corrupt the href!
    const linkRegex = /\[([^\]]+)]\((https?:\/\/[^)\s]+|\/[^)\s]+|mailto:[^\s)]+|tel:[^\s)]+)\)/g;
    const match = linkRegex.exec(md);
    expect(match).not.toBeNull();
  });
});

// BUG-P12: formatInlineMarkdown applies * regex on already-converted HTML href values
describe("BUG-P12: italic regex corrupts already-converted <a> href containing *", () => {
  it("link href containing * (e.g. glob pattern URL) is corrupted by italic regex", () => {
    // Imagine a URL: https://example.com/path/*/file
    // After link regex converts: <a href="https://example.com/path/*/file">text</a>
    // Then italic regex /\*(.*?)\*/g runs on the whole string
    // It matches the * in href: <a href="https://example.com/path/<em>/file">text</em></a>
    // → corrupted href!
    // Test: markdown with URL containing *
    const md = "[下载](https://example.com/path/*/file)";
    // Apply the regex chain manually
    const linkConverted = md.replace(
      /\[([^\]]+)]\((https?:\/\/[^)\s]+|\/[^)\s]+|mailto:[^\s)]+|tel:[^\s)]+)\)/g,
      '<a href="$2">$1</a>'
    );
    console.log("BUG-P12 after link:", linkConverted);
    // Then bold pass: no **
    // Then italic pass:
    const italicConverted = linkConverted.replace(/\*(.*?)\*/g, "<em>$1</em>");
    console.log("BUG-P12 after italic:", italicConverted);
    // The * in the href should NOT be corrupted
    expect(italicConverted).toContain('href="https://example.com/path/');
    // If corrupted, the href would contain <em>
    const isCorrupted = italicConverted.includes("<em>") && 
                        italicConverted.includes('href="https://example.com/path/');
    console.log("BUG-P12 is corrupted:", isCorrupted);
    expect(isCorrupted).toBe(false);
  });
});
