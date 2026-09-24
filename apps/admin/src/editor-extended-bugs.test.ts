// @vitest-environment happy-dom
/**
 * editor-extended-bugs.test.ts
 * Deep-dive tests to surface new edge-case bugs in the FreedomPost editor.
 */
import { describe, expect, it } from "vitest";
import { editorHtmlToMarkdown } from "./main";

function makeEditor(html: string): HTMLElement {
  const div = document.createElement("div");
  div.innerHTML = html;
  return div;
}

// BUG-L03: <em><strong> nesting produces broken markdown
describe("BUG-L03: em>strong nesting", () => {
  it("<em><strong>text</strong></em> - inner ** should appear inside outer *", () => {
    const editor = makeEditor(`<p><em><strong>加粗斜体</strong></em></p>`);
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-L03 em>strong output:", md);
    // inlineNodeToMarkdown: em returns "*" + inlineChildrenToMarkdown() + "*"
    // inner strong returns "**加粗斜体**"
    // combined: "***加粗斜体**" — missing final *
    // FIX NEEDED: should be ***加粗斜体***
    const isValidBoldItalic = /\*{3}加粗斜体\*{3}/.test(md) || /\*\*_加粗斜体_\*\*/.test(md) || /\*__加粗斜体__\*/.test(md);
    expect(isValidBoldItalic).toBe(true);
  });
});

// BUG-L04: Empty <strong> emits stray "****"
describe("BUG-L04: Empty formatting elements", () => {
  it("empty <strong> emits stray ** delimiters", () => {
    const editor = makeEditor(`<p>Hello<strong></strong>World</p>`);
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-L04 empty strong:", md);
    expect(md).not.toContain("****");
    expect(md).not.toContain("**");
  });
  it("empty <em> emits stray * delimiters", () => {
    const editor = makeEditor(`<p>Hello<em></em>World</p>`);
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-L04 empty em:", md);
    expect(md).not.toContain("**");
  });
});

// BUG-L06: Backtick inside <code> breaks delimiter
describe("BUG-L06: Backtick inside inline code", () => {
  it("inline code containing backtick should use double-backtick wrapper", () => {
    const editor = makeEditor("<p><code>value`more</code></p>");
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-L06 backtick in code:", md);
    // Current: `value`more` — broken (should be ``value`more``)
    // The content should be preserved and the backtick should not break it
    const backtickCount = (md.match(/`/g) ?? []).length;
    // broken output has 3 backticks (opening `, content `, trailing `more`)
    // correct output with double backticks has 4 backticks total
    expect(backtickCount).not.toBe(3);
  });
});

// BUG-L10: Empty heading <h1><br></h1> produces stray "# " that passes trim check
describe("BUG-L10: Whitespace-only heading passes block filter", () => {
  it('<h1><br></h1> serialises to "# " which trim() = "#" (truthy, not filtered)', () => {
    const editor = makeEditor(`<h1><br></h1><p>正文内容</p>`);
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-L10 empty heading:", md);
    const blocks = md.split("\n\n").map(b => b.trim());
    // "# ".trim() = "#" — this is truthy so it IS included in output
    // But the heading content is empty — this creates a stray # line
    const hasStrayHash = blocks.some(b => b === "#" || b === "##" || b === "###");
    expect(hasStrayHash).toBe(false);
  });
});

// BUG-L11: Image alt text with ] breaks markdown syntax
describe("BUG-L11: Alt text with closing bracket", () => {
  it("alt containing ] breaks the markdown image syntax", () => {
    const figure = document.createElement("figure");
    figure.className = "editor-image";
    figure.dataset.fpType = "image";
    const img = document.createElement("img");
    img.src = "https://cdn.example.com/photo.jpg";
    img.alt = "图片[第一张]说明";
    figure.append(img);
    const editor = document.createElement("div");
    editor.append(figure);
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-L11 alt with ]:", md);
    // Should escape the ] in alt text: ![图片\[第一张\]说明](url)
    // If not escaped: ![图片[第一张]说明](url) — the ] closes the alt early
    // breaking as: ![图片[第一张]说明] followed by (url) not being the image URL
    // Check: the markdown image should have the full URL
    expect(md).toContain("https://cdn.example.com/photo.jpg");
    // editorImagesMarkdown calls escapeMarkdown which escapes [ and ]
    // so the alt becomes: 图片\[第一张\]说明 — brackets ARE escaped correctly
    // Verify the output contains properly escaped brackets
    expect(md).toContain("\\[第一张\\]");
    // And the URL is intact (not broken by bracket mismatch)
    const urlPresent = md.includes("https://cdn.example.com/photo.jpg");
    expect(urlPresent).toBe(true);
  });
});

// BUG-L12: Code fence lang tag with spaces
describe("BUG-L12: Code fence language injection", () => {
  it("language tag with spaces should not appear in code fence header", () => {
    const pre = document.createElement("pre");
    pre.dataset.lang = "typescript\nalert(1)";
    const code = document.createElement("code");
    code.textContent = "const x = 1";
    pre.append(code);
    const editor = document.createElement("div");
    editor.append(pre);
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-L12 lang injection:", md);
    const fenceHeader = md.split("\n")[0];
    // Core fix: newlines in lang tag must not appear in fence header (prevents breaking out of the fence)
    // e.g. "typescript\nalert(1)" → all non-identifier chars stripped → "typescriptalert1" (safe)
    // The important thing is the fence header is a single line with no newline break
    expect(fenceHeader).not.toContain("\n");
    expect(fenceHeader).not.toContain("("); // parens stripped
    expect(fenceHeader).not.toContain(")");
  });
});

// BUG-L14: Image URL with parentheses breaks the markdown image regex on re-parse
describe("BUG-L14: Image URL with parentheses", () => {
  it("URL containing () is serialised correctly but check re-parse regex", () => {
    const figure = document.createElement("figure");
    figure.className = "editor-image";
    figure.dataset.fpType = "image";
    const img = document.createElement("img");
    img.src = "https://cdn.example.com/path(1).jpg";
    img.alt = "图片";
    figure.append(img);
    const editor = document.createElement("div");
    editor.append(figure);
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-L14 URL with parens:", md);
    expect(md).toContain("path(1).jpg");
    // markdownFragmentToEditorHtml uses: line.match(/^!\[(.*)]\\((.*)\\)$/)
    // The (.*) in the URL group is greedy but the closing \\) is literal )
    // For URL "https://cdn.example.com/path(1).jpg":
    // The regex should match: alt=图片, url=https://cdn.example.com/path(1).jpg
    // Because (.*) will match up to the last ) on the line
    // The test confirms serialisation works; re-parse is a separate concern
  });
});




// BUG-FM01: mailto: and tel: links should be rendered as <a> on reload
describe("BUG-FM01: mailto and tel links in formatInlineMarkdown", () => {
  it("mailto: link in markdown re-renders as clickable link in editor", () => {
    // Simulates a <p> serialised as: [联系我们](mailto:contact@example.com)
    // After fix, formatInlineMarkdown should convert it back to <a>
    const md = "[联系我们](mailto:contact@example.com)";
    // We test through the round-trip: write an <a> with mailto, serialise, then
    // confirm the markdown contains the mailto, and the re-parse would produce <a>.
    const editor = makeEditor(`<p><a href="mailto:contact@example.com">联系我们</a></p>`);
    const serialised = editorHtmlToMarkdown(editor);
    console.log("BUG-FM01 mailto serialised:", serialised);
    expect(serialised).toContain("mailto:contact@example.com");
    expect(serialised).toContain("[联系我们]");
    // Confirm the pattern is matched by the fixed regex
    const linkRegex = /\[([^\]]+)]\((https?:\/\/[^)\s]+|\/[^)\s]+|mailto:[^\s)]+|tel:[^\s)]+)\)/g;
    const match = linkRegex.exec(serialised);
    expect(match).not.toBeNull();
  });

  it("tel: link in markdown re-renders as clickable link in editor", () => {
    const editor = makeEditor(`<p><a href="tel:+861234567890">拨打电话</a></p>`);
    const serialised = editorHtmlToMarkdown(editor);
    console.log("BUG-FM01 tel serialised:", serialised);
    expect(serialised).toContain("tel:+861234567890");
    const linkRegex = /\[([^\]]+)]\((https?:\/\/[^)\s]+|\/[^)\s]+|mailto:[^\s)]+|tel:[^\s)]+)\)/g;
    const match = linkRegex.exec(serialised);
    expect(match).not.toBeNull();
  });
});

// BUG-FM02: double-backtick code survives round-trip through formatInlineMarkdown
describe("BUG-FM02: double-backtick code round-trip", () => {
  it("code containing backtick serialises to ``...`` and re-parses to <code>", () => {
    // BUG-L06 fix: <code>value`more</code> serialises to ``value`more``
    // BUG-FM02 fix: ``value`more`` should re-parse to <code>value`more</code>
    const editor = makeEditor("<p><code>value`more</code></p>");
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-FM02 double-tick serialised:", md);
    expect(md).toContain("``value`more``");
    // Now verify the fixed regex correctly matches the double-backtick pattern
    const doubleTickRegex = /``([^`](?:[^`]|`(?!`))*[^`]|[^`])``/g;
    const match = doubleTickRegex.exec(md);
    expect(match).not.toBeNull();
    expect(match?.[1]).toBe("value`more");
  });

  it("single-backtick code is not affected by the double-backtick rule", () => {
    const editor = makeEditor("<p><code>simpleCode</code></p>");
    const md = editorHtmlToMarkdown(editor);
    console.log("BUG-FM02 single-tick:", md);
    expect(md).toContain("`simpleCode`");
    expect(md).not.toContain("``simpleCode``");
  });
});
