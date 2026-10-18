import { describe, expect, it } from "vitest";
import { parseYouTubeVideoInput, youtubeDirective } from "@freedompost/shared";
import { renderMarkdownArticle } from "./index.js";

describe("renderMarkdownArticle", () => {
  it("generates toc and sanitized html", () => {
    const result = renderMarkdownArticle({
      slug: "hello",
      title: "Hello",
      markdown: "# 标题\n\n<script>alert(1)</script>\n\n```ts\nconsole.log('ok')\n```",
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.toc[0]?.text).toBe("标题");
    expect(result.html).not.toContain("<script>");
    expect(result.html).toContain("code-block");
  });

  it("renders images as links to the original asset", () => {
    const result = renderMarkdownArticle({
      slug: "image",
      title: "Image",
      markdown: "![screenshot](https://pic.example.com/a.png)",
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.html).toContain('class="article-image-link"');
    expect(result.html).toContain('href="https://pic.example.com/a.png"');
    expect(result.html).toContain('src="https://pic.example.com/a.png"');
  });

  it("opens article links and attachment previews in a new tab", () => {
    const result = renderMarkdownArticle({
      slug: "links",
      title: "Links",
      markdown:
        "[Example](https://example.com)\n\n[附件: movie.mp4](https://pic.example.com/movie.mp4)\n\n[Jump](#section)",
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.html).toContain('<a href="https://example.com" target="_blank" rel="noreferrer noopener">Example</a>');
    expect(result.html).toContain('class="attachment-card"');
    expect(result.html).toContain('href="https://pic.example.com/movie.mp4" target="_blank" rel="noreferrer noopener" download');

    const inPageAnchor = result.html.match(/<a href="#section"[^>]*>/)?.[0] ?? "";
    expect(inPageAnchor).not.toContain('target="_blank"');
  });

  it("keeps editor inline formatting while stripping unsafe attributes", () => {
    const result = renderMarkdownArticle({
      slug: "formatting",
      title: "Formatting",
      markdown:
        '<span class="fp-color-red fp-size-lg bad-class" onclick="alert(1)">Red text</span> and <u>underlined</u>',
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.html).toContain('<span class="fp-color-red fp-size-lg">Red text</span>');
    expect(result.html).toContain("<u>underlined</u>");
    expect(result.html).not.toContain("onclick");
    expect(result.html).not.toContain("bad-class");
  });

  it("renders every image in an image-heavy article without imposing a count limit", () => {
    const markdown = Array.from(
      { length: 12 },
      (_, index) => `![图片 ${index + 1}](https://pic.example.com/${index + 1}.webp)`
    ).join("\n\n");
    const result = renderMarkdownArticle({
      slug: "image-heavy",
      title: "Image-heavy article",
      markdown,
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.html.match(/<img\b/g)).toHaveLength(12);
    expect(result.html).toContain("https://pic.example.com/12.webp");
  });

  it("renders a responsive privacy-enhanced YouTube player", () => {
    const result = renderMarkdownArticle({
      slug: "video",
      title: "Video",
      markdown: "::youtube[dQw4w9WgXcQ?start=62]",
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.html).toContain('class="youtube-embed"');
    expect(result.html).toContain('src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?start=62"');
    expect(result.html).toContain('href="https://www.youtube.com/watch?v=dQw4w9WgXcQ&amp;t=62s"');
    expect(result.html).toContain("在 YouTube 官方页面观看");
    expect(result.html).toContain("allowfullscreen");
  });

  it("does not allow arbitrary iframe HTML", () => {
    const result = renderMarkdownArticle({
      slug: "unsafe-video",
      title: "Unsafe video",
      markdown: '<iframe src="https://evil.example/embed"></iframe>',
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.html).not.toContain("<iframe");
    expect(result.html).not.toContain("evil.example");
  });

  it("renders a multi-block callout with existing rich content", () => {
    const result = renderMarkdownArticle({
      slug: "callout",
      title: "Callout",
      markdown:
        ':::callout{emoji="💡"}\n**Important** paragraph\n\n- First\n- Second\n\n::youtube[dQw4w9WgXcQ]\n:::',
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.html).toContain('<aside class="fp-callout" role="note">');
    expect(result.html).toContain('<span class="fp-callout-emoji" aria-hidden="true">💡</span>');
    expect(result.html).toContain("<strong>Important</strong>");
    expect(result.html).toContain("<ul>");
    expect(result.html).toContain('class="youtube-embed"');
    expect(result.searchText).toContain("Important paragraph First Second");
    expect(result.searchText).not.toContain("💡");
  });

  it("normalizes untrusted callout metadata and sanitizes its content", () => {
    const result = renderMarkdownArticle({
      slug: "safe-callout",
      title: "Safe callout",
      markdown: ':::callout{emoji="<img>"}\n<span onclick="alert(1)" class="bad-class">Safe</span>\n:::',
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.html).toContain("fp-callout");
    expect(result.html).toContain("Safe");
    expect(result.html).not.toContain("onclick");
    expect(result.html).not.toContain("bad-class");
    expect(result.html).not.toContain("<img>");
  });

  it("extracts plain text from headings with links, emphasis, and inline code for TOC and anchor IDs", () => {
    const result = renderMarkdownArticle({
      slug: "heading-inline",
      title: "Heading Inline",
      markdown: "## Hello [World](https://example.com) and `code` **bold**",
      createdAt: "2026-07-02T00:00:00.000Z",
      updatedAt: "2026-07-02T00:00:00.000Z"
    });

    expect(result.toc[0]?.text).toBe("Hello World and code bold");
    expect(result.toc[0]?.id).toBe("hello-world-and-code-bold");
    expect(result.html).toContain('<h2 id="hello-world-and-code-bold">');
  });
});

describe("YouTube link parsing", () => {
  it.each([
    ["https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=1m2s", 62],
    ["https://youtu.be/dQw4w9WgXcQ?t=90", 90],
    ["https://www.youtube.com/shorts/dQw4w9WgXcQ", 0],
    ["youtube.com/embed/dQw4w9WgXcQ?start=12", 12]
  ])("accepts %s", (input, startSeconds) => {
    const video = parseYouTubeVideoInput(input);

    expect(video).toEqual({ videoId: "dQw4w9WgXcQ", startSeconds });
    expect(youtubeDirective(video!)).toContain("::youtube[dQw4w9WgXcQ");
  });

  it("rejects non-YouTube and malformed links", () => {
    expect(parseYouTubeVideoInput("https://example.com/watch?v=dQw4w9WgXcQ")).toBeNull();
    expect(parseYouTubeVideoInput("https://youtube.com/watch?v=too-short")).toBeNull();
  });
});
