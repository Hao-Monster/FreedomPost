type ChallengeApi = {
  render(container: HTMLElement, options: {
    sitekey: string; action: string; theme: "auto"; size: "flexible";
    callback(token: string): void; "expired-callback"(): void; "error-callback"(): void;
  }): string;
  remove(id: string): void;
};

const scriptURL = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
let loading: Promise<ChallengeApi> | undefined;

function currentAPI(): ChallengeApi | undefined {
  return (window as unknown as { turnstile?: ChallengeApi }).turnstile;
}

function loadAPI(): Promise<ChallengeApi> {
  const current = currentAPI();
  if (current) return Promise.resolve(current);
  if (loading) return loading;
  loading = new Promise<ChallengeApi>((resolve, reject) => {
    let script = document.querySelector<HTMLScriptElement>('script[src^="https://challenges.cloudflare.com/turnstile/v0/api.js"]');
    const owned = !script;
    script ??= document.createElement("script");
    const element = script;
    const finish = (error?: Error) => {
      clearTimeout(timeout);
      clearInterval(poll);
      element.removeEventListener("error", onError);
      const api = currentAPI();
      if (error || !api) {
        if (owned) element.remove();
        reject(error ?? new Error("人机验证加载失败，请重试"));
      } else resolve(api);
    };
    const onError = () => finish(new Error("人机验证加载失败，请重试"));
    const timeout = setTimeout(onError, 15000);
    const poll = setInterval(() => { if (currentAPI()) finish(); }, 50);
    element.addEventListener("error", onError, { once: true });
    if (owned) {
      element.src = scriptURL;
      element.async = true;
      document.head.append(element);
    }
  }).catch((error: unknown) => { loading = undefined; throw error; });
  return loading;
}

// Tokens belong to a single action and a single request. Never persist them.
export async function requestChallenge(action: string, options: { signal?: AbortSignal } = {}): Promise<string> {
  const { signal } = options;
  if (signal?.aborted) throw new DOMException("操作已取消", "AbortError");
  const configuration = new AbortController();
  const abortConfiguration = () => configuration.abort();
  signal?.addEventListener("abort", abortConfiguration, { once: true });
  const configurationTimeout = setTimeout(abortConfiguration, 10000);
  let siteKey: string | undefined;
  try {
    const response = await fetch("/api/security/challenge", { cache: "no-store", signal: configuration.signal });
    if (!response.ok) throw new Error("人机验证暂不可用，请稍后再试");
    ({ siteKey } = await response.json() as { siteKey?: string });
  } finally {
    clearTimeout(configurationTimeout);
    signal?.removeEventListener("abort", abortConfiguration);
  }
  if (!siteKey) throw new Error("人机验证暂不可用，请稍后再试");
  const api = await loadAPI();
  if (signal?.aborted) throw new DOMException("操作已取消", "AbortError");
  return new Promise<string>((resolve, reject) => {
    const dialog = document.createElement("dialog");
    dialog.setAttribute("aria-label", "安全验证");
    dialog.style.cssText = "padding:24px;border:1px solid #777;border-radius:12px;max-width:90vw;";
    const label = document.createElement("p");
    label.textContent = "请完成安全验证以继续";
    const container = document.createElement("div");
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.textContent = "取消";
    let widget: string | undefined;
    let settled = false;
    const previousFocus = document.activeElement;
    const finish = (token?: string, error?: Error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      signal?.removeEventListener("abort", abort);
      if (widget !== undefined) api.remove(widget);
      dialog.close();
      dialog.remove();
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected) previousFocus.focus();
      if (token) resolve(token);
      else reject(error ?? new Error("验证未完成，请重试"));
    };
    const abort = () => finish(undefined, new DOMException("操作已取消", "AbortError"));
    const timeout = setTimeout(() => finish(undefined, new Error("验证已超时，请重试")), 120000);
    signal?.addEventListener("abort", abort, { once: true });
    cancel.addEventListener("click", abort);
    dialog.addEventListener("cancel", (event) => { event.preventDefault(); abort(); });
    dialog.append(label, container, cancel);
    document.body.append(dialog);
    try {
      dialog.showModal();
      widget = api.render(container, {
        sitekey: siteKey, action, theme: "auto", size: "flexible",
        callback: (token) => finish(token),
        "expired-callback": () => finish(undefined, new Error("验证已过期，请重试")),
        "error-callback": () => finish(undefined, new Error("验证失败，请重试"))
      });
      // Test APIs can complete synchronously during render.
      if (settled) api.remove(widget);
    } catch (error) { finish(undefined, error instanceof Error ? error : new Error("验证加载失败")); }
  });
}
