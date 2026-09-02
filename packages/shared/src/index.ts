export type ContentFormat = "tiptap" | "markdown" | "html";

export interface PostListItem {
  slug: string;
  title: string;
  updatedAt: string;
  createdAt: string;
  viewCount: number;
  commentCount: number;
  excerpt?: string;
  visibility?: "public" | "paid";
  priceCents?: number;
  currency?: string;
}

export interface ArticleMeta extends PostListItem {
  id?: string;
  attachmentCount: number;
  seoTitle?: string;
  seoDescription?: string;
  canonicalPath: string;
}

export interface SearchDocument {
  id: string;
  slug: string;
  title: string;
  body: string;
  excerpt: string;
  updatedAt: string;
}

export interface SearchIndexPayload {
  version: string;
  engine: "local-weighted";
  documents: SearchDocument[];
}

export interface TocItem {
  id: string;
  text: string;
  level: 1 | 2 | 3 | 4 | 5 | 6;
  children?: TocItem[];
}

export interface Attachment {
  id: string;
  ownerType: "post" | "comment";
  ownerId?: string;
  originalFilename: string;
  storedFilename: string;
  storageProvider: "local" | "oss" | "r2";
  storageKey: string;
  publicUrl: string;
  mimeType: string;
  detectedMimeType?: string;
  sizeBytes: number;
  width?: number;
  height?: number;
  sha256?: string;
  createdAt: string;
}

export interface CommentAttachment {
  id: string;
  name: string;
  mimeType: string;
  sizeBytes: number;
  url: string;
  storageProvider?: "local" | "oss" | "r2";
  storageKey?: string;
  storedFilename?: string;
  sha256?: string;
}

export interface Comment {
  id: string;
  postSlug: string;
  parentId: string | null;
  rootId: string | null;
  depth: number;
  path: string;
  username: string;
  content: string;
  attachments: CommentAttachment[];
  createdAt: string;
}

export interface AdminSession {
  adminId: string;
  username: string;
  createdAt: string;
}

export interface ApiEnvelope<T> {
  data: T;
}

export interface ApiErrorBody {
  error: {
    code: string;
    message: string;
    issues?: ApiErrorIssue[];
  };
}

export interface ApiErrorIssue {
  field: string;
  code: string;
  message: string;
  resolution: string;
}

export interface YouTubeVideo {
  videoId: string;
  startSeconds: number;
}

export const CALLOUT_EMOJI_OPTIONS = ["⛱️", "💡", "⚠️", "✅", "📌", "🚀", "❤️", "🔔"] as const;
export const CALLOUT_DEFAULT_EMOJI = CALLOUT_EMOJI_OPTIONS[0];

export type CalloutEmoji = (typeof CALLOUT_EMOJI_OPTIONS)[number];
export type CalloutMarkdownSegment =
  | { type: "markdown"; markdown: string }
  | { type: "callout"; emoji: CalloutEmoji; markdown: string };

const calloutStartPattern = /^:::callout(?:\{emoji="([^"]{0,64})"\})?\s*$/;
const markdownFencePattern = /^\s*(`{3,}|~{3,})/;

export function normalizeCalloutEmoji(value: string | null | undefined): CalloutEmoji {
  return CALLOUT_EMOJI_OPTIONS.find((emoji) => emoji === value) ?? CALLOUT_DEFAULT_EMOJI;
}

export function splitCalloutBlocks(markdown: string): CalloutMarkdownSegment[] {
  const lines = markdown.split(/\r?\n/);
  const segments: CalloutMarkdownSegment[] = [];
  let plainStart = 0;
  let plainFence: { marker: string; length: number } | null = null;

  for (let index = 0; index < lines.length; index += 1) {
    plainFence = nextMarkdownFence(lines[index] ?? "", plainFence);
    if (plainFence) continue;

    const start = lines[index]?.match(calloutStartPattern);
    if (!start) continue;

    let closingIndex = index + 1;
    let contentFence: { marker: string; length: number } | null = null;
    while (closingIndex < lines.length) {
      contentFence = nextMarkdownFence(lines[closingIndex] ?? "", contentFence);
      if (!contentFence && lines[closingIndex]?.trim() === ":::") break;
      closingIndex += 1;
    }
    if (closingIndex >= lines.length) continue;

    const before = lines.slice(plainStart, index).join("\n");
    if (before) segments.push({ type: "markdown", markdown: before });
    segments.push({
      type: "callout",
      emoji: normalizeCalloutEmoji(start[1]),
      markdown: lines.slice(index + 1, closingIndex).join("\n")
    });
    plainStart = closingIndex + 1;
    index = closingIndex;
  }

  const after = lines.slice(plainStart).join("\n");
  if (after || segments.length === 0) segments.push({ type: "markdown", markdown: after });
  return segments;
}

function nextMarkdownFence(
  line: string,
  current: { marker: string; length: number } | null
): { marker: string; length: number } | null {
  const match = line.match(markdownFencePattern);
  if (!match?.[1]) return current;

  const marker = match[1][0] ?? "";
  if (!current) return { marker, length: match[1].length };
  return marker === current.marker && match[1].length >= current.length ? null : current;
}

export function calloutDirective(emoji: string, markdown: string): string {
  const content = markdown.replace(/^\n+|\n+$/g, "");
  return `:::callout{emoji="${normalizeCalloutEmoji(emoji)}"}\n${content}\n:::`;
}

const youtubeVideoIdPattern = /^[A-Za-z0-9_-]{11}$/;
const youtubeDirectivePattern = /^::youtube\[([A-Za-z0-9_-]{11})(?:\?start=(\d+))?]$/;

export function parseYouTubeVideoInput(value: string): YouTubeVideo | null {
  const input = value.trim();
  if (youtubeVideoIdPattern.test(input)) {
    return { videoId: input, startSeconds: 0 };
  }

  const withProtocol = /^[a-z][a-z\d+.-]*:\/\//i.test(input) ? input : `https://${input}`;

  try {
    const url = new URL(withProtocol);
    const hostname = url.hostname.toLowerCase().replace(/^www\./, "");
    let videoId = "";

    if (hostname === "youtu.be") {
      videoId = url.pathname.split("/").filter(Boolean)[0] ?? "";
    } else if (
      hostname === "youtube.com" ||
      hostname === "m.youtube.com" ||
      hostname === "music.youtube.com" ||
      hostname === "youtube-nocookie.com"
    ) {
      const pathParts = url.pathname.split("/").filter(Boolean);
      if (pathParts[0] === "watch") {
        videoId = url.searchParams.get("v") ?? "";
      } else if (["embed", "shorts", "live"].includes(pathParts[0] ?? "")) {
        videoId = pathParts[1] ?? "";
      }
    }

    if (!youtubeVideoIdPattern.test(videoId)) return null;

    const timeValue = url.searchParams.get("start") ?? url.searchParams.get("t") ?? readHashTime(url.hash);
    return {
      videoId,
      startSeconds: parseYouTubeTime(timeValue)
    };
  } catch {
    return null;
  }
}

export function parseYouTubeDirective(value: string): YouTubeVideo | null {
  const match = value.trim().match(youtubeDirectivePattern);
  if (!match) return null;

  return {
    videoId: match[1] ?? "",
    startSeconds: Number.parseInt(match[2] ?? "0", 10)
  };
}

export function youtubeDirective(video: YouTubeVideo): string {
  const start = normalizeStartSeconds(video.startSeconds);
  return `::youtube[${video.videoId}${start ? `?start=${start}` : ""}]`;
}

export function youtubeEmbedUrl(video: YouTubeVideo): string {
  const start = normalizeStartSeconds(video.startSeconds);
  return `https://www.youtube-nocookie.com/embed/${video.videoId}${start ? `?start=${start}` : ""}`;
}

export function youtubeWatchUrl(video: YouTubeVideo): string {
  const start = normalizeStartSeconds(video.startSeconds);
  return `https://www.youtube.com/watch?v=${video.videoId}${start ? `&t=${start}s` : ""}`;
}

function readHashTime(hash: string): string | null {
  const match = hash.match(/(?:^#|[&#])t=([^&]+)/);
  return match?.[1] ? decodeURIComponent(match[1]) : null;
}

function parseYouTubeTime(value: string | null): number {
  if (!value) return 0;
  if (/^\d+$/.test(value)) return normalizeStartSeconds(Number.parseInt(value, 10));

  const match = value.toLowerCase().match(/^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/);
  if (!match) return 0;

  return normalizeStartSeconds(
    Number.parseInt(match[1] ?? "0", 10) * 3600 +
      Number.parseInt(match[2] ?? "0", 10) * 60 +
      Number.parseInt(match[3] ?? "0", 10)
  );
}

function normalizeStartSeconds(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 0;
  return Math.min(Math.floor(value), 86_400);
}
