import { createIcons, icons } from "lucide";
import {
  articlePermalinkPath,
  articleReaderPath,
  isCanonicalArticleSlug,
  normalizeReferral,
  readArticleSlugFromPath,
  referralStorageKey
} from "../lib/article-links.js";
import { portalActivePath, portalMobileActivePath } from "../lib/portal-routes.js";
import { createPortalPageLifecycle, eventPathIncludes } from "../lib/portal-page-lifecycle.js";
import { formatCommissionEarnings } from "../lib/market-commission.js";
import { renderOrderReferralField } from "../lib/market-order.js";

type PostListItem = {
  slug: string;
  title: string;
  createdAt: string;
  updatedAt: string;
  viewCount: number;
  commentCount: number;
  excerpt?: string;
};

type SearchDocument = {
  slug: string;
  title: string;
  body: string;
  excerpt: string;
};

type StoreProduct = {
  id: string;
  slug: string;
  title: string;
  summary: string;
  description: string;
  category: string;
  priceCents: number;
  commissionCents: number;
  customerPriceCents?: number;
  compareAtCents: number | null;
  currency: string;
  stock: number;
  soldCount: number;
  coverUrl: string | null;
  imageUrl?: string | null;
  status: "published";
};

type CreatorTool = {
  id: string;
  slug: string;
  title: string;
  summary: string;
  description: string;
  category: string;
  url: string;
  coverUrl: string | null;
  status: "published";
};

type AffiliateOrder = {
  id: string;
  orderCode: string;
  productTitle: string;
  priceCents: number;
  commissionCents: number;
  currency: string;
  orderStatus: "pending" | "completed" | "canceled";
  commissionStatus: "not_due" | "pending" | "paid";
  createdAt: string;
};

type AffiliateDashboard = {
  affiliate: { wechatId: string; defaultMarkupPercent?: number };
  totalClicks: number;
  uniqueClicks: number;
  completedOrders: number;
  pendingCommissionCents: number;
  paidCommissionCents: number;
  orders: AffiliateOrder[];
};

type AffiliateProduct = StoreProduct & {
  markupPercent: number;
  customerPriceCents: number;
};

const themeKey = "fp_theme_v1";
const referralKey = referralStorageKey;
const affiliateVisitorStorageKey = "fp_affiliate_visitor_v1"; // gitleaks:allow — public localStorage key, not a credential
const root = document.documentElement;
const navToggle = document.querySelector<HTMLButtonElement>("#navToggle");
const primaryNav = document.querySelector<HTMLElement>("#primaryNav");
const themeButton = document.querySelector<HTMLButtonElement>("#portalThemeBtn");
const servicePulse = document.querySelector<HTMLElement>("#servicePulse");
let postGrid = document.querySelector<HTMLElement>("#portalPostGrid");
let searchInput = document.querySelector<HTMLInputElement>("#portalSearchInput");
let searchMeta = document.querySelector<HTMLElement>("#articleSearchMeta");
let emptyState = document.querySelector<HTMLElement>("#emptyArticles");
let routeLoading = false;
let portalToastTimer = 0;
const portalPageLifecycle = createPortalPageLifecycle([
  async (root) => {
    const benefitRoot = root.querySelector<HTMLElement>("[data-benefit-root]");
    if (!benefitRoot) return;
    const { initializeBenefitPage } = await import("./benefit.js");
    return initializeBenefitPage(benefitRoot);
  },
  async (root) => {
    const accountRoot = root.querySelector<HTMLElement>("[data-account-root]");
    if (!accountRoot) return;
    const { initializeAccountPage } = await import("./account.js");
    return initializeAccountPage(accountRoot);
  }
]);

let posts: PostListItem[] = [];
let searchDocuments: SearchDocument[] = [];

initTheme();
void captureReferral();
bindNavigation();
bindRouteNavigation();
bindArticleReaderMessages();
bindPageInteractions();
createPortalIcons();
void checkService();

function initTheme() {
  const saved = localStorage.getItem(themeKey);
  root.dataset.theme = saved === "light" ? "light" : "dark";
  updateThemeIcon();

  themeButton?.addEventListener("click", () => {
    root.dataset.theme = root.dataset.theme === "dark" ? "light" : "dark";
    localStorage.setItem(themeKey, root.dataset.theme);
    updateThemeIcon();
  });
}

function updateThemeIcon() {
  if (!themeButton) return;
  themeButton.innerHTML = `<i data-lucide="${root.dataset.theme === "dark" ? "sun" : "moon"}"></i>`;
  createPortalIcons();
}

function bindNavigation() {
  navToggle?.addEventListener("click", () => {
    const open = primaryNav?.classList.toggle("open") ?? false;
    navToggle.setAttribute("aria-expanded", String(open));
    navToggle.setAttribute("aria-label", open ? "关闭导航" : "打开导航");
    navToggle.innerHTML = `<i data-lucide="${open ? "x" : "menu"}"></i>`;
    createPortalIcons();
  });

  document.addEventListener("click", (event) => {
    if (!primaryNav?.classList.contains("open")) return;
    const target = event.target as Node;
    if (primaryNav.contains(target) || (navToggle && eventPathIncludes(event, navToggle))) return;
    primaryNav.classList.remove("open");
    navToggle?.setAttribute("aria-expanded", "false");
    navToggle?.setAttribute("aria-label", "打开导航");
    if (navToggle) navToggle.innerHTML = '<i data-lucide="menu"></i>';
    createPortalIcons();
  });
}

function bindPageInteractions() {
  void portalPageLifecycle.mount(document).catch(() => showBenefitFeatureLoadError());
  postGrid = document.querySelector<HTMLElement>("#portalPostGrid");
  searchInput = document.querySelector<HTMLInputElement>("#portalSearchInput");
  searchMeta = document.querySelector<HTMLElement>("#articleSearchMeta");
  emptyState = document.querySelector<HTMLElement>("#emptyArticles");
  syncArticleReader();

  if (searchInput) {
    const requestedQuery = new URLSearchParams(location.search).get("q")?.trim() ?? "";
    searchInput.value = requestedQuery;
    searchInput.addEventListener("input", () => renderPosts(searchInput?.value ?? ""));
  }

  if (postGrid) void hydratePosts();
  void hydrateMarket();
  void hydrateTools();
  void hydrateAffiliateDashboard();
  propagateReferralLinks();
}

function syncArticleReader() {
  const frame = document.querySelector<HTMLIFrameElement>("#articleReaderFrame");
  if (!frame) return;

  const routeSlug = readArticleSlugFromPath(location.pathname);
  const declaredSlug = document.querySelector<HTMLElement>(".reader-route")?.dataset.articleSlug?.trim();
  const requestedPost = new URLSearchParams(location.search).get("post")?.trim() || routeSlug || declaredSlug || null;
  const nextSource = articleReaderPath(requestedPost, lockedReferral(), { embedded: true });
  if (frame.getAttribute("src") !== nextSource) frame.src = nextSource;
}

function bindArticleReaderMessages() {
  window.addEventListener("message", (event) => {
    if (event.origin !== location.origin) return;
    const frame = document.querySelector<HTMLIFrameElement>("#articleReaderFrame");
    if (!frame?.contentWindow || event.source !== frame.contentWindow) return;
    const data = event.data as { type?: unknown; slug?: unknown; title?: unknown; push?: unknown } | null;
    if (data?.type !== "freedompost:article-change" || !isCanonicalArticleSlug(data.slug)) return;

    const destination = new URL(articlePermalinkPath(data.slug, lockedReferral()), location.origin);
    const nextLocation = `${destination.pathname}${destination.search}`;
    const currentLocation = `${location.pathname}${location.search}`;
    if (nextLocation !== currentLocation) {
      if (data.push === true) history.pushState({}, "", nextLocation);
      else history.replaceState(history.state, "", nextLocation);
    }

    document.body.dataset.page = "articles";
    updateActiveNavigation("/articles/");
    updateArticleMetadata(destination, typeof data.title === "string" ? data.title : null);
    propagateReferralLinks();
  });
}

function updateArticleMetadata(destination: URL, title: string | null) {
  const canonical = document.querySelector<HTMLLinkElement>('link[rel="canonical"]');
  if (canonical) canonical.href = destination.toString();
  const openGraphUrl = document.querySelector<HTMLMetaElement>('meta[property="og:url"]');
  if (openGraphUrl) openGraphUrl.content = destination.toString();
  if (!title) return;
  document.title = `${title} - FreedomPost`;
  const openGraphTitle = document.querySelector<HTMLMetaElement>('meta[property="og:title"]');
  if (openGraphTitle) openGraphTitle.content = title;
}

function bindRouteNavigation() {
  document.addEventListener("click", (event) => {
    const target = event.target as Element | null;
    const link = target?.closest<HTMLAnchorElement>("a[data-portal-route]");
    if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const destination = new URL(link.href, location.href);
    if (destination.origin !== location.origin) return;
    addLockedReferral(destination);
    event.preventDefault();
    void loadRoute(destination, true);
  });

  document.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      if (!searchInput) return;
      event.preventDefault();
      searchInput.focus();
      searchInput.select();
    }
  });

  window.addEventListener("popstate", () => {
    void loadRoute(new URL(location.href), false);
  });
}

async function loadRoute(destination: URL, push: boolean) {
  if (routeLoading || (push && destination.pathname === location.pathname && destination.search === location.search)) return;
  const content = document.querySelector<HTMLElement>("#portalContent");
  if (!content) return;

  routeLoading = true;
  document.body.classList.add("route-loading");
  content.setAttribute("aria-busy", "true");
  try {
    const response = await fetch(destination.href, { headers: { "X-FreedomPost-Route": "1" } });
    if (!response.ok) throw new Error(`Route request failed: ${response.status}`);
    const documentText = await response.text();
    const nextDocument = new DOMParser().parseFromString(documentText, "text/html");
    const nextContent = nextDocument.querySelector<HTMLElement>("#portalContent");
    const footer = document.querySelector<HTMLElement>("#portalFooter");
    const nextFooter = nextDocument.querySelector<HTMLElement>("#portalFooter");
    if (!nextContent) throw new Error("Route content is missing");

    const resolvedDestination = new URL(response.url || destination.href);
    portalPageLifecycle.unmount();
    content.innerHTML = nextContent.innerHTML;
    if (footer && nextFooter) {
      footer.innerHTML = nextFooter.innerHTML;
      footer.hidden = nextFooter.hidden;
    }
    syncPortalMetadata(nextDocument);
    document.body.dataset.page = nextDocument.body.dataset.page ?? "";
    if (push) history.pushState({}, "", `${resolvedDestination.pathname}${resolvedDestination.search}${resolvedDestination.hash}`);
    updateActiveNavigation(resolvedDestination.pathname);
    closePrimaryNavigation();
    bindPageInteractions();
    createPortalIcons();
    focusRouteContent(content, resolvedDestination.hash);
  } catch {
    location.assign(destination.href);
  } finally {
    routeLoading = false;
    document.body.classList.remove("route-loading");
    content.removeAttribute("aria-busy");
  }
}

function syncPortalMetadata(nextDocument: Document): void {
  document.title = nextDocument.title;
  for (const selector of [
    'meta[name="description"]',
    'link[rel="canonical"]',
    'meta[property="og:title"]',
    'meta[property="og:description"]',
    'meta[property="og:url"]',
    'meta[property="og:image"]'
  ]) {
    const current = document.head.querySelector<HTMLElement>(selector);
    const next = nextDocument.head.querySelector<HTMLElement>(selector);
    if (!current || !next) continue;
    for (const attribute of ["content", "href"]) {
      const value = next.getAttribute(attribute);
      if (value !== null) current.setAttribute(attribute, value);
    }
  }
  const currentRobots = document.head.querySelector<HTMLMetaElement>('meta[name="robots"]');
  const nextRobots = nextDocument.head.querySelector<HTMLMetaElement>('meta[name="robots"]');
  if (!nextRobots) {
    currentRobots?.remove();
  } else if (currentRobots) {
    currentRobots.content = nextRobots.content;
  } else {
    document.head.append(nextRobots.cloneNode(true));
  }

  const currentLinks = Array.from(document.head.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]'));
  nextDocument.head.querySelectorAll<HTMLLinkElement>('link[rel="stylesheet"]').forEach((nextLink) => {
    if (!currentLinks.some((link) => link.href === nextLink.href)) {
      document.head.append(nextLink.cloneNode(true));
    }
  });

  const currentStyles = Array.from(document.head.querySelectorAll<HTMLStyleElement>("style"));
  nextDocument.head.querySelectorAll<HTMLStyleElement>("style").forEach((nextStyle) => {
    if (!currentStyles.some((style) => style.textContent === nextStyle.textContent)) {
      document.head.append(nextStyle.cloneNode(true));
    }
  });
}

function closePrimaryNavigation(): void {
  primaryNav?.classList.remove("open");
  navToggle?.setAttribute("aria-expanded", "false");
  navToggle?.setAttribute("aria-label", "打开导航");
  if (navToggle) navToggle.innerHTML = '<i data-lucide="menu"></i>';
}

function focusRouteContent(content: HTMLElement, hash: string): void {
  const hashTarget = hash ? document.getElementById(safeDecodeHash(hash)) : null;
  const target = hashTarget ?? content.querySelector<HTMLElement>("h1") ?? content;
  if (!target.matches("a, button, input, select, textarea, [tabindex]")) target.tabIndex = -1;
  target.focus({ preventScroll: true });
  if (hashTarget) hashTarget.scrollIntoView();
  else window.scrollTo(0, 0);
}

function safeDecodeHash(hash: string): string {
  try {
    return decodeURIComponent(hash.slice(1));
  } catch {
    return hash.slice(1);
  }
}

function showBenefitFeatureLoadError(): void {
  const root = document.querySelector<HTMLElement>("[data-benefit-root]");
  if (!root) return;
  root.dataset.state = "error";
  root.setAttribute("aria-busy", "false");
  setPortalText(root, "#benefitStatusText", "页面功能加载失败");
  setPortalText(root, "#benefitErrorTitle", "页面功能加载失败");
  setPortalText(root, "#benefitErrorMessage", "请刷新页面后重试。");
  root.querySelectorAll<HTMLElement>("[data-benefit-panel]").forEach((panel) => {
    panel.hidden = panel.dataset.benefitPanel !== "error";
  });
}

function setPortalText(root: ParentNode, selector: string, value: string): void {
  const element = root.querySelector<HTMLElement>(selector);
  if (element) element.textContent = value;
}

function updateActiveNavigation(pathname: string) {
  const activePath = portalActivePath(pathname);
  const mobileActivePath = portalMobileActivePath(pathname);
  document.querySelectorAll<HTMLAnchorElement>(".nav-link").forEach((link) => {
    const linkPath = new URL(link.href, location.href).pathname;
    const active = linkPath === activePath;
    link.classList.toggle("active", active);
    if (active) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
  document.querySelectorAll<HTMLAnchorElement>(".mobile-bottom-link").forEach((link) => {
    const active = new URL(link.href, location.href).pathname === mobileActivePath;
    link.classList.toggle("active", active);
    if (active) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
}

async function checkService() {
  if (!servicePulse) return;
  try {
    const response = await fetch("/health", { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error("health check failed");
    servicePulse.dataset.status = "online";
    const label = servicePulse.querySelector("b");
    if (label) label.textContent = "服务正常";
  } catch {
    servicePulse.dataset.status = "offline";
    const label = servicePulse.querySelector("b");
    if (label) label.textContent = "服务暂不可用";
  }
}

async function hydratePosts() {
  if (!postGrid) return;

  try {
    const [postResponse, searchResponse] = await Promise.all([
      fetch("/api/posts", { headers: { Accept: "application/json" } }),
      fetch("/api/search-index", { headers: { Accept: "application/json" } })
    ]);
    if (!postResponse.ok) throw new Error("posts unavailable");

    const postPayload = (await postResponse.json()) as { items: PostListItem[] };
    posts = postPayload.items;

    if (searchResponse.ok) {
      const searchPayload = (await searchResponse.json()) as { documents: SearchDocument[] };
      searchDocuments = searchPayload.documents;
    }

    renderPosts(searchInput?.value ?? "");
    updateHomeStats();
  } catch {
    // Static seed cards remain readable when the API is unavailable.
  }
}

async function hydrateMarket() {
  const grid = document.querySelector<HTMLElement>("#marketProductGrid");
  const filters = document.querySelector<HTMLElement>("#marketFilters");
  const count = document.querySelector<HTMLElement>("#marketCount");
  const empty = document.querySelector<HTMLElement>("#marketEmpty");
  const dialog = document.querySelector<HTMLDialogElement>("#productDialog");
  const dialogContent = document.querySelector<HTMLElement>("#productDialogContent");
  const closeDialog = document.querySelector<HTMLButtonElement>("#productDialogClose");
  const orderDialog = document.querySelector<HTMLDialogElement>("#orderDialog");
  const orderDialogContent = document.querySelector<HTMLElement>("#orderDialogContent");
  const closeOrderDialog = document.querySelector<HTMLButtonElement>("#orderDialogClose");
  if (!grid || !filters || !count || !empty || !dialog || !dialogContent || !closeDialog || !orderDialog || !orderDialogContent || !closeOrderDialog) return;

  let products: StoreProduct[] = [];
  let category = "all";

  const render = () => {
    const visible = products.filter((product) => category === "all" || product.category === category);
    grid.innerHTML = visible.map(renderMarketProduct).join("");
    empty.hidden = visible.length > 0;
    count.textContent = `${visible.length} 件在售商品`;
    createPortalIcons();
  };

  grid.addEventListener("click", (event) => {
    if (!(event.target instanceof Element)) return;
    const button = event.target.closest<HTMLButtonElement>("[data-market-action][data-product-slug]");
    if (!button || !grid.contains(button)) return;
    const product = products.find((item) => item.slug === button.dataset.productSlug);
    if (!product) return;

    switch (button.dataset.marketAction) {
      case "details":
        openProductDetails(product, dialog, dialogContent, orderDialog, orderDialogContent);
        break;
      case "share":
        void shareMarketProduct(product, button);
        break;
      case "order":
        openProductOrder(product, orderDialog, orderDialogContent);
        break;
    }
  });

  filters.querySelectorAll<HTMLButtonElement>("[data-category]").forEach((button) => {
    button.addEventListener("click", () => {
      category = button.dataset.category ?? "all";
      filters.querySelectorAll("button").forEach((item) => item.classList.toggle("active", item === button));
      render();
    });
  });

  closeDialog.addEventListener("click", () => dialog.close());
  closeOrderDialog.addEventListener("click", () => orderDialog.close());
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) dialog.close();
  });
  orderDialog.addEventListener("click", (event) => {
    if (event.target === orderDialog) orderDialog.close();
  });

  try {
    const productUrl = new URL("/api/products", location.origin);
    const ref = lockedReferral();
    if (ref) productUrl.searchParams.set("ref", ref);
    const response = await fetch(productUrl, { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error("products unavailable");
    const rawItems: any[] = ((await response.json()) as { items?: any[]; products?: any[] }).items ?? [];
    products = rawItems.map((p) => ({
      ...p,
      summary: p.summary ?? p.description ?? "",
      coverUrl: p.coverUrl ?? p.imageUrl ?? null,
      category: p.category ?? "service",
      stock: p.stock ?? -1,
      soldCount: p.soldCount ?? 0,
      compareAtCents: p.compareAtCents ?? null,
    } as StoreProduct));
    render();
    const requestedProduct = new URLSearchParams(location.search).get("product");
    const product = products.find((item) => item.slug === requestedProduct);
    if (product) {
      openProductDetails(product, dialog, dialogContent, orderDialog, orderDialogContent);
    }
  } catch {
    count.textContent = "商品加载失败";
    empty.hidden = false;
  }
}

async function hydrateTools() {
  const grid = document.querySelector<HTMLElement>("#toolsGrid");
  const filters = document.querySelector<HTMLElement>("#toolsCategoryFilters");
  const count = document.querySelector<HTMLElement>("#toolsCount");
  const empty = document.querySelector<HTMLElement>("#toolsEmpty");
  if (!grid || !filters || !count || !empty) return;
  let tools: CreatorTool[] = [];
  let category = "all";
  const render = () => {
    const visible = tools.filter((tool) => category === "all" || tool.category === category);
    grid.innerHTML = visible.map(renderToolCard).join("");
    count.textContent = `${visible.length} 个工具`;
    empty.hidden = visible.length > 0;
    grid.querySelectorAll<HTMLButtonElement>("[data-share-tool]").forEach((button) => {
      button.addEventListener("click", () => {
        const tool = tools.find((item) => item.slug === button.dataset.shareTool);
        if (tool) void shareTool(tool);
      });
    });
    createPortalIcons();
  };
  filters.querySelectorAll<HTMLButtonElement>("[data-tool-category]").forEach((button) => {
    button.addEventListener("click", () => {
      category = button.dataset.toolCategory ?? "all";
      filters.querySelectorAll("button").forEach((item) => item.classList.toggle("active", item === button));
      render();
    });
  });
  try {
    const response = await fetch("/api/tools", { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error("tools unavailable");
    tools = ((await response.json()) as { items: CreatorTool[] }).items;
    render();
  } catch {
    count.textContent = "工具加载失败";
    empty.hidden = false;
  }
}

function renderToolCard(tool: CreatorTool) {
  const cover = tool.coverUrl
    ? `<img src="${escapeAttribute(tool.coverUrl)}" alt="${escapeAttribute(tool.title)} Logo" loading="lazy" />`
    : `<span class="tool-card-mark" aria-hidden="true"><i data-lucide="wrench"></i></span>`;
  const note = tool.description.trim()
    ? `<div class="tool-card-note"><span>我的用途</span><p>${escapeHtml(tool.description)}</p></div>`
    : "";
  return `<article class="tool-card"><header class="tool-card-header"><div class="tool-card-logo">${cover}</div><div class="tool-card-identity"><div><span class="tool-category">${escapeHtml(toolCategoryLabel(tool.category))}</span><span class="tool-used-badge">我用过</span></div><h2>${escapeHtml(tool.title)}</h2></div><button class="tool-share-button" type="button" data-share-tool="${escapeAttribute(tool.slug)}" aria-label="分享 ${escapeAttribute(tool.title)}" title="分享"><i data-lucide="share-2"></i></button></header><p class="tool-card-summary">${escapeHtml(tool.summary)}</p>${note}<footer class="tool-card-footer"><span class="tool-site"><i data-lucide="globe-2"></i>${escapeHtml(toolWebsiteLabel(tool.url))}</span><a class="button primary" href="${escapeAttribute(tool.url)}" target="_blank" rel="noopener noreferrer">访问官网 <i data-lucide="arrow-up-right"></i></a></footer></article>`;
}

function toolWebsiteLabel(value: string) {
  try {
    return new URL(value).hostname.replace(/^www\./, "");
  } catch {
    return "官方网站";
  }
}

async function shareTool(tool: CreatorTool) {
  const shareData = { title: tool.title, text: tool.summary, url: tool.url };
  const canShare = typeof navigator.share === "function";
  try {
    if (canShare) await navigator.share(shareData);
    else await navigator.clipboard.writeText(tool.url);
    showPortalToast(canShare ? "分享面板已打开" : "工具链接已复制");
  } catch {
    // A dismissed native share sheet is not an error to show the visitor.
  }
}

function renderMarketProduct(product: StoreProduct) {
  const displayPrice = product.customerPriceCents ?? product.priceCents;
  const availability = product.stock === 0 ? `已售出 ${product.soldCount} · 暂时售罄` : product.stock < 0 ? `已售出 ${product.soldCount} · 不限量` : `已售出 ${product.soldCount} · 库存 ${product.stock}`;
  const cover = product.coverUrl
    ? `<img src="${escapeAttribute(product.coverUrl)}" alt="${escapeAttribute(product.title)}" />`
    : `<span class="market-product-placeholder"><i data-lucide="package"></i></span>`;
  const compareAt = product.compareAtCents ? `<del>${formatCurrency(product.compareAtCents, product.currency)}</del>` : "";
  return `<article class="market-product-card">
    <div class="market-product-cover">${cover}</div>
    <div class="market-product-body">
      <span class="market-product-category">${escapeHtml(productCategoryLabel(product.category))}</span>
      <h2>${escapeHtml(product.title)}</h2>
      <p>${escapeHtml(product.summary)}</p>
      <div class="market-product-bottom"><div class="market-product-price"><strong>${formatCurrency(displayPrice, product.currency)}</strong>${compareAt}</div><span>${availability}</span></div>
      <div class="market-product-actions">
        <button class="market-details-button" type="button" data-market-action="details" data-product-slug="${escapeAttribute(product.slug)}">查看详情 <i data-lucide="arrow-up-right"></i></button>
        <div class="market-product-share"><button class="button secondary" type="button" data-market-action="share" data-product-slug="${escapeAttribute(product.slug)}">分享赚钱</button><span class="market-product-commission">每单赚${escapeHtml(formatCommissionEarnings(product.commissionCents, product.currency))}</span></div>
        <button class="button primary" type="button" data-market-action="order" data-product-slug="${escapeAttribute(product.slug)}" ${product.stock === 0 ? "disabled" : ""}>立即下单</button>
      </div>
    </div>
  </article>`;
}

function renderProductDialog(product: StoreProduct) {
  const displayPrice = product.customerPriceCents ?? product.priceCents;
  const cover = product.coverUrl ? `<img src="${escapeAttribute(product.coverUrl)}" alt="${escapeAttribute(product.title)}" />` : "";
  const availability = product.stock === 0 ? `已售出 ${product.soldCount} · 暂时售罄` : product.stock < 0 ? `已售出 ${product.soldCount} · 不限量供应` : `已售出 ${product.soldCount} · 当前库存 ${product.stock}`;
  const commission = product.commissionCents > 0 ? ` · 可得 ${formatCurrency(product.commissionCents, product.currency)}` : "";
  return `<div class="product-dialog-scroll"><div class="product-dialog-cover">${cover}</div><p class="section-kicker">${escapeHtml(productCategoryLabel(product.category))}</p><h2>${escapeHtml(product.title)}</h2><p class="product-dialog-summary">${escapeHtml(product.summary)}</p><div class="product-dialog-price">${formatCurrency(displayPrice, product.currency)} <span>${availability}</span></div><div class="product-dialog-description">${escapeHtml(product.description).replace(/\n/g, "<br>")}</div></div><div class="product-dialog-actions"><button class="button secondary" type="button" data-share-product>分享此商品赚钱${escapeHtml(commission)}</button><button class="button primary" type="button" data-order-product ${product.stock === 0 ? "disabled" : ""}>立即下单</button></div>`;
}

function openProductDetails(product: StoreProduct, productDialog: HTMLDialogElement, content: HTMLElement, orderDialog: HTMLDialogElement, orderContent: HTMLElement) {
  content.innerHTML = renderProductDialog(product);
  productDialog.showModal();
  bindProductDialogActions(product, productDialog, orderDialog, orderContent);
  createPortalIcons();
}

function bindProductDialogActions(product: StoreProduct, productDialog: HTMLDialogElement, orderDialog: HTMLDialogElement, content: HTMLElement) {
  productDialog.querySelector<HTMLButtonElement>("[data-share-product]")?.addEventListener("click", (event) => {
    void shareMarketProduct(product, event.currentTarget as HTMLButtonElement, productDialog);
  });
  productDialog.querySelector<HTMLButtonElement>("[data-order-product]")?.addEventListener("click", () => {
    openProductOrder(product, orderDialog, content, productDialog);
  });
}

function openProductOrder(product: StoreProduct, orderDialog: HTMLDialogElement, content: HTMLElement, productDialog?: HTMLDialogElement) {
  const ref = lockedReferral();
  const displayPrice = product.customerPriceCents ?? product.priceCents;
  const referralField = renderOrderReferralField(ref);
  content.innerHTML = `<p class="section-kicker">Order</p><h2>提交下单信息</h2><p class="product-dialog-summary">${escapeHtml(product.title)} · ${formatCurrency(displayPrice, product.currency)}</p><form id="affiliateOrderForm" class="order-form">${referralField}<p>提交后会生成订单号，请添加客服微信或 QQ 完成人工交易。</p><button class="button primary" type="submit">生成订单号</button><p class="form-error" role="alert" hidden></p></form>`;
  if (productDialog?.open) productDialog.close();
  if (!orderDialog.open) orderDialog.showModal();
  content.querySelector<HTMLFormElement>("#affiliateOrderForm")?.addEventListener("submit", (event) => void submitAffiliateOrder(event, product, content));
}

async function shareMarketProduct(product: StoreProduct, button: HTMLButtonElement, productDialog?: HTMLDialogElement) {
  button.disabled = true;
  try {
    const response = await fetch("/api/affiliate/dashboard", { headers: { Accept: "application/json" } });
    if (response.ok) {
      const result = await response.json() as { shareUrl: string };
      const shareUrl = new URL(result.shareUrl);
      shareUrl.searchParams.set("product", product.slug);
      await copyTextToClipboard(shareUrl.toString());
      if (productDialog?.open) productDialog.close();
      showPortalToast("分享链接复制成功", { center: true, success: true, duration: 2000 });
      return;
    }

    if (response.status === 401 || response.status === 403) {
      const destination = new URL("/earn/", location.origin);
      destination.searchParams.set("product", product.slug);
      addLockedReferral(destination);
      if (productDialog?.open) productDialog.close();
      await loadRoute(destination, true);
      return;
    }
    throw new Error("Affiliate session check failed");
  } catch {
    showPortalToast("分享链接复制失败，请稍后重试", { center: true, duration: 2000 });
  } finally {
    button.disabled = false;
  }
}

async function copyTextToClipboard(value: string) {
  if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable");
  await navigator.clipboard.writeText(value);
}

async function submitAffiliateOrder(event: SubmitEvent, product: StoreProduct, content: HTMLElement) {
  event.preventDefault();
  const form = event.currentTarget as HTMLFormElement;
  const button = form.querySelector<HTMLButtonElement>("button[type=submit]");
  const error = form.querySelector<HTMLElement>(".form-error");
  const recommenderWechatId = String(new FormData(form).get("recommenderWechatId") ?? "").trim();
  if (button) button.disabled = true;
  try {
    const response = await fetch("/api/orders", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ productSlug: product.slug, recommenderWechatId })
    });
    const result = await response.json() as { order?: AffiliateOrder; error?: { message?: string } };
    if (!response.ok || !result.order) throw new Error(result.error?.message || "下单失败");
    content.innerHTML = renderContactPanel(result.order);
    createPortalIcons();
  } catch (reason) {
    if (error) {
      error.textContent = reason instanceof Error ? reason.message : "下单失败，请稍后再试";
      error.hidden = false;
    }
    if (button) button.disabled = false;
  }
}

function renderContactPanel(order: AffiliateOrder) {
  return `<p class="section-kicker">Order created</p><h2>订单已登记</h2><div class="order-code"><span>订单号</span><strong>${escapeHtml(order.orderCode)}</strong><small>联系时请发送此订单号</small></div><div class="contact-grid"><figure><img src="/images/contact-wechat.jpg" alt="客服微信二维码" /><figcaption>微信：扫码添加客服</figcaption></figure><figure><img src="/images/contact-qq.jpg" alt="客服 QQ 二维码" /><figcaption>QQ：2682460530</figcaption></figure></div><p class="settlement-note">管理员确认成交后，推广佣金将在当天人工结算。</p>`;
}

async function hydrateAffiliateDashboard() {
  const form = document.querySelector<HTMLFormElement>("#affiliateAccessForm");
  const dashboard = document.querySelector<HTMLElement>("#affiliateDashboard");
  if (!form || !dashboard) return;
  form.addEventListener("submit", (event) => void accessAffiliateDashboard(event, form, dashboard));
  document.querySelector<HTMLButtonElement>("#copyAffiliateLink")?.addEventListener("click", () => void copyAffiliateShareLink());
  document.querySelector<HTMLButtonElement>("#affiliateLogout")?.addEventListener("click", async () => {
    await fetch("/api/affiliate/logout", { method: "POST" });
    dashboard.hidden = true;
    form.hidden = false;
  });
  try {
    const response = await fetch("/api/affiliate/dashboard", { headers: { Accept: "application/json" } });
    if (response.ok) {
      renderAffiliateDashboard(await response.json() as { shareUrl: string; dashboard: AffiliateDashboard }, form, dashboard);
      void hydrateAffiliatePricing();
    }
  } catch {
    // Anonymous visitors stay on the access form.
  }
}

async function accessAffiliateDashboard(event: SubmitEvent, form: HTMLFormElement, dashboard: HTMLElement) {
  event.preventDefault();
  const data = new FormData(form);
  const error = document.querySelector<HTMLElement>("#affiliateAccessError");
  const submit = form.querySelector<HTMLButtonElement>("button[type=submit]");
  if (error) error.hidden = true;
  if (submit) submit.disabled = true;
  try {
    const response = await fetch("/api/affiliate/access", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ wechatId: data.get("wechatId"), password: data.get("password") })
    });
    const result = await response.json() as { shareUrl?: string; dashboard?: AffiliateDashboard; generatedPassword?: string; error?: { message?: string } };
    if (!response.ok || !result.dashboard || !result.shareUrl) throw new Error(result.error?.message || "查询失败");
    renderAffiliateDashboard({ shareUrl: productShareUrl(result.shareUrl), dashboard: result.dashboard, generatedPassword: result.generatedPassword }, form, dashboard);
    void hydrateAffiliatePricing();
  } catch (reason) {
    if (error) {
      error.textContent = reason instanceof Error ? reason.message : "查询失败，请稍后再试";
      error.hidden = false;
    }
  } finally {
    if (submit) submit.disabled = false;
  }
}

function renderAffiliateDashboard(result: { shareUrl: string; dashboard: AffiliateDashboard; generatedPassword?: string }, form: HTMLFormElement, panel: HTMLElement) {
  form.hidden = true;
  panel.hidden = false;
  const shareInput = document.querySelector<HTMLInputElement>("#affiliateShareUrl");
  if (shareInput) shareInput.value = productShareUrl(result.shareUrl);
  const passwordNotice = document.querySelector<HTMLElement>("#generatedPasswordNotice");
  if (passwordNotice) {
    passwordNotice.hidden = !result.generatedPassword;
    passwordNotice.innerHTML = result.generatedPassword ? `<strong>请立即保存查询密码</strong><code>${escapeHtml(result.generatedPassword)}</code><span>密码仅展示这一次，之后查询推广数据需要使用。</span>` : "";
  }
  const stats = document.querySelector<HTMLElement>("#affiliateStats");
  if (stats) stats.innerHTML = [
    ["总点击", result.dashboard.totalClicks],
    ["独立访客", result.dashboard.uniqueClicks],
    ["成交订单", result.dashboard.completedOrders],
    ["待结算", formatCurrency(result.dashboard.pendingCommissionCents, "CNY")],
    ["已结算", formatCurrency(result.dashboard.paidCommissionCents, "CNY")]
  ].map(([label, value]) => `<div><span>${label}</span><strong>${value}</strong></div>`).join("");
  const orders = document.querySelector<HTMLElement>("#affiliateOrders");
  if (orders) orders.innerHTML = result.dashboard.orders.length
    ? result.dashboard.orders.map(renderAffiliateOrder).join("")
    : `<p class="affiliate-no-orders">暂无订单记录</p>`;
  const defaultMarkup = document.querySelector<HTMLInputElement>("#affiliateDefaultMarkup");
  if (defaultMarkup && result.dashboard.affiliate.defaultMarkupPercent !== undefined) defaultMarkup.value = String(result.dashboard.affiliate.defaultMarkupPercent);
  createPortalIcons();
}

async function hydrateAffiliatePricing() {
  const list = document.querySelector<HTMLElement>("#affiliateProductPricing");
  if (!list) return;
  const response = await fetch("/api/affiliate/catalog", { headers: { Accept: "application/json" } });
  if (!response.ok) return;
  const items = ((await response.json()) as { items: AffiliateProduct[] }).items;
  list.innerHTML = items.length
    ? items.map((item) => `<label class="affiliate-product-price-row"><input type="checkbox" data-pricing-product="${escapeAttribute(item.id)}" /><span><strong>${escapeHtml(item.title)}</strong><small>管理员价 ${formatCurrency(item.priceCents, item.currency)} · 专属价 ${formatCurrency(item.customerPriceCents, item.currency)} · 专属链接下单每单赚${escapeHtml(formatCommissionEarnings(item.commissionCents, item.currency))}</small></span><b>${item.markupPercent}%</b></label>`).join("")
    : `<p class="affiliate-no-orders">暂无已发布商品</p>`;
  document.querySelector<HTMLButtonElement>("#applyAffiliateDefault")?.addEventListener("click", () => void applyAffiliateMarkup(null));
  document.querySelector<HTMLButtonElement>("#applyAffiliateSelected")?.addEventListener("click", () => {
    const ids = [...list.querySelectorAll<HTMLInputElement>("[data-pricing-product]:checked")].map((input) => input.dataset.pricingProduct).filter((id): id is string => Boolean(id));
    void applyAffiliateMarkup(ids);
  });
}

async function applyAffiliateMarkup(productIds: string[] | null) {
  const input = document.querySelector<HTMLInputElement>(productIds === null ? "#affiliateDefaultMarkup" : "#affiliateSelectedMarkup");
  const markupPercent = Number(input?.value ?? 0);
  if (!Number.isInteger(markupPercent) || markupPercent < 0 || markupPercent > 1000) {
    showPortalToast("加价比例需在 0% 到 1000% 之间");
    return;
  }
  const response = await fetch("/api/affiliate/markups", { method: "PATCH", headers: { "content-type": "application/json" }, body: JSON.stringify({ markupPercent, productIds }) });
  if (!response.ok) {
    showPortalToast("专属价格保存失败");
    return;
  }
  showPortalToast("专属价格已更新");
  await hydrateAffiliatePricing();
}

function renderAffiliateOrder(order: AffiliateOrder) {
  return `<article><div><strong>${escapeHtml(order.productTitle)}</strong><span>${escapeHtml(order.orderCode)} · ${formatDate(order.createdAt)}</span></div><div><strong>${formatCurrency(order.priceCents, order.currency)}</strong><span>佣金 ${formatCurrency(order.commissionCents, order.currency)}</span></div><div><span>${orderStatusLabel(order.orderStatus)}</span><b>${commissionStatusLabel(order.commissionStatus)}</b></div></article>`;
}

async function copyAffiliateShareLink() {
  const input = document.querySelector<HTMLInputElement>("#affiliateShareUrl");
  if (!input) return;
  await navigator.clipboard.writeText(input.value);
  showPortalToast("专属链接已复制");
}

function productShareUrl(shareUrl: string) {
  const product = new URLSearchParams(location.search).get("product");
  if (!product) return shareUrl;
  const url = new URL(shareUrl);
  url.searchParams.set("product", product);
  return url.toString();
}

function renderPosts(rawQuery: string) {
  if (!postGrid || posts.length === 0) return;
  const query = rawQuery.trim().toLocaleLowerCase("zh-CN");
  const searchBySlug = new Map(searchDocuments.map((document) => [document.slug, document]));
  let filtered = posts.filter((post) => {
    if (!query) return true;
    const document = searchBySlug.get(post.slug);
    return [post.title, post.excerpt, document?.title, document?.excerpt, document?.body]
      .filter(Boolean)
      .some((value) => String(value).toLocaleLowerCase("zh-CN").includes(query));
  });

  const limit = Number(postGrid.dataset.limit || 0);
  if (limit > 0) filtered = filtered.slice(0, limit);

  postGrid.innerHTML = filtered.map((post, index) => renderPostCard(post, index, Boolean(limit))).join("");
  emptyState?.toggleAttribute("hidden", filtered.length > 0);
  const count = document.querySelector<HTMLElement>("#articleCount");
  if (count) count.textContent = String(filtered.length);
  if (searchMeta) searchMeta.textContent = query ? `找到 ${filtered.length} 篇匹配内容` : "按最新发布排序";
  createPortalIcons();
}

function renderPostCard(post: PostListItem, index: number, home: boolean) {
  const featured = home && index === 0 ? " featured" : "";
  const href = articlePermalinkPath(post.slug, lockedReferral());
  return `<article class="post-card${featured}">
    <a href="${escapeAttribute(href)}" data-portal-route aria-label="阅读 ${escapeHtml(post.title)}">
      <div class="post-card-topline"><span>${featured ? "最新文章" : "文章"}</span><time datetime="${escapeHtml(post.createdAt)}">${formatDate(post.createdAt)}</time></div>
      <h${home ? "3" : "2"}>${escapeHtml(post.title)}</h${home ? "3" : "2"}>
      <p>${escapeHtml(post.excerpt || "打开文章，继续阅读完整内容。")}</p>
      <div class="post-card-meta"><span>${post.viewCount} 阅读</span><span>${post.commentCount} 评论</span><i data-lucide="arrow-up-right"></i></div>
    </a>
  </article>`;
}

function updateHomeStats() {
  const postCount = document.querySelector<HTMLElement>("#heroPostCount");
  const viewCount = document.querySelector<HTMLElement>("#heroViewCount");
  const commentCount = document.querySelector<HTMLElement>("#heroCommentCount");
  const latestLink = document.querySelector<HTMLAnchorElement>("#latestArticleLink");
  if (postCount) postCount.textContent = String(posts.length);
  if (viewCount) viewCount.textContent = formatNumber(posts.reduce((sum, post) => sum + post.viewCount, 0));
  if (commentCount) commentCount.textContent = formatNumber(posts.reduce((sum, post) => sum + post.commentCount, 0));
  if (latestLink && posts[0]) latestLink.href = articlePermalinkPath(posts[0].slug, lockedReferral());
}

async function captureReferral() {
  const currentUrl = new URL(location.href);
  const incoming = normalizeReferral(currentUrl.searchParams.get("ref"));
  let locked = lockedReferral();
  const isNewLock = !locked && Boolean(incoming);
  if (!locked && incoming) {
    localStorage.setItem(referralKey, incoming);
    locked = incoming;
  }
  if (!locked) return;

  currentUrl.searchParams.set("ref", locked);
  history.replaceState(history.state, "", `${currentUrl.pathname}${currentUrl.search}${currentUrl.hash}`);
  propagateReferralLinks();
  if (!incoming || incoming !== locked) return;

  try {
    const response = await fetch("/api/affiliate/clicks", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ ref: locked, localId: affiliateVisitorId(), path: currentUrl.pathname })
    });
    if (!response.ok && isNewLock) {
      localStorage.removeItem(referralKey);
      currentUrl.searchParams.delete("ref");
      history.replaceState(history.state, "", `${currentUrl.pathname}${currentUrl.search}${currentUrl.hash}`);
      propagateReferralLinks();
      syncArticleReader();
    }
  } catch {
    // Keep the first referral locally when the API is temporarily unavailable.
  }
}

function lockedReferral(): string | null {
  return normalizeReferral(localStorage.getItem(referralKey));
}

function affiliateVisitorId(): string {
  const existing = localStorage.getItem(affiliateVisitorStorageKey);
  if (existing) return existing;
  const created = crypto.randomUUID();
  localStorage.setItem(affiliateVisitorStorageKey, created);
  return created;
}

function addLockedReferral(url: URL) {
  const ref = lockedReferral();
  if (ref) url.searchParams.set("ref", ref);
}

function propagateReferralLinks() {
  const ref = lockedReferral();
  document.querySelectorAll<HTMLAnchorElement>("a[data-portal-route]").forEach((link) => {
    const url = new URL(link.href, location.href);
    if (url.origin !== location.origin) return;
    if (ref) url.searchParams.set("ref", ref);
    else url.searchParams.delete("ref");
    link.href = url.toString();
  });
}

function orderStatusLabel(status: AffiliateOrder["orderStatus"]) {
  return ({ pending: "待联系", completed: "已成交", canceled: "已取消" } as const)[status];
}

function commissionStatusLabel(status: AffiliateOrder["commissionStatus"]) {
  return ({ not_due: "无需结算", pending: "待支付", paid: "已支付" } as const)[status];
}

function showPortalToast(message: string, options: { center?: boolean; success?: boolean; duration?: number } = {}) {
  const toast = document.querySelector<HTMLElement>("#portalToast");
  if (!toast) return;
  window.clearTimeout(portalToastTimer);
  toast.textContent = message;
  toast.classList.toggle("portal-toast-center", options.center === true);
  toast.classList.toggle("portal-toast-success", options.success === true);
  toast.classList.add("is-visible");
  portalToastTimer = window.setTimeout(() => {
    toast.classList.remove("is-visible");
  }, options.duration ?? 1800);
}

function createPortalIcons() {
  createIcons({ icons });
}

function formatDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
}

function formatNumber(value: number) {
  return new Intl.NumberFormat("zh-CN", { notation: value >= 10000 ? "compact" : "standard" }).format(value);
}

function formatCurrency(cents: number, currency: string) {
  return new Intl.NumberFormat("zh-CN", { style: "currency", currency: currency || "CNY", minimumFractionDigits: 2 }).format(cents / 100);
}

function productCategoryLabel(category: string) {
  return ({ service: "服务", digital: "数字内容", software: "软件工具", other: "其它" } as Record<string, string>)[category] ?? "其它";
}

function toolCategoryLabel(category: string) {
  return ({ ai: "AI", writing: "写作", design: "设计", productivity: "效率", development: "开发", other: "其它" } as Record<string, string>)[category] ?? "其它";
}

function escapeHtml(value: string) {
  return value.replace(/[&<>'"]/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    "'": "&#39;",
    '"': "&quot;"
  })[character] ?? character);
}

function escapeAttribute(value: string) {
  return escapeHtml(value);
}
