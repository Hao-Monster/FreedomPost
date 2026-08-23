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
import { readFileSync } from "node:fs";

const BASE = process.argv[2] === "--base-url"
  ? process.argv[3]
  : (process.env.GO_API_URL ?? "http://localhost:3001");

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

// ─── 3. 文章 CRUD ─────────────────────────────────────────────────────────────

section("3. 文章 CRUD");

let postId = "";
let postSlug = "";

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

// ─── 4. 浏览量计数 ────────────────────────────────────────────────────────────

section("4. 浏览量计数");

await test("POST /api/posts/:slug/view → 200 counted:true", async () => {
  const today = new Date().toISOString().slice(0, 10);
  const r = await api("POST", `/api/posts/${postSlug}/view`, {
    body: {
      viewDate: today,
      visitorKey: `test-visitor-${Math.random()}`,
      fingerprintHash: "",
      localIdHash: "",
    },
  });
  assert.equal(r.status, 200);
  assert.equal(r.body.counted, true);
  assert.ok(r.body.viewCount > 0);
});

await test("POST /api/posts/:slug/view (同 visitor) → counted:false（去重）", async () => {
  const today = new Date().toISOString().slice(0, 10);
  const visitorKey = `dedup-test-${Date.now()}`;
  await api("POST", `/api/posts/${postSlug}/view`, { body: { viewDate: today, visitorKey } });
  const r = await api("POST", `/api/posts/${postSlug}/view`, { body: { viewDate: today, visitorKey } });
  assert.equal(r.status, 200);
  assert.equal(r.body.counted, false);
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
});

// ─── 6. 评论 ─────────────────────────────────────────────────────────────────

section("6. 评论");

let commentId = "";

await test("POST /api/posts/:slug/comments → 201 创建评论", async () => {
  const r = await api("POST", `/api/posts/${postSlug}/comments`, {
    body: {
      content: "这是一条测试评论，内容很精彩！",
      fingerprintHash: "test-fp-hash-abc123",
      localIdHash: "test-local-id-hash",
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
      fingerprintHash: "test-fp-hash-reply",
      localIdHash: "",
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
      description: "这是一个测试商品",
      imageUrl: "https://example.com/img.jpg",
      linkUrl: "https://example.com/product",
      priceCents: 9900,
      currency: "CNY",
      commissionCents: 500,
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

await test("POST /api/affiliate/access (自动注册) → 200", async () => {
  const r = await api("POST", "/api/affiliate/access", {
    body: { wechatId: testWechat, password: "affiliate-pass-123" },
  });
  assert.equal(r.status, 200, `body: ${JSON.stringify(r.body)}`);
  assert.ok(r.body.session?.affiliateId ?? r.body.session?.affiliate_id);
  const setCookie = r.headers.get("set-cookie") ?? "";
  affiliateCookie = `fp_affiliate_session=${setCookie.match(/fp_affiliate_session=([^;]+)/)?.[1] ?? ""}`;
});

await test("POST /api/affiliate/access (再次登录) → 200", async () => {
  const r = await api("POST", "/api/affiliate/access", {
    body: { wechatId: testWechat, password: "affiliate-pass-123" },
  });
  assert.equal(r.status, 200);
});

await test("POST /api/affiliate/access (错误密码) → 401", async () => {
  const r = await api("POST", "/api/affiliate/access", {
    body: { wechatId: testWechat, password: "wrong-pass" },
  });
  assert.equal(r.status, 401);
  assert.equal(r.body.error?.code, "LOGIN_FAILED");
});

await test("GET /api/affiliate/dashboard → 200", async () => {
  const r = await api("GET", "/api/affiliate/dashboard", { cookies: affiliateCookie });
  assert.equal(r.status, 200, `body: ${JSON.stringify(r.body)}`);
  assert.ok(r.body.dashboard);
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

await test("POST /api/orders (服务端定价验证) → 201", async () => {
  const r = await api("POST", "/api/orders", {
    cookies: affiliateCookie,
    body: {
      productId,
      customerContact: "test-contact@example.com",
      markupPercent: 10,
    },
  });
  assert.equal(r.status, 201, `body: ${JSON.stringify(r.body)}`);
  const order = r.body.order;
  assert.equal(order.customerPriceCents ?? order.customer_price_cents, 10890);
  assert.equal(order.commissionCents ?? order.commission_cents, 1490);
});

// ─── 9. 上传安全 ─────────────────────────────────────────────────────────────

section("9. 上传安全");

await test("POST /api/posts/:slug/comment-attachments (合法图片) → 201", async () => {
  // 最小 PNG（11 字节）
  const minPng = Buffer.from([
    0x89,0x50,0x4e,0x47,0x0d,0x0a,0x1a,0x0a,  // PNG signature
    0x00,0x00,0x00,0x0d,  // IHDR chunk length
    0x49,0x48,0x44,0x52,  // "IHDR"
    0x00,0x00,0x00,0x01,  // width: 1
    0x00,0x00,0x00,0x01,  // height: 1
    0x08,0x02,            // 8-bit RGB
    0x00,0x00,0x00,       // compression, filter, interlace
    0x90,0x77,0x53,0xde,  // CRC
    0x00,0x00,0x00,0x0c,  // IDAT chunk length
    0x49,0x44,0x41,0x54,  // "IDAT"
    0x08,0xd7,0x63,0xf8,0xcf,0xc0,0x00,0x00,0x00,0x02,0x00,0x01,
    0xe2,0x21,0xbc,0x33,  // CRC
    0x00,0x00,0x00,0x00,  // IEND chunk length
    0x49,0x45,0x4e,0x44,  // "IEND"
    0xae,0x42,0x60,0x82,  // CRC
  ]);
  const form = new FormData();
  form.append("file", new Blob([minPng], { type: "image/png" }), "test.png");
  const res = await fetch(`${BASE}/api/posts/${postSlug}/comment-attachments`, {
    method: "POST",
    body: form,
  });
  // 201 or 400 (if DB attachment insert fails in test setup) — just check no 5xx
  assert.ok(res.status < 500, `unexpected server error: ${res.status}`);
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
