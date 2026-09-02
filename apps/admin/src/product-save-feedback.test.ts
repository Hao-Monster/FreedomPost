import { describe, expect, it } from "vitest";
import {
  failureFromProductIssues,
  networkProductFailure,
  readProductApiFailure,
  readReauthenticationFailure,
  validateProductForSave,
  type ProductFormValue
} from "./product-save-feedback";

const validProduct: ProductFormValue = {
  title: "远程安装服务",
  summary: "协助完成远程安装",
  description: "预约后联系管理员安排服务。",
  category: "service",
  priceCents: 3000,
  commissionCents: 500,
  compareAtCents: null,
  currency: "CNY",
  stock: -1,
  soldCount: 19,
  coverUrl: "/api/uploads/product.png",
  linkUrl: "",
  status: "published",
  sortOrder: 2
};

describe("validateProductForSave", () => {
  it("explains the screenshot case when compare-at price is below the sale price", () => {
    const issues = validateProductForSave({ ...validProduct, compareAtCents: 500 });

    expect(issues).toEqual([
      expect.objectContaining({
        field: "compareAtCents",
        code: "COMPARE_AT_BELOW_PRICE",
        message: "划线价不能低于售价"
      })
    ]);
    expect(issues[0]?.resolution).toContain("30.00 CNY");
  });

  it("accepts an empty, equal, or higher compare-at price", () => {
    expect(validateProductForSave(validProduct)).toEqual([]);
    expect(validateProductForSave({ ...validProduct, compareAtCents: 3000 })).toEqual([]);
    expect(validateProductForSave({ ...validProduct, compareAtCents: 5000 })).toEqual([]);
  });

  it("reports all relevant field problems instead of one generic error", () => {
    const issues = validateProductForSave({
      ...validProduct,
      title: " ",
      summary: "",
      description: "",
      priceCents: -1,
      commissionCents: 100_000_001,
      stock: -2,
      soldCount: 1.5,
      sortOrder: 100_001,
      coverUrl: "javascript:alert(1)",
      status: "invalid" as ProductFormValue["status"]
    });

    expect(new Set(issues.map((issue) => issue.field))).toEqual(new Set([
      "title",
      "summary",
      "description",
      "priceCents",
      "commissionCents",
      "stock",
      "soldCount",
      "sortOrder",
      "coverUrl",
      "status"
    ]));
  });
});

describe("product API failures", () => {
  it("keeps allowlisted field details and the server request id", async () => {
    const response = new Response(JSON.stringify({
      error: {
        code: "INVALID_PRODUCT",
        message: "请修正 1 项商品信息",
        issues: [{
          field: "compareAtCents",
          code: "COMPARE_AT_BELOW_PRICE",
          message: "划线价不能低于售价",
          resolution: "请将划线价设为不低于 30.00 CNY，或留空"
        }]
      }
    }), {
      status: 400,
      headers: { "Content-Type": "application/json", "X-Request-ID": "277b75d0-535e-4b7c-99dc-5ae46fbc540d" }
    });

    const failure = await readProductApiFailure(response, "save");

    expect(failure.code).toBe("INVALID_PRODUCT");
    expect(failure.requestId).toBe("277b75d0-535e-4b7c-99dc-5ae46fbc540d");
    expect(failure.issues).toHaveLength(1);
    expect(failure.issues[0]?.field).toBe("compareAtCents");
  });

  it("does not expose unknown server or database details", async () => {
    const internalDetail = "duplicate key products_slug_key at postgres.internal";
    const response = new Response(JSON.stringify({ error: { code: "PG_ERROR", message: internalDetail } }), { status: 500 });

    const failure = await readProductApiFailure(response, "save");

    expect(JSON.stringify(failure)).not.toContain(internalDetail);
    expect(failure.code).toBe("INTERNAL_ERROR");
    expect(failure.solution).toContain("稍后重试");
  });

  it("drops non-allowlisted field issue details", async () => {
    const internalDetail = "constraint products_slug_key at postgres.internal";
    const response = new Response(JSON.stringify({
      error: {
        code: "INVALID_PRODUCT",
        message: "invalid",
        issues: [{ field: "title", code: "SQL_DETAIL", message: internalDetail, resolution: internalDetail }]
      }
    }), { status: 400 });

    const failure = await readProductApiFailure(response, "save");

    expect(failure.issues).toEqual([]);
    expect(JSON.stringify(failure)).not.toContain(internalDetail);
  });

  it("turns an expired session into an in-place reauthentication action", async () => {
    const response = new Response(JSON.stringify({ error: { code: "UNAUTHENTICATED", message: "未登录" } }), { status: 401 });

    const failure = await readProductApiFailure(response, "save");

    expect(failure.requiresReauthentication).toBe(true);
    expect(failure.reason).toContain("登录状态已失效");
    expect(failure.solution).toContain("未保存内容会保留");
  });

  it("provides retry guidance for a network failure", () => {
    const failure = networkProductFailure("save");

    expect(failure.retryable).toBe(true);
    expect(failure.reason).toContain("无法连接");
  });

  it("builds a persistent validation failure from local issues", () => {
    const failure = failureFromProductIssues(validateProductForSave({ ...validProduct, compareAtCents: 500 }), "save");

    expect(failure.issues[0]?.field).toBe("compareAtCents");
    expect(failure.retryable).toBe(false);
  });
});

describe("reauthentication failures", () => {
  it("shows a safe credential error without treating it as another expired session", async () => {
    const response = new Response(JSON.stringify({ error: { code: "LOGIN_FAILED", message: "用户名或密码不正确" } }), { status: 401 });

    const failure = await readReauthenticationFailure(response);

    expect(failure).toBe("用户名或密码不正确");
  });
});
