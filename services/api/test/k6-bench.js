/**
 * k6 性能基准测试 — FreedomPost Go API vs TS API
 *
 * 运行方式（需安装 k6: https://k6.io/docs/get-started/installation/）：
 *
 *   # 基准测试 Go API（:3001）
 *   k6 run --env BASE=http://localhost:3001 services/api/test/k6-bench.js
 *
 *   # 对比测试 TS API（:3000）
 *   k6 run --env BASE=http://localhost:3000 services/api/test/k6-bench.js
 *
 *   # 压力测试（高并发）
 *   k6 run --env BASE=http://localhost:3001 --vus 50 --duration 30s \
 *     services/api/test/k6-bench.js
 *
 * 目标 SLO（从文档 04-test-plan.md）：
 *   - p95 响应时间 < 80ms（Go API）
 *   - p95 响应时间 < 200ms（TS API，基准）
 *   - 错误率 < 0.1%
 */

import http from "k6/http";
import { check, group, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

// ─── Configuration ────────────────────────────────────────────────────────────

const BASE = __ENV.BASE || "http://localhost:3001";
const ADMIN_USER = __ENV.ADMIN_USERNAME || "admin";
const ADMIN_PASS = __ENV.ADMIN_PASSWORD || "test-password-123";

export const options = {
  // Default: ramp up to 20 VUs over 10s, hold 30s, ramp down 10s
  stages: [
    { duration: "10s", target: 20 },
    { duration: "30s", target: 20 },
    { duration: "10s", target: 0 },
  ],
  thresholds: {
    // p95 latency must be < 200ms (conservative; Go target is < 80ms)
    http_req_duration: ["p(95)<200"],
    // Error rate < 0.5%
    http_req_failed: ["rate<0.005"],
    // All specific checks must pass > 99%
    checks: ["rate>0.99"],
  },
};

// ─── Custom metrics ───────────────────────────────────────────────────────────

const renderLatency = new Trend("render_latency_ms");
const searchLatency = new Trend("search_index_latency_ms");
const affiliateLatency = new Trend("affiliate_dashboard_latency_ms");
const errorRate = new Rate("api_errors");

// ─── Shared state ─────────────────────────────────────────────────────────────

// Setup: login and create a test post; teardown: delete the post.
export function setup() {
  // Admin login
  const loginRes = http.post(
    `${BASE}/api/admin/login`,
    JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASS }),
    { headers: { "Content-Type": "application/json" } }
  );
  check(loginRes, { "admin login 200": (r) => r.status === 200 });

  const cookie = loginRes.headers["Set-Cookie"]?.match(/fp_admin_session=[^;]+/)?.[0] ?? "";

  // Create a test post with moderate-length markdown content
  const markdown = [
    "# Benchmark Post",
    "",
    "This is a **benchmark** post with *italic*, `code`, and [links](https://example.com).",
    "",
    "## Section 1",
    "",
    "Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt.",
    "",
    "| Column A | Column B | Column C |",
    "|----------|----------|----------|",
    "| Value 1  | Value 2  | Value 3  |",
    "",
    ":::callout info",
    "This is an informational callout block.",
    ":::",
    "",
    "## Section 2",
    "",
    "- Item one",
    "- Item two",
    "  - Nested item",
    "- Item three",
    "",
    "```javascript",
    "const result = await fetch('/api/posts').then(r => r.json());",
    "console.log(result);",
    "```",
  ].join("\n");

  const createRes = http.post(
    `${BASE}/api/admin/posts`,
    JSON.stringify({
      title: `k6 Bench ${Date.now()}`,
      content: markdown,
      visibility: "public",
    }),
    { headers: { "Content-Type": "application/json", Cookie: cookie } }
  );
  check(createRes, { "create post 201": (r) => r.status === 201 });

  const body = createRes.json();
  // Go API: { post: {...} }
  const post = body?.post ?? body;
  return { cookie, postId: post.id, postSlug: post.slug };
}

export function teardown(data) {
  if (!data?.postId) return;
  http.del(`${BASE}/api/admin/posts/${data.postId}`, null, {
    headers: { Cookie: data.cookie },
  });
  http.post(`${BASE}/api/admin/logout`, null, {
    headers: { Cookie: data.cookie },
  });
}

// ─── Main VU scenario ─────────────────────────────────────────────────────────

export default function (data) {
  const headers = { Cookie: data.cookie };

  // ── Health check ────────────────────────────────────────────────────────────
  group("health", function () {
    const res = http.get(`${BASE}/health`);
    const ok = check(res, {
      "GET /health → 200": (r) => r.status === 200,
      "health ok:true": (r) => r.json("ok") === true,
    });
    errorRate.add(!ok);
  });

  sleep(0.05);

  // ── Post list ───────────────────────────────────────────────────────────────
  group("post_list", function () {
    const res = http.get(`${BASE}/api/posts`);
    const ok = check(res, {
      "GET /api/posts → 200": (r) => r.status === 200,
      "response has posts array": (r) => {
        const body = r.json();
        return Array.isArray(body?.posts ?? body);
      },
    });
    errorRate.add(!ok);
  });

  sleep(0.05);

  // ── Post detail (rendered markdown) ────────────────────────────────────────
  group("post_detail", function () {
    const res = http.get(`${BASE}/api/posts/${data.postSlug}`);
    const start = Date.now();
    const ok = check(res, {
      "GET /api/posts/:slug → 200": (r) => r.status === 200,
      // Go API returns { post: { contentHtml, ... } }; TS API returns { html, ... }
      "post has html": (r) => {
        const body = r.json();
        const post = body?.post ?? body;
        const html = post?.contentHtml ?? post?.html;
        return typeof html === "string";
      },
      "post has excerpt": (r) => {
        const body = r.json();
        const post = body?.post ?? body;
        return typeof post?.excerpt === "string";
      },
      "html is non-empty": (r) => {
        const body = r.json();
        const post = body?.post ?? body;
        const html = post?.contentHtml ?? post?.html ?? "";
        return html.length > 0;
      },
    });
    renderLatency.add(Date.now() - start);
    errorRate.add(!ok);
  });

  sleep(0.05);

  // ── Search index ─────────────────────────────────────────────────────────────
  group("search_index", function () {
    const start = Date.now();
    const res = http.get(`${BASE}/api/search-index`);
    const ok = check(res, {
      "GET /api/search-index → 200": (r) => r.status === 200,
    });
    searchLatency.add(Date.now() - start);
    errorRate.add(!ok);
  });

  sleep(0.05);

  // ── Benefit info (no-store cache header) ──────────────────────────────────
  group("benefit_info", function () {
    const res = http.get(`${BASE}/api/benefits/webmaster`);
    const ok = check(res, {
      "GET /api/benefits/webmaster → 200": (r) => r.status === 200,
      "no-store cache header": (r) =>
        (r.headers["Cache-Control"] ?? "").includes("no-store"),
      "has id field": (r) => typeof r.json("id") === "string",
    });
    errorRate.add(!ok);
  });

  sleep(0.1);
}
