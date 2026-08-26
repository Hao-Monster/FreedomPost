#!/usr/bin/env node
/**
 * Go API 功能测试 & 系统测试
 * 运行方式：
 *   node services/api/test/integration.mjs [--base-url http://localhost:3001]
 *
 * 测试范围：
 *   1. 健康检查
 *   2. 文章 CRUD（Admin API）
 *   3. 评论创建
 *   4. 搜索索引
 *   5. 推广员注册 / 登录 / Dashboard
 *   6. 佣金计算验证（黄金值对比）
 *   7. 限流验证
 *   8. 上传文件（MIME 检测）
 */

import assert from "node:assert/strict";
import { setTimeout as sleep } from "node:timers/promises";

const BASE = process.argv[2] === "--base-url"
  ? process.argv[3]
  : (process.env.GO_API_URL ?? "http://localhost:3001");
const PAID_BASE = process.env.PAID_ACCESS_URL ?? "http://localhost:8080";

const ADMIN_USER = process.env.ADMIN_USERNAME ?? "admin";
const ADMIN_PASS = process.env.ADMIN_PASSWORD ?? "test-password-123";

let passed = 0;
let failed = 0;
const failures = [];

// ─── Test runner ──────────────────────────────────────────────────────────────

async function test(name, fn) {
  process.stdout.write(`  ${name} ... `);
  try {
    await fn();
    console.log("✅ PASS");
    passed++;
  } catch (err) {
    console.log(`❌ FAIL`);
    console.error(`     ${err.message}`);
    failed++;
    failures.push({ name, error: err.message });
  }
}

function section(name) {
  console.log(`\n📋 ${name}`);
}

async function api(method, path, { body, cookies, headers } = {}) {
  const opts = {
    method,
    headers: {
      "Content-Type": "application/json",
      ...(cookies ? { Cookie: cookies } : {}),
      ...headers,
    },
    ...(body != null ? { body: JSON.stringify(body) } : {}),
  };
  const res = await fetch(`${BASE}${path}`, opts);
  const text = await res.text();
  let json;
  try { json = JSON.parse(text); } catch {}
  return { status: res.status, body: json ?? text, headers: res.headers };
}

// ─── 1. 健康检查 ──────────────────────────────────────────────────────────────

section("1. 健康检查");

await test("GET /health → 200 ok:true", async () => {
  const r = await api("GET", "/health");
  assert.equal(r.status, 200);
  assert.equal(r.body.ok, true);
});

await test("GET /health/ready → 200 postgres:ok", async () => {
  const r = await api("GET", "/health/ready");
  assert.equal(r.status, 200);
  assert.equal(r.body.ok, true);
  assert.equal(r.body.checks?.postgres, "ok");
});

// ─── 2. Admin 认证 ────────────────────────────────────────────────────────────

section("2. Admin 认证");

let adminCookie = "";

await test("POST /api/admin/login → 200 session", async () => {
  const r = await api("POST", "/api/admin/login", {
    body: { username: ADMIN_USER, password: ADMIN_PASS },
  });
  assert.equal(r.status, 200, `body: ${JSON.stringify(r.body)}`);
  assert.ok(r.body.session?.username);
  const setCookie = r.headers.get("set-cookie") ?? "";
  assert.ok(setCookie.includes("fp_admin_session"), "should set admin session cookie");
  adminCookie = `fp_admin_session=${setCookie.match(/fp_admin_session=([^;]+)/)?.[1] ?? ""}`;
});

await test("POST /api/admin/login (wrong pw) → 401", async () => {
  const r = await api("POST", "/api/admin/login", {
    body: { username: ADMIN_USER, password: "wrong-password" },
  });
  assert.equal(r.status, 401);
  assert.equal(r.body.error?.code, "LOGIN_FAILED");
});

await test("GET /api/admin/session → 200 with cookie", async () => {
  const r = await api("GET", "/api/admin/session", { cookies: adminCookie });
  assert.equal(r.status, 200);
  assert.equal(r.body.session?.username, ADMIN_USER);
});

await test("GET /api/admin/session (no cookie) → 401", async () => {
  const r = await api("GET", "/api/admin/session");
  assert.equal(r.status, 401);
});

await test("paid-access 管理代理使用正确签名路径 → 200", async () => {
  const [orders, accounts] = await Promise.all([
    api("GET", "/api/admin/article-orders", { cookies: adminCookie }),
    api("GET", "/api/admin/reader-accounts", { cookies: adminCookie }),
  ]);
  assert.equal(orders.status, 200, `orders body: ${JSON.stringify(orders.body)}`);
  assert.equal(accounts.status, 200, `accounts body: ${JSON.stringify(accounts.body)}`);
  assert.ok(Array.isArray(orders.body.items));
  assert.ok(Array.isArray(accounts.body.items));
});

// ─── 3. 文章 CRUD ─────────────────────────────────────────────────────────────

section("3. 文章 CRUD");

let postId = "";
let postSlug = "";
let paidPostId = "";
let paidPostSlug = "";
const paidSecretMarker = `PAID-SECRET-${Date.now()}`;

await test("POST /api/admin/posts → 201 创建文章", async () => {
  const r = await api("POST", "/api/admin/posts", {
    cookies: adminCookie,
    body: {
      title: "测试文章 Go API",
      visibility: "public",
      priceCents: 0,
      currency: "CNY",
      content: "# 标题\n\n这是**测试**内容，包含 `代码` 和列表：\n\n- 项目 1\n- 项目 2\n\n:::callout 💡\n这是一个 callout 块\n:::",
    },
  });
  assert.equal(r.status, 201, `body: ${JSON.stringify(r.body)}`);
  assert.ok(r.body.post?.id);
  assert.ok(r.body.post?.slug);
  assert.ok(r.body.post?.contentHtml?.includes("<h1") || r.body.post?.content_html?.includes("<h1"), "HTML should contain h1");
  postId = r.body.post?.id ?? r.body.post?.id;
  postSlug = r.body.post?.slug;
});

await test("GET /api/posts → 200 包含新文章", async () => {
  const r = await api("GET", "/api/posts");
  assert.equal(r.status, 200);
  assert.ok(Array.isArray(r.body.posts));
  const found = r.body.posts.some(p => p.slug === postSlug);
  assert.ok(found, `post slug=${postSlug} not found in list`);
});

await test("GET /api/posts/:slug → 200 文章详情", async () => {
  const r = await api("GET", `/api/posts/${postSlug}`);
  assert.equal(r.status, 200);
  assert.equal(r.body.post?.slug ?? r.body.post?.slug, postSlug);
});

await test("GET /api/posts/non-existent-slug → 404", async () => {
  const r = await api("GET", "/api/posts/this-does-not-exist-xyz");
  assert.equal(r.status, 404);
});

await test("PUT /api/admin/posts/:id → 200 更新文章", async () => {
  const r = await api("PUT", `/api/admin/posts/${postId}`, {
    cookies: adminCookie,
    body: {
      title: "测试文章 Go API（已更新）",
      visibility: "public",
      priceCents: 0,
      currency: "CNY",
      content: "# 更新后标题\n\n已更新内容。",
    },
  });
  assert.equal(r.status, 200);
  assert.ok((r.body.post?.title ?? "").includes("已更新"));
});

await test("POST /api/admin/posts → 201 创建付费文章", async () => {
  const r = await api("POST", "/api/admin/posts", {
    cookies: adminCookie,
    body: {
      title: "付费文章安全回归",
      visibility: "paid",
      priceCents: 199,
      currency: "CNY",
      content: `# 付费内容\n\n${paidSecretMarker}`,
    },
  });
  assert.equal(r.status, 201, `body: ${JSON.stringify(r.body)}`);
  paidPostId = r.body.post?.id;
  paidPostSlug = r.body.post?.slug;
  assert.ok(paidPostId && paidPostSlug);
});

await test("公开文章接口不会泄露付费正文或摘要", async () => {
  const [detail, list, paidDetail] = await Promise.all([
    api("GET", `/api/posts/${paidPostSlug}`),
    api("GET", "/api/posts"),
    fetch(`${PAID_BASE}/api/reader/posts/${paidPostSlug}`),
  ]);
  assert.equal(detail.status, 200);
  assert.equal(paidDetail.status, 200);
  const paidBody = await paidDetail.text();
  assert.ok(!JSON.stringify(detail.body).includes(paidSecretMarker), "main API leaked paid content");
  assert.ok(!paidBody.includes(paidSecretMarker), "paid-access locked response leaked paid content");
  const summary = list.body.posts.find((post) => post.slug === paidPostSlug);
  assert.ok(summary, "paid post missing from list metadata");
  assert.equal(summary.excerpt ?? "", "");
  assert.equal(detail.body.post?.contentHtml ?? "", "");
  assert.equal(detail.body.post?.contentMarkdown ?? "", "");
  assert.equal(detail.body.post?.searchText ?? "", "");
});

await test("未购买读者无法读取、创建或上传付费评论", async () => {
  const list = await api("GET", `/api/posts/${paidPostSlug}/comments`);
  const create = await api("POST", `/api/posts/${paidPostSlug}/comments`, {
    body: { content: "越权评论", localId: "paid-denied", attachments: [] },
  });
  const form = new FormData();
  form.append("file", new Blob(["denied"], { type: "text/plain" }), "denied.txt");
  const upload = await fetch(`${BASE}/api/posts/${paidPostSlug}/comment-attachments`, {
    method: "POST",
    body: form,
  });
  assert.equal(list.status, 404);
  assert.equal(create.status, 404);
  assert.equal(upload.status, 404);
});

// ─── 4. 浏览量计数 ────────────────────────────────────────────────────────────

section("4. 浏览量计数");

await test("POST /api/posts/:slug/view → 200 counted:true", async () => {
  const r = await api("POST", `/api/posts/${postSlug}/view`, {
    body: {
      fingerprint: `test-fingerprint-${Math.random()}`,
      localId: `test-local-${Math.random()}`,
    },
  });
  assert.equal(r.status, 200);
  assert.equal(r.body.counted, true);
  assert.ok(r.body.viewCount > 0);
});

await test("POST /api/posts/:slug/view (同 visitor) → counted:false（去重）", async () => {
  const localId = `dedup-test-${Date.now()}`;
  await api("POST", `/api/posts/${postSlug}/view`, { body: { localId } });
  const r = await api("POST", `/api/posts/${postSlug}/view`, { body: { localId } });
  assert.equal(r.status, 200);
  assert.equal(r.body.counted, false);
});

await test("POST /api/posts/:slug/view 拒绝客户端 visitorKey/viewDate", async () => {
  const r = await api("POST", `/api/posts/${postSlug}/view`, {
    body: { localId: "server-authoritative", visitorKey: "forged", viewDate: "2099-01-01" },
  });
  assert.equal(r.status, 400);
  assert.equal(r.body.error?.code, "INVALID_JSON");
});

// ─── 5. 搜索索引 ─────────────────────────────────────────────────────────────

section("5. 搜索索引");

await test("GET /api/search-index → 200 含新文章", async () => {
  const r = await api("GET", "/api/search-index");
  assert.equal(r.status, 200);
  assert.equal(typeof r.body.version, "string");
  assert.ok(Array.isArray(r.body.documents));
  const found = r.body.documents.some(d => d.slug === postSlug);
  assert.ok(found, `slug=${postSlug} not in search index`);
  const doc = r.body.documents.find(d => d.slug === postSlug);
  assert.ok(doc.body?.length > 0, "body (search text) should not be empty");
  assert.ok(doc.excerpt?.length > 0, "excerpt should not be empty");
  assert.ok(!r.body.documents.some(d => d.slug === paidPostSlug), "paid post entered public search index");
});

// ─── 6. 评论 ─────────────────────────────────────────────────────────────────

section("6. 评论");

let commentId = "";

await test("POST /api/posts/:slug/comments → 201 创建评论", async () => {
  const r = await api("POST", `/api/posts/${postSlug}/comments`, {
    body: {
      content: "这是一条测试评论，内容很精彩！",
      fingerprint: "test-fingerprint-abc123",
      localId: "test-local-id",
      attachments: [],
    },
  });
  assert.equal(r.status, 201, `body: ${JSON.stringify(r.body)}`);
  assert.ok(r.body.comment?.id);
  assert.ok(r.body.comment?.username?.length > 0);
  commentId = r.body.comment.id;
});

await test("GET /api/posts/:slug/comments → 200 含新评论", async () => {
  const r = await api("GET", `/api/posts/${postSlug}/comments`);
  assert.equal(r.status, 200);
  assert.ok(Array.isArray(r.body.comments));
  const found = r.body.comments.some(c => c.id === commentId);
  assert.ok(found, "newly created comment not found");
});

await test("POST /api/posts/:slug/comments (嵌套回复) → 201 depth:1", async () => {
  const r = await api("POST", `/api/posts/${postSlug}/comments`, {
    body: {
      parentId: commentId,
      content: "这是一条回复",
      fingerprint: "test-fingerprint-reply",
      localId: "test-local-reply",
      attachments: [],
    },
  });
  assert.equal(r.status, 201);
  assert.equal(r.body.comment?.depth, 1);
  assert.equal(r.body.comment?.parentId ?? r.body.comment?.parent_id, commentId);
});

// ─── 7. 商品管理 ─────────────────────────────────────────────────────────────

section("7. 商品管理");

let productId = "";
let productSlug = "";

await test("POST /api/admin/products → 201 创建商品", async () => {
  const r = await api("POST", "/api/admin/products", {
    cookies: adminCookie,
    body: {
      title: "测试商品",
      summary: "这是一条商品简介",
      description: "这是一个测试商品",
      category: "service",
      imageUrl: "https://example.com/img.jpg",
      linkUrl: "https://example.com/product",
      priceCents: 9900,
      compareAtCents: null,
      currency: "CNY",
      commissionCents: 500,
      stock: -1,
      soldCount: 0,
      status: "published",
      sortOrder: 0,
    },
  });
  assert.equal(r.status, 201, `body: ${JSON.stringify(r.body)}`);
  assert.ok(r.body.product?.id);
  productId = r.body.product.id;
  productSlug = r.body.product.slug;
});

await test("GET /api/products → 200 含新商品", async () => {
  const r = await api("GET", "/api/products");
  assert.equal(r.status, 200);
  const found = r.body.products?.some(p => p.id === productId);
  assert.ok(found, "new product not in public list");
});

// ─── 8. 推广员流程 + 佣金计算 ────────────────────────────────────────────────

section("8. 推广员流程 + 佣金计算");

const testWechat = `test-wechat-${Date.now()}`;
let affiliateCookie = "";
let affiliatePassword = "";
let affiliateId = "";

await test("POST /api/affiliate/access (自动注册) → 200", async () => {
  const r = await api("POST", "/api/affiliate/access", {
    body: { wechatId: testWechat },
  });
  assert.equal(r.status, 200, `body: ${JSON.stringify(r.body)}`);
  affiliateId = r.body.session?.affiliateId ?? r.body.session?.affiliate_id;
  affiliatePassword = r.body.generatedPassword;
  assert.ok(affiliateId);
  assert.match(affiliatePassword, /^.{8,72}$/);
  const setCookie = r.headers.get("set-cookie") ?? "";
  affiliateCookie = `fp_affiliate_session=${setCookie.match(/fp_affiliate_session=([^;]+)/)?.[1] ?? ""}`;
});

await test("POST /api/affiliate/access (再次登录) → 200", async () => {
  const r = await api("POST", "/api/affiliate/access", {
    body: { wechatId: testWechat, password: affiliatePassword },
  });
  assert.equal(r.status, 200);
});

await test("POST /api/affiliate/access (错误密码) → 401", async () => {
  const r = await api("POST", "/api/affiliate/access", {
    body: { wechatId: testWechat, password: "wrong-pass" },
  });
  assert.equal(r.status, 401);
  assert.equal(r.body.error?.code, "INVALID_PASSWORD");
});

await test("GET /api/affiliate/dashboard → 200", async () => {
  const r = await api("GET", "/api/affiliate/dashboard", { cookies: affiliateCookie });
  assert.equal(r.status, 200, `body: ${JSON.stringify(r.body)}`);
  assert.ok(r.body.dashboard);
});

await test("POST /api/affiliate/clicks 使用服务端访客键去重", async () => {
  const localId = `click-local-${Date.now()}`;
  const first = await api("POST", "/api/affiliate/clicks", {
    body: { ref: testWechat, localId, path: "/market/" },
  });
  const second = await api("POST", "/api/affiliate/clicks", {
    body: { ref: testWechat, localId, path: "/market/" },
  });
  const forged = await api("POST", "/api/affiliate/clicks", {
    body: { ref: testWechat, localId: `${localId}-other`, path: "/market/", visitorKey: "forged" },
  });
  assert.equal(first.status, 200);
  assert.equal(first.body.isUnique, true);
  assert.equal(second.status, 200);
  assert.equal(second.body.isUnique, false);
  assert.equal(forged.status, 400);
});

await test("GET /api/affiliate/catalog → 200 含商品", async () => {
  const r = await api("GET", "/api/affiliate/catalog", { cookies: affiliateCookie });
  assert.equal(r.status, 200);
  assert.ok(Array.isArray(r.body.products));
  const prod = r.body.products.find(p => p.id === productId);
  assert.ok(prod, "test product not in affiliate catalog");
  // 验证 0% markup：价格不变，baseCommission = 500
  assert.equal(prod.customerPriceCents ?? prod.customer_price_cents, 9900);
  assert.equal(prod.baseCommissionCents ?? prod.base_commission_cents, 500);
  assert.equal(prod.markupCommissionCents ?? prod.markup_commission_cents, 0);
  assert.equal(prod.commissionCents ?? prod.commission_cents, 500);
});

await test("PATCH /api/affiliate/markups → 200 设置 10% markup", async () => {
  const r = await api("PATCH", "/api/affiliate/markups", {
    cookies: affiliateCookie,
    body: { productIds: [productId], markupPercent: 10 },
  });
  assert.equal(r.status, 200);
});

await test("GET /api/affiliate/catalog → 10% markup 佣金计算验证", async () => {
  const r = await api("GET", "/api/affiliate/catalog", { cookies: affiliateCookie });
  const prod = r.body.products.find(p => p.id === productId);
  assert.ok(prod, "product not found after markup");
  // 9900 * 1.1 = 10890
  assert.equal(prod.customerPriceCents ?? prod.customer_price_cents, 10890, "10% markup: 9900→10890");
  assert.equal(prod.markupCommissionCents ?? prod.markup_commission_cents, 990, "markup profit: 10890-9900=990");
  assert.equal(prod.baseCommissionCents ?? prod.base_commission_cents, 500, "base commission unchanged");
  assert.equal(prod.commissionCents ?? prod.commission_cents, 1490, "total: 500+990=1490");
});

await test("POST /api/orders 拒绝客户端价格/加价字段", async () => {
  const r = await api("POST", "/api/orders", {
    body: {
      productId,
      productSlug,
      recommenderWechatId: testWechat,
      markupPercent: -100,
      priceCents: 1,
    },
  });
  assert.equal(r.status, 400);
  assert.equal(r.body.error?.code, "INVALID_JSON");
});

await test("POST /api/orders (公开契约 + 服务端定价) → 201", async () => {
  const r = await api("POST", "/api/orders", {
    body: { productSlug, recommenderWechatId: testWechat },
  });
  assert.equal(r.status, 201, `body: ${JSON.stringify(r.body)}`);
  const order = r.body.order;
  assert.equal(order.customerPriceCents ?? order.customer_price_cents, 10890);
  assert.equal(order.baseCommissionCents ?? order.base_commission_cents, 500);
  assert.equal(order.markupCommissionCents ?? order.markup_commission_cents, 990);
  assert.equal(order.commissionCents ?? order.commission_cents, 1490);
});

await test("推广员改密后旧会话立即失效", async () => {
  const reset = await api("POST", `/api/admin/affiliates/${affiliateId}/reset-password`, {
    cookies: adminCookie,
    body: { password: "new-affiliate-password-123" },
  });
  assert.equal(reset.status, 200, `body: ${JSON.stringify(reset.body)}`);
  const stale = await api("GET", "/api/affiliate/dashboard", { cookies: affiliateCookie });
  assert.equal(stale.status, 401);

  const login = await api("POST", "/api/affiliate/access", {
    body: { wechatId: testWechat, password: "new-affiliate-password-123" },
  });
  assert.equal(login.status, 200, `body: ${JSON.stringify(login.body)}`);
  const setCookie = login.headers.get("set-cookie") ?? "";
  affiliateCookie = `fp_affiliate_session=${setCookie.match(/fp_affiliate_session=([^;]+)/)?.[1] ?? ""}`;
});

await test("推广员停用后旧会话和新登录均被拒绝", async () => {
  const disable = await api("PATCH", `/api/admin/affiliates/${affiliateId}`, {
    cookies: adminCookie,
    body: { status: "disabled" },
  });
  assert.equal(disable.status, 200);
  const stale = await api("GET", "/api/affiliate/dashboard", { cookies: affiliateCookie });
  assert.equal(stale.status, 401);
  const login = await api("POST", "/api/affiliate/access", {
    body: { wechatId: testWechat, password: "new-affiliate-password-123" },
  });
  assert.equal(login.status, 403);
  assert.equal(login.body.error?.code, "AFFILIATE_DISABLED");
});

// ─── 9. 上传安全 ─────────────────────────────────────────────────────────────

section("9. 上传安全");

const minPng = Buffer.from([
  0x89,0x50,0x4e,0x47,0x0d,0x0a,0x1a,0x0a,
  0x00,0x00,0x00,0x0d,0x49,0x48,0x44,0x52,
  0x00,0x00,0x00,0x01,0x00,0x00,0x00,0x01,
  0x08,0x02,0x00,0x00,0x00,0x90,0x77,0x53,0xde,
  0x00,0x00,0x00,0x0c,0x49,0x44,0x41,0x54,
  0x08,0xd7,0x63,0xf8,0xcf,0xc0,0x00,0x00,0x00,0x02,0x00,0x01,
  0xe2,0x21,0xbc,0x33,0x00,0x00,0x00,0x00,
  0x49,0x45,0x4e,0x44,0xae,0x42,0x60,0x82,
]);
let uploadedCommentFile;

await test("POST /api/posts/:slug/comment-attachments (合法图片) → 201 + claimToken", async () => {
  const form = new FormData();
  form.append("file", new Blob([minPng], { type: "image/png" }), "test.png");
  const res = await fetch(`${BASE}/api/posts/${postSlug}/comment-attachments`, {
    method: "POST",
    body: form,
  });
  const body = await res.json();
  assert.equal(res.status, 201, `body: ${JSON.stringify(body)}`);
  uploadedCommentFile = body.file;
  assert.ok(uploadedCommentFile.id);
  assert.match(uploadedCommentFile.claimToken, /^.{32,256}$/);
  assert.match(uploadedCommentFile.storedFilename, /\.png$/);

  const served = await fetch(`${BASE}${uploadedCommentFile.url}`);
  assert.equal(served.status, 200);
  assert.equal(served.headers.get("content-type"), "image/png");
  assert.equal(served.headers.get("x-content-type-options"), "nosniff");
});

await test("附件错误 claimToken 被拒绝且不创建评论", async () => {
  const r = await api("POST", `/api/posts/${postSlug}/comments`, {
    body: {
      content: "错误 token",
      localId: `wrong-claim-${Date.now()}`,
      attachments: [{ id: uploadedCommentFile.id, claimToken: "x".repeat(43) }],
    },
  });
  assert.equal(r.status, 400);
  assert.equal(r.body.error?.code, "INVALID_ATTACHMENT");
});

await test("附件元数据由数据库决定且只能认领一次", async () => {
  const first = await api("POST", `/api/posts/${postSlug}/comments`, {
    body: {
      content: "带安全附件",
      localId: `claim-ok-${Date.now()}`,
      attachments: [{
        id: uploadedCommentFile.id,
        claimToken: uploadedCommentFile.claimToken,
        name: "forged.html",
        mimeType: "text/html",
        url: "https://attacker.invalid/payload",
        storageKey: "../../etc/passwd",
        sizeBytes: 1,
        sha256: "0".repeat(64),
      }],
    },
  });
  assert.equal(first.status, 201, `body: ${JSON.stringify(first.body)}`);
  const attachment = first.body.comment?.attachments?.[0];
  assert.equal(attachment.mimeType, "image/png");
  assert.equal(attachment.url, uploadedCommentFile.url);
  assert.notEqual(attachment.name, "forged.html");

  const reused = await api("POST", `/api/posts/${postSlug}/comments`, {
    body: {
      content: "重复认领",
      localId: `claim-reuse-${Date.now()}`,
      attachments: [{ id: uploadedCommentFile.id, claimToken: uploadedCommentFile.claimToken }],
    },
  });
  assert.equal(reused.status, 400);
  assert.equal(reused.body.error?.code, "INVALID_ATTACHMENT");
});

await test("脚本伪装 HTML 只能作为 .txt 下载", async () => {
  const form = new FormData();
  form.append("file", new Blob(["globalThis.__xss = true;"], { type: "text/html" }), "payload.html");
  const res = await fetch(`${BASE}/api/posts/${postSlug}/comment-attachments`, {
    method: "POST",
    body: form,
  });
  assert.equal(res.status, 201);
  const body = await res.json();
  assert.match(body.file.storedFilename, /\.txt$/);
  const served = await fetch(`${BASE}${body.file.url}`);
  assert.equal(served.headers.get("content-type"), "text/plain");
  assert.match(served.headers.get("content-disposition") ?? "", /^attachment;/);
  assert.equal(served.headers.get("x-content-type-options"), "nosniff");
});

await test("SVG 主动内容上传被拒绝", async () => {
  const form = new FormData();
  form.append("file", new Blob([`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`], { type: "image/svg+xml" }), "payload.svg");
  const res = await fetch(`${BASE}/api/posts/${postSlug}/comment-attachments`, {
    method: "POST",
    body: form,
  });
  assert.equal(res.status, 400);
});

await test("POST admin /api/admin/attachments (恶意文件，假装 PNG) → 400", async () => {
  // EXE magic bytes 伪装成 PNG 文件名
  const exeBytes = Buffer.from([0x4d, 0x5a, 0x90, 0x00, 0x03, 0x00]);  // MZ header
  const form = new FormData();
  form.append("file", new Blob([exeBytes], { type: "image/png" }), "evil.png");
  const res = await fetch(`${BASE}/api/admin/attachments`, {
    method: "POST",
    headers: { Cookie: adminCookie },
    body: form,
  });
  assert.equal(res.status, 400, "should reject EXE file disguised as PNG");
});

// ─── 10. 限流验证 ─────────────────────────────────────────────────────────────

section("10. 限流验证（管理员登录）");

await test("Admin 登录超过 5 次 → 429（需要等待新窗口）", async () => {
  // 使用唯一 IP（通过不同 header 模拟），触发速率限制
  // 实际测试中 limiter 按 IP 计数，此处验证 429 响应格式
  let gotRateLimited = false;
  for (let i = 0; i < 7; i++) {
    const r = await api("POST", "/api/admin/login", {
      body: { username: ADMIN_USER, password: "wrong-for-rate-limit-test" },
    });
    if (r.status === 429) {
      gotRateLimited = true;
      assert.equal(r.body.error?.code, "RATE_LIMITED");
      break;
    }
    // 短暂等待避免同一毫秒内的并发问题
    await sleep(10);
  }
  assert.ok(gotRateLimited, "should have been rate limited after 5 failed attempts");
});

// ─── 11. 清理 ────────────────────────────────────────────────────────────────

section("11. 清理（删除测试数据）");

await test("DELETE /api/admin/posts/:id → 200", async () => {
  const r = await api("DELETE", `/api/admin/posts/${postId}`, { cookies: adminCookie });
  assert.equal(r.status, 200);
  assert.equal(r.body.ok, true);
});

await test("DELETE 付费测试文章 → 200", async () => {
  const r = await api("DELETE", `/api/admin/posts/${paidPostId}`, { cookies: adminCookie });
  assert.equal(r.status, 200);
  assert.equal(r.body.ok, true);
});

await test("DELETE /api/admin/products/:id → 200", async () => {
  const r = await api("DELETE", `/api/admin/products/${productId}`, { cookies: adminCookie });
  assert.equal(r.status, 200);
});

await test("POST /api/admin/logout → 200", async () => {
  const r = await api("POST", "/api/admin/logout", { cookies: adminCookie });
  assert.equal(r.status, 200);
  assert.equal(r.body.ok, true);
});

await test("GET /api/admin/session (登出后) → 401", async () => {
  const r = await api("GET", "/api/admin/session", { cookies: adminCookie });
  assert.equal(r.status, 401);
});

// ─── 结果汇总 ─────────────────────────────────────────────────────────────────

console.log("\n" + "─".repeat(60));
console.log(`测试结果：${passed} 通过，${failed} 失败`);

if (failures.length > 0) {
  console.log("\n失败用例：");
  for (const f of failures) {
    console.log(`  ❌ ${f.name}`);
    console.log(`     ${f.error}`);
  }
}

if (failed > 0) {
  console.log("\n🔴 集成测试未通过，请修复上述问题再进行下一阶段");
  process.exit(1);
} else {
  console.log("\n🟢 所有集成测试通过！可以进入下一阶段。");
}
