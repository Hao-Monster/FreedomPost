import MarkdownIt from "markdown-it";
import footnote from "markdown-it-footnote";
import mark from "markdown-it-mark";
import taskLists from "markdown-it-task-lists";
import { sanitizeArticleHtml } from "@freedompost/security";
import {
  splitCalloutBlocks,
  parseYouTubeDirective,
  youtubeEmbedUrl,
  youtubeWatchUrl,
  type ArticleMeta,
  type TocItem
} from "@freedompost/shared";

export interface RenderArticleInput {
  slug: string;
  title: string;
  markdown: string;
  createdAt: string;
  updatedAt: string;
  viewCount?: number;
  commentCount?: number;
  attachmentCount?: number;
}

export interface RenderedArticle {
  html: string;
  fragmentHtml: string;
  toc: TocItem[];
  searchText: string;
  excerpt: string;
  meta: ArticleMeta;
}

export function renderMarkdownArticle(input: RenderArticleInput): RenderedArticle {
  const toc: TocItem[] = [];
  const usedIds = new Map<string, number>();
  const md = createMarkdownRenderer(toc, usedIds);
  const renderedMarkdown = splitCalloutBlocks(input.markdown)
    .map((segment) => {
      if (segment.type === "markdown") return md.render(segment.markdown);
      return `<aside class="fp-callout" role="note"><span class="fp-callout-emoji" aria-hidden="true">${escapeHtml(segment.emoji)}</span><div class="fp-callout-content">${md.render(segment.markdown)}</div></aside>`;
    })
    .join("");
  const html = renderYouTubeEmbeds(renderAttachmentCards(sanitizeArticleHtml(renderedMarkdown)));
  const searchText = extractSearchText(html);
  const excerpt = makeExcerpt(searchText);
  const meta: ArticleMeta = {
    slug: input.slug,
    title: input.title,
    createdAt: input.createdAt,
    updatedAt: input.updatedAt,
    viewCount: input.viewCount ?? 0,
    commentCount: input.commentCount ?? 0,
    attachmentCount: input.attachmentCount ?? 0,
    excerpt,
    seoTitle: input.title,
    seoDescription: excerpt,
    canonicalPath: `/p/${input.slug}`
  };

  return {
    html,
    fragmentHtml: html,
    toc,
    searchText,
    excerpt,
    meta
  };
}

export function createMarkdownRenderer(toc: TocItem[], usedIds: Map<string, number>): MarkdownIt {
  const md = new MarkdownIt({
    html: true,
    linkify: true,
    typographer: true
  })
    .use(footnote)
    .use(mark)
    .use(taskLists, { enabled: true, label: true });

  md.renderer.rules.heading_open = (tokens, idx) => {
    const token = tokens[idx];
    if (!token) return "";
    const next = tokens[idx + 1];
    const level = Number(token.tag.slice(1)) as TocItem["level"];
    const text =
      next?.children
        ?.filter((child) => child.type === "text" || child.type === "code_inline")
        .map((child) => child.content)
        .join("")
        .trim() ||
      next?.content ||
      "";
    const id = uniqueSlug(slugify(text), usedIds);
    token.attrSet("id", id);
    toc.push({ id, text, level });
    return md.renderer.renderToken(tokens, idx, {});
  };

  md.renderer.rules.fence = (tokens, idx) => {
    const token = tokens[idx];
    if (!token) return "";
    const language = token.info.trim().split(/\s+/)[0] || "text";
    const lines = token.content.replace(/\n$/, "").split("\n");
    const numbered = lines
      .map(
        (line, lineIndex) =>
          `<span class="code-line"><span class="line-no">${lineIndex + 1}</span><span>${escapeHtml(
            line
          )}</span></span>`
      )
      .join("");

    return `<div class="code-block" data-lang="${escapeHtml(
      language
    )}"><div class="code-head"><span>${escapeHtml(
      language
    )}</span><span class="code-actions"><button class="fold-code" type="button">折叠</button><button class="copy-code" type="button">复制</button></span></div><pre><code class="language-${escapeHtml(
      language
    )}">${numbered}</code></pre></div>`;
  };

  md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
    const token = tokens[idx];
    if (!token) return "";

    const href = token.attrGet("href");
    if (href && !href.startsWith("#")) {
      token.attrSet("target", "_blank");
      token.attrSet("rel", "noreferrer noopener");
    }

    return self.renderToken(tokens, idx, options);
  };

  md.renderer.rules.image = (tokens, idx, options, env, self) => {
    const token = tokens[idx];
    if (!token) return "";

    token.attrSet("loading", "lazy");
    token.attrSet("alt", token.content);
    const imageHtml = self.renderToken(tokens, idx, options);
    const src = token.attrGet("src");
    if (!src || tokens[idx - 1]?.type === "link_open") {
      return imageHtml;
    }

    return `<a class="article-image-link" href="${escapeHtml(src)}" target="_blank" rel="noreferrer noopener">${imageHtml}</a>`;
  };

  return md;
}

export function extractSearchText(html: string): string {
  return html
    .replace(/<script[\s\S]*?<\/script>/gi, " ")
    .replace(/<style[\s\S]*?<\/style>/gi, " ")
    .replace(/<span\b[^>]*class="[^"]*\bfp-callout-emoji\b[^"]*"[^>]*>[\s\S]*?<\/span>/gi, " ")
    .replace(/<[^>]+>/g, " ")
    .replace(/&nbsp;/g, " ")
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/\s+/g, " ")
    .trim();
}

export function makeExcerpt(text: string, maxLength = 160): string {
  if (text.length <= maxLength) return text;
  return `${text.slice(0, maxLength).trim()}...`;
}

export function renderAttachmentCards(html: string): string {
  return html.replace(
    /<p><a href="([^"]+)"(?:\s+target="[^"]*")?(?:\s+rel="[^"]*")?>(?:附件:|附件：)\s*([^<]+)<\/a><\/p>/g,
    (_match, href: string, filename: string) =>
      `<div class="attachment-card"><span>${escapeHtml(
        filename
          .replace(/&quot;/g, '"')
          .replace(/&#0?39;/g, "'")
          .replace(/&lt;/g, "<")
          .replace(/&gt;/g, ">")
          .replace(/&amp;/g, "&")
      )}</span><a href="${href}" target="_blank" rel="noreferrer noopener" download>下载 / 查看</a></div>`
  );
}

export function renderYouTubeEmbeds(html: string): string {
  return html.replace(
    /<p>(::youtube\[[A-Za-z0-9_-]{11}(?:\?start=\d+)?])<\/p>/g,
    (match, directive: string) => {
      const video = parseYouTubeDirective(directive);
      if (!video) return match;

      return `<figure class="youtube-embed"><div class="youtube-player"><iframe src="${youtubeEmbedUrl(video)}" title="YouTube 视频播放器" loading="lazy" referrerpolicy="strict-origin-when-cross-origin" allow="accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share" allowfullscreen></iframe></div><figcaption><a class="youtube-watch-link" href="${escapeHtml(youtubeWatchUrl(video))}" target="_blank" rel="noreferrer noopener">在 YouTube 官方页面观看 ↗</a></figcaption></figure>`;
    }
  );
}

export function slugify(value: string): string {
  const normalized = value
    .trim()
    .toLowerCase()
    .replace(/[\s_]+/g, "-")
    .replace(/[^\p{Letter}\p{Number}-]+/gu, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "");

  return normalized || "section";
}

export function uniqueSlug(base: string, usedIds: Map<string, number>): string {
  const seen = usedIds.get(base) ?? 0;
  usedIds.set(base, seen + 1);
  return seen === 0 ? base : `${base}-${seen + 1}`;
}

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}
