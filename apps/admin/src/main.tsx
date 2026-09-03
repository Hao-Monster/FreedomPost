import {
  type ClipboardEvent,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  useEffect,
  useMemo,
  useRef,
  useState
} from "react";
import { createRoot } from "react-dom/client";
import {
  CircleAlert,
  Bold,
  Code2,
  Copy,
  ImagePlus,
  Italic,
  KeyRound,
  Link2,
  LoaderCircle,
  LogOut,
  Package,
  Plus,
  RefreshCw,
  Save,
  Strikethrough,
  Trash2,
  Type,
  Underline,
  Upload,
  Video
} from "lucide-react";
import {
  CALLOUT_DEFAULT_EMOJI,
  calloutDirective,
  normalizeCalloutEmoji,
  parseYouTubeDirective,
  parseYouTubeVideoInput,
  splitCalloutBlocks,
  youtubeDirective
} from "@freedompost/shared";
import { editorCalloutHtml } from "./editor-callout.js";
import {
  createClientID,
  imageImportFailureHtml,
  localizePastedImages,
  type ImageImportFailure,
  type ImportedImageClaim,
  type ImportedImageFile,
  type PendingImageImport
} from "./editor-image-import.js";
import { editorImageHtml, editorImagesMarkdown, editorYouTubeHtml } from "./editor-media.js";
import { sanitizePastedEditorHtml } from "./editor-paste.js";
import { focusEditorStart, insertBlockPlaceholderAtSelection } from "./editor-selection.js";
import { createNewPostPayload } from "./new-post.js";
import { startPendingMediaInsertion } from "./pending-media-insertion.js";
import { PendingTaskBarrier } from "./pending-task-barrier.js";
import {
  failureFromProductIssues,
  invalidProductResponseFailure,
  networkProductFailure,
  readProductApiFailure,
  readReauthenticationFailure,
  validateProductForSave,
  type ProductField,
  type ProductIssue,
  type ProductOperation,
  type ProductSaveFailure
} from "./product-save-feedback.js";
import "./styles.css";

type AdminPost = {
  id: string;
  slug: string;
  title: string;
  markdown: string;
  createdAt: string;
  updatedAt: string;
  viewCount: number;
  commentCount: number;
  attachmentCount: number;
  visibility: "public" | "private" | "paid";
  priceCents: number;
  currency: string;
};

type Toast = {
  id: number;
  text: string;
};

type UploadedFile = {
  id: string;
  name: string;
  mimeType: string;
  sizeBytes: number;
  url: string;
  storageProvider: "local" | "oss" | "r2";
  storageKey: string;
  storedFilename: string;
  claimToken?: string;
};

type AdminProduct = {
  id: string;
  slug: string;
  title: string;
  summary: string;
  description: string;
  category: string;
  priceCents: number;
  commissionCents: number;
  compareAtCents: number | null;
  currency: string;
  stock: number;
  soldCount: number;
  coverUrl: string | null;
  linkUrl: string;
  status: "draft" | "published";
  sortOrder: number;
  createdAt: string;
  updatedAt: string;
};

type AdminTool = {
  id: string;
  slug: string;
  title: string;
  summary: string;
  description: string;
  category: string;
  url: string;
  coverUrl: string | null;
  status: "draft" | "published";
  sortOrder: number;
  createdAt: string;
  updatedAt: string;
};

type AdminAffiliate = {
  id: string;
  wechatId: string;
  status: "active" | "disabled";
  totalClicks: number;
  uniqueClicks: number;
  orderCount: number;
  createdAt: string;
};

type AdminAffiliateOrder = {
  id: string;
  orderCode: string;
  affiliateWechatId: string;
  productTitle: string;
  priceCents: number;
  commissionCents: number;
  currency: string;
  orderStatus: "pending" | "completed" | "canceled";
  commissionStatus: "not_due" | "pending" | "paid";
  createdAt: string;
};

type AdminArticleOrder = {
  id: string;
  orderCode: string;
  loginName: string;
  postTitle: string;
  priceCents: number;
  currency: string;
  status: "pending" | "completed" | "canceled";
  createdAt: string;
};

type AdminReaderAccount = {
  id: string;
  loginName: string;
  status: "active" | "disabled";
  createdAt: string;
};

const headingOptions = [
  { label: "正文", value: "p" },
  { label: "标题 1", value: "h1" },
  { label: "标题 2", value: "h2" },
  { label: "标题 3", value: "h3" },
  { label: "高亮块", value: "callout" }
] as const;

const sizeOptions = [
  { label: "小字", className: "fp-size-sm" },
  { label: "正文", className: "fp-size-md" },
  { label: "大字", className: "fp-size-lg" },
  { label: "强调", className: "fp-size-xl" }
] as const;

const colorOptions = [
  { label: "墨色", className: "fp-color-ink" },
  { label: "红色", className: "fp-color-red" },
  { label: "绿色", className: "fp-color-green" },
  { label: "蓝色", className: "fp-color-blue" },
  { label: "紫色", className: "fp-color-purple" },
  { label: "金色", className: "fp-color-gold" }
] as const;

function App() {
  const [isAuthed, setAuthed] = useState(false);
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [posts, setPosts] = useState<AdminPost[]>([]);
  const [products, setProducts] = useState<AdminProduct[]>([]);
  const [tools, setTools] = useState<AdminTool[]>([]);
  const [affiliates, setAffiliates] = useState<AdminAffiliate[]>([]);
  const [affiliateOrders, setAffiliateOrders] = useState<AdminAffiliateOrder[]>([]);
  const [articleOrders, setArticleOrders] = useState<AdminArticleOrder[]>([]);
  const [readerAccounts, setReaderAccounts] = useState<AdminReaderAccount[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [workspace, setWorkspace] = useState<"posts" | "products" | "tools" | "distribution" | "paid">("posts");
  const [toast, setToast] = useState<Toast | null>(null);
  const [pendingMediaCount, setPendingMediaCount] = useState(0);
  const [isSavingPost, setSavingPost] = useState(false);
  const editorRef = useRef<HTMLDivElement | null>(null);
  const attachmentInputRef = useRef<HTMLInputElement | null>(null);
  const imageReplacementInputRef = useRef<HTMLInputElement | null>(null);
  const imageReplacementTargetRef = useRef<string | null>(null);
  const savedRangeRef = useRef<Range | null>(null);
  const focusCreatedPostRef = useRef<string | null>(null);
  const pendingMediaRef = useRef(new PendingTaskBarrier());
  const importedImageClaimsRef = useRef(new Map<string, ImportedImageClaim>());
  const failedImageImportsRef = useRef(new Map<string, PendingImageImport>());
  const activePost = useMemo(() => posts.find((post) => post.id === activeId) ?? posts[0], [posts, activeId]);

  useEffect(() => {
    void fetchSession();
  }, []);

  useEffect(() => {
    if (!activePost || !editorRef.current) return;
    importedImageClaimsRef.current.clear();
    failedImageImportsRef.current.clear();
    editorRef.current.innerHTML = markdownToEditorHtml(activePost.markdown);
    // Auto-import external images already in the markdown so the user
    // doesn't hit a save rejection when opening an existing article.
    if (/!\[[^\]]*\]\(https?:\/\//i.test(activePost.markdown) && activePost.id) {
      const postID = activePost.id;
      const editor = editorRef.current;
      void trackPendingMedia(autoImportEditorImages(postID, editor));
    }
    if (focusCreatedPostRef.current === activePost.id) {
      focusCreatedPostRef.current = null;
      savedRangeRef.current = focusEditorStart(editorRef.current)?.cloneRange() ?? null;
    }
  }, [activePost?.id]);

  useEffect(() => {
    const rememberEditorSelection = () => {
      const editor = editorRef.current;
      const selection = window.getSelection();
      if (!editor || !selection?.rangeCount || !editor.contains(selection.anchorNode)) return;
      savedRangeRef.current = selection.getRangeAt(0).cloneRange();
    };

    document.addEventListener("selectionchange", rememberEditorSelection);
    return () => document.removeEventListener("selectionchange", rememberEditorSelection);
  }, []);

  async function fetchSession() {
    const response = await fetch("/api/admin/session", { credentials: "include" });
    if (response.ok) {
      const body = await response.json().catch(() => null) as { session?: { username?: string } } | null;
      if (body?.session?.username) setUsername(body.session.username);
      setAuthed(true);
      await Promise.all([loadPosts(), loadProducts(), loadTools(), loadDistribution()]);
    }
  }

  async function login(event: FormEvent) {
    event.preventDefault();
    const response = await fetch("/api/admin/login", {
      method: "POST",
      headers: { "content-type": "application/json" },
      credentials: "include",
      body: JSON.stringify({ username, password })
    });

    if (!response.ok) {
      showToast("登录失败");
      return;
    }

    setAuthed(true);
    setPassword("");
    await Promise.all([loadPosts(), loadProducts(), loadTools(), loadDistribution()]);
    showToast("已登录");
  }

  async function logout() {
    await fetch("/api/admin/logout", { method: "POST", credentials: "include" });
    setAuthed(false);
    setPosts([]);
    setProducts([]);
    setTools([]);
    setAffiliates([]);
    setAffiliateOrders([]);
  }

  async function loadPosts() {
    const response = await fetch("/api/admin/posts", { credentials: "include" });
    if (!response.ok) {
      setAuthed(false);
      return;
    }

    const body = (await response.json()) as { items: AdminPost[] };
    setPosts(body.items);
    setActiveId((current) => current ?? body.items[0]?.id ?? null);
  }

  async function loadProducts() {
    const response = await fetch("/api/admin/products", { credentials: "include" });
    if (!response.ok) return;
    const body = (await response.json()) as { items: AdminProduct[] };
    setProducts(body.items.map((product) => ({ ...product, linkUrl: product.linkUrl ?? "" })));
  }

  async function loadTools() {
    const response = await fetch("/api/admin/tools", { credentials: "include" });
    if (!response.ok) return;
    setTools(((await response.json()) as { items: AdminTool[] }).items);
  }

  async function loadDistribution() {
    const [affiliateResponse, orderResponse] = await Promise.all([
      fetch("/api/admin/affiliates", { credentials: "include" }),
      fetch("/api/admin/affiliate-orders", { credentials: "include" })
    ]);
    if (affiliateResponse.ok) setAffiliates(((await affiliateResponse.json()) as { items: AdminAffiliate[] }).items);
    if (orderResponse.ok) setAffiliateOrders(((await orderResponse.json()) as { items: AdminAffiliateOrder[] }).items);
  }

  async function loadPaidAccess() {
    const [ordersResponse, accountsResponse] = await Promise.all([
      fetch("/api/admin/article-orders", { credentials: "include" }),
      fetch("/api/admin/reader-accounts", { credentials: "include" })
    ]);
    if (ordersResponse.ok) setArticleOrders(((await ordersResponse.json()) as { items: AdminArticleOrder[] }).items);
    if (accountsResponse.ok) setReaderAccounts(((await accountsResponse.json()) as { items: AdminReaderAccount[] }).items);
  }

  function openProductWorkspace() {
    setWorkspace("products");
    void loadProducts();
  }

  function openToolsWorkspace() {
    setWorkspace("tools");
    void loadTools();
  }

  async function createPost() {
    const response = await fetch("/api/admin/posts", {
      method: "POST",
      headers: { "content-type": "application/json" },
      credentials: "include",
      body: JSON.stringify(createNewPostPayload())
    });

    if (!response.ok) {
      showToast("创建失败");
      return;
    }

    const created = (await response.json()) as AdminPost;
    focusCreatedPostRef.current = created.id;
    setPosts((items) => [created, ...items]);
    setActiveId(created.id);
    showToast("文章已创建");
  }

  async function savePost() {
    if (!activePost || isSavingPost) return;
    setSavingPost(true);

    try {
      if (pendingMediaRef.current.size > 0) {
        showToast("正在等待图片上传完成…");
      }

      const { failureCount } = await pendingMediaRef.current.waitForIdle();
      setPendingMediaCount(0);
      if (failureCount > 0) {
        showToast("有图片上传失败，文章尚未保存，请重试");
        return;
      }

      const getMarkdown = () => editorRef.current ? editorHtmlToMarkdown(editorRef.current) : activePost.markdown;
      const checkUnresolved = () => {
        const count = editorRef.current?.querySelectorAll('[data-fp-type="image-import-error"]').length ?? 0;
        if (count > 0) {
          showToast(`有 ${count} 张图片尚未转存；请重试、上传本地图片或删除后再保存`);
          return true;
        }
        return false;
      };
      const doSave = (markdown: string) => {
        const importedImages = [...importedImageClaimsRef.current.values()]
          .filter((claim) => markdown.includes(claim.url))
          .map(({ id, claimToken }) => ({ id, claimToken }));
        return fetch(`/api/admin/posts/${activePost.id}`, {
          method: "PUT",
          headers: { "content-type": "application/json" },
          credentials: "include",
          body: JSON.stringify({ title: activePost.title, markdown, visibility: activePost.visibility, priceCents: activePost.priceCents, currency: activePost.currency, importedImages })
        });
      };

      if (checkUnresolved()) return;

      let markdown = getMarkdown();
      let response = await doSave(markdown);

      // Auto-import external images and retry once when the backend rejects the
      // article because it contains unmanaged image hosts.
      if (!response.ok && editorRef.current) {
        const errorBody = await response.json().catch(() => null) as { error?: { code?: string; message?: string; resolution?: string } } | null;
        if (errorBody?.error?.code === "EXTERNAL_IMAGE_NOT_IMPORTED") {
          showToast("检测到外链图片，正在自动转存…");
          await autoImportEditorImages(activePost.id, editorRef.current);
          if (checkUnresolved()) return;
          markdown = getMarkdown();
          response = await doSave(markdown);
        } else {
          const msg = errorBody?.error?.message?.trim();
          const res = errorBody?.error?.resolution?.trim();
          showToast(msg ? `${msg}${res ? `。${res}` : ""}` : "保存失败");
          return;
        }
      }

      if (!response.ok) {
        showToast(await readImageImportApiMessage(response, "保存失败"));
        return;
      }

      const saved = (await response.json()) as AdminPost;
      importedImageClaimsRef.current.clear();
      setPosts((items) => items.map((item) => (item.id === saved.id ? saved : item)));
      showToast(saved.visibility === "private" ? "保存成功，仅自己可见" : saved.visibility === "paid" ? "保存成功，购买后可见" : "保存成功，文章已公开");
    } finally {
      setSavingPost(false);
    }
  }

  async function deletePost() {
    if (!activePost) return;
    if (!confirm(`确认删除《${activePost.title}》？关联评论会一起删除。`)) return;

    const response = await fetch(`/api/admin/posts/${activePost.id}`, {
      method: "DELETE",
      credentials: "include"
    });

    if (!response.ok) {
      showToast("删除失败");
      return;
    }

    setPosts((items) => items.filter((item) => item.id !== activePost.id));
    setActiveId(null);
    showToast("文章已删除");
  }

  function patchActivePost(patch: Partial<AdminPost>) {
    if (!activePost) return;
    setPosts((items) => items.map((item) => (item.id === activePost.id ? { ...item, ...patch } : item)));
  }

  async function handleAttachmentFiles(files: FileList | null) {
    if (!activePost || !files?.length) return;
    try {
      await trackPendingMedia(
        insertPendingMediaAtCaret(() =>
          Promise.all([...files].map(fileToEditorHtml)).then((snippets) => snippets.join(""))
        )
      );
      showToast(`已上传并插入 ${files.length} 个附件`);
    } catch {
      showToast("附件上传失败");
    }
  }

  // Shared image paste logic used by both HTML clipboard and synthetic HTML
  // generated from plain-text Markdown pastes (e.g. from VS Code / Typora).
  async function pasteHtmlWithImages(html: string) {
    try {
      let pastedResult: Awaited<ReturnType<typeof localizePastedImages>> = null;
      let pastedFailureCount = 0;
      let pastedImageCount = 0;
      await trackPendingMedia(
        insertPendingMediaAtCaret(async () => {
          pastedResult = await localizePastedImages(html, location.href, {
            uploadEmbeddedImage,
            importRemoteImages: (items) => importRemoteImages(activePost?.id ?? "", items)
          });
          if (!pastedResult) throw new Error("No images in pasted HTML");
          for (const image of pastedResult.importedImages) {
            importedImageClaimsRef.current.set(image.id, image);
          }
          for (const failedImage of pastedResult.failedImages) {
            failedImageImportsRef.current.set(failedImage.id, failedImage);
          }
          pastedFailureCount = pastedResult.failedImages.length;
          pastedImageCount = pastedResult.importedImages.length + pastedResult.failedImages.length;
          return pastedResult.html;
        })
      );
      showToast(pastedFailureCount > 0 ? `${pastedFailureCount} 张图片未能转存，已显示原因和处理方式` : pastedImageCount > 0 ? "图片已转存并插入" : "图片已处理");
    } catch {
      showToast("图片处理失败；请重试或上传本地图片");
    }
  }

  async function handleEditorPaste(event: ClipboardEvent<HTMLDivElement>) {
    const files = [...event.clipboardData.items]
      .filter((item) => item.kind === "file" && item.type.startsWith("image/"))
      .map((item) => item.getAsFile())
      .filter((file): file is File => Boolean(file));

    if (files.length) {
      event.preventDefault();
      try {
        await trackPendingMedia(
          insertPendingMediaAtCaret(() =>
            Promise.all(files.map(fileToEditorHtml)).then((snippets) => snippets.join(""))
          )
        );
        showToast("图片已上传并插入");
      } catch {
        showToast("图片上传失败");
      }
      return;
    }

    const pastedHtml = event.clipboardData.getData("text/html");

    // If no HTML in clipboard, check if plain text contains Markdown image syntax.
    // This handles pastes from VS Code, Typora, Obsidian, GitHub raw markdown, etc.
    if (!pastedHtml) {
      const pastedText = event.clipboardData.getData("text/plain");
      const syntheticHtml = markdownImagesToPasteHtml(pastedText);
      if (syntheticHtml) {
        event.preventDefault();
        await pasteHtmlWithImages(syntheticHtml);
      }
      // else: let the browser handle plain text naturally (no preventDefault)
      return;
    }

    event.preventDefault();
    if (!/<img[\s>]/i.test(pastedHtml)) {
      const sanitizedHtml = sanitizePastedEditorHtml(pastedHtml, location.href);
      if (sanitizedHtml) insertPastedHtmlAtCaret(sanitizedHtml);
      return;
    }

    await pasteHtmlWithImages(pastedHtml);
  }

  function trackPendingMedia<T>(task: Promise<T>): Promise<T> {
    const tracked = pendingMediaRef.current.track(task);
    setPendingMediaCount(pendingMediaRef.current.size);
    void tracked.then(
      () => setPendingMediaCount(pendingMediaRef.current.size),
      () => setPendingMediaCount(pendingMediaRef.current.size)
    );
    return tracked;
  }

  function insertPendingMediaAtCaret(createHtml: () => Promise<string>): Promise<void> {
    return startPendingMediaInsertion(createHtml, {
      insertPlaceholder: insertMediaPlaceholderAtCaret,
      replacePlaceholder: replaceMediaPlaceholder,
      removePlaceholder: (placeholder) => placeholder.remove()
    });
  }

  function insertMediaPlaceholderAtCaret(): HTMLDivElement {
    const editor = editorRef.current;
    if (!editor) throw new Error("Editor is unavailable");

    const placeholder = document.createElement("div");
    placeholder.className = "editor-media-placeholder";
    placeholder.dataset.fpType = "pending-media";
    placeholder.contentEditable = "false";
    placeholder.textContent = "图片上传中…";

    editor.focus();
    const selection = window.getSelection();
    insertBlockPlaceholderAtSelection(editor, placeholder, selection);

    const nextRange = document.createRange();
    nextRange.setStartAfter(placeholder);
    nextRange.collapse(true);
    selection?.removeAllRanges();
    selection?.addRange(nextRange);
    savedRangeRef.current = nextRange.cloneRange();
    return placeholder;
  }

  function replaceMediaPlaceholder(placeholder: HTMLDivElement, html: string) {
    const editor = editorRef.current;
    if (!editor || !editor.contains(placeholder)) {
      throw new Error("Media insertion point is no longer available");
    }

    const template = document.createElement("template");
    template.innerHTML = html;
    placeholder.replaceWith(template.content);
    syncEditorMarkdown();
  }

  function insertHtmlAtCaret(html: string) {
    if (!activePost) return;
    const editor = editorRef.current;
    if (!editor) return;

    editor.focus();
    const selection = window.getSelection();
    const template = document.createElement("template");
    template.innerHTML = html;
    const fragment = template.content;
    const lastNode = fragment.lastChild;
    const insertsRichBlock = Boolean(
      fragment.querySelector("figure.editor-image,figure.editor-youtube,.editor-attachment")
    );

    if (selection?.rangeCount && editor.contains(selection.anchorNode)) {
      const range = selection.getRangeAt(0);
      const container = closestCalloutContent(selection.anchorNode, editor) ?? editor;
      const topLevelNode = closestEditorChild(selection.anchorNode, container);
      if (insertsRichBlock && topLevelNode) {
        topLevelNode.after(fragment);
      } else {
        range.deleteContents();
        range.insertNode(fragment);
      }
      if (lastNode) {
        range.setStartAfter(lastNode);
        range.collapse(true);
        selection.removeAllRanges();
        selection.addRange(range);
      }
    } else {
      editor.append(fragment);
    }

    syncEditorMarkdown();
  }

  function insertPastedHtmlAtCaret(html: string) {
    const editor = editorRef.current;
    if (!activePost || !editor) return;

    editor.focus();
    if (!document.execCommand("insertHTML", false, html)) {
      insertHtmlAtCaret(html);
      return;
    }
    syncEditorMarkdown();
  }

  function restoreEditorSelection() {
    const editor = editorRef.current;
    if (!editor) return;

    editor.focus();
    const range = savedRangeRef.current;
    const selection = window.getSelection();
    if (!range || !selection || !editor.contains(range.commonAncestorContainer)) return;

    selection.removeAllRanges();
    selection.addRange(range);
  }

  function runEditorCommand(command: string, value?: string) {
    if (!activePost) return;
    restoreEditorSelection();
    document.execCommand(command, false, value);
    syncEditorMarkdown();
  }

  function applyBlockFormat(tagName: string) {
    runEditorCommand("formatBlock", tagName);
  }

  function applyInlineClass(className: string) {
    if (!activePost) return;
    restoreEditorSelection();

    const editor = editorRef.current;
    const selection = window.getSelection();
    if (!editor || !selection?.rangeCount || !editor.contains(selection.anchorNode) || selection.isCollapsed) {
      showToast("请先选中文本");
      return;
    }

    const range = selection.getRangeAt(0);
    const span = document.createElement("span");
    span.className = className;

    try {
      range.surroundContents(span);
    } catch {
      span.append(range.extractContents());
      range.insertNode(span);
    }

    const nextRange = document.createRange();
    nextRange.selectNodeContents(span);
    selection.removeAllRanges();
    selection.addRange(nextRange);
    savedRangeRef.current = nextRange.cloneRange();
    syncEditorMarkdown();
  }

  function createLinkAtSelection() {
    const href = prompt("输入链接地址");
    if (!href) return;

    try {
      const url = new URL(href, location.origin);
      if (url.protocol !== "http:" && url.protocol !== "https:") {
        showToast("只支持 http/https 链接");
        return;
      }
      runEditorCommand("createLink", url.toString());
    } catch {
      showToast("链接地址无效");
    }
  }

  function insertCodeBlock() {
    insertHtmlAtCaret('<pre data-lang="ts"><code>// code</code></pre><p><br></p>');
  }

  function insertCallout() {
    if (!activePost) return;
    restoreEditorSelection();

    const editor = editorRef.current;
    const selection = window.getSelection();
    if (!editor) return;

    if (selection?.anchorNode && closestCalloutContent(selection.anchorNode, editor)) {
      showToast("高亮块不能嵌套");
      return;
    }

    const template = document.createElement("template");
    template.innerHTML = editorCalloutHtml("", CALLOUT_DEFAULT_EMOJI);
    const callout = template.content.firstElementChild;
    const content = callout?.querySelector<HTMLElement>(".editor-callout-content");
    if (!(callout instanceof HTMLElement) || !content) return;

    let selectedNodes: ChildNode[] = [];
    if (selection?.rangeCount && editor.contains(selection.anchorNode)) {
      const range = selection.getRangeAt(0);
      const startNode = closestEditorChild(range.startContainer, editor);
      const endNode = closestEditorChild(range.endContainer, editor) ?? startNode;
      const children = [...editor.childNodes];
      const startIndex = startNode ? children.indexOf(startNode) : -1;
      const endIndex = endNode ? children.indexOf(endNode) : startIndex;
      if (startIndex >= 0 && endIndex >= startIndex) {
        selectedNodes = children.slice(startIndex, endIndex + 1);
      }
    }

    if (selectedNodes.some((node) => node instanceof Element && (node.matches('[data-fp-type="callout"]') || node.querySelector('[data-fp-type="callout"]')))) {
      showToast("高亮块不能嵌套");
      return;
    }

    if (selectedNodes.length) {
      selectedNodes[0]?.before(callout);
      content.replaceChildren(...selectedNodes);
    } else {
      editor.append(callout);
    }

    if (!callout.nextSibling) {
      const trailingParagraph = document.createElement("p");
      trailingParagraph.append(document.createElement("br"));
      callout.after(trailingParagraph);
    }

    const focusTarget = content.querySelector<HTMLElement>("p,h1,h2,h3,h4,h5,h6,li,pre") ?? content;
    const nextRange = document.createRange();
    nextRange.selectNodeContents(focusTarget);
    nextRange.collapse(false);
    selection?.removeAllRanges();
    selection?.addRange(nextRange);
    savedRangeRef.current = nextRange.cloneRange();
    syncEditorMarkdown();
    showToast("高亮块已插入");
  }

  function handleEditorClick(event: ReactMouseEvent<HTMLDivElement>) {
    const target = event.target instanceof Element ? event.target : null;
    const imageImportAction = target?.closest<HTMLButtonElement>("[data-image-import-action]");
    if (imageImportAction) {
      event.preventDefault();
      const card = imageImportAction.closest<HTMLElement>('[data-fp-type="image-import-error"]');
      const importID = card?.dataset.importId;
      if (!card || !importID) return;
      if (imageImportAction.dataset.imageImportAction === "remove") {
        card.remove();
        failedImageImportsRef.current.delete(importID);
        syncEditorMarkdown();
        return;
      }
      if (imageImportAction.dataset.imageImportAction === "upload-local") {
        imageReplacementTargetRef.current = importID;
        imageReplacementInputRef.current?.click();
        return;
      }
      if (imageImportAction.dataset.imageImportAction === "retry") {
        const pending = failedImageImportsRef.current.get(importID);
        if (!pending) {
          showToast("原始图片地址仅在本次粘贴期间保留，请重新粘贴或上传本地图片");
          return;
        }
        void trackPendingMedia(retryFailedImageImport(card, pending));
        return;
      }
    }
    const option = target?.closest<HTMLButtonElement>("[data-callout-emoji-option]");
    if (option) {
      event.preventDefault();
      const callout = option.closest<HTMLElement>('[data-fp-type="callout"]');
      const emoji = normalizeCalloutEmoji(option.dataset.calloutEmojiOption);
      const trigger = callout?.querySelector<HTMLElement>("[data-callout-emoji-trigger]");
      const picker = callout?.querySelector<HTMLElement>("[data-callout-emoji-picker]");
      if (!callout || !trigger || !picker) return;

      callout.dataset.emoji = emoji;
      trigger.textContent = emoji;
      trigger.setAttribute("aria-expanded", "false");
      picker.hidden = true;
      picker.querySelectorAll<HTMLElement>("[data-callout-emoji-option]").forEach((item) => {
        item.setAttribute("aria-selected", String(item.dataset.calloutEmojiOption === emoji));
      });
      syncEditorMarkdown();
      return;
    }

    const trigger = target?.closest<HTMLElement>("[data-callout-emoji-trigger]");
    if (trigger) {
      event.preventDefault();
      toggleCalloutEmojiPicker(trigger);
    }
  }

  function handleEditorKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    const target = event.target instanceof Element ? event.target : null;
    const trigger = target?.closest<HTMLElement>("[data-callout-emoji-trigger]");
    if (trigger && (event.key === "Enter" || event.key === " ")) {
      event.preventDefault();
      toggleCalloutEmojiPicker(trigger);
      return;
    }

    if (event.key === "Escape") {
      const callout = target?.closest<HTMLElement>('[data-fp-type="callout"]');
      const picker = callout?.querySelector<HTMLElement>("[data-callout-emoji-picker]");
      const emojiTrigger = callout?.querySelector<HTMLElement>("[data-callout-emoji-trigger]");
      if (picker && emojiTrigger && !picker.hidden) {
        picker.hidden = true;
        emojiTrigger.setAttribute("aria-expanded", "false");
        emojiTrigger.focus();
      }
    }
  }

  function toggleCalloutEmojiPicker(trigger: HTMLElement) {
    const editor = editorRef.current;
    const callout = trigger.closest<HTMLElement>('[data-fp-type="callout"]');
    const picker = callout?.querySelector<HTMLElement>("[data-callout-emoji-picker]");
    if (!editor || !picker) return;

    const willOpen = picker.hidden;
    editor.querySelectorAll<HTMLElement>("[data-callout-emoji-picker]").forEach((item) => {
      item.hidden = true;
    });
    editor.querySelectorAll<HTMLElement>("[data-callout-emoji-trigger]").forEach((item) => {
      item.setAttribute("aria-expanded", "false");
    });
    picker.hidden = !willOpen;
    trigger.setAttribute("aria-expanded", String(willOpen));
    if (willOpen) picker.querySelector<HTMLElement>("[data-callout-emoji-option]")?.focus();
  }

  function insertYouTubeVideo() {
    const input = prompt("粘贴 YouTube 视频链接");
    if (!input) return;

    const video = parseYouTubeVideoInput(input);
    if (!video) {
      showToast("无法识别这个 YouTube 视频链接");
      return;
    }

    restoreEditorSelection();
    insertHtmlAtCaret(`${editorYouTubeHtml(video)}<p><br></p>`);
    showToast("YouTube 视频已插入");
  }

  function closestEditorChild(node: Node | null, container: HTMLElement): ChildNode | null {
    let current = node;
    while (current?.parentNode && current.parentNode !== container) {
      current = current.parentNode;
    }
    return current?.parentNode === container ? (current as ChildNode) : null;
  }

  function closestCalloutContent(node: Node | null, editor: HTMLElement): HTMLElement | null {
    const element = node instanceof Element ? node : node?.parentElement;
    const content = element?.closest<HTMLElement>(".editor-callout-content") ?? null;
    return content && editor.contains(content) ? content : null;
  }

  function syncEditorMarkdown() {
    if (!activePost || !editorRef.current) return;
    patchActivePost({ markdown: editorHtmlToMarkdown(editorRef.current) });
  }

  async function retryFailedImageImport(card: HTMLElement, pending: PendingImageImport) {
    card.classList.add("is-retrying");
    try {
      const results = await importRemoteImages(activePost?.id ?? "", [{ clientId: pending.id, url: pending.source, name: pending.name }]);
      const result = results.get(pending.id);
      if (result && "url" in result) {
        replaceFailedImageCard(card, result, pending.id);
        return;
      }
      const failure = result ?? { code: "IMPORT_UNAVAILABLE", message: "图片转存服务暂时不可用", resolution: "请稍后重试，或上传本地图片" };
      failedImageImportsRef.current.set(pending.id, { ...pending, failure });
      replaceFailedImageCardWithHtml(card, imageImportFailureHtml(pending.id, failure));
    } catch {
      const failure = { code: "IMPORT_UNAVAILABLE", message: "图片转存服务暂时不可用", resolution: "请稍后重试，或上传本地图片" };
      failedImageImportsRef.current.set(pending.id, { ...pending, failure });
      replaceFailedImageCardWithHtml(card, imageImportFailureHtml(pending.id, failure));
    }
  }

  // Scans the editor DOM for figure.editor-image elements whose <img> has an
  // external (http/https) src that hasn't been imported yet, imports them via
  // the server API, then either replaces the src in-place (success) or swaps
  // the figure for an actionable error card (failure).
  // Returns the number of items processed.
  async function autoImportEditorImages(postID: string, editor: HTMLElement): Promise<number> {
    if (!postID) return 0;

    const figures = [...editor.querySelectorAll<HTMLElement>("figure.editor-image")];
    const toImport: Array<{ figure: HTMLElement; clientId: string; src: string; alt: string }> = [];

    for (const figure of figures) {
      const img = figure.querySelector("img");
      if (!img) continue;
      const src = img.getAttribute("src") ?? "";
      if (!src.startsWith("http://") && !src.startsWith("https://")) continue;
      // Skip images that were already successfully imported this session
      if ([...importedImageClaimsRef.current.values()].some((c) => c.url === src)) continue;
      toImport.push({ figure, clientId: createClientID(), src, alt: img.getAttribute("alt") ?? "图片" });
    }

    if (toImport.length === 0) return 0;

    try {
      const results = await importRemoteImages(
        postID,
        toImport.map((item) => ({ clientId: item.clientId, url: item.src, name: item.alt }))
      );
      for (const item of toImport) {
        const result = results.get(item.clientId);
        if (result && "url" in result) {
          // Success: update the img and anchor in-place, record the claim token
          item.figure.querySelector("img")?.setAttribute("src", result.url);
          item.figure.querySelector("a")?.setAttribute("href", result.url);
          if (result.claimToken) {
            importedImageClaimsRef.current.set(result.id, { id: result.id, claimToken: result.claimToken, url: result.url });
          }
        } else {
          // Failure: replace the figure with an actionable error card
          const failure: ImageImportFailure = result ?? { code: "IMPORT_UNAVAILABLE", message: "图片转存失败", resolution: "请上传本地图片" };
          failedImageImportsRef.current.set(item.clientId, { id: item.clientId, source: item.src, name: item.alt, failure });
          const tpl = document.createElement("template");
          tpl.innerHTML = imageImportFailureHtml(item.clientId, failure);
          item.figure.replaceWith(tpl.content);
        }
      }
    } catch {
      // If the API call itself fails, mark every figure as an error card
      for (const item of toImport) {
        const failure: ImageImportFailure = { code: "IMPORT_UNAVAILABLE", message: "图片转存服务暂时不可用", resolution: "请稍后重试，或上传本地图片" };
        failedImageImportsRef.current.set(item.clientId, { id: item.clientId, source: item.src, name: item.alt, failure });
        const tpl = document.createElement("template");
        tpl.innerHTML = imageImportFailureHtml(item.clientId, failure);
        item.figure.replaceWith(tpl.content);
      }
    }

    syncEditorMarkdown();
    return toImport.length;
  }

  async function replaceFailedImageWithLocalFile(file: File) {
    const targetID = imageReplacementTargetRef.current;
    imageReplacementTargetRef.current = null;
    if (!targetID || !editorRef.current) return;
    const card = editorRef.current.querySelector<HTMLElement>(`[data-fp-type="image-import-error"][data-import-id="${CSS.escape(targetID)}"]`);
    if (!card) return;
    try {
      const uploaded = await uploadFile(file);
      if (!uploaded.mimeType.startsWith("image/")) {
        showToast("请选择图片文件");
        return;
      }
      replaceFailedImageCard(card, uploaded, targetID);
      showToast("本地图片已上传并替换");
    } catch {
      showToast("本地图片上传失败，请重试");
    }
  }

  function replaceFailedImageCard(card: HTMLElement, uploaded: ImportedImageFile | UploadedFile, importID: string) {
    if ("claimToken" in uploaded && uploaded.claimToken) {
      importedImageClaimsRef.current.set(uploaded.id, { id: uploaded.id, claimToken: uploaded.claimToken, url: uploaded.url });
    }
    failedImageImportsRef.current.delete(importID);
    replaceFailedImageCardWithHtml(card, `${editorImageHtml(uploaded.url, uploaded.name)}<p><br></p>`);
  }

  function replaceFailedImageCardWithHtml(card: HTMLElement, html: string) {
    if (!editorRef.current?.contains(card)) return;
    const template = document.createElement("template");
    template.innerHTML = html;
    card.replaceWith(template.content);
    syncEditorMarkdown();
  }

  function showToast(text: string) {
    const id = Date.now();
    setToast({ id, text });
    window.setTimeout(() => {
      setToast((current) => (current?.id === id ? null : current));
    }, 1800);
  }

  if (!isAuthed) {
    return (
      <main className="login-screen">
        <form className="login-box" onSubmit={login}>
          <div>
            <h1>FreedomPost</h1>
            <p>管理员登录</p>
          </div>
          <label>
            <span>账号</span>
            <input value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" />
          </label>
          <label>
            <span>密码</span>
            <input
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoComplete="current-password"
              type="password"
            />
          </label>
          <button className="primary" type="submit">
            登录
          </button>
        </form>
        {toast && <div className="toast">{toast.text}</div>}
      </main>
    );
  }

  if (workspace === "products") {
    return (
      <ProductWorkspace
        products={products}
        setProducts={setProducts}
        onOpenPosts={() => setWorkspace("posts")}
        onOpenTools={openToolsWorkspace}
        onOpenDistribution={() => { setWorkspace("distribution"); void loadDistribution(); }}
        onRefresh={loadProducts}
        onLogout={logout}
        adminUsername={username}
        showToast={showToast}
        toast={toast}
      />
    );
  }

  if (workspace === "tools") {
    return <ToolWorkspace tools={tools} setTools={setTools} onOpenPosts={() => setWorkspace("posts")} onOpenProducts={openProductWorkspace} onOpenDistribution={() => { setWorkspace("distribution"); void loadDistribution(); }} onRefresh={loadTools} onLogout={logout} showToast={showToast} toast={toast} />;
  }

  if (workspace === "distribution") {
    return <DistributionWorkspace affiliates={affiliates} orders={affiliateOrders} setAffiliates={setAffiliates} setOrders={setAffiliateOrders} onOpenPosts={() => setWorkspace("posts")} onOpenProducts={openProductWorkspace} onOpenTools={openToolsWorkspace} onRefresh={loadDistribution} onLogout={logout} showToast={showToast} toast={toast} />;
  }

  if (workspace === "paid") {
    return <PaidAccessWorkspace orders={articleOrders} accounts={readerAccounts} setOrders={setArticleOrders} onOpenPosts={() => setWorkspace("posts")} onRefresh={loadPaidAccess} onLogout={logout} showToast={showToast} toast={toast} />;
  }

  return (
    <main className="admin-shell">
      <aside className="post-rail">
        <div className="workspace-tabs" role="tablist" aria-label="后台工作区">
          <button className="active" type="button" role="tab" aria-selected="true">文章</button>
          <button type="button" role="tab" aria-selected="false" onClick={openProductWorkspace}>商品</button>
          <button type="button" role="tab" aria-selected="false" onClick={openToolsWorkspace}>工具</button>
          <button type="button" role="tab" aria-selected="false" onClick={() => { setWorkspace("distribution"); void loadDistribution(); }}>分销</button>
          <button type="button" role="tab" aria-selected="false" onClick={() => { setWorkspace("paid"); void loadPaidAccess(); }}>付费</button>
        </div>
        <div className="rail-head">
          <strong>文章管理</strong>
          <button className="icon-button" type="button" onClick={createPost} title="新建文章">
            <Plus size={17} />
          </button>
        </div>
        <div className="rail-actions">
          <button type="button" onClick={loadPosts}>
            <RefreshCw size={15} />
            刷新
          </button>
          <button type="button" onClick={logout}>
            <LogOut size={15} />
            退出
          </button>
        </div>
        <div className="post-list">
          {posts.map((post) => (
            <button
              key={post.id}
              type="button"
              className={post.id === activePost?.id ? "active" : ""}
              onClick={() => setActiveId(post.id)}
            >
              <span>{post.title}</span>
              <small>
                {formatDate(post.updatedAt)} · {post.viewCount} 访问 · {post.commentCount} 评论
              </small>
            </button>
          ))}
        </div>
      </aside>

      <section className="editor-pane">
        {activePost ? (
          <>
            <header className="editor-topbar">
              <div>
                <strong>{activePost.title}</strong>
                <span>/p/{activePost.slug}</span>
              </div>
            </header>
            <div className="toolbar article-toolbar" aria-label="编辑工具栏">
              <div className="toolbar-tools">
              <label className="toolbar-field">
                <Type size={15} aria-hidden="true" />
                <select
                  aria-label="块类型"
                  defaultValue="p"
                  onChange={(event) => {
                    if (event.target.value === "callout") insertCallout();
                    else applyBlockFormat(event.target.value);
                    event.target.value = "p";
                  }}
                >
                  {headingOptions.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </select>
              </label>
              <span className="toolbar-divider" />
              <button
                className="icon-button"
                type="button"
                title="加粗"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => runEditorCommand("bold")}
              >
                <Bold size={16} />
              </button>
              <button
                className="icon-button"
                type="button"
                title="删除线"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => runEditorCommand("strikeThrough")}
              >
                <Strikethrough size={16} />
              </button>
              <button
                className="icon-button"
                type="button"
                title="斜体"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => runEditorCommand("italic")}
              >
                <Italic size={16} />
              </button>
              <button
                className="icon-button"
                type="button"
                title="下划线"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => runEditorCommand("underline")}
              >
                <Underline size={16} />
              </button>
              <button
                className="icon-button"
                type="button"
                title="插入链接"
                onMouseDown={(event) => event.preventDefault()}
                onClick={createLinkAtSelection}
              >
                <Link2 size={16} />
              </button>
              <button
                className="icon-button"
                type="button"
                title="代码块"
                onMouseDown={(event) => event.preventDefault()}
                onClick={insertCodeBlock}
              >
                <Code2 size={16} />
              </button>
              <button
                className="icon-button"
                type="button"
                title="插入 YouTube 视频"
                aria-label="插入 YouTube 视频"
                onMouseDown={(event) => event.preventDefault()}
                onClick={insertYouTubeVideo}
              >
                <Video size={17} />
              </button>
              <label className="toolbar-field">
                <span>字号</span>
                <select
                  aria-label="字号"
                  defaultValue=""
                  onChange={(event) => {
                    if (event.target.value) {
                      applyInlineClass(event.target.value);
                    }
                    event.target.value = "";
                  }}
                >
                  <option value="" disabled>
                    选择
                  </option>
                  {sizeOptions.map((option) => (
                    <option key={option.className} value={option.className}>
                      {option.label}
                    </option>
                  ))}
                </select>
              </label>
              <div className="color-group" role="group" aria-label="字体颜色">
                {colorOptions.map((option) => (
                  <button
                    key={option.className}
                    className={`swatch-button ${option.className}`}
                    type="button"
                    title={option.label}
                    aria-label={option.label}
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={() => applyInlineClass(option.className)}
                  >
                    <span />
                  </button>
                ))}
              </div>
              <span className="toolbar-divider" />
              <button
                type="button"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => attachmentInputRef.current?.click()}
              >
                <Upload size={15} />
                附件
              </button>
              <input
                ref={attachmentInputRef}
                className="hidden-input"
                type="file"
                multiple
                onChange={(event) => {
                  void handleAttachmentFiles(event.target.files);
                  event.target.value = "";
                }}
              />
              <input
                ref={imageReplacementInputRef}
                className="hidden-input"
                type="file"
                accept="image/jpeg,image/png,image/gif,image/webp,image/avif"
                onChange={(event) => {
                  const [file] = [...(event.target.files ?? [])];
                  if (file) void trackPendingMedia(replaceFailedImageWithLocalFile(file));
                  event.target.value = "";
                }}
              />
              </div>
              <div className="toolbar-actions">
                <button className="danger-button" type="button" onClick={deletePost}>
                  <Trash2 size={15} />
                  删除
                </button>
                <button className="primary" type="button" onClick={savePost} disabled={isSavingPost}>
                  <Save size={15} />
                  {isSavingPost
                    ? "保存中…"
                    : pendingMediaCount > 0
                      ? `等待媒体（${pendingMediaCount}）`
                      : "保存"}
                </button>
              </div>
            </div>
            <div className="editor-workspace">
              <label className="title-field">
                <span>标题</span>
                <input value={activePost.title} onChange={(event) => patchActivePost({ title: event.target.value })} />
              </label>
              <label className="post-visibility-field">
                <span>文章可见性</span>
                <select value={activePost.visibility} onChange={(event) => patchActivePost({ visibility: event.target.value as AdminPost["visibility"] })}>
                  <option value="public">公开，所有人可见</option>
                  <option value="private">私密，仅自己可见</option>
                  <option value="paid">付费，购买后可见</option>
                </select>
              </label>
              {activePost.visibility === "paid" && (
                <label className="post-visibility-field">
                  <span>文章价格（人民币元）</span>
                  <input type="number" min="0.01" max="1000000" step="0.01" value={(activePost.priceCents / 100).toFixed(2)} onChange={(event) => patchActivePost({ priceCents: Math.round(Number(event.target.value) * 100), currency: "CNY" })} />
                </label>
              )}
              <div
                ref={editorRef}
                className="rich-editor"
                contentEditable
                suppressContentEditableWarning
                role="textbox"
                aria-label="文章正文"
                onInput={syncEditorMarkdown}
                onPaste={handleEditorPaste}
                onClick={handleEditorClick}
                onKeyDown={handleEditorKeyDown}
              />
            </div>
          </>
        ) : (
          <div className="empty-state">暂无文章</div>
        )}
      </section>
      {toast && <div className="toast">{toast.text}</div>}
    </main>
  );
}

export function ProductWorkspace({
  products,
  setProducts,
  onOpenPosts,
  onOpenDistribution,
  onOpenTools,
  onRefresh,
  onLogout,
  adminUsername,
  showToast,
  toast
}: {
  products: AdminProduct[];
  setProducts: (next: AdminProduct[] | ((items: AdminProduct[]) => AdminProduct[])) => void;
  onOpenPosts: () => void;
  onOpenDistribution: () => void;
  onOpenTools: () => void;
  onRefresh: () => Promise<void>;
  onLogout: () => Promise<void>;
  adminUsername: string;
  showToast: (text: string) => void;
  toast: Toast | null;
}) {
  const [activeId, setActiveId] = useState<string | null>(null);
  const [productFailure, setProductFailure] = useState<{ operation: ProductOperation; details: ProductSaveFailure } | null>(null);
  const [isCreatingProduct, setCreatingProduct] = useState(false);
  const [isSavingProduct, setSavingProduct] = useState(false);
  const [isReauthenticationOpen, setReauthenticationOpen] = useState(false);
  const [reauthUsername, setReauthUsername] = useState(adminUsername);
  const [reauthPassword, setReauthPassword] = useState("");
  const [reauthError, setReauthError] = useState<string | null>(null);
  const [isReauthenticating, setReauthenticating] = useState(false);
  const coverInputRef = useRef<HTMLInputElement | null>(null);
  const productFailureRef = useRef<HTMLDivElement | null>(null);
  const saveProductButtonRef = useRef<HTMLButtonElement | null>(null);
  const reauthDialogRef = useRef<HTMLElement | null>(null);
  const reauthPasswordRef = useRef<HTMLInputElement | null>(null);
  const reauthReturnFocusRef = useRef<HTMLElement | null>(null);
  const productFieldRefs = useRef<Partial<Record<ProductField, HTMLElement>>>({});
  const creatingProductRef = useRef(false);
  const savingProductRef = useRef(false);
  const reauthenticatingRef = useRef(false);
  const activeProduct = useMemo(() => products.find((product) => product.id === activeId) ?? products[0], [products, activeId]);

  useEffect(() => {
    setActiveId((current) => current ?? products[0]?.id ?? null);
  }, [products]);

  useEffect(() => {
    setProductFailure(null);
  }, [activeProduct?.id]);

  useEffect(() => {
    setReauthUsername(adminUsername);
  }, [adminUsername]);

  useEffect(() => {
    if (!isReauthenticationOpen) return;
    window.setTimeout(() => reauthPasswordRef.current?.focus(), 0);
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape" && !reauthenticatingRef.current) {
        event.preventDefault();
        closeReauthentication();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = [...(reauthDialogRef.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [tabindex]:not([tabindex="-1"])') ?? [])];
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [isReauthenticationOpen]);

  function patchProduct(patch: Partial<AdminProduct>) {
    if (!activeProduct) return;
    const updated = { ...activeProduct, ...patch };
    setProducts((items) => items.map((item) => (item.id === activeProduct.id ? updated : item)));
    if (productFailure?.details.code === "INVALID_PRODUCT") {
      const issues = validateProductForSave(updated);
      setProductFailure(issues.length > 0 ? { operation: "save", details: failureFromProductIssues(issues, "save") } : null);
    }
  }

  async function createProduct() {
    if (creatingProductRef.current) return;
    creatingProductRef.current = true;
    setCreatingProduct(true);
    try {
      const response = await fetch("/api/admin/products", {
        method: "POST",
        headers: { "content-type": "application/json" },
        credentials: "include",
        body: JSON.stringify(defaultProductPayload())
      });
      if (!response.ok) {
        presentProductFailure(await readProductApiFailure(response, "create"), "create");
        return;
      }
      const created = await readAdminProductResponse(response);
      if (!created) {
        presentProductFailure(invalidProductResponseFailure("create", response), "create");
        return;
      }
      setProductFailure(null);
      setProducts((items) => [created, ...items]);
      setActiveId(created.id);
      showToast("商品草稿已创建");
    } catch {
      presentProductFailure(networkProductFailure("create"), "create");
    } finally {
      creatingProductRef.current = false;
      setCreatingProduct(false);
    }
  }

  async function saveProduct() {
    if (!activeProduct || savingProductRef.current) return;
    const issues = validateProductForSave(activeProduct);
    if (issues.length > 0) {
      presentProductFailure(failureFromProductIssues(issues, "save"), "save");
      return;
    }

    savingProductRef.current = true;
    setSavingProduct(true);
    try {
      const response = await fetch(`/api/admin/products/${activeProduct.id}`, {
        method: "PUT",
        headers: { "content-type": "application/json" },
        credentials: "include",
        body: JSON.stringify(productPayload(activeProduct))
      });
      if (!response.ok) {
        presentProductFailure(await readProductApiFailure(response, "save"), "save");
        return;
      }
      const saved = await readAdminProductResponse(response);
      if (!saved) {
        presentProductFailure(invalidProductResponseFailure("save", response), "save");
        return;
      }
      setProductFailure(null);
      setProducts((items) => items.map((item) => (item.id === saved.id ? saved : item)));
      showToast(saved.status === "published" ? "商品已发布" : "商品草稿已保存");
    } catch {
      presentProductFailure(networkProductFailure("save"), "save");
    } finally {
      savingProductRef.current = false;
      setSavingProduct(false);
    }
  }

  function presentProductFailure(details: ProductSaveFailure, operation: ProductOperation) {
    setProductFailure({ operation, details });
    showToast(`${operation === "create" ? "创建" : "保存"}失败，请查看具体原因`);
    if (details.requiresReauthentication) {
      openReauthentication();
      return;
    }
    window.setTimeout(() => {
      const firstField = details.issues[0]?.field;
      ((firstField && productFieldRefs.current[firstField]) || productFailureRef.current)?.focus();
    }, 0);
  }

  async function reauthenticate(event: FormEvent) {
    event.preventDefault();
    if (reauthenticatingRef.current) return;
    reauthenticatingRef.current = true;
    setReauthenticating(true);
    setReauthError(null);
    try {
      const response = await fetch("/api/admin/login", {
        method: "POST",
        headers: { "content-type": "application/json" },
        credentials: "include",
        body: JSON.stringify({ username: reauthUsername, password: reauthPassword })
      });
      if (!response.ok) {
        setReauthError(await readReauthenticationFailure(response));
        return;
      }
      setReauthenticationOpen(false);
      setReauthPassword("");
      setProductFailure(null);
      showToast("登录已恢复，请再次保存商品");
      restoreReauthenticationFocus();
    } catch {
      setReauthError("无法连接服务器，请检查网络后重试");
    } finally {
      reauthenticatingRef.current = false;
      setReauthenticating(false);
    }
  }

  function openReauthentication() {
    if (!isReauthenticationOpen) {
      reauthReturnFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
    setReauthError(null);
    setReauthPassword("");
    setReauthenticationOpen(true);
  }

  function closeReauthentication() {
    setReauthenticationOpen(false);
    setReauthPassword("");
    setReauthError(null);
    restoreReauthenticationFocus();
  }

  function restoreReauthenticationFocus() {
    window.setTimeout(() => {
      const target = reauthReturnFocusRef.current?.isConnected ? reauthReturnFocusRef.current : saveProductButtonRef.current;
      target?.focus();
      reauthReturnFocusRef.current = null;
    }, 0);
  }

  async function copyRequestId() {
    const requestId = productFailure?.details.requestId;
    if (!requestId) return;
    try {
      await navigator.clipboard.writeText(requestId);
      showToast("错误编号已复制");
    } catch {
      showToast(`请手动复制错误编号：${requestId}`);
    }
  }

  function rememberProductField(field: ProductField, element: HTMLElement | null) {
    if (element) productFieldRefs.current[field] = element;
    else delete productFieldRefs.current[field];
  }

  function issueFor(field: ProductField): ProductIssue | undefined {
    return productFailure?.details.issues.find((issue) => issue.field === field);
  }

  function fieldErrorId(field: ProductField): string | undefined {
    return issueFor(field) ? `product-${field}-error` : undefined;
  }

  function renderFieldError(field: ProductField) {
    const issue = issueFor(field);
    if (!issue) return null;
    return <small className="product-field-error" id={`product-${field}-error`}>{issue.message}；{issue.resolution}</small>;
  }

  function renderProductFailure() {
    if (!productFailure) return null;
    const { details, operation } = productFailure;
    return (
      <div className="product-save-failure" ref={productFailureRef} role="alert" aria-live="assertive" tabIndex={-1}>
        <div className="product-save-failure-heading">
          <CircleAlert size={20} aria-hidden="true" />
          <div><strong>{details.title}</strong><span>{details.code}</span></div>
        </div>
        <p><strong>失败原因：</strong>{details.reason}</p>
        <p><strong>解决办法：</strong>{details.solution}</p>
        {details.issues.length > 0 && (
          <ul className="product-issue-list">
            {details.issues.map((issue) => (
              <li key={`${issue.field}-${issue.code}`}>
                <button type="button" onClick={() => (productFieldRefs.current[issue.field] ?? productFailureRef.current)?.focus()}>
                  <strong>{issue.message}</strong><span>{issue.resolution}</span>
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className="product-save-failure-actions">
          {details.requiresReauthentication && <button type="button" onClick={openReauthentication}><KeyRound size={15} />重新登录</button>}
          {details.retryable && <button type="button" onClick={() => void (operation === "create" ? createProduct() : saveProduct())}><RefreshCw size={15} />重试</button>}
          {details.requestId && <button type="button" onClick={() => void copyRequestId()}><Copy size={15} />复制错误编号 <code>{details.requestId}</code></button>}
        </div>
      </div>
    );
  }

  async function deleteProduct() {
    if (!activeProduct || !confirm(`确认删除商品《${activeProduct.title}》？`)) return;
    const response = await fetch(`/api/admin/products/${activeProduct.id}`, { method: "DELETE", credentials: "include" });
    if (!response.ok) return showToast("删除失败");
    setProducts((items) => items.filter((item) => item.id !== activeProduct.id));
    setActiveId(null);
    showToast("商品已删除");
  }

  async function uploadCover(files: FileList | null) {
    const file = files?.[0];
    if (!file) return;
    if (!file.type.startsWith("image/")) return showToast("封面只能使用图片文件");
    try {
      const uploaded = await uploadFile(file);
      patchProduct({ coverUrl: uploaded.url });
      showToast("封面已上传，保存商品后生效");
    } catch {
      showToast("封面上传失败");
    }
  }

  return (
    <main className="admin-shell">
      <aside className="post-rail">
        <div className="workspace-tabs" role="tablist" aria-label="后台工作区">
          <button type="button" role="tab" aria-selected="false" onClick={onOpenPosts}>文章</button>
          <button className="active" type="button" role="tab" aria-selected="true">商品</button>
          <button type="button" role="tab" aria-selected="false" onClick={onOpenTools}>工具</button>
          <button type="button" role="tab" aria-selected="false" onClick={onOpenDistribution}>分销</button>
        </div>
        <div className="rail-head">
          <strong>商品管理</strong>
          <button className="icon-button" type="button" onClick={() => void createProduct()} title="新建商品" aria-label="新建商品" disabled={isCreatingProduct}>{isCreatingProduct ? <LoaderCircle className="spin" size={17} /> : <Plus size={17} />}</button>
        </div>
        <div className="rail-actions">
          <button type="button" onClick={() => void onRefresh()}><RefreshCw size={15} />刷新</button>
          <button type="button" onClick={() => void onLogout()}><LogOut size={15} />退出</button>
        </div>
        <div className="post-list product-list">
          {products.map((product) => (
            <button key={product.id} type="button" className={product.id === activeProduct?.id ? "active" : ""} onClick={() => setActiveId(product.id)}>
              <span>{product.title}</span>
              <small>{product.status === "published" ? "已发布" : "草稿"} · {formatMoney(product.priceCents, product.currency)}</small>
            </button>
          ))}
        </div>
      </aside>

      <section className="editor-pane product-editor-pane">
        {activeProduct ? (
          <>
            <header className="editor-topbar"><div><strong>{activeProduct.title}</strong><span>/market/{activeProduct.slug}</span></div></header>
            <div className="editor-workspace product-workspace">
              <div className="product-form-grid">
                <label className="title-field product-title-field"><span>商品名称</span><input ref={(element) => rememberProductField("title", element)} required maxLength={120} aria-invalid={Boolean(issueFor("title"))} aria-describedby={fieldErrorId("title")} value={activeProduct.title} onChange={(event) => patchProduct({ title: event.target.value })} />{renderFieldError("title")}</label>
                <label><span>分类</span><select ref={(element) => rememberProductField("category", element)} aria-invalid={Boolean(issueFor("category"))} aria-describedby={fieldErrorId("category")} value={activeProduct.category} onChange={(event) => patchProduct({ category: event.target.value })}><option value="service">服务</option><option value="digital">数字内容</option><option value="software">软件工具</option><option value="other">其它</option></select>{renderFieldError("category")}</label>
                <label><span>价格</span><input ref={(element) => rememberProductField("priceCents", element)} type="number" min="0" max="1000000" step="0.01" aria-invalid={Boolean(issueFor("priceCents"))} aria-describedby={fieldErrorId("priceCents")} value={formatPriceInput(activeProduct.priceCents)} onChange={(event) => patchProduct({ priceCents: priceToCents(event.target.value) })} />{renderFieldError("priceCents")}</label>
                <label><span>分销佣金</span><input ref={(element) => rememberProductField("commissionCents", element)} type="number" min="0" max="1000000" step="0.01" aria-invalid={Boolean(issueFor("commissionCents"))} aria-describedby={fieldErrorId("commissionCents")} value={formatPriceInput(activeProduct.commissionCents)} onChange={(event) => patchProduct({ commissionCents: priceToCents(event.target.value) })} /><small>每笔确认成交订单的佣金</small>{renderFieldError("commissionCents")}</label>
                <label><span>划线价（可选）</span><input ref={(element) => rememberProductField("compareAtCents", element)} type="number" min="0" max="1000000" step="0.01" aria-invalid={Boolean(issueFor("compareAtCents"))} aria-describedby={fieldErrorId("compareAtCents")} value={activeProduct.compareAtCents === null ? "" : formatPriceInput(activeProduct.compareAtCents)} onChange={(event) => patchProduct({ compareAtCents: event.target.value ? priceToCents(event.target.value) : null })} />{renderFieldError("compareAtCents")}</label>
                <label><span>币种</span><select ref={(element) => rememberProductField("currency", element)} aria-invalid={Boolean(issueFor("currency"))} aria-describedby={fieldErrorId("currency")} value={activeProduct.currency} onChange={(event) => patchProduct({ currency: event.target.value })}><option value="CNY">CNY</option><option value="USD">USD</option></select>{renderFieldError("currency")}</label>
                <label><span>库存</span><input ref={(element) => rememberProductField("stock", element)} type="number" min="-1" max="1000000" step="1" aria-invalid={Boolean(issueFor("stock"))} aria-describedby={fieldErrorId("stock")} value={activeProduct.stock} onChange={(event) => patchProduct({ stock: numberInputValue(event.target.value) })} /><small>-1 表示不限量</small>{renderFieldError("stock")}</label>
                <label><span>已售出</span><input ref={(element) => rememberProductField("soldCount", element)} type="number" min="0" max="1000000" step="1" aria-invalid={Boolean(issueFor("soldCount"))} aria-describedby={fieldErrorId("soldCount")} value={activeProduct.soldCount} onChange={(event) => patchProduct({ soldCount: numberInputValue(event.target.value) })} /><small>商城展示的累计销量</small>{renderFieldError("soldCount")}</label>
                <label><span>排序</span><input ref={(element) => rememberProductField("sortOrder", element)} type="number" min="-100000" max="100000" step="1" aria-invalid={Boolean(issueFor("sortOrder"))} aria-describedby={fieldErrorId("sortOrder")} value={activeProduct.sortOrder} onChange={(event) => patchProduct({ sortOrder: numberInputValue(event.target.value) })} />{renderFieldError("sortOrder")}</label>
                <label><span>发布状态</span><select ref={(element) => rememberProductField("status", element)} aria-invalid={Boolean(issueFor("status"))} aria-describedby={fieldErrorId("status")} value={activeProduct.status} onChange={(event) => patchProduct({ status: event.target.value as AdminProduct["status"] })}><option value="draft">草稿</option><option value="published">公开发布</option></select>{renderFieldError("status")}</label>
              </div>
              <label className="wide-field"><span>一句话简介</span><input ref={(element) => rememberProductField("summary", element)} required maxLength={500} aria-invalid={Boolean(issueFor("summary"))} aria-describedby={fieldErrorId("summary")} value={activeProduct.summary} onChange={(event) => patchProduct({ summary: event.target.value })} />{renderFieldError("summary")}</label>
              <div className={`product-cover-row${issueFor("coverUrl") ? " invalid" : ""}`}>
                <div className="product-cover-preview">{activeProduct.coverUrl ? <img src={activeProduct.coverUrl} alt="商品封面" /> : <Package size={30} />}</div>
                <div><strong>商品封面</strong><p>上传后的图片存入现有 R2 存储。</p><button ref={(element) => rememberProductField("coverUrl", element)} type="button" aria-invalid={Boolean(issueFor("coverUrl"))} aria-describedby={fieldErrorId("coverUrl")} onClick={() => coverInputRef.current?.click()}><ImagePlus size={15} />上传封面</button>{renderFieldError("coverUrl")}<input ref={coverInputRef} className="hidden-input" type="file" accept="image/*" onChange={(event) => { void uploadCover(event.target.files); event.target.value = ""; }} /></div>
              </div>
              <label className="wide-field"><span>商品详情</span><textarea ref={(element) => rememberProductField("description", element)} required rows={12} maxLength={12000} aria-invalid={Boolean(issueFor("description"))} aria-describedby={fieldErrorId("description")} value={activeProduct.description} onChange={(event) => patchProduct({ description: event.target.value })} />{renderFieldError("description")}</label>
            </div>
            {renderProductFailure()}
            <div className="toolbar product-toolbar"><span className="product-status-note">{activeProduct.status === "published" ? "公开页可见" : "仅后台可见"}</span><span className="toolbar-fill" /><button className="danger-button" type="button" onClick={() => void deleteProduct()} disabled={isSavingProduct}><Trash2 size={15} />删除</button><button ref={saveProductButtonRef} className="primary" type="button" onClick={() => void saveProduct()} disabled={isSavingProduct}>{isSavingProduct ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}{isSavingProduct ? "保存中…" : "保存"}</button></div>
          </>
        ) : <><div className="empty-state">暂无商品，点击左上角加号创建第一个商品。</div>{renderProductFailure()}</>}
      </section>
      {isReauthenticationOpen && (
        <div className="reauth-backdrop">
          <section ref={reauthDialogRef} className="reauth-dialog" role="dialog" aria-modal="true" aria-labelledby="reauth-title" aria-describedby="reauth-description">
            <div className="reauth-heading"><KeyRound size={22} aria-hidden="true" /><div><h2 id="reauth-title">重新登录</h2><p id="reauth-description">登录状态已过期。重新登录不会刷新页面或清除未保存的商品内容。</p></div></div>
            <form onSubmit={reauthenticate}>
              <label><span>用户名</span><input autoComplete="username" value={reauthUsername} onChange={(event) => setReauthUsername(event.target.value)} /></label>
              <label><span>密码</span><input ref={reauthPasswordRef} type="password" autoComplete="current-password" value={reauthPassword} onChange={(event) => setReauthPassword(event.target.value)} /></label>
              {reauthError && <p className="reauth-error" role="alert">{reauthError}</p>}
              <div className="reauth-actions"><button type="button" onClick={closeReauthentication} disabled={isReauthenticating}>稍后处理</button><button className="primary" type="submit" disabled={isReauthenticating || !reauthUsername.trim() || !reauthPassword}>{isReauthenticating ? <LoaderCircle className="spin" size={15} /> : <KeyRound size={15} />}{isReauthenticating ? "登录中…" : "重新登录"}</button></div>
            </form>
          </section>
        </div>
      )}
      {toast && <div className="toast" role="status" aria-live="polite">{toast.text}</div>}
    </main>
  );
}

function defaultProductPayload(): Omit<AdminProduct, "id" | "slug" | "createdAt" | "updatedAt"> {
  return { title: "未命名商品", summary: "请填写商品简介", description: "请填写商品详情", category: "service", priceCents: 0, commissionCents: 0, compareAtCents: null, currency: "CNY", stock: -1, soldCount: 0, coverUrl: null, linkUrl: "", status: "draft", sortOrder: 0 };
}

function ToolWorkspace({
  tools,
  setTools,
  onOpenPosts,
  onOpenProducts,
  onOpenDistribution,
  onRefresh,
  onLogout,
  showToast,
  toast
}: {
  tools: AdminTool[];
  setTools: (next: AdminTool[] | ((items: AdminTool[]) => AdminTool[])) => void;
  onOpenPosts: () => void;
  onOpenProducts: () => void;
  onOpenDistribution: () => void;
  onRefresh: () => Promise<void>;
  onLogout: () => Promise<void>;
  showToast: (text: string) => void;
  toast: Toast | null;
}) {
  const [activeId, setActiveId] = useState<string | null>(null);
  const coverInputRef = useRef<HTMLInputElement | null>(null);
  const activeTool = useMemo(() => tools.find((tool) => tool.id === activeId) ?? tools[0], [tools, activeId]);

  useEffect(() => {
    setActiveId((current) => current ?? tools[0]?.id ?? null);
  }, [tools]);

  function patchTool(patch: Partial<AdminTool>) {
    if (!activeTool) return;
    setTools((items) => items.map((item) => item.id === activeTool.id ? { ...item, ...patch } : item));
  }

  async function createTool() {
    const response = await fetch("/api/admin/tools", { method: "POST", headers: { "content-type": "application/json" }, credentials: "include", body: JSON.stringify(defaultToolPayload()) });
    if (!response.ok) return showToast(await apiErrorMessage(response, "创建工具失败"));
    const created = await response.json() as AdminTool;
    setTools((items) => [created, ...items]);
    setActiveId(created.id);
    showToast("工具草稿已创建");
  }

  async function saveTool() {
    if (!activeTool) return;
    if (activeTool.url.trim() && !isHttpUrl(activeTool.url)) return showToast("官网地址需要以 http:// 或 https:// 开头");
    if (activeTool.status === "published" && !activeTool.summary.trim()) return showToast("公开发布前，请填写一句话简介");
    if (activeTool.status === "published" && !activeTool.url.trim()) return showToast("公开发布前，请填写官网地址");
    const response = await fetch(`/api/admin/tools/${activeTool.id}`, { method: "PUT", headers: { "content-type": "application/json" }, credentials: "include", body: JSON.stringify(toolPayload(activeTool)) });
    if (!response.ok) return showToast(await apiErrorMessage(response, "保存失败，请检查工具信息"));
    const saved = await response.json() as AdminTool;
    setTools((items) => items.map((item) => item.id === saved.id ? saved : item));
    showToast(saved.status === "published" ? "工具已发布" : "工具草稿已保存");
  }

  async function deleteTool() {
    if (!activeTool || !confirm(`确认删除工具《${activeTool.title}》？`)) return;
    const response = await fetch(`/api/admin/tools/${activeTool.id}`, { method: "DELETE", credentials: "include" });
    if (!response.ok) return showToast("删除失败");
    setTools((items) => items.filter((item) => item.id !== activeTool.id));
    setActiveId(null);
    showToast("工具已删除");
  }

  async function uploadCover(files: FileList | null) {
    const file = files?.[0];
    if (!file) return;
    if (!file.type.startsWith("image/")) return showToast("Logo 只能使用图片文件");
    try {
      const uploaded = await uploadFile(file);
      patchTool({ coverUrl: uploaded.url });
      showToast("Logo 已上传，保存工具后生效");
    } catch {
      showToast("Logo 上传失败");
    }
  }

  return (
    <main className="admin-shell">
      <aside className="post-rail">
        <div className="workspace-tabs" role="tablist" aria-label="后台工作区">
          <button type="button" role="tab" aria-selected="false" onClick={onOpenPosts}>文章</button>
          <button type="button" role="tab" aria-selected="false" onClick={onOpenProducts}>商品</button>
          <button className="active" type="button" role="tab" aria-selected="true">工具</button>
          <button type="button" role="tab" aria-selected="false" onClick={onOpenDistribution}>分销</button>
        </div>
        <div className="rail-head"><strong>工具管理</strong><button className="icon-button" type="button" onClick={createTool} title="新建工具"><Plus size={17} /></button></div>
        <div className="rail-actions"><button type="button" onClick={() => void onRefresh()}><RefreshCw size={15} />刷新</button><button type="button" onClick={() => void onLogout()}><LogOut size={15} />退出</button></div>
        <div className="post-list product-list">{tools.map((tool) => <button key={tool.id} type="button" className={tool.id === activeTool?.id ? "active" : ""} onClick={() => setActiveId(tool.id)}><span>{tool.title}</span><small>{tool.status === "published" ? "已发布" : "草稿"} · {tool.category}</small></button>)}</div>
      </aside>
      <section className="editor-pane product-editor-pane">
        {activeTool ? <>
          <header className="editor-topbar"><div><strong>{activeTool.title}</strong><span>/tools/{activeTool.slug}</span></div></header>
          <div className="editor-workspace product-workspace">
            <div className="product-form-grid">
              <label className="title-field product-title-field"><span>工具名称</span><input value={activeTool.title} onChange={(event) => patchTool({ title: event.target.value })} /></label>
              <label><span>分类</span><select value={activeTool.category} onChange={(event) => patchTool({ category: event.target.value })}><option value="ai">AI</option><option value="writing">写作</option><option value="design">设计</option><option value="productivity">效率</option><option value="development">开发</option><option value="other">其它</option></select></label>
              <label><span>排序</span><input type="number" value={activeTool.sortOrder} onChange={(event) => patchTool({ sortOrder: Number(event.target.value) || 0 })} /></label>
              <label><span>发布状态</span><select value={activeTool.status} onChange={(event) => patchTool({ status: event.target.value as AdminTool["status"] })}><option value="draft">草稿</option><option value="published">公开发布</option></select></label>
            </div>
            <label className="wide-field"><span>官网地址</span><input type="url" maxLength={2000} value={activeTool.url} onChange={(event) => patchTool({ url: event.target.value })} placeholder="https://example.com" /></label>
            <label className="wide-field"><span>这是什么（一句话）</span><input maxLength={500} value={activeTool.summary} onChange={(event) => patchTool({ summary: event.target.value })} placeholder="例如：把零散灵感整理成文章大纲的 AI 写作工具" /></label>
            <div className="product-cover-row tool-logo-row"><div className="product-cover-preview tool-logo-preview">{activeTool.coverUrl ? <img src={activeTool.coverUrl} alt={`${activeTool.title} Logo`} /> : <Package size={30} />}</div><div><strong>品牌 Logo</strong><p>可选，推荐 1:1 方形 PNG 或 WebP，前台卡片会自动裁切。</p><button type="button" onClick={() => coverInputRef.current?.click()}><ImagePlus size={15} />上传 Logo</button><input ref={coverInputRef} className="hidden-input" type="file" accept="image/*" onChange={(event) => { void uploadCover(event.target.files); event.target.value = ""; }} /></div></div>
            <label className="wide-field"><span>我的使用说明（可选）</span><textarea rows={8} maxLength={12000} value={activeTool.description} onChange={(event) => patchTool({ description: event.target.value })} placeholder="可以写你用它解决什么问题、适合谁，以及你真实使用后的感受。" /></label>
          </div>
          <div className="toolbar product-toolbar"><span className="product-status-note">{activeTool.status === "published" ? "公开页可见" : "仅后台可见"}</span><span className="toolbar-fill" /><button className="danger-button" type="button" onClick={() => void deleteTool()}><Trash2 size={15} />删除</button><button className="primary" type="button" onClick={() => void saveTool()}><Save size={15} />保存</button></div>
        </> : <div className="empty-state">暂无工具，点击左上角加号创建第一个工具。</div>}
      </section>
      {toast && <div className="toast">{toast.text}</div>}
    </main>
  );
}

function defaultToolPayload(): Omit<AdminTool, "id" | "slug" | "createdAt" | "updatedAt"> {
  return { title: "未命名工具", summary: "", description: "", category: "other", url: "", coverUrl: null, status: "draft", sortOrder: 0 };
}

function toolPayload(tool: AdminTool) {
  const { id: _id, slug: _slug, createdAt: _createdAt, updatedAt: _updatedAt, ...payload } = tool;
  return payload;
}

function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

async function apiErrorMessage(response: Response, fallback: string): Promise<string> {
  const payload = await response.json().catch(() => null) as { error?: { message?: string } } | null;
  return payload?.error?.message || `${fallback}（${response.status}）`;
}

function PaidAccessWorkspace({
  orders,
  accounts,
  setOrders,
  onOpenPosts,
  onRefresh,
  onLogout,
  showToast,
  toast
}: {
  orders: AdminArticleOrder[];
  accounts: AdminReaderAccount[];
  setOrders: React.Dispatch<React.SetStateAction<AdminArticleOrder[]>>;
  onOpenPosts: () => void;
  onRefresh: () => Promise<void>;
  onLogout: () => Promise<void>;
  showToast: (text: string) => void;
  toast: Toast | null;
}) {
  async function updateOrder(order: AdminArticleOrder, status: AdminArticleOrder["status"]) {
    const response = await fetch(`/api/admin/article-orders/${order.id}`, {
      method: "PATCH",
      headers: { "content-type": "application/json" },
      credentials: "include",
      body: JSON.stringify({ status })
    });
    if (!response.ok) { showToast("订单状态更新失败"); return; }
    const payload = (await response.json()) as { order: AdminArticleOrder };
    setOrders((items) => items.map((item) => item.id === order.id ? payload.order : item));
    showToast(status === "completed" ? "已确认收款并开通阅读权限" : "订单状态已更新");
  }

  async function resetReaderPassword(account: AdminReaderAccount) {
    if (!confirm(`确认重置 ${account.loginName} 的密码并撤销全部登录态？`)) return;
    const response = await fetch(`/api/admin/reader-accounts/${account.id}/reset-password`, { method: "POST", credentials: "include" });
    if (!response.ok) { showToast("密码重置失败"); return; }
    const payload = (await response.json()) as { temporaryPassword: string };
    prompt("新密码只显示这一次，请安全发送给用户：", payload.temporaryPassword);
  }

  return (
    <main className="admin-shell">
      <aside className="post-rail">
        <div className="workspace-tabs" role="tablist" aria-label="后台工作区"><button type="button" onClick={onOpenPosts}>文章</button><button className="active" type="button">付费</button></div>
        <div className="rail-head"><strong>读者账号</strong><span>{accounts.length} 人</span></div>
        <div className="rail-actions"><button type="button" onClick={() => void onRefresh()}><RefreshCw size={15} />刷新</button><button type="button" onClick={() => void onLogout()}><LogOut size={15} />退出</button></div>
        <div className="post-list affiliate-list">{accounts.map((account) => <button key={account.id} type="button" onClick={() => void resetReaderPassword(account)}><span>{account.loginName}</span><small>{formatDate(account.createdAt)} · 点击重置密码</small></button>)}</div>
      </aside>
      <section className="management-pane">
        <header className="management-header"><div><p className="section-kicker">Paid access</p><h1>文章购买订单</h1></div><span>{orders.filter((order) => order.status === "pending").length} 个待确认</span></header>
        <div className="distribution-summary"><div><span>订单总数</span><strong>{orders.length}</strong></div><div><span>待确认</span><strong>{orders.filter((order) => order.status === "pending").length}</strong></div><div><span>已开通</span><strong>{orders.filter((order) => order.status === "completed").length}</strong></div><div><span>成交金额</span><strong>{formatMoney(orders.filter((order) => order.status === "completed").reduce((sum, order) => sum + order.priceCents, 0), "CNY")}</strong></div></div>
        <section className="distribution-orders"><div className="distribution-table-wrap"><table><thead><tr><th>订单号 / 时间</th><th>账号</th><th>文章</th><th>价格</th><th>状态</th></tr></thead><tbody>{orders.map((order) => <tr key={order.id}><td><strong>{order.orderCode}</strong><small>{formatDate(order.createdAt)}</small></td><td>{order.loginName}</td><td>{order.postTitle}</td><td>{formatMoney(order.priceCents, order.currency)}</td><td><select value={order.status} disabled={order.status === "completed"} onChange={(event) => void updateOrder(order, event.target.value as AdminArticleOrder["status"])}><option value="pending">待收款</option><option value="completed">已收款并开通</option><option value="canceled">已取消</option></select></td></tr>)}</tbody></table>{orders.length === 0 && <p className="empty-table">暂无文章订单</p>}</div></section>
      </section>
      {toast && <div className="toast">{toast.text}</div>}
    </main>
  );
}

function DistributionWorkspace({
  affiliates,
  orders,
  setAffiliates,
  setOrders,
  onOpenPosts,
  onOpenProducts,
  onOpenTools,
  onRefresh,
  onLogout,
  showToast,
  toast
}: {
  affiliates: AdminAffiliate[];
  orders: AdminAffiliateOrder[];
  setAffiliates: (next: AdminAffiliate[] | ((items: AdminAffiliate[]) => AdminAffiliate[])) => void;
  setOrders: (next: AdminAffiliateOrder[] | ((items: AdminAffiliateOrder[]) => AdminAffiliateOrder[])) => void;
  onOpenPosts: () => void;
  onOpenProducts: () => void;
  onOpenTools: () => void;
  onRefresh: () => Promise<void>;
  onLogout: () => Promise<void>;
  showToast: (text: string) => void;
  toast: Toast | null;
}) {
  const [activeAffiliateId, setActiveAffiliateId] = useState<string | null>(null);
  const activeAffiliate = affiliates.find((affiliate) => affiliate.id === activeAffiliateId) ?? null;
  const visibleOrders = activeAffiliate
    ? orders.filter((order) => order.affiliateWechatId === activeAffiliate.wechatId)
    : orders;

  async function updateAffiliateStatus(affiliate: AdminAffiliate) {
    const status = affiliate.status === "active" ? "disabled" : "active";
    const response = await fetch(`/api/admin/affiliates/${affiliate.id}`, {
      method: "PATCH",
      headers: { "content-type": "application/json" },
      credentials: "include",
      body: JSON.stringify({ status })
    });
    if (!response.ok) return showToast("推广者状态更新失败");
    setAffiliates((items) => items.map((item) => item.id === affiliate.id ? { ...item, status } : item));
    showToast(status === "active" ? "推广资格已恢复" : "推广资格已停用");
  }

  async function resetAffiliatePassword(affiliate: AdminAffiliate) {
    const response = await fetch(`/api/admin/affiliates/${affiliate.id}/reset-password`, { method: "POST", credentials: "include" });
    if (!response.ok) return showToast("查询密码重置失败");
    const { queryPassword } = await response.json() as { queryPassword: string };
    await navigator.clipboard.writeText(queryPassword);
    showToast(`新查询密码 ${queryPassword} 已复制`);
  }

  async function updateOrder(order: AdminAffiliateOrder, patch: Partial<Pick<AdminAffiliateOrder, "orderStatus" | "commissionStatus">>) {
    const next = { ...order, ...patch };
    if (next.orderStatus !== "completed") next.commissionStatus = "not_due";
    if (next.orderStatus === "completed" && next.commissionStatus === "not_due") next.commissionStatus = "pending";
    const response = await fetch(`/api/admin/affiliate-orders/${order.id}`, {
      method: "PATCH",
      headers: { "content-type": "application/json" },
      credentials: "include",
      body: JSON.stringify({ orderStatus: next.orderStatus, commissionStatus: next.commissionStatus })
    });
    if (!response.ok) return showToast("订单状态更新失败");
    const saved = await response.json() as AdminAffiliateOrder;
    setOrders((items) => items.map((item) => item.id === saved.id ? saved : item));
    showToast("订单状态已更新");
  }

  const completedTotal = orders.filter((order) => order.orderStatus === "completed").reduce((sum, order) => sum + order.priceCents, 0);
  const pendingCommission = orders.filter((order) => order.commissionStatus === "pending").reduce((sum, order) => sum + order.commissionCents, 0);

  return (
    <main className="admin-shell distribution-shell">
      <aside className="post-rail">
        <div className="workspace-tabs" role="tablist" aria-label="后台工作区">
          <button type="button" role="tab" aria-selected="false" onClick={onOpenPosts}>文章</button>
          <button type="button" role="tab" aria-selected="false" onClick={onOpenProducts}>商品</button>
          <button type="button" role="tab" aria-selected="false" onClick={onOpenTools}>工具</button>
          <button className="active" type="button" role="tab" aria-selected="true">分销</button>
        </div>
        <div className="rail-head"><strong>推广者</strong><span>{affiliates.length} 人</span></div>
        <div className="rail-actions"><button type="button" onClick={() => void onRefresh()}><RefreshCw size={15} />刷新</button><button type="button" onClick={() => void onLogout()}><LogOut size={15} />退出</button></div>
        <div className="post-list affiliate-list">
          <button type="button" className={activeAffiliateId === null ? "active" : ""} onClick={() => setActiveAffiliateId(null)}><span>全部订单</span><small>{orders.length} 个订单</small></button>
          {affiliates.map((affiliate) => <button key={affiliate.id} type="button" className={affiliate.id === activeAffiliateId ? "active" : ""} onClick={() => setActiveAffiliateId(affiliate.id)}><span>{affiliate.wechatId}</span><small>{affiliate.totalClicks} 点击 · {affiliate.orderCount} 订单</small></button>)}
        </div>
      </aside>
      <section className="editor-pane distribution-pane">
        <header className="editor-topbar"><div><strong>分销管理</strong><span>订单确认与线下佣金结算</span></div></header>
        <div className="distribution-workspace">
          <div className="distribution-summary"><div><span>推广者</span><strong>{affiliates.length}</strong></div><div><span>成交金额</span><strong>{formatMoney(completedTotal, "CNY")}</strong></div><div><span>待支付佣金</span><strong>{formatMoney(pendingCommission, "CNY")}</strong></div><div><span>待联系订单</span><strong>{orders.filter((order) => order.orderStatus === "pending").length}</strong></div></div>
          {activeAffiliate && <section className="affiliate-admin-detail"><div><span>微信号</span><strong>{activeAffiliate.wechatId}</strong></div><div><span>总点击 / 独立访客</span><strong>{activeAffiliate.totalClicks} / {activeAffiliate.uniqueClicks}</strong></div><div className="affiliate-admin-actions"><button type="button" onClick={() => void resetAffiliatePassword(activeAffiliate)}>重置密码</button><button className={activeAffiliate.status === "active" ? "danger-button" : "primary"} type="button" onClick={() => void updateAffiliateStatus(activeAffiliate)}>{activeAffiliate.status === "active" ? "停用推广资格" : "恢复推广资格"}</button></div></section>}
          <section className="distribution-orders"><div className="distribution-section-head"><h2>{activeAffiliate ? `${activeAffiliate.wechatId} 的订单` : "全部分销订单"}</h2><span>{visibleOrders.length} 条</span></div>
            <div className="distribution-table-wrap"><table><thead><tr><th>订单号 / 时间</th><th>推广者</th><th>商品</th><th>价格 / 佣金</th><th>订单状态</th><th>佣金状态</th></tr></thead><tbody>{visibleOrders.map((order) => <tr key={order.id}><td><strong>{order.orderCode}</strong><small>{formatDate(order.createdAt)}</small></td><td>{order.affiliateWechatId}</td><td>{order.productTitle}</td><td><strong>{formatMoney(order.priceCents, order.currency)}</strong><small>佣金 {formatMoney(order.commissionCents, order.currency)}</small></td><td><select value={order.orderStatus} onChange={(event) => void updateOrder(order, { orderStatus: event.target.value as AdminAffiliateOrder["orderStatus"] })}><option value="pending">待联系</option><option value="completed">已成交</option><option value="canceled">已取消</option></select></td><td><select value={order.commissionStatus} disabled={order.orderStatus !== "completed"} onChange={(event) => void updateOrder(order, { commissionStatus: event.target.value as AdminAffiliateOrder["commissionStatus"] })}><option value="not_due">无需结算</option><option value="pending">待支付</option><option value="paid">已支付</option></select></td></tr>)}</tbody></table>{visibleOrders.length === 0 && <p className="empty-table">暂无订单</p>}</div>
          </section>
        </div>
      </section>
      {toast && <div className="toast">{toast.text}</div>}
    </main>
  );
}

function productPayload(product: AdminProduct) {
  const { id: _id, slug: _slug, createdAt: _createdAt, updatedAt: _updatedAt, ...payload } = product;
  return payload;
}

async function readAdminProductResponse(response: Response): Promise<AdminProduct | null> {
  const body = await response.json().catch(() => null) as unknown;
  if (!body || typeof body !== "object") return null;
  const record = body as Record<string, unknown>;
  const candidate = record.product && typeof record.product === "object" ? record.product as Record<string, unknown> : record;
  if (typeof candidate.id !== "string" || typeof candidate.slug !== "string" || typeof candidate.title !== "string") return null;
  return { ...candidate, linkUrl: typeof candidate.linkUrl === "string" ? candidate.linkUrl : "" } as AdminProduct;
}

function numberInputValue(value: string): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function priceToCents(value: string): number {
  const price = Number(value);
  return Number.isFinite(price) ? Math.round(price * 100) : 0;
}

function formatPriceInput(cents: number): string {
  return (cents / 100).toFixed(2);
}

function formatMoney(cents: number, currency: string): string {
  return new Intl.NumberFormat("zh-CN", { style: "currency", currency: currency || "CNY", minimumFractionDigits: 2 }).format(cents / 100);
}

async function fileToEditorHtml(file: File): Promise<string> {
  const uploaded = await uploadFile(file);
  if (uploaded.mimeType.startsWith("image/")) {
    return `${editorImageHtml(uploaded.url, uploaded.name)}<p><br></p>`;
  }

  return `<div class="editor-attachment" data-fp-type="attachment" data-name="${escapeAttribute(uploaded.name)}" data-href="${escapeAttribute(uploaded.url)}" contenteditable="false"><span>${escapeHtml(uploaded.name)}</span><a href="${escapeAttribute(uploaded.url)}" download="${escapeAttribute(uploaded.name)}">下载 / 查看</a></div><p><br></p>`;
}

async function uploadEmbeddedImage(src: string, name: string): Promise<UploadedFile> {
  const response = await fetch(src);
  const blob = await response.blob();
  const file = new File([blob], filenameWithImageExtension(name, blob.type), { type: blob.type || "image/png" });
  return uploadFile(file);
}

async function uploadFile(file: File): Promise<UploadedFile> {
  const formData = new FormData();
  formData.set("file", file);

  const response = await fetch("/api/admin/attachments", {
    method: "POST",
    credentials: "include",
    body: formData
  });

  if (!response.ok) {
    throw new Error("Upload failed");
  }

  const payload = (await response.json()) as { file: UploadedFile };
  return payload.file;
}

async function importRemoteImages(postID: string, items: ReadonlyArray<{ clientId: string; url: string; name: string }>): Promise<ReadonlyMap<string, ImportedImageFile | ImageImportFailure>> {
  if (!postID || items.length === 0) return new Map();
  const response = await fetch(`/api/admin/posts/${postID}/image-imports`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    credentials: "include",
    body: JSON.stringify({ items })
  });
  if (!response.ok) {
    throw new Error(await readImageImportApiMessage(response, "图片转存失败"));
  }
  const payload = (await response.json()) as {
    items?: Array<{ clientId?: string; file?: ImportedImageFile; error?: ImageImportFailure }>;
  };
  const results = new Map<string, ImportedImageFile | ImageImportFailure>();
  for (const item of payload.items ?? []) {
    if (!item.clientId) continue;
    if (item.file?.url && item.file.claimToken) {
      results.set(item.clientId, item.file);
    } else if (item.error?.message && item.error.resolution) {
      results.set(item.clientId, item.error);
    }
  }
  return results;
}

async function readImageImportApiMessage(response: Response, fallback: string): Promise<string> {
  try {
    const payload = (await response.json()) as { error?: { message?: string; resolution?: string } };
    const message = payload.error?.message?.trim();
    const resolution = payload.error?.resolution?.trim();
    return message ? `${message}${resolution ? `。${resolution}` : ""}` : fallback;
  } catch {
    return fallback;
  }
}

function filenameWithImageExtension(name: string, mimeType: string): string {
  const cleanName = name.trim() || "image";
  if (/\.[a-z0-9]{2,5}$/i.test(cleanName)) {
    return cleanName;
  }

  const extensions: Record<string, string> = {
    "image/jpeg": "jpg",
    "image/png": "png",
    "image/gif": "gif",
    "image/webp": "webp",
    "image/svg+xml": "svg"
  };

  return `${cleanName}.${extensions[mimeType.toLowerCase().split(";")[0] ?? ""] ?? "png"}`;
}

function markdownToEditorHtml(markdown: string): string {
  const html = splitCalloutBlocks(markdown)
    .map((segment) =>
      segment.type === "callout"
        ? editorCalloutHtml(markdownFragmentToEditorHtml(segment.markdown), segment.emoji)
        : markdownFragmentToEditorHtml(segment.markdown)
    )
    .join("");
  return html || "<p><br></p>";
}

function markdownFragmentToEditorHtml(markdown: string): string {
  const lines = markdown.split(/\r?\n/);
  const html: string[] = [];
  let paragraph: string[] = [];
  let codeLines: string[] | null = null;
  let codeLang = "";
  let listItems: string[] = [];
  let listOrdered = false;

  const flushParagraph = () => {
    if (!paragraph.length) return;
    html.push(`<p>${formatInlineMarkdown(paragraph.join("<br>"))}</p>`);
    paragraph = [];
  };

  const flushList = () => {
    if (!listItems.length) return;
    const tagName = listOrdered ? "ol" : "ul";
    html.push(`<${tagName}>${listItems.map((item) => `<li>${formatInlineMarkdown(escapeHtml(item))}</li>`).join("")}</${tagName}>`);
    listItems = [];
  };

  for (const line of lines) {
    const fence = line.match(/^```(\w+)?\s*$/);
    if (fence) {
      if (codeLines) {
        html.push(`<pre data-lang="${escapeAttribute(codeLang)}"><code>${escapeHtml(codeLines.join("\n"))}</code></pre>`);
        codeLines = null;
        codeLang = "";
      } else {
        flushParagraph();
        flushList();
        codeLines = [];
        codeLang = fence[1] ?? "";
      }
      continue;
    }

    if (codeLines) {
      codeLines.push(line);
      continue;
    }

    if (!line.trim()) {
      flushParagraph();
      flushList();
      continue;
    }

    const youtube = parseYouTubeDirective(line);
    if (youtube) {
      flushParagraph();
      flushList();
      html.push(editorYouTubeHtml(youtube));
      continue;
    }

    const image = line.match(/^!\[(.*)]\((.*)\)$/);
    if (image) {
      flushParagraph();
      flushList();
      html.push(editorImageHtml(image[2] ?? "", image[1] ?? "image.png"));
      continue;
    }

    const attachment = line.match(/^\[附件[:：]\s*(.+?)]\((.+)\)$/);
    if (attachment) {
      flushParagraph();
      flushList();
      html.push(`<div class="editor-attachment" data-fp-type="attachment" data-name="${escapeAttribute(attachment[1] ?? "")}" data-href="${escapeAttribute(attachment[2] ?? "")}" contenteditable="false"><span>${escapeHtml(attachment[1] ?? "")}</span><a href="${escapeAttribute(attachment[2] ?? "")}" download="${escapeAttribute(attachment[1] ?? "")}">下载 / 查看</a></div>`);
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      flushList();
      const level = heading[1]?.length ?? 1;
      html.push(`<h${level}>${formatInlineMarkdown(escapeHtml(heading[2] ?? ""))}</h${level}>`);
      continue;
    }

    const unorderedItem = line.match(/^[-*+]\s+(.+)$/);
    const orderedItem = line.match(/^\d+[.)]\s+(.+)$/);
    if (unorderedItem || orderedItem) {
      flushParagraph();
      const ordered = Boolean(orderedItem);
      if (listItems.length && ordered !== listOrdered) flushList();
      listOrdered = ordered;
      listItems.push((orderedItem ?? unorderedItem)?.[1] ?? "");
      continue;
    }

    flushList();
    paragraph.push(escapeHtml(line));
  }

  if (codeLines) {
    html.push(`<pre data-lang="${escapeAttribute(codeLang)}"><code>${escapeHtml(codeLines.join("\n"))}</code></pre>`);
  }
  flushParagraph();
  flushList();

  return html.join("");
}

function editorHtmlToMarkdown(editor: HTMLElement): string {
  const blocks: string[] = [];

  for (const node of [...editor.childNodes]) {
    const markdown = nodeToMarkdown(node);
    if (markdown.trim()) {
      blocks.push(markdown);
    }
  }

  return blocks.join("\n\n");
}

function nodeToMarkdown(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) {
    return node.textContent?.trim() ?? "";
  }

  if (!(node instanceof HTMLElement)) {
    return "";
  }

  if (node.matches('[data-fp-type="pending-media"]')) {
    return "";
  }

  if (node.matches('[data-fp-type="image-import-error"]')) {
    return "";
  }

  if (node.matches('[data-fp-type="callout"]')) {
    const content = node.querySelector<HTMLElement>(":scope > .editor-callout-content");
    if (!content) return "";
    const markdown = [...content.childNodes]
      .map(nodeToMarkdown)
      .filter((value) => value.trim())
      .join("\n\n");
    return calloutDirective(node.dataset.emoji ?? CALLOUT_DEFAULT_EMOJI, markdown);
  }

  if (node.matches("figure.editor-image")) {
    const images = [...node.querySelectorAll("img")].map((image) => ({
      src: image.getAttribute("src") ?? "",
      alt: image.alt || "图片"
    }));
    return editorImagesMarkdown(images);
  }

  if (node.matches("figure.editor-youtube")) {
    const video = parseYouTubeVideoInput(node.dataset.videoId ?? "");
    if (!video) return "";
    video.startSeconds = Number.parseInt(node.dataset.start ?? "0", 10) || 0;
    return youtubeDirective(video);
  }

  if (node.matches(".editor-attachment")) {
    const name = node.dataset.name || node.querySelector("span")?.textContent?.trim() || "附件";
    const href = node.dataset.href || node.querySelector("a")?.getAttribute("href") || "#";
    return `[附件: ${escapeMarkdown(name)}](${href})`;
  }

  if (node.tagName === "IMG") {
    const img = node as HTMLImageElement;
    return `![${escapeMarkdown(img.alt || "图片")}](${img.getAttribute("src") ?? ""})`;
  }

  if (node.querySelector("figure.editor-image,figure.editor-youtube,img,.editor-attachment")) {
    const childMarkdown = [...node.childNodes].map(nodeToMarkdown).filter((value) => value.trim());
    if (childMarkdown.length) {
      return childMarkdown.join("\n\n");
    }
  }

  if (/^H[1-6]$/.test(node.tagName)) {
    const level = Number(node.tagName.slice(1));
    return `${"#".repeat(level)} ${inlineChildrenToMarkdown(node).trim()}`;
  }

  if (node.tagName === "PRE") {
    const lang = node.dataset.lang ?? "";
    return `\`\`\`${lang}\n${node.textContent?.replace(/\n$/, "") ?? ""}\n\`\`\``;
  }

  if (node.tagName === "UL" || node.tagName === "OL") {
    return [...node.querySelectorAll<HTMLElement>(":scope > li")]
      .map((li, index) => `${node.tagName === "OL" ? `${index + 1}.` : "-"} ${inlineChildrenToMarkdown(li).trim()}`)
      .join("\n");
  }

  return inlineChildrenToMarkdown(node).trim();
}

function inlineChildrenToMarkdown(node: HTMLElement): string {
  return [...node.childNodes].map(inlineNodeToMarkdown).join("");
}

function inlineNodeToMarkdown(node: Node): string {
  if (node.nodeType === Node.TEXT_NODE) {
    return node.textContent ?? "";
  }

  if (!(node instanceof HTMLElement)) {
    return "";
  }

  if (node.tagName === "BR") {
    return "\n";
  }

  if (node.tagName === "IMG") {
    const img = node as HTMLImageElement;
    return `![${escapeMarkdown(img.alt || "图片")}](${img.src})`;
  }

  if (node.matches("figure.editor-image,figure.editor-youtube,.editor-attachment,[data-fp-type=\"image-import-error\"]")) {
    return nodeToMarkdown(node);
  }

  const content = inlineChildrenToMarkdown(node);

  if (node.tagName === "A" && node.querySelector("img")) {
    return content;
  }

  if (node.tagName === "A") {
    const href = node.getAttribute("href") ?? "";
    return href ? `[${content || href}](${href})` : content;
  }

  if (node.tagName === "STRONG" || node.tagName === "B") {
    return `**${content}**`;
  }

  if (node.tagName === "EM" || node.tagName === "I") {
    return `*${content}*`;
  }

  if (node.tagName === "DEL" || node.tagName === "S" || node.tagName === "STRIKE") {
    return `~~${content}~~`;
  }

  if (node.tagName === "U") {
    return `<u>${content}</u>`;
  }

  if (node.tagName === "CODE") {
    return `\`${node.textContent ?? ""}\``;
  }

  if (node.tagName === "SPAN") {
    const className = sanitizeInlineClassList([...node.classList].join(" "));
    return className ? `<span class="${className}">${content}</span>` : content;
  }

  return content || (node.textContent ?? "");
}

function formatInlineMarkdown(value: string): string {
  return restoreSafeInlineHtml(value
    .replace(/\[([^\]]+)]\((https?:\/\/[^)\s]+|\/[^)\s]+)\)/g, '<a href="$2">$1</a>')
    .replace(/\*\*(.*?)\*\*/g, "<strong>$1</strong>")
    .replace(/~~(.*?)~~/g, "<del>$1</del>")
    .replace(/\*(.*?)\*/g, "<em>$1</em>")
    .replace(/`([^`]+)`/g, "<code>$1</code>"));
}

function restoreSafeInlineHtml(value: string): string {
  return value
    .replace(/&lt;u&gt;([\s\S]*?)&lt;\/u&gt;/g, "<u>$1</u>")
    .replace(/&lt;span class=&quot;([^&]+)&quot;&gt;([\s\S]*?)&lt;\/span&gt;/g, (_match, className: string, content: string) => {
      const safeClassName = sanitizeInlineClassList(className);
      return safeClassName ? `<span class="${safeClassName}">${content}</span>` : content;
    });
}

function sanitizeInlineClassList(value: string): string {
  const allowedClasses = new Set<string>([
    ...sizeOptions.map((option) => option.className),
    ...colorOptions.map((option) => option.className)
  ]);
  return value
    .split(/\s+/)
    .map((className) => className.trim())
    .filter((className) => allowedClasses.has(className))
    .join(" ");
}

// Converts plain-text Markdown (containing image references like ![alt](url))
// into a synthetic HTML string suitable for localizePastedImages. Only called
// when the clipboard has no text/html data (e.g. pastes from VS Code / Typora).
// Images are emitted as standalone <img> elements; surrounding text becomes <p>.
// Returns null when the text contains no external Markdown image syntax.
function markdownImagesToPasteHtml(text: string): string | null {
  if (!text || !/!\[[^\]]*\]\(https?:\/\//i.test(text)) return null;

  const parts: string[] = [];
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line) continue;

    const images: Array<{ alt: string; url: string }> = [];
    const textWithoutImages = line
      .replace(/!\[([^\]]*)\]\((https?:\/\/[^)]+)\)/gi, (_, alt: string, url: string) => {
        images.push({ alt, url });
        return "";
      })
      .trim();

    if (textWithoutImages) parts.push(`<p>${escapeHtml(textWithoutImages)}</p>`);
    for (const { alt, url } of images) {
      parts.push(`<img src="${escapeAttribute(url)}" alt="${escapeAttribute(alt)}">`);
    }
  }
  return parts.length > 0 ? parts.join("") : null;
}

function escapeMarkdown(value: string): string {
  return value.replaceAll("[", "\\[").replaceAll("]", "\\]").replaceAll("(", "\\(").replaceAll(")", "\\)");
}

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function escapeAttribute(value: string): string {
  return escapeHtml(value).replaceAll("\n", "&#10;");
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit"
  }).format(new Date(value));
}

const appRoot = document.getElementById("root");
if (appRoot) createRoot(appRoot).render(<App />);
