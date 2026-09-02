export type ProductOperation = "create" | "save";

export type ProductField =
  | "title"
  | "summary"
  | "description"
  | "category"
  | "priceCents"
  | "commissionCents"
  | "compareAtCents"
  | "currency"
  | "stock"
  | "soldCount"
  | "coverUrl"
  | "linkUrl"
  | "status"
  | "sortOrder";

export type ProductFormValue = {
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
};

export type ProductIssue = {
  field: ProductField;
  code: string;
  message: string;
  resolution: string;
};

export type ProductSaveFailure = {
  title: string;
  reason: string;
  solution: string;
  code: string;
  requestId: string | undefined;
  issues: ProductIssue[];
  retryable: boolean;
  requiresReauthentication: boolean;
};

const MAX_MONEY_CENTS = 100_000_000;
const MAX_COUNT = 1_000_000;
const MAX_SORT_ORDER = 100_000;
const allowedFields = new Set<ProductField>([
  "title",
  "summary",
  "description",
  "category",
  "priceCents",
  "commissionCents",
  "compareAtCents",
  "currency",
  "stock",
  "soldCount",
  "coverUrl",
  "linkUrl",
  "status",
  "sortOrder"
]);
const allowedIssueCodes = new Set([
  "REQUIRED",
  "TOO_LONG",
  "OUT_OF_RANGE",
  "INVALID_VALUE",
  "INVALID_URL",
  "NOT_INTEGER",
  "COMPARE_AT_BELOW_PRICE"
]);

export function validateProductForSave(product: ProductFormValue): ProductIssue[] {
  // Keep these client-side checks aligned with normalizeProductInputWithIssues
  // so common mistakes can be corrected before a write request is sent.
  const issues: ProductIssue[] = [];
  const add = (field: ProductField, code: string, message: string, resolution: string) => {
    issues.push({ field, code, message, resolution });
  };

  const title = product.title.trim();
  if (!title) add("title", "REQUIRED", "商品名称不能为空", "请输入商品名称（最多 120 个字符）");
  else if (runeLength(title) > 120) add("title", "TOO_LONG", "商品名称不能超过 120 个字符", "请缩短商品名称后重试");

  const summary = product.summary.trim();
  if (!summary) add("summary", "REQUIRED", "一句话简介不能为空", "请输入一句话简介（最多 500 个字符）");
  else if (runeLength(summary) > 500) add("summary", "TOO_LONG", "一句话简介不能超过 500 个字符", "请缩短简介后重试");

  const description = product.description.trim();
  if (!description) add("description", "REQUIRED", "商品详情不能为空", "请输入商品详情（最多 12000 个字符）");
  else if (runeLength(description) > 12000) add("description", "TOO_LONG", "商品详情不能超过 12000 个字符", "请缩短商品详情后重试");

  if (runeLength(product.category.trim() || "other") > 32) add("category", "TOO_LONG", "商品分类不能超过 32 个字符", "请选择有效的商品分类");
  if (runeLength(product.currency.trim() || "CNY") > 8) add("currency", "TOO_LONG", "币种代码不能超过 8 个字符", "请选择支持的币种");
  if (product.status !== "draft" && product.status !== "published") add("status", "INVALID_VALUE", "发布状态无效", "请选择草稿或公开发布");

  addIntegerRangeIssue(issues, "priceCents", product.priceCents, 0, MAX_MONEY_CENTS, "价格", "请修改商品价格后重试");
  addIntegerRangeIssue(issues, "commissionCents", product.commissionCents, 0, MAX_MONEY_CENTS, "分销佣金", "请修改分销佣金后重试");
  addIntegerRangeIssue(issues, "stock", product.stock, -1, MAX_COUNT, "库存", "请修改库存，-1 表示不限量");
  addIntegerRangeIssue(issues, "soldCount", product.soldCount, 0, MAX_COUNT, "已售出数量", "请修改已售出数量后重试");
  addIntegerRangeIssue(issues, "sortOrder", product.sortOrder, -MAX_SORT_ORDER, MAX_SORT_ORDER, "排序", "请修改排序后重试");

  if (!validProductUrl(product.coverUrl ?? "")) add("coverUrl", "INVALID_URL", "商品封面地址无效", "请重新上传封面，或清空无效地址");
  if (!validProductUrl(product.linkUrl ?? "")) add("linkUrl", "INVALID_URL", "商品链接地址无效", "请使用站内路径或 http/https 地址");

  if (product.compareAtCents !== null) {
    if (!Number.isInteger(product.compareAtCents)) {
      add("compareAtCents", "NOT_INTEGER", "划线价格式无效", "请填写最多两位小数的金额，或留空");
    } else if (product.compareAtCents < 0 || product.compareAtCents > MAX_MONEY_CENTS) {
      add("compareAtCents", "OUT_OF_RANGE", "划线价必须在 0 到 1000000.00 之间", "请修改划线价，或留空");
    } else if (product.compareAtCents < product.priceCents) {
      add(
        "compareAtCents",
        "COMPARE_AT_BELOW_PRICE",
        "划线价不能低于售价",
        `请将划线价设为不低于 ${formatPrice(product.priceCents)} ${product.currency || "CNY"}，或留空`
      );
    }
  }

  return issues;
}

export function failureFromProductIssues(issues: ProductIssue[], operation: ProductOperation): ProductSaveFailure {
  return {
    title: operation === "create" ? "商品创建失败" : "商品保存失败",
    reason: `有 ${issues.length} 项商品信息需要修改。`,
    solution: "请按下面的提示修改字段后再次保存。",
    code: "INVALID_PRODUCT",
    requestId: undefined,
    issues,
    retryable: false,
    requiresReauthentication: false
  };
}

export async function readProductApiFailure(response: Response, operation: ProductOperation): Promise<ProductSaveFailure> {
  const requestId = safeRequestId(response.headers.get("X-Request-ID"));
  const payload = await response.json().catch(() => null) as unknown;
  const error = readErrorEnvelope(payload);
  const base = { requestId, issues: [] as ProductIssue[], requiresReauthentication: false };

  if (response.status === 401 || error?.code === "UNAUTHENTICATED") {
    return {
      ...base,
      title: "登录已过期",
      reason: "后台登录状态已失效，服务器没有保存本次修改。",
      solution: "请在当前页面重新登录；未保存内容会保留，登录成功后请再次保存。",
      code: "UNAUTHENTICATED",
      retryable: false,
      requiresReauthentication: true
    };
  }

  if (response.status === 404 || error?.code === "PRODUCT_NOT_FOUND") {
    return {
      ...base,
      title: operation === "create" ? "商品创建失败" : "商品已不存在",
      reason: operation === "create" ? "商品创建接口不存在。" : "该商品可能已被其他管理员删除，服务器没有保存本次修改。",
      solution: operation === "create" ? "请刷新后台后重试。" : "请保留当前内容并刷新商品列表；需要时可新建商品后重新填写。",
      code: "PRODUCT_NOT_FOUND",
      retryable: false
    };
  }

  if (response.status === 400 && error?.code === "INVALID_PRODUCT") {
    const issues = readProductIssues(error.issues);
    if (issues.length > 0) return { ...failureFromProductIssues(issues, operation), requestId };
    return {
      ...base,
      title: operation === "create" ? "商品创建失败" : "商品保存失败",
      reason: "服务器判定商品信息不完整或格式不正确。",
      solution: "请检查必填项、金额、库存和发布状态后重试；持续失败时请提供错误编号。",
      code: "INVALID_PRODUCT",
      retryable: false
    };
  }

  if (response.status === 400 && error?.code === "INVALID_JSON") {
    return {
      ...base,
      title: operation === "create" ? "商品创建失败" : "商品保存失败",
      reason: "服务器无法读取后台提交的商品数据。",
      solution: "请刷新后台后重试；若仍然失败，请提供错误编号给开发者。",
      code: "INVALID_JSON",
      retryable: true
    };
  }

  if (response.status >= 500 || error?.code === "INTERNAL_ERROR") {
    return {
      ...base,
      title: operation === "create" ? "商品创建失败" : "商品保存失败",
      reason: "服务器暂时无法完成商品写入，未保存本次修改。",
      solution: "请稍后重试；持续失败时请复制错误编号给开发者查询日志。",
      code: "INTERNAL_ERROR",
      retryable: true
    };
  }

  return {
    ...base,
    title: operation === "create" ? "商品创建失败" : "商品保存失败",
    reason: `服务器拒绝了请求（HTTP ${response.status}）。`,
    solution: "请稍后重试；持续失败时请提供错误编号给开发者。",
    code: `HTTP_${response.status}`,
    retryable: response.status >= 408
  };
}

export function networkProductFailure(operation: ProductOperation): ProductSaveFailure {
  return {
    title: operation === "create" ? "商品创建失败" : "商品保存失败",
    reason: "无法连接服务器，未保存本次修改。",
    solution: "请检查网络连接和后台服务状态，然后重试。",
    code: "NETWORK_ERROR",
    requestId: undefined,
    issues: [],
    retryable: true,
    requiresReauthentication: false
  };
}

export function invalidProductResponseFailure(operation: ProductOperation, response: Response): ProductSaveFailure {
  return {
    title: operation === "create" ? "商品创建失败" : "商品保存失败",
    reason: "服务器返回了无法识别的成功响应，当前页面未应用该结果。",
    solution: "请刷新商品列表确认服务器状态；持续出现时请提供错误编号。",
    code: "INVALID_RESPONSE",
    requestId: safeRequestId(response.headers.get("X-Request-ID")),
    issues: [],
    retryable: true,
    requiresReauthentication: false
  };
}

export async function readReauthenticationFailure(response: Response): Promise<string> {
  const payload = await response.json().catch(() => null) as unknown;
  const code = readErrorEnvelope(payload)?.code;
  if (code === "LOGIN_FAILED" || response.status === 401) return "用户名或密码不正确";
  if (code === "RATE_LIMITED" || response.status === 429) return "登录尝试过多，请稍后再试";
  if (response.status >= 500) return "服务器暂时无法恢复登录，请稍后再试";
  return `重新登录失败（HTTP ${response.status}）`;
}

function addIntegerRangeIssue(
  issues: ProductIssue[],
  field: ProductField,
  value: number,
  minimum: number,
  maximum: number,
  label: string,
  resolution: string
) {
  if (!Number.isInteger(value)) {
    issues.push({ field, code: "NOT_INTEGER", message: `${label}必须是整数`, resolution });
  } else if (value < minimum || value > maximum) {
    issues.push({ field, code: "OUT_OF_RANGE", message: `${label}必须在 ${minimum} 到 ${maximum} 之间`, resolution });
  }
}

function runeLength(value: string): number {
  return [...value].length;
}

function formatPrice(cents: number): string {
  return (cents / 100).toFixed(2);
}

function validProductUrl(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return true;
  if (trimmed.length > 2000 || trimmed.startsWith("//")) return false;
  const scheme = /^([a-z][a-z\d+.-]*):/i.exec(trimmed)?.[1]?.toLowerCase();
  if (scheme && scheme !== "http" && scheme !== "https") return false;
  try {
    const parsed = new URL(trimmed, "https://admin.invalid");
    return (parsed.protocol === "http:" || parsed.protocol === "https:") && (!scheme || Boolean(parsed.host));
  } catch {
    return false;
  }
}

function readErrorEnvelope(payload: unknown): { code: string; issues?: unknown } | null {
  if (!payload || typeof payload !== "object") return null;
  const error = (payload as { error?: unknown }).error;
  if (!error || typeof error !== "object") return null;
  const code = (error as { code?: unknown }).code;
  if (typeof code !== "string") return null;
  return { code, issues: (error as { issues?: unknown }).issues };
}

function readProductIssues(value: unknown): ProductIssue[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((candidate) => {
    if (!candidate || typeof candidate !== "object") return [];
    const issue = candidate as Record<string, unknown>;
    if (typeof issue.field !== "string" || !allowedFields.has(issue.field as ProductField)) return [];
    if (typeof issue.code !== "string" || !allowedIssueCodes.has(issue.code)) return [];
    if (typeof issue.message !== "string" || typeof issue.resolution !== "string") return [];
    const message = safeDisplayText(issue.message);
    const resolution = safeDisplayText(issue.resolution);
    if (!message || !resolution) return [];
    return [{ field: issue.field as ProductField, code: issue.code, message, resolution }];
  });
}

function safeDisplayText(value: string): string {
  return value.replace(/[\u0000-\u001f\u007f]/g, " ").trim().slice(0, 300);
}

function safeRequestId(value: string | null): string | undefined {
  const requestId = value?.trim();
  return requestId && /^[A-Za-z0-9_-]{8,128}$/.test(requestId) ? requestId : undefined;
}
