export interface ReaderAccessState {
  locked: boolean;
  authenticated: boolean;
  purchased: boolean;
}

export interface PaidArticleGateInput {
  slug: string;
  title: string;
  excerpt: string;
  priceCents: number;
  currency: string;
  access: ReaderAccessState;
  wechatImageUrl?: string;
}

type TurnstileApi = {
  render(container: HTMLElement, options: Record<string, unknown>): string;
  reset(widgetId: string): void;
  remove(widgetId: string): void;
};

export type ReaderAuthMode = "login" | "register";

let turnstileLoader: Promise<TurnstileApi> | null = null;

export function renderPaidArticleGate(container: HTMLElement, input: PaidArticleGateInput, onUnlocked: () => void): void {
  container.classList.remove("paid-content-protected");
  container.replaceChildren();
  const gate = document.createElement("section");
  gate.className = "paid-article-gate";

  const kicker = document.createElement("p");
  kicker.className = "section-kicker";
  kicker.textContent = "Paid article";
  const heading = document.createElement("h2");
  heading.textContent = "购买后阅读全文";
  const excerpt = document.createElement("p");
  excerpt.className = "paid-article-preview";
  excerpt.textContent = input.excerpt || "这是一篇付费文章，完成订单并由站长确认收款后即可永久阅读。";
  const price = document.createElement("strong");
  price.className = "paid-article-price";
  price.textContent = formatMoney(input.priceCents, input.currency);
  const button = document.createElement("button");
  button.type = "button";
  button.className = "button primary paid-article-buy";
  button.textContent = input.access.authenticated ? "生成购买订单" : "登录后购买";
  const note = document.createElement("small");
  note.textContent = "下单后添加站长微信并发送订单号；收款确认后自动开通。";
  gate.append(kicker, heading, excerpt, price, button, note);
  container.append(gate);

  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      if (!input.access.authenticated) {
        await openReaderAuthDialog();
        onUnlocked();
        return;
      }
      const response = await fetch(`/api/reader/posts/${encodeURIComponent(input.slug)}/orders`, {
        method: "POST",
        credentials: "include",
        headers: { "content-type": "application/json" },
        body: "{}"
      });
      const payload = await response.json().catch(() => null) as { order?: { orderCode: string }; contact?: { wechatImageUrl?: string }; error?: { message?: string } } | null;
      if (!response.ok || !payload?.order) throw new Error(payload?.error?.message || "订单创建失败");
      renderOrderCreated(gate, payload.order.orderCode, payload.contact?.wechatImageUrl || input.wechatImageUrl);
    } catch (error) {
      renderInlineError(gate, error instanceof Error ? error.message : "操作失败，请稍后重试");
    } finally {
      button.disabled = false;
    }
  });
}

export function setPaidContentProtection(container: HTMLElement, enabled: boolean): void {
  container.classList.toggle("paid-content-protected", enabled);
  container.querySelectorAll<HTMLButtonElement>(".copy-code").forEach((button) => {
    button.disabled = enabled;
    if (enabled) button.title = "付费文章不支持复制";
  });
  if (!container.dataset.paidProtectionBound) {
    container.dataset.paidProtectionBound = "1";
    for (const eventName of ["copy", "cut", "contextmenu", "dragstart", "selectstart"] as const) {
      container.addEventListener(eventName, (event) => {
        if (!container.classList.contains("paid-content-protected")) return;
        if (isEditableEventTarget(event.target)) return;
        event.preventDefault();
      });
    }
    document.addEventListener("keydown", (event) => {
      if (!container.classList.contains("paid-content-protected")) return;
      if (isEditableEventTarget(event.target)) return;
      if (shouldBlockPaidShortcut(event.key, event.ctrlKey, event.metaKey)) {
        event.preventDefault();
      }
    });
  }
}

function isEditableEventTarget(target: EventTarget | null): boolean {
  return target instanceof Element && Boolean(target.closest('input, textarea, [contenteditable="true"]'));
}

export function shouldBlockPaidShortcut(key: string, ctrlKey: boolean, metaKey: boolean): boolean {
  return (ctrlKey || metaKey) && ["c", "x", "s", "p", "u"].includes(key.toLowerCase());
}

export async function openReaderAuthDialog(initialMode: ReaderAuthMode = "login"): Promise<void> {
  const configResponse = await fetch("/api/reader/config", { credentials: "include" });
  const config = await configResponse.json().catch(() => null) as { enabled?: boolean; turnstileSiteKey?: string; actions?: { login?: string; register?: string } } | null;
  if (!configResponse.ok || !config?.enabled || !config.turnstileSiteKey) throw new Error("登录服务暂不可用");

  const dialog = document.createElement("dialog");
  dialog.className = "reader-auth-dialog";
  dialog.innerHTML = `<form method="dialog" class="reader-auth-shell"><button class="reader-auth-close" type="button" value="cancel" aria-label="关闭">×</button><p class="section-kicker">Reader account</p><h2>读者账户</h2><div class="reader-auth-tabs" role="tablist"><button type="button" data-mode="login">登录</button><button type="button" data-mode="register">注册</button></div><label><span>邮箱或用户名</span><input name="loginName" autocomplete="username" minlength="3" maxlength="128" required /></label><label><span>密码</span><input name="password" type="password" minlength="8" maxlength="128" required /></label><div class="reader-turnstile" aria-label="人机验证"></div><p class="reader-auth-error" role="alert"></p><button class="button primary reader-auth-submit" type="submit"></button><small>无需邮箱或短信验证码。登录态会安全保存在当前浏览器。</small></form>`;
  document.body.append(dialog);
  bindReaderAuthDismiss(dialog);
  const form = requiredElement(dialog, "form", HTMLFormElement);
  const errorBox = requiredElement(dialog, ".reader-auth-error", HTMLElement);
  const submit = requiredElement(dialog, ".reader-auth-submit", HTMLButtonElement);
  const password = requiredElement(dialog, 'input[name="password"]', HTMLInputElement);
  let mode: ReaderAuthMode = initialMode;
  let turnstileToken = "";
  let turnstile: TurnstileApi;
  try {
    turnstile = await loadTurnstile();
  } catch (error) {
    dialog.remove();
    throw error;
  }
  const widgetContainer = requiredElement(dialog, ".reader-turnstile", HTMLElement);
  const renderWidget = () => turnstile.render(widgetContainer, {
      sitekey: config.turnstileSiteKey,
      action: mode === "register" ? config.actions?.register || "reader_register" : config.actions?.login || "reader_login",
      theme: "auto",
      callback: (token: string) => { turnstileToken = token; errorBox.textContent = ""; },
      "expired-callback": () => { turnstileToken = ""; },
      "error-callback": () => { turnstileToken = ""; errorBox.textContent = "人机验证加载失败，请刷新后重试"; }
    });
  let widgetId = renderWidget();

  const selectMode = (nextMode: ReaderAuthMode) => {
    mode = nextMode;
    dialog.querySelectorAll<HTMLButtonElement>("[data-mode]").forEach((item) => {
      item.classList.toggle("active", item.dataset.mode === mode);
      item.setAttribute("aria-selected", String(item.dataset.mode === mode));
    });
    submit.textContent = mode === "register" ? "注册并登录" : "登录";
    password.autocomplete = mode === "register" ? "new-password" : "current-password";
  };
  selectMode(initialMode);

  const completion = new Promise<void>((resolve, reject) => {
    dialog.querySelectorAll<HTMLButtonElement>("[data-mode]").forEach((tab) => {
      tab.addEventListener("click", () => {
        selectMode(tab.dataset.mode === "register" ? "register" : "login");
        turnstileToken = "";
        turnstile.remove(widgetId);
        widgetId = renderWidget();
      });
    });
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!turnstileToken) { errorBox.textContent = "请先完成人机验证"; return; }
      submit.disabled = true;
      errorBox.textContent = "";
      const data = new FormData(form);
      try {
        const response = await fetch(`/api/reader/${mode}`, {
          method: "POST", credentials: "include", headers: { "content-type": "application/json" },
          body: JSON.stringify({ loginName: data.get("loginName"), password: data.get("password"), turnstileToken })
        });
        const payload = await response.json().catch(() => null) as { error?: { message?: string } } | null;
        if (!response.ok) throw new Error(payload?.error?.message || "登录失败");
        resolve();
        dialog.close("success");
      } catch (error) {
        errorBox.textContent = error instanceof Error ? error.message : "登录失败";
        turnstileToken = "";
        turnstile.reset(widgetId);
      } finally {
        submit.disabled = false;
      }
    });
    dialog.addEventListener("close", () => {
      if (dialog.returnValue !== "success") reject(new Error("已取消登录"));
    }, { once: true });
  });
  dialog.showModal();
  try { await completion; } finally { turnstile.remove(widgetId); dialog.remove(); }
}

export function bindReaderAuthDismiss(dialog: HTMLDialogElement): void {
  const close = requiredElement(dialog, ".reader-auth-close", HTMLButtonElement);
  close.type = "button";
  close.addEventListener("click", () => dialog.close("cancel"));
}

function renderOrderCreated(container: HTMLElement, orderCode: string, imageURL?: string): void {
  container.replaceChildren();
  const heading = document.createElement("h2");
  heading.textContent = "订单已生成";
  const code = document.createElement("strong");
  code.className = "paid-order-code";
  code.textContent = orderCode;
  const instruction = document.createElement("p");
  instruction.textContent = "请添加站长微信并发送以上订单号。确认收款后，刷新文章即可阅读。";
  container.append(heading, code, instruction);
  const safeImage = safeSameOriginPath(imageURL);
  if (safeImage) {
    const image = document.createElement("img");
    image.src = safeImage;
    image.alt = "站长微信二维码";
    image.className = "paid-wechat-qr";
    container.append(image);
  }
}

function renderInlineError(container: HTMLElement, message: string): void {
  let error = container.querySelector<HTMLElement>(".paid-inline-error");
  if (!error) { error = document.createElement("p"); error.className = "paid-inline-error"; container.append(error); }
  error.textContent = message;
}

function loadTurnstile(): Promise<TurnstileApi> {
  const current = (window as Window & { turnstile?: TurnstileApi }).turnstile;
  if (current) return Promise.resolve(current);
  if (turnstileLoader) return turnstileLoader;
  turnstileLoader = new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
    script.async = true;
    script.defer = true;
    script.onload = () => {
      const loaded = (window as Window & { turnstile?: TurnstileApi }).turnstile;
      if (loaded) {
        resolve(loaded);
      } else {
        script.remove();
        turnstileLoader = null;
        reject(new Error("人机验证加载失败"));
      }
    };
    script.onerror = () => {
      script.remove();
      turnstileLoader = null;
      reject(new Error("人机验证加载失败"));
    };
    document.head.append(script);
  });
  return turnstileLoader;
}

function formatMoney(cents: number, currency: string): string {
  return new Intl.NumberFormat("zh-CN", { style: "currency", currency: /^[A-Z]{3}$/.test(currency) ? currency : "CNY" }).format(cents / 100);
}

function safeSameOriginPath(value?: string): string | null {
  if (!value) return null;
  try { const parsed = new URL(value, location.origin); return parsed.origin === location.origin ? `${parsed.pathname}${parsed.search}` : null; } catch { return null; }
}

function requiredElement<T extends Element>(root: ParentNode, selector: string, constructor: { new(): T }): T {
  const element = root.querySelector(selector);
  if (!(element instanceof constructor)) throw new Error(`Missing required element: ${selector}`);
  return element;
}
