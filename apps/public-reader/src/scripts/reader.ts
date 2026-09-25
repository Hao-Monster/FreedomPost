import { createIcons, icons } from "lucide";
import {
  articlePermalinkPath,
  normalizeReferral,
  readArticleSlugFromPath,
  referralStorageKey
} from "../lib/article-links.js";
import {
  finishReaderBootGuard,
  releaseReaderBootGuardIfUnrequested
} from "../lib/reader-boot.js";
import {
  renderPaidArticleGate,
  setPaidContentProtection,
  type ReaderAccessState
} from "./paid-access.js";

type TocItem = {
  id: string;
  text: string;
  level: 1 | 2 | 3 | 4 | 5 | 6;
};

type PostListItem = {
  slug: string;
  title: string;
  createdAt: string;
  updatedAt: string;
  viewCount: number;
  commentCount: number;
  excerpt?: string;
  visibility?: "public" | "paid";
  priceCents?: number;
  currency?: string;
};

type SearchDocument = {
  id: string;
  slug: string;
  title: string;
  body: string;
  excerpt: string;
  updatedAt: string;
};

type ArticleMeta = PostListItem & {
  attachmentCount?: number;
  canonicalPath?: string;
};

type ApiPostDetail = PostListItem & {
  contentHtml: string;
  markdown?: string;
  attachmentCount: number;
  visibility?: "public" | "paid";
  priceCents?: number;
  currency?: string;
};

type ArticleCacheItem = {
  slug: string;
  html: string;
  toc: TocItem[];
  meta: ArticleMeta;
  cachedAt: number;
  access?: ReaderAccessState;
  paid?: {
    priceCents: number;
    currency: string;
    wechatImageUrl?: string;
  };
};

type StoredCommentAttachment = {
  id?: string;
  name: string;
  type?: string;
  mimeType?: string;
  size?: number;
  sizeBytes?: number;
  url?: string;
  storageProvider?: "local" | "oss" | "r2";
  storageKey?: string;
  storedFilename?: string;
  sha256?: string;
};

type StoredComment = {
  id: string;
  postSlug?: string;
  parentId: string | null;
  rootId?: string | null;
  depth?: number;
  path?: string;
  username: string;
  content: string;
  attachments: StoredCommentAttachment[];
  createdAt: string;
};

const storageKeys = {
  theme: "fp_theme_v1",
  listWidth: "fp_list_width_v1",
  user: "fp_comment_user_v1",
  device: "fp_device_id_v1",
  comments: "fp_comments_v1",
  rate: "fp_comment_rate_v1"
};
const configuredPublicOrigin = normalizeOrigin(import.meta.env.PUBLIC_SITE_URL);
const pageSearchParams = new URLSearchParams(location.search);
const activeReferral = readAndLockReferral(pageSearchParams.get("ref"));

const app = mustGet<HTMLElement>("app");
const postList = mustGet<HTMLElement>("postList");
const listMeta = mustGet<HTMLElement>("listMeta");
const searchInput = mustGet<HTMLInputElement>("searchInput");
const articleBody = mustGet<HTMLElement>("articleBody");
const articleTitle = mustGet<HTMLElement>("articleTitle");
const articleDate = mustGet<HTMLElement>("articleDate");
const articleViews = mustGet<HTMLElement>("articleViews");
const articleComments = mustGet<HTMLElement>("articleComments");
const topArticleTitle = mustGet<HTMLElement>("topArticleTitle");
const topArticleDate = mustGet<HTMLElement>("topArticleDate");
const topArticleViews = mustGet<HTMLElement>("topArticleViews");
const topArticleComments = mustGet<HTMLElement>("topArticleComments");
const tocBody = mustGet<HTMLElement>("tocBody");
const tocToggle = mustGet<HTMLButtonElement>("tocToggle");
const readerScroll = mustGet<HTMLElement>("readerScroll");
const shareBtn = mustGet<HTMLButtonElement>("shareBtn");
const mobilePostsBtn = mustGet<HTMLButtonElement>("mobilePostsBtn");
const mobileTocBtn = mustGet<HTMLButtonElement>("mobileTocBtn");
const mobileCommentsBtn = mustGet<HTMLButtonElement>("mobileCommentsBtn");
const mobileShareBtn = mustGet<HTMLButtonElement>("mobileShareBtn");
const mobileReaderBackdrop = mustGet<HTMLButtonElement>("mobileReaderBackdrop");
const mobilePostsClose = mustGet<HTMLButtonElement>("mobilePostsClose");
const articleListPanel = document.querySelector<HTMLElement>(".article-list-panel");
const tocPanel = mustGet<HTMLElement>("tocPanel");
const themeBtn = mustGet<HTMLButtonElement>("themeBtn");
const toast = mustGet<HTMLElement>("toast");
const commentForm = mustGet<HTMLFormElement>("commentForm");
const commentSection = commentForm.closest<HTMLElement>(".comments");
if (!commentSection) throw new Error("Missing comments section");
const commentText = mustGet<HTMLTextAreaElement>("commentText");
const commentPendingFiles = mustGet<HTMLElement>("commentPendingFiles");
const commentSubmit = mustGet<HTMLButtonElement>("commentSubmit");
const commentList = mustGet<HTMLElement>("commentList");
const commentUser = mustGet<HTMLElement>("commentUser");
const commentDefaultPlaceholder = "写下评论或者粘贴图片或者拖入文件";

const initial = readInitialPayload();
const pathSlug = readArticleSlugFromPath(location.pathname);
const requestedSlug = pageSearchParams.get("post")?.trim() || pathSlug || null;
const embeddedPortal = pageSearchParams.get("embed") === "portal";
releaseReaderBootGuardIfUnrequested(document.documentElement, document.body, requestedSlug, embeddedPortal);
let activeSlug = requestedSlug ?? initial?.slug ?? document.body.dataset.activeSlug ?? "";
let posts: PostListItem[] = [];
let searchDocs: SearchDocument[] = [];
let query = "";
let replyParentId: string | null = null;
let toastTimer = 0;
let headingObserver: IntersectionObserver | null = null;
let pendingCommentFiles: File[] = [];
let commentDragDepth = 0;
let commentSubmitting = false;

const articleCache = new Map<string, ArticleCacheItem>();
const commentCache = new Map<string, StoredComment[]>();

class ApiPostNotFoundError extends Error {}

if (initial) {
  articleCache.set(initial.slug, {
    slug: initial.slug,
    html: articleBody.innerHTML,
    toc: initial.toc,
    meta: initial.meta,
    cachedAt: Date.now()
  });
}

void init();

async function init() {
  applyStoredTheme();
  applyStoredListWidth();
  applyStoredTocState();
  // Restore persisted state before replacing the icon placeholders.  Both
  // helpers update button.innerHTML, so rendering icons first leaves the
  // restored dark-theme buttons as empty <i data-lucide> elements.
  createIcons({ icons });
  enhanceCodeBlocks();
  propagateReaderLinks();
  renderToc(initial?.toc ?? []);
  renderComments();
  void refreshComments(activeSlug);
  bindEvents();
  const requestedArticleHydration = requestedSlug ? hydrateRequestedArticle(requestedSlug) : null;

  try {
    const [postItems, searchPayload] = await Promise.all([fetchPostSummaries(), fetchSearchPayload()]);
    posts = postItems;
    searchDocs = searchPayload.documents;
    renderList();
  } catch {
    listMeta.textContent = "文章列表暂不可用";
  }

  if (requestedSlug && requestedArticleHydration) {
    await requestedArticleHydration;
    if (!articleCache.has(requestedSlug) || !activeSlug) return;
  } else if (posts.length > 0) {
    const nextSlug = posts.some((post) => post.slug === activeSlug) ? activeSlug : posts[0]?.slug;
    if (nextSlug) {
      articleCache.delete(nextSlug);
      await openArticle(nextSlug, { push: false, countView: false });
    }
  } else if (embeddedPortal) {
    renderUnavailableArticle();
    finishReaderBootGuard(document.documentElement, document.body);
  }

  prefetchIdleArticles();

  commentUser.textContent = getRandomUsername();
  if (!articleCache.get(activeSlug)?.access?.locked) countViewOnce(activeSlug);
}

async function hydrateRequestedArticle(slug: string) {
  // A URL slug is authoritative and must never be replaced by the first list item.
  articleCache.delete(slug);
  await openArticle(slug, { push: false, countView: false });
}

function bindEvents() {
  searchInput.addEventListener("input", () => {
    query = searchInput.value.trim();
    renderList();
    const first = getFilteredPosts()[0];
    if (first && first.slug !== activeSlug) {
      void openArticle(first.slug, { push: true, countView: true });
    }
  });

  tocToggle.addEventListener("click", () => {
    if (isMobileReader()) {
      closeMobileReaderPanels();
      return;
    }
    const collapsed = app.classList.toggle("toc-collapsed");
    localStorage.setItem("fp_toc_collapsed_v1", collapsed ? "1" : "0");
    tocToggle.innerHTML = collapsed
      ? '<i data-lucide="panel-left-open"></i>'
      : '<i data-lucide="panel-left-close"></i>';
    createIcons({ icons });
  });

  shareBtn.addEventListener("click", copyArticleLink);
  mobileShareBtn.addEventListener("click", copyArticleLink);
  mobilePostsBtn.addEventListener("click", () => toggleMobileReaderPanel("posts"));
  mobileTocBtn.addEventListener("click", () => toggleMobileReaderPanel("toc"));
  mobileReaderBackdrop.addEventListener("click", closeMobileReaderPanels);
  mobilePostsClose.addEventListener("click", closeMobileReaderPanels);
  mobileCommentsBtn.addEventListener("click", () => {
    closeMobileReaderPanels();
    commentSection?.scrollIntoView({ behavior: prefersReducedMotion() ? "auto" : "smooth", block: "start" });
  });

  themeBtn.addEventListener("click", () => {
    const current = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = current;
    localStorage.setItem(storageKeys.theme, current);
    themeBtn.innerHTML = current === "dark" ? '<i data-lucide="sun"></i>' : '<i data-lucide="moon"></i>';
    createIcons({ icons });
  });

  commentForm.addEventListener("submit", submitComment);
  commentText.addEventListener("input", updateCommentSubmitState);
  commentText.addEventListener("paste", handleCommentPaste);
  commentForm.addEventListener("dragenter", handleCommentDragEnter);
  commentForm.addEventListener("dragover", handleCommentDragOver);
  commentForm.addEventListener("dragleave", handleCommentDragLeave);
  commentForm.addEventListener("drop", handleCommentDrop);
  commentPendingFiles.addEventListener("click", handlePendingFileClick);
  updateCommentSubmitState();
  initResizer();

  window.addEventListener("popstate", () => {
    const slug = readArticleSlugFromPath(location.pathname) || new URLSearchParams(location.search).get("post")?.trim() || posts[0]?.slug;

    if (slug) {
      void openArticle(slug, { push: false, countView: false });
    }
  });

  window.addEventListener("focus", () => {
    void refreshPostsFromApi();
  });
  window.addEventListener("resize", () => {
    if (!isMobileReader()) closeMobileReaderPanels();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && app.classList.contains("mobile-panel-open")) {
      closeMobileReaderPanels();
    }
  });
}

async function copyArticleLink(): Promise<void> {
  const url = publicArticleUrl(activeSlug);
  await navigator.clipboard.writeText(url).catch(() => undefined);
  showToast("文章链接已复制");
}

function toggleMobileReaderPanel(panel: "posts" | "toc"): void {
  const target = panel === "posts" ? articleListPanel : tocPanel;
  const wasOpen = target?.classList.contains("mobile-open") === true;
  closeMobileReaderPanels();
  if (wasOpen || !target) return;
  target.classList.add("mobile-open");
  app.classList.add("mobile-panel-open");
  mobileReaderBackdrop.hidden = false;
  mobilePostsBtn.setAttribute("aria-expanded", String(panel === "posts"));
  mobileTocBtn.setAttribute("aria-expanded", String(panel === "toc"));
  const focusTarget = panel === "posts" ? mobilePostsClose : tocPanel.querySelector<HTMLElement>("a,button");
  window.setTimeout(() => focusTarget?.focus(), 0);
}

function closeMobileReaderPanels(): void {
  articleListPanel?.classList.remove("mobile-open");
  tocPanel.classList.remove("mobile-open");
  app.classList.remove("mobile-panel-open");
  mobileReaderBackdrop.hidden = true;
  mobilePostsBtn.setAttribute("aria-expanded", "false");
  mobileTocBtn.setAttribute("aria-expanded", "false");
}

function isMobileReader(): boolean {
  return window.matchMedia("(max-width: 767px)").matches;
}

function prefersReducedMotion(): boolean {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function renderList() {
  const filtered = getFilteredPosts();
  listMeta.textContent = query ? `匹配 ${filtered.length} 篇内容` : `共 ${filtered.length} 篇内容`;
  postList.innerHTML = filtered
    .map((post) => {
      const localCommentCount = getComments(activeSlug === post.slug ? activeSlug : post.slug).length;
      const commentCount = Math.max(post.commentCount, localCommentCount);
      return `<button class="post-item ${post.slug === activeSlug ? "active" : ""}" type="button" data-slug="${escapeHtml(
        post.slug
      )}">
        <span class="post-title" title="${escapeHtml(post.title)}">${highlight(post.title, query)}${post.visibility === "paid" ? ' <small class="paid-list-badge">付费</small>' : ""}</span>
        <span class="post-stats">
          <span>${formatDate(post.updatedAt)}</span>
          <span>${post.viewCount} 阅读</span>
          <span>${commentCount} 评论</span>
        </span>
      </button>`;
    })
    .join("");

  postList.querySelectorAll<HTMLButtonElement>(".post-item").forEach((button) => {
    const slug = button.dataset.slug;
    if (!slug) return;

    button.addEventListener("click", () => {
      closeMobileReaderPanels();
      void openArticle(slug, { push: true, countView: true });
    });
    button.addEventListener("mouseenter", () => {
      void prefetchArticle(slug);
    });
  });
}

function getFilteredPosts() {
  const sorted = [...posts].sort((left, right) => right.createdAt.localeCompare(left.createdAt));
  const normalized = normalize(query);
  if (!normalized) return sorted;

  const docMap = new Map(searchDocs.map((doc) => [doc.slug, doc]));
  const postMap = new Map(sorted.map((post) => [post.slug, post]));
  const titleHits: PostListItem[] = [];
  const bodyHits: PostListItem[] = [];

  for (const post of sorted) {
    const doc = docMap.get(post.slug);
    const title = normalize(post.title);
    const body = normalize(doc?.body ?? post.excerpt ?? "");

    if (title.includes(normalized)) {
      titleHits.push(post);
    } else if (body.includes(normalized)) {
      bodyHits.push(post);
    }
  }

  return [...titleHits, ...bodyHits].filter((post) => postMap.has(post.slug));
}

async function openArticle(
  slug: string,
  options: { push?: boolean; countView?: boolean } = {}
) {
  const cached = articleCache.get(slug) ?? (await prefetchArticle(slug));
  if (!cached) {
    if (requestedSlug) renderUnavailableArticle();
    finishReaderBootGuard(document.documentElement, document.body);
    if (location.pathname.startsWith("/p/")) location.replace("/");
    return;
  }

  const canonicalSlug = cached.slug || cached.meta.slug || slug;
  activeSlug = canonicalSlug;
  if (cached.paid && cached.access?.purchased) articleCache.delete(canonicalSlug);
  else articleCache.set(canonicalSlug, cached);
  const lockedPaidArticle = cached.access?.locked === true && cached.paid !== undefined;
  if (lockedPaidArticle && cached.paid) {
    renderPaidArticleGate(articleBody, {
      slug: canonicalSlug,
      title: cached.meta.title,
      excerpt: cached.meta.excerpt ?? "",
      priceCents: cached.paid.priceCents,
      currency: cached.paid.currency,
      access: cached.access ?? { locked: true, authenticated: false, purchased: false },
      ...(cached.paid.wechatImageUrl ? { wechatImageUrl: cached.paid.wechatImageUrl } : {})
    }, () => {
      articleCache.delete(canonicalSlug);
      void openArticle(canonicalSlug, { push: false, countView: false });
    });
  } else {
    articleBody.innerHTML = cached.html;
  }
  setPaidContentProtection(articleBody, cached.paid !== undefined && cached.access?.purchased === true);
  articleTitle.textContent = cached.meta.title;
  articleDate.textContent = `发布时间：${formatDate(cached.meta.createdAt)}`;
  articleViews.textContent = `${cached.meta.viewCount} 阅读`;
  articleComments.textContent = `${currentCommentCount(cached.meta)} 评论`;
  topArticleTitle.textContent = cached.meta.title;
  topArticleDate.textContent = formatDate(cached.meta.createdAt);
  topArticleViews.textContent = `${cached.meta.viewCount} 阅读`;
  topArticleComments.textContent = `${currentCommentCount(cached.meta)} 评论`;
  document.title = cached.meta.title;

  renderToc(cached.toc);
  if (!lockedPaidArticle) enhanceCodeBlocks();
  propagateReaderLinks();
  commentForm.hidden = lockedPaidArticle;
  commentList.hidden = lockedPaidArticle;
  commentSection!.hidden = lockedPaidArticle;
  mobileCommentsBtn.disabled = lockedPaidArticle;
  if (!lockedPaidArticle) {
    renderComments();
    void refreshComments(canonicalSlug);
  }
  renderList();
  readerScroll.scrollTop = 0;

  if (options.countView && !lockedPaidArticle) {
    countViewOnce(canonicalSlug);
  }

  const permalink = articlePermalinkPath(canonicalSlug, activeReferral);
  if (window.parent === window) {
    if (options.push && `${location.pathname}${location.search}` !== permalink) {
      history.pushState(null, "", permalink);
    } else if (slug !== canonicalSlug && location.pathname.startsWith("/p/")) {
      history.replaceState(history.state, "", permalink);
    }
  }
  notifyParentArticleChange(canonicalSlug, cached.meta.title, options.push === true);

  finishReaderBootGuard(document.documentElement, document.body);
}

function renderUnavailableArticle() {
  const title = "文章暂时无法加载";
  activeSlug = "";
  articleTitle.textContent = title;
  articleDate.textContent = "请刷新页面后重试";
  articleViews.textContent = "";
  articleComments.textContent = "";
  topArticleTitle.textContent = title;
  topArticleDate.textContent = "";
  topArticleViews.textContent = "";
  topArticleComments.textContent = "";
  articleBody.replaceChildren();
  renderToc([]);
  renderComments();
  document.title = title;
}

async function prefetchArticle(slug: string): Promise<ArticleCacheItem | null> {
  const cached = articleCache.get(slug);
  if (cached) return cached;

  let apiItem: ArticleCacheItem | null = null;
  try {
    apiItem = await fetchArticleFromApi(slug);
  } catch (error) {
    if (error instanceof ApiPostNotFoundError) return null;
  }
  if (apiItem) {
    if (!apiItem.paid || !apiItem.access?.purchased) {
      articleCache.set(slug, apiItem);
      articleCache.set(apiItem.slug, apiItem);
    }
    return apiItem;
  }

  try {
    const [html, meta, toc] = await Promise.all([
      fetchText(`/p/${encodeURIComponent(slug)}/article.fragment.html`),
      fetchJson<ArticleMeta>(`/p/${encodeURIComponent(slug)}/article.meta.json`),
      fetchJson<TocItem[]>(`/p/${encodeURIComponent(slug)}/toc.json`)
    ]);
    const item = {
      slug,
      html,
      meta,
      toc,
      cachedAt: Date.now()
    };
    articleCache.set(slug, item);
    return item;
  } catch {
    showToast("文章暂时无法打开");
    return null;
  }
}

async function fetchArticleFromApi(slug: string): Promise<ArticleCacheItem | null> {
  try {
    let response = await fetch(`/api/reader/posts/${encodeURIComponent(slug)}`, { credentials: "include" });
    let access: ReaderAccessState | undefined;
    let contact: { wechatImageUrl?: string } | undefined;
    if (response.ok) {
      const readerPayload = (await response.json()) as { item: ApiPostDetail; access?: ReaderAccessState; contact?: { wechatImageUrl?: string } };
      const item = readerPayload.item;
      access = readerPayload.access;
      contact = readerPayload.contact;
      return {
        slug: item.slug,
        html: item.contentHtml || "",
        toc: item.contentHtml ? extractTocFromHtml(item.contentHtml) : [],
        meta: {
          slug: item.slug,
          title: item.title,
          createdAt: item.createdAt,
          updatedAt: item.updatedAt,
          viewCount: item.viewCount,
          commentCount: item.commentCount,
          excerpt: item.excerpt,
          attachmentCount: item.attachmentCount,
          canonicalPath: articlePermalinkPath(item.slug)
        },
        cachedAt: Date.now(),
        ...(item.visibility === "paid" ? {
          access: access ?? { locked: true, authenticated: false, purchased: false },
          paid: {
            priceCents: item.priceCents ?? 0,
            currency: item.currency ?? "CNY",
            ...(contact?.wechatImageUrl ? { wechatImageUrl: contact.wechatImageUrl } : {})
          }
        } : {})
      };
    }
    if (response.status !== 404) throw new Error(`Failed to fetch article ${slug}`);
    response = await fetch(`/api/posts/${encodeURIComponent(slug)}`);
    if (response.status === 404) throw new ApiPostNotFoundError("Article is not readable");
    if (!response.ok) throw new Error(`Failed to fetch article ${slug}`);
    const payload = (await response.json()) as { item: ApiPostDetail };
    const item = payload.item;
    return {
      slug: item.slug,
      html: item.contentHtml,
      toc: extractTocFromHtml(item.contentHtml),
      meta: {
        slug: item.slug,
        title: item.title,
        createdAt: item.createdAt,
        updatedAt: item.updatedAt,
        viewCount: item.viewCount,
        commentCount: item.commentCount,
        excerpt: item.excerpt,
        attachmentCount: item.attachmentCount,
        canonicalPath: articlePermalinkPath(item.slug)
      },
      cachedAt: Date.now()
    };
  } catch (error) {
    if (error instanceof ApiPostNotFoundError) throw error;
    return null;
  }
}

async function fetchPostSummaries(): Promise<PostListItem[]> {
  try {
    const payload = await fetchJson<{ items: PostListItem[] }>("/api/posts");
    return payload.items;
  } catch {
    return fetchJson<PostListItem[]>("/search/post-list.json");
  }
}

async function fetchSearchPayload(): Promise<{ documents: SearchDocument[] }> {
  try {
    return await fetchJson<{ documents: SearchDocument[] }>("/api/search-index");
  } catch {
    return fetchJson<{ documents: SearchDocument[] }>("/search/search-index.json");
  }
}

async function refreshPostsFromApi() {
  try {
    const [postItems, searchPayload] = await Promise.all([
      fetchJson<{ items: PostListItem[] }>("/api/posts"),
      fetchJson<{ documents: SearchDocument[] }>("/api/search-index")
    ]);
    posts = postItems.items;
    searchDocs = searchPayload.documents;
    renderList();
  } catch {
    // Static fallback already covers offline API; focus refresh can fail quietly.
  }
}

async function refreshComments(slug: string) {
  if (!slug) return;

  try {
    const payload = await fetchJson<{ items: StoredComment[] }>(`/api/posts/${encodeURIComponent(slug)}/comments`);
    commentCache.set(slug, payload.items.map(normalizeComment));
    if (slug === activeSlug) {
      renderComments();
      renderList();
      updateCommentCounts();
    }
  } catch {
    // Local cached comments remain visible if the API is unavailable.
  }
}

function prefetchIdleArticles() {
  const slugs = posts
    .filter((post) => post.slug !== activeSlug)
    .slice(0, 4)
    .map((post) => post.slug);

  const run = () => {
    for (const slug of slugs) {
      void prefetchArticle(slug);
    }
  };

  if (typeof window.requestIdleCallback === "function") {
    window.requestIdleCallback(run);
  } else {
    globalThis.setTimeout(run, 500);
  }
}

function renderToc(toc: TocItem[]) {
  headingObserver?.disconnect();

  if (!toc.length) {
    tocBody.innerHTML = '<span class="toc-empty">无目录</span>';
    return;
  }

  tocBody.innerHTML = toc
    .map(
      (item) =>
        `<a class="toc-link level-${item.level}" href="#${escapeHtml(item.id)}">${escapeHtml(
          item.text
        )}</a>`
    )
    .join("");

  tocBody.querySelectorAll<HTMLAnchorElement>("a").forEach((link) => {
    link.addEventListener("click", (event) => {
      event.preventDefault();
      const id = decodeURIComponent(link.getAttribute("href")?.slice(1) ?? "");
      articleBody.querySelector<HTMLElement>(`#${CSS.escape(id)}`)?.scrollIntoView({
        behavior: "auto",
        block: "start"
      });
      closeMobileReaderPanels();
    });
  });

  const headings = [...articleBody.querySelectorAll<HTMLElement>("h1,h2,h3,h4,h5,h6")];
  headingObserver = new IntersectionObserver(
    (entries) => {
      const visible = entries
        .filter((entry) => entry.isIntersecting)
        .sort((left, right) => left.boundingClientRect.top - right.boundingClientRect.top)[0];
      if (!visible?.target.id) return;
      tocBody.querySelectorAll(".toc-link").forEach((link) => link.classList.remove("active"));
      tocBody
        .querySelector(`.toc-link[href="#${CSS.escape(visible.target.id)}"]`)
        ?.classList.add("active");
    },
    { root: readerScroll, rootMargin: "0px 0px -65% 0px", threshold: 0.1 }
  );

  headings.forEach((heading) => headingObserver?.observe(heading));
}

function extractTocFromHtml(html: string): TocItem[] {
  const template = document.createElement("template");
  template.innerHTML = html;
  return [...template.content.querySelectorAll<HTMLHeadingElement>("h1,h2,h3,h4,h5,h6")]
    .filter((heading) => heading.id && heading.textContent?.trim())
    .map((heading) => ({
      id: heading.id,
      text: heading.textContent?.trim() ?? "",
      level: Number(heading.tagName.slice(1)) as TocItem["level"]
    }));
}

function enhanceCodeBlocks() {
  articleBody.querySelectorAll<HTMLElement>(".code-block").forEach((block) => {
    if (block.dataset.enhanced === "1") return;
    block.dataset.enhanced = "1";
    const copyButton = block.querySelector<HTMLButtonElement>(".copy-code");
    const foldButton = block.querySelector<HTMLButtonElement>(".fold-code");
    const pre = block.querySelector<HTMLElement>("pre");

    copyButton?.addEventListener("click", async () => {
      const text = [...block.querySelectorAll(".code-line span:last-child")]
        .map((line) => line.textContent ?? "")
        .join("\n");
      await navigator.clipboard.writeText(text).catch(() => undefined);
      showToast("代码已复制");
    });

    foldButton?.addEventListener("click", () => {
      pre?.classList.toggle("hidden");
      foldButton.textContent = pre?.classList.contains("hidden") ? "展开" : "折叠";
    });
  });
}

function handleCommentPaste(event: ClipboardEvent) {
  const files = filesFromDataTransfer(event.clipboardData);
  if (files.length === 0) return;

  event.preventDefault();
  addPendingCommentFiles(files);
}

function handleCommentDragEnter(event: DragEvent) {
  if (!hasDraggedFiles(event.dataTransfer)) return;

  event.preventDefault();
  commentDragDepth += 1;
  commentForm.classList.add("dragging");
}

function handleCommentDragOver(event: DragEvent) {
  if (!hasDraggedFiles(event.dataTransfer)) return;

  event.preventDefault();
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = "copy";
  }
  commentForm.classList.add("dragging");
}

function handleCommentDragLeave(event: DragEvent) {
  if (!hasDraggedFiles(event.dataTransfer)) return;

  commentDragDepth = Math.max(0, commentDragDepth - 1);
  if (commentDragDepth === 0) {
    commentForm.classList.remove("dragging");
  }
}

function handleCommentDrop(event: DragEvent) {
  if (!hasDraggedFiles(event.dataTransfer)) return;

  event.preventDefault();
  commentDragDepth = 0;
  commentForm.classList.remove("dragging");
  addPendingCommentFiles(filesFromDataTransfer(event.dataTransfer));
}

function handlePendingFileClick(event: MouseEvent) {
  const target = event.target instanceof Element ? event.target : null;
  const button = target?.closest<HTMLButtonElement>("button[data-index]");
  if (!button) return;

  const index = Number(button.dataset.index);
  if (!Number.isInteger(index)) return;

  pendingCommentFiles.splice(index, 1);
  renderPendingCommentFiles();
  updateCommentSubmitState();
}

function addPendingCommentFiles(files: File[]) {
  const usableFiles = files.filter((file) => file.size > 0);
  if (usableFiles.length === 0) return;

  const availableSlots = Math.max(0, 10 - pendingCommentFiles.length);
  if (availableSlots === 0) {
    showToast("单次评论最多添加 10 个文件");
    return;
  }

  const acceptedFiles = usableFiles.slice(0, availableSlots);
  pendingCommentFiles = [...pendingCommentFiles, ...acceptedFiles];
  renderPendingCommentFiles();
  updateCommentSubmitState();

  if (usableFiles.length > acceptedFiles.length) {
    showToast("单次评论最多添加 10 个文件");
  }
}

function clearPendingCommentFiles() {
  pendingCommentFiles = [];
  renderPendingCommentFiles();
}

function renderPendingCommentFiles() {
  commentPendingFiles.hidden = pendingCommentFiles.length === 0;
  commentPendingFiles.innerHTML = pendingCommentFiles
    .map(
      (file, index) => `<span class="comment-pending-file">
        <span>${escapeHtml(file.name || "attachment")}</span>
        <small>${formatBytes(file.size)}</small>
        <button type="button" data-index="${index}" aria-label="移除 ${escapeHtml(file.name || "attachment")}">×</button>
      </span>`
    )
    .join("");
}

function updateCommentSubmitState() {
  const canSubmit = commentText.value.trim().length > 0 || pendingCommentFiles.length > 0;
  commentSubmit.disabled = commentSubmitting || !canSubmit;
}

function filesFromDataTransfer(data: DataTransfer | null): File[] {
  if (!data) return [];

  const files: File[] = [];
  for (const item of Array.from(data.items ?? [])) {
    if (item.kind !== "file") continue;
    const file = item.getAsFile();
    if (file) files.push(file);
  }

  return files.length > 0 ? files : Array.from(data.files ?? []);
}

function hasDraggedFiles(data: DataTransfer | null): boolean {
  if (!data) return false;
  return Array.from(data.types ?? []).includes("Files");
}

async function submitComment(event: SubmitEvent) {
  event.preventDefault();
  const content = commentText.value.trim();
  const files = [...pendingCommentFiles];

  if (!content && files.length === 0) {
    updateCommentSubmitState();
    return;
  }

  const totalBytes = files.reduce((sum, file) => sum + file.size, 0);
  if (totalBytes > 500 * 1024 * 1024) {
    showToast("附件总量不能超过 500MB");
    return;
  }

  if (!checkCommentRate(activeSlug)) return;

  commentSubmitting = true;
  updateCommentSubmitState();

  try {
    const attachments = await Promise.all(files.map(uploadCommentAttachment));
    const comment = await createRemoteComment(content, attachments);
    commentCache.set(activeSlug, [...getComments(activeSlug), comment]);

    replyParentId = null;
    commentText.value = "";
    commentText.placeholder = commentDefaultPlaceholder;
    clearPendingCommentFiles();
    renderComments();
    renderList();
    updateCommentCounts();
    showToast("评论已发布");
  } catch {
    showToast("评论发布失败，请稍后重试");
  } finally {
    commentSubmitting = false;
    updateCommentSubmitState();
  }
}

async function uploadCommentAttachment(file: File): Promise<StoredCommentAttachment> {
  const formData = new FormData();
  formData.set("file", file);

  const response = await fetch(`/api/posts/${encodeURIComponent(activeSlug)}/comment-attachments`, {
    method: "POST",
    body: formData
  });

  if (!response.ok) {
    throw new Error("Failed to upload comment attachment");
  }

  const payload = (await response.json()) as { file: StoredCommentAttachment };
  return normalizeAttachment(payload.file);
}

async function createRemoteComment(
  content: string,
  attachments: StoredCommentAttachment[]
): Promise<StoredComment> {
  const response = await fetch(`/api/posts/${encodeURIComponent(activeSlug)}/comments`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json"
    },
    body: JSON.stringify({
      parentId: replyParentId,
      content,
      attachments,
      localId: getDeviceId()
    })
  });

  if (!response.ok) {
    throw new Error("Failed to create comment");
  }

  return normalizeComment((await response.json()) as StoredComment);
}

function renderComments() {
  const comments = getComments(activeSlug);
  const roots = comments
    .filter((comment) => !comment.parentId)
    .sort((left, right) => right.createdAt.localeCompare(left.createdAt));

  commentList.innerHTML =
    roots.map((comment) => renderComment(comment, comments, 0)).join("") ||
    '<div class="muted">暂无评论</div>';

  commentList.querySelectorAll<HTMLButtonElement>(".reply-btn").forEach((button) => {
    button.addEventListener("click", () => {
      replyParentId = button.dataset.id ?? null;
      commentText.placeholder = `回复 ${button.dataset.user ?? ""}，也可以粘贴图片或者拖入文件`;
      commentText.focus();
    });
  });
}

function renderComment(comment: StoredComment, all: StoredComment[], level: number): string {
  const replies = all
    .filter((item) => item.parentId === comment.id)
    .sort((left, right) => left.createdAt.localeCompare(right.createdAt));
  const attachments = comment.attachments
    .map((file) => renderCommentAttachment(normalizeAttachment(file)))
    .join("");

  return `<div class="comment ${level > 0 ? "reply" : ""}">
    <div class="comment-head">
      <span><span class="comment-user">${escapeHtml(comment.username)}</span> · ${formatDateTime(
        comment.createdAt
      )}</span>
      <button class="reply-btn" type="button" data-id="${escapeHtml(comment.id)}" data-user="${escapeHtml(
        comment.username
      )}">回复</button>
    </div>
    <div class="comment-body">${escapeHtml(comment.content)}</div>
    <div class="comment-attachments">${attachments}</div>
    ${replies.map((reply) => renderComment(reply, all, level + 1)).join("")}
  </div>`;
}

function renderCommentAttachment(file: StoredCommentAttachment): string {
  const url = file.url ?? "";
  const mimeType = attachmentMimeType(file);
  const sizeBytes = attachmentSizeBytes(file);
  const isSmallImage = url && mimeType.startsWith("image/") && sizeBytes <= 2 * 1024 * 1024;

  if (isSmallImage) {
    return `<a class="comment-image-link" href="${escapeHtml(url)}" target="_blank" rel="noreferrer noopener"><img src="${escapeHtml(
      url
    )}" alt="${escapeHtml(file.name)}" /></a>`;
  }

  if (url) {
    return `<a class="comment-attachment-link" href="${escapeHtml(url)}" target="_blank" rel="noreferrer noopener" download="${escapeHtml(
      file.name
    )}">${escapeHtml(file.name)}</a>`;
  }

  return `<span class="comment-attachment-link disabled">${escapeHtml(file.name)}</span>`;
}

function normalizeComment(comment: StoredComment): StoredComment {
  return {
    ...comment,
    parentId: comment.parentId ?? null,
    attachments: (comment.attachments ?? []).map(normalizeAttachment)
  };
}

function normalizeAttachment(file: StoredCommentAttachment): StoredCommentAttachment {
  return {
    ...file,
    name: file.name || "attachment",
    mimeType: attachmentMimeType(file),
    sizeBytes: attachmentSizeBytes(file)
  };
}

function attachmentMimeType(file: StoredCommentAttachment): string {
  return file.mimeType ?? file.type ?? "application/octet-stream";
}

function attachmentSizeBytes(file: StoredCommentAttachment): number {
  return file.sizeBytes ?? file.size ?? 0;
}

function checkCommentRate(slug: string): boolean {
  const user = getDeviceId();
  const now = Date.now();
  const today = new Date().toISOString().slice(0, 10);
  const raw = localStorage.getItem(storageKeys.rate);
  const rate = raw ? (JSON.parse(raw) as Record<string, number[]>) : {};
  const dayKey = `${slug}:${user}:${today}`;
  const fiveMinKey = `${user}:5min`;
  const dayEntries = rate[dayKey] ?? [];
  const fiveMinEntries = (rate[fiveMinKey] ?? []).filter((time) => now - time < 5 * 60 * 1000);

  if (dayEntries.length >= 5) {
    showToast("该文章今日评论次数已达上限");
    return false;
  }

  if (fiveMinEntries.length >= 3 && !confirm("评论频率异常，需要确认后继续。")) {
    return false;
  }

  rate[dayKey] = [...dayEntries, now];
  rate[fiveMinKey] = [...fiveMinEntries, now];
  localStorage.setItem(storageKeys.rate, JSON.stringify(rate));
  return true;
}

function updateCommentCounts() {
  const cached = articleCache.get(activeSlug);
  if (!cached) return;
  const count = currentCommentCount(cached.meta);
  articleComments.textContent = `${count} 评论`;
  topArticleComments.textContent = `${count} 评论`;
}

function currentCommentCount(meta: ArticleMeta): number {
  return Math.max(meta.commentCount, getComments(meta.slug).length);
}

function getComments(slug: string): StoredComment[] {
  return commentCache.get(slug) ?? (getCommentStore()[slug] ?? []).map(normalizeComment);
}

function getCommentStore(): Record<string, StoredComment[]> {
  const raw = localStorage.getItem(storageKeys.comments);
  if (!raw) return {};

  try {
    return JSON.parse(raw) as Record<string, StoredComment[]>;
  } catch {
    return {};
  }
}

function countViewOnce(slug: string) {
  if (!slug) return;
  const key = `fp_view_${slug}_${new Date().toISOString().slice(0, 10)}`;
  if (localStorage.getItem(key)) return;
  localStorage.setItem(key, "1");

  navigator.sendBeacon?.(`/api/posts/${encodeURIComponent(slug)}/view`, JSON.stringify({ localId: getDeviceId() }));
}

function getRandomUsername(): string {
  const saved = localStorage.getItem(storageKeys.user);
  if (saved) {
    return (JSON.parse(saved) as { name: string }).name;
  }

  const adjectives = ["安静的", "自由的", "清醒的", "温和的", "明亮的", "专注的", "透明的", "从容的"];
  const nouns = ["河流", "山影", "晨光", "星火", "纸页", "远帆", "云层", "石径"];
  const name = `${adjectives[Math.floor(Math.random() * adjectives.length)]}${nouns[Math.floor(Math.random() * nouns.length)]}`;
  localStorage.setItem(storageKeys.user, JSON.stringify({ id: getDeviceId(), name }));
  return name;
}

function getDeviceId(): string {
  const saved = localStorage.getItem(storageKeys.device);
  if (saved) return saved;
  const next = crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
  localStorage.setItem(storageKeys.device, next);
  return next;
}

function initResizer() {
  const resizer = mustGet<HTMLElement>("listResizer");
  let dragging = false;

  resizer.addEventListener("pointerdown", (event) => {
    dragging = true;
    resizer.setPointerCapture(event.pointerId);
    document.body.style.userSelect = "none";
  });

  resizer.addEventListener("pointermove", (event) => {
    if (!dragging) return;
    const width = Math.min(560, Math.max(200, event.clientX));
    document.documentElement.style.setProperty("--list-width", `${width}px`);
    localStorage.setItem(storageKeys.listWidth, String(width));
  });

  resizer.addEventListener("pointerup", (event) => {
    dragging = false;
    resizer.releasePointerCapture(event.pointerId);
    document.body.style.userSelect = "";
  });
}

function applyStoredTheme() {
  const saved = localStorage.getItem(storageKeys.theme);
  const theme = saved === "light" || saved === "dark" ? saved : "dark";
  document.documentElement.dataset.theme = theme;
  themeBtn.innerHTML = theme === "dark" ? '<i data-lucide="sun"></i>' : '<i data-lucide="moon"></i>';
}

function applyStoredListWidth() {
  const width = Number(localStorage.getItem(storageKeys.listWidth));
  if (Number.isFinite(width) && width >= 200 && width <= 560) {
    document.documentElement.style.setProperty("--list-width", `${width}px`);
  }
}

function applyStoredTocState() {
  if (localStorage.getItem("fp_toc_collapsed_v1") === "1") {
    app.classList.add("toc-collapsed");
    tocToggle.innerHTML = '<i data-lucide="panel-left-open"></i>';
  }
}

async function fetchJson<T>(url: string): Promise<T> {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`Failed to fetch ${url}`);
  return (await response.json()) as T;
}

async function fetchText(url: string): Promise<string> {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`Failed to fetch ${url}`);
  return response.text();
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit"
  }).format(new Date(value));
}

function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false
  }).format(new Date(value));
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";

  const units = ["B", "KB", "MB", "GB"];
  let size = value;
  let unitIndex = 0;

  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }

  const precision = unitIndex === 0 || size >= 10 ? 0 : 1;
  return `${size.toFixed(precision)} ${units[unitIndex]}`;
}

function highlight(value: string, term: string): string {
  if (!term) return escapeHtml(value);
  const normalizedValue = value.toLowerCase();
  const normalizedTerm = term.toLowerCase();
  const index = normalizedValue.indexOf(normalizedTerm);
  if (index < 0) return escapeHtml(value);

  return `${escapeHtml(value.slice(0, index))}<mark>${escapeHtml(
    value.slice(index, index + term.length)
  )}</mark>${escapeHtml(value.slice(index + term.length))}`;
}

function normalize(value: string): string {
  return value.trim().toLowerCase();
}

function publicArticleUrl(slug: string): string {
  return new URL(articlePermalinkPath(slug, activeReferral), configuredPublicOrigin ?? location.origin).toString();
}

function readAndLockReferral(incomingValue: string | null): string | null {
  const stored = normalizeReferral(localStorage.getItem(referralStorageKey));
  if (stored) return stored;
  const incoming = normalizeReferral(incomingValue);
  if (incoming) localStorage.setItem(referralStorageKey, incoming);
  return incoming;
}

function propagateReaderLinks() {
  const brand = document.querySelector<HTMLAnchorElement>("a.brand");
  if (brand && window.parent !== window) brand.target = "_top";
  if (!activeReferral) return;

  document.querySelectorAll<HTMLAnchorElement>("a[href]").forEach((link) => {
    const rawHref = link.getAttribute("href");
    if (!rawHref || rawHref.startsWith("#")) return;
    const url = new URL(link.href, location.href);
    if (url.origin !== location.origin) return;
    url.searchParams.set("ref", activeReferral);
    link.href = url.toString();
    if (window.parent !== window && !link.hasAttribute("download")) link.target = "_top";
  });
}

function notifyParentArticleChange(slug: string, title: string, push: boolean) {
  if (window.parent === window) return;
  window.parent.postMessage({ type: "freedompost:article-change", slug, title, push }, location.origin);
}

function normalizeOrigin(value: string | undefined): string | null {
  if (!value) return null;

  try {
    return new URL(value).origin;
  } catch {
    return null;
  }
}

function escapeHtml(value: string): string {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function showToast(message: string) {
  toast.textContent = message;
  toast.classList.add("show");
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => toast.classList.remove("show"), 1800);
}

function mustGet<T extends HTMLElement>(id: string): T {
  const element = document.getElementById(id);
  if (!element) {
    throw new Error(`Missing element #${id}`);
  }
  return element as T;
}

function readInitialPayload() {
  const element = document.getElementById("initialPayload");
  if (!element?.textContent) return undefined;

  try {
    return JSON.parse(element.textContent) as Window["__FREEDOMPOST_INITIAL__"];
  } catch {
    return undefined;
  }
}
