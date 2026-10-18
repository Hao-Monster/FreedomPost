import {
  BenefitApiError,
  createWebmasterBenefitClient,
  describeBenefitError,
  type BenefitCampaign,
  type BenefitClaimResult,
  type ReadyBenefitClaim
} from "../lib/benefit-client.js";
import { renderSubscriptionQr } from "../lib/benefit-qr.js";
import { benefitPanelForState, type BenefitPageState } from "../lib/benefit-state.js";

const turnstileScriptUrl = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
const turnstileScriptId = "freedompost-turnstile-script";
const turnstileAction = "webmaster_benefit_claim";
const maxPollAttempts = 10;
let turnstileLoader: Promise<TurnstileApi> | null = null;

interface TurnstileApi {
  render(container: HTMLElement, options: {
    sitekey: string;
    action: string;
    theme: "auto";
    size: "compact" | "flexible";
    callback(token: string): void;
    "expired-callback"(): void;
    "error-callback"(): void;
  }): string;
  reset(widgetId: string): void;
  remove(widgetId: string): void;
}

declare global {
  interface Window {
    turnstile?: TurnstileApi;
  }
}

const benefitControllers = new WeakMap<HTMLElement, BenefitPageController>();

export function initializeBenefitPage(root: HTMLElement | null = document.querySelector("[data-benefit-root]")): () => void {
  if (!root) return () => undefined;
  const existing = benefitControllers.get(root);
  if (existing) return () => destroyBenefitPage(root);
  root.dataset.benefitInitialized = "true";
  const controller = new BenefitPageController(root);
  benefitControllers.set(root, controller);
  void controller.start();
  return () => destroyBenefitPage(root);
}

export function destroyBenefitPage(root: HTMLElement | null = document.querySelector("[data-benefit-root]")): void {
  if (!root) return;
  benefitControllers.get(root)?.destroy();
  benefitControllers.delete(root);
}

class BenefitPageController {
  private readonly client = createWebmasterBenefitClient();
  private readonly claimButton: HTMLButtonElement;
  private readonly retryButton: HTMLButtonElement;
  private readonly copyButton: HTMLButtonElement;
  private readonly downloadButton: HTMLButtonElement;
  private readonly qrCanvas: HTMLCanvasElement;
  private campaign: BenefitCampaign | null = null;
  private readyClaim: ReadyBenefitClaim | null = null;
  private token = "";
  private widgetId: string | null = null;
  private busy = false;
  private pollAttempts = 0;
  private lastError: unknown = null;
  private readonly requestController = new AbortController();
  private pollTimer: number | null = null;
  private destroyed = false;
  private readonly onClaimClick = () => void this.submitClaim();
  private readonly onRetryClick = () => void this.retry();
  private readonly onCopyClick = () => void this.copySubscription();
  private readonly onDownloadClick = () => this.downloadQrCode();

  constructor(private readonly root: HTMLElement) {
    this.claimButton = requiredElement(root, "#benefitClaimButton", HTMLButtonElement);
    this.retryButton = requiredElement(root, "#benefitRetryButton", HTMLButtonElement);
    this.copyButton = requiredElement(root, "#benefitCopyButton", HTMLButtonElement);
    this.downloadButton = requiredElement(root, "#benefitDownloadButton", HTMLButtonElement);
    this.qrCanvas = requiredElement(root, "#benefitQrCanvas", HTMLCanvasElement);
    this.claimButton.addEventListener("click", this.onClaimClick);
    this.retryButton.addEventListener("click", this.onRetryClick);
    this.copyButton.addEventListener("click", this.onCopyClick);
    this.downloadButton.addEventListener("click", this.onDownloadClick);
  }

  async start(): Promise<void> {
    this.renderState("loading", "正在读取活动信息…");
    try {
      this.campaign = await this.client.getCampaign(this.requestController.signal);
      if (!this.isActive()) return;
      this.renderCampaign(this.campaign);
      const restored = await this.client.restoreClaim(this.requestController.signal);
      if (!this.isActive()) return;
      if (restored?.status === "ready") {
        await this.renderReady(restored);
        return;
      }
      if (restored?.status === "provisioning") {
        this.beginProvisioning(restored);
        return;
      }
      if (!this.campaign.enabled) {
        this.renderState("disabled", "活动暂未开放");
        return;
      }
      if (!this.campaign.turnstileSiteKey) {
        throw new BenefitApiError(503, "BENEFIT_UNAVAILABLE");
      }
      this.renderState("idle", "完成人机验证后即可领取");
      await this.prepareTurnstile();
    } catch (error) {
      if (!this.destroyed) this.renderError(error);
    }
  }

  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.requestController.abort();
    if (this.pollTimer !== null) window.clearTimeout(this.pollTimer);
    if (this.widgetId && window.turnstile) {
      try {
        window.turnstile.remove(this.widgetId);
      } catch {
        // The widget may already have removed itself while the route changed.
      }
    }
    this.claimButton.removeEventListener("click", this.onClaimClick);
    this.retryButton.removeEventListener("click", this.onRetryClick);
    this.copyButton.removeEventListener("click", this.onCopyClick);
    this.downloadButton.removeEventListener("click", this.onDownloadClick);
    this.readyClaim = null;
    this.token = "";
    this.widgetId = null;
    this.root.removeAttribute("aria-busy");
    delete this.root.dataset.benefitInitialized;
  }

  private renderCampaign(campaign: BenefitCampaign): void {
    setText(this.root, "[data-benefit-traffic]", formatBytes(campaign.trafficBytes));
    setText(this.root, "[data-benefit-duration]", `${campaign.durationDays} 天`);
    setText(this.root, "[data-benefit-hwid]", campaign.hwidRequired ? "强制绑定" : "无需绑定");
    setText(this.root, "[data-benefit-ip-limit]", `${campaign.ipLimit} 个`);
  }

  private async prepareTurnstile(): Promise<void> {
    if (!this.campaign?.turnstileSiteKey) throw new BenefitApiError(503, "BENEFIT_UNAVAILABLE");
    const container = requiredElement(this.root, "#benefitTurnstile", HTMLElement);
    show(container);
    const turnstile = await loadTurnstile();
    if (!this.root.isConnected) return;
    if (this.widgetId) {
      turnstile.reset(this.widgetId);
      this.token = "";
      this.claimButton.disabled = true;
      return;
    }
    this.widgetId = turnstile.render(container, {
      sitekey: this.campaign.turnstileSiteKey,
      action: turnstileAction,
      theme: "auto",
      size: window.matchMedia("(max-width: 380px)").matches ? "compact" : "flexible",
      callback: (token) => {
        if (this.destroyed) return;
        this.token = token;
        this.claimButton.disabled = this.busy;
        this.setStatus("验证已完成，可以领取");
      },
      "expired-callback": () => {
        if (this.destroyed) return;
        this.token = "";
        this.claimButton.disabled = true;
        this.setStatus("验证已过期，请重新验证");
      },
      "error-callback": () => {
        if (this.destroyed) return;
        this.token = "";
        this.claimButton.disabled = true;
        this.renderError(new BenefitApiError(503, "TURNSTILE_UNAVAILABLE"));
      }
    });
  }

  private async submitClaim(): Promise<void> {
    if (this.busy || !this.token) return;
    this.busy = true;
    this.claimButton.disabled = true;
    this.renderState("verifying", "正在校验人机验证…");
    await Promise.resolve();
    this.renderState("claiming", "正在创建专属订阅…");
    try {
      const result = await this.client.claim(this.token, this.requestController.signal);
      if (!this.isActive()) return;
      this.resetTurnstile();
      if (result.status === "ready") await this.renderReady(result);
      else this.beginProvisioning(result);
    } catch (error) {
      this.resetTurnstile();
      if (!this.destroyed) this.renderError(error);
    } finally {
      this.busy = false;
    }
  }

  private beginProvisioning(result: Extract<BenefitClaimResult, { status: "provisioning" }>): void {
    this.pollAttempts = 0;
    this.renderState("provisioning", "订阅正在安全创建，请勿关闭页面…");
    this.schedulePoll(result.retryAfterSeconds);
  }

  private schedulePoll(delaySeconds: number): void {
    if (this.pollTimer !== null) window.clearTimeout(this.pollTimer);
    this.pollTimer = window.setTimeout(() => {
      this.pollTimer = null;
      void this.pollClaim();
    }, Math.max(1, delaySeconds) * 1_000);
  }

  private async pollClaim(): Promise<void> {
    if (!this.root.isConnected || this.root.dataset.state !== "provisioning") return;
    this.pollAttempts += 1;
    try {
      const result = await this.client.restoreClaim(this.requestController.signal);
      if (!this.isActive()) return;
      if (result?.status === "ready") {
        await this.renderReady(result);
        return;
      }
      if (result?.status === "provisioning" && this.pollAttempts < maxPollAttempts) {
        this.setStatus(`订阅正在创建（第 ${this.pollAttempts} 次检查）…`);
        this.schedulePoll(result.retryAfterSeconds);
        return;
      }
      this.renderError(new BenefitApiError(503, "BENEFIT_PROVISIONING_UNAVAILABLE"));
    } catch (error) {
      if (!this.destroyed) this.renderError(error);
    }
  }

  private async renderReady(claim: ReadyBenefitClaim): Promise<void> {
    this.readyClaim = claim;
    this.renderState("ready", "领取成功，订阅二维码已生成");
    setText(this.root, "#benefitExpiresAt", formatDateTime(claim.expiresAt));
    setText(this.root, "#benefitReadyTraffic", formatBytes(claim.trafficBytes));
    setText(this.root, "#benefitReadyIpLimit", `${claim.ipLimit} 个公网 IP`);
    setText(this.root, "#benefitReadyHwid", claim.hwidRequired ? "首次连接将绑定设备 HWID" : "不绑定设备 HWID");
    await renderSubscriptionQr(this.qrCanvas, claim.subscriptionUrl);
  }

  private renderError(error: unknown): void {
    this.lastError = error;
    const description = describeBenefitError(error);
    this.renderState("error", description.title);
    setText(this.root, "#benefitErrorTitle", description.title);
    setText(this.root, "#benefitErrorMessage", description.message);
    this.retryButton.hidden = !description.retryable;
  }

  private async retry(): Promise<void> {
    if (this.lastError instanceof BenefitApiError && this.lastError.code === "CLAIM_CREDENTIAL_REQUIRED") {
      location.reload();
      return;
    }
    this.lastError = null;
    if (!this.campaign) {
      await this.start();
      return;
    }
    try {
      const restored = await this.client.restoreClaim(this.requestController.signal);
      if (!this.isActive()) return;
      if (restored?.status === "ready") {
        await this.renderReady(restored);
        return;
      }
      if (restored?.status === "provisioning") {
        this.beginProvisioning(restored);
        return;
      }
      this.renderState("idle", "请重新完成人机验证");
      await this.prepareTurnstile();
    } catch (error) {
      if (!this.destroyed) this.renderError(error);
    }
  }

  private async copySubscription(): Promise<void> {
    if (!this.readyClaim) return;
    try {
      await navigator.clipboard.writeText(this.readyClaim.subscriptionUrl);
      this.setActionFeedback("订阅链接已复制");
    } catch {
      this.setActionFeedback("复制失败，请使用二维码导入");
    }
  }

  private downloadQrCode(): void {
    if (!this.readyClaim) return;
    this.qrCanvas.toBlob((blob) => {
      if (!blob) {
        this.setActionFeedback("二维码下载失败");
        return;
      }
      const objectUrl = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = objectUrl;
      anchor.download = "freedompost-webmaster-benefit.png";
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(objectUrl), 0);
      this.setActionFeedback("二维码已下载");
    }, "image/png");
  }

  private resetTurnstile(): void {
    this.token = "";
    this.claimButton.disabled = true;
    if (this.widgetId && window.turnstile) window.turnstile.reset(this.widgetId);
  }

  private renderState(state: BenefitPageState, status: string): void {
    this.root.dataset.state = state;
    this.root.setAttribute("aria-busy", String(
      state === "loading" || state === "verifying" || state === "claiming" || state === "provisioning"
    ));
    this.setStatus(status);
    const visiblePanel = benefitPanelForState(state);
    const progressCopy = progressCopyForState(state);
    if (progressCopy) {
      setText(this.root, "#benefitProgressTitle", progressCopy.title);
      setText(this.root, "#benefitProgressMessage", progressCopy.message);
    }
    this.root.querySelectorAll<HTMLElement>("[data-benefit-panel]").forEach((panel) => {
      panel.hidden = panel.dataset.benefitPanel !== visiblePanel;
    });
    if (state === "ready" || state === "error") this.focusVisiblePanelHeading(visiblePanel);
  }

  private setStatus(message: string): void {
    setText(this.root, "#benefitStatusText", message);
  }

  private setActionFeedback(message: string): void {
    setText(this.root, "#benefitActionFeedback", message);
  }

  private focusVisiblePanelHeading(panelName: string): void {
    const heading = this.root.querySelector<HTMLElement>(`[data-benefit-panel="${panelName}"] h2`);
    if (!heading) return;
    heading.tabIndex = -1;
    heading.focus({ preventScroll: true });
  }

  private isActive(): boolean {
    return !this.destroyed && this.root.isConnected;
  }
}

function loadTurnstile(): Promise<TurnstileApi> {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  if (turnstileLoader) return turnstileLoader;
  turnstileLoader = new Promise<TurnstileApi>((resolve, reject) => {
    const fail = (script?: HTMLScriptElement) => {
      script?.remove();
      turnstileLoader = null;
      reject(new BenefitApiError(503, "TURNSTILE_UNAVAILABLE"));
    };
    const complete = (script?: HTMLScriptElement) => window.turnstile
      ? resolve(window.turnstile)
      : fail(script);
    const existing = document.getElementById(turnstileScriptId) as HTMLScriptElement | null;
    if (existing) {
      existing.addEventListener("load", () => complete(existing), { once: true });
      existing.addEventListener("error", () => fail(existing), { once: true });
      return;
    }
    const script = document.createElement("script");
    script.id = turnstileScriptId;
    script.src = turnstileScriptUrl;
    script.async = true;
    script.defer = true;
    script.addEventListener("load", () => complete(script), { once: true });
    script.addEventListener("error", () => fail(script), { once: true });
    document.head.append(script);
  });
  return turnstileLoader;
}

function requiredElement<T extends Element>(root: ParentNode, selector: string, constructor: { new(): T }): T {
  const element = root.querySelector(selector);
  if (!(element instanceof constructor)) throw new Error(`Benefit page element is missing: ${selector}`);
  return element;
}

function setText(root: ParentNode, selector: string, value: string): void {
  const element = root.querySelector<HTMLElement>(selector);
  if (element) element.textContent = value;
}

function show(element: HTMLElement): void {
  element.hidden = false;
}

function formatBytes(value: number): string {
  return `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 0 }).format(value / 1024 / 1024 / 1024)} GB`;
}

function formatDateTime(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    timeZoneName: "short"
  }).format(new Date(value));
}

function progressCopyForState(state: BenefitPageState): { title: string; message: string } | null {
  return ({
    loading: { title: "正在准备", message: "系统正在恢复当前浏览器的领取状态。" },
    verifying: { title: "正在验证", message: "正在由服务端校验本次人机验证结果。" },
    claiming: { title: "正在领取", message: "正在创建这台浏览器专属的福利订阅。" },
    provisioning: { title: "正在创建订阅", message: "系统会自动检查结果，请勿重复提交。" }
  } as Partial<Record<BenefitPageState, { title: string; message: string }>>)[state] ?? null;
}
