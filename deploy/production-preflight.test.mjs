import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  formatPreflightErrors,
  validateProductionEnvironment,
  validateRuntimeEnvironment
} from "./production-preflight.mjs";

const repositoryRoot = fileURLToPath(new URL("../", import.meta.url));

function validEnvironment() {
  return {
    DEPLOY_HOST: "203.0.113.10",
    DEPLOY_USER: "deployer",
    DEPLOY_KEY: "private-key-material",
    DEPLOY_PATH: "/srv/freedompost",
    COOKIE_SECRET: "c".repeat(32),
    VISITOR_HASH_SALT: "v".repeat(32),
    ADMIN_PASSWORD: "a".repeat(16),
    POSTGRES_PASSWORD: "p".repeat(32),
    PAID_ARTICLES_ENABLED: "true",
    PAID_ACCESS_INTERNAL_URL: "http://paid-access:8080",
    PAID_ACCESS_INTERNAL_SECRET: "q".repeat(32),
    PREVIEW_DOMAIN: "www.example.com",
    STORAGE_DRIVER: "local",
    TRUST_PROXY: "true",
    REDIS_URL: "redis://redis:6379",
    TURNSTILE_SITE_KEY: "turnstile-site-key",
    TURNSTILE_SECRET_KEY: "t".repeat(32),
    TURNSTILE_EXPECTED_HOSTNAME: "www.example.com",
    TURNSTILE_EXPECTED_ACTION: "webmaster_benefit_claim",
    TURNSTILE_TIMEOUT_MS: "3000",
    OPUS8_INTEGRATION_BASE_URL: "https://api.example.net",
    OPUS8_INTEGRATION_KEY_ID: "freedompost-prod",
    OPUS8_INTEGRATION_SECRET: "o".repeat(32),
    OPUS8_INTEGRATION_TIMEOUT_MS: "5000",
    BENEFIT_CLAIM_HMAC_SECRET: "h".repeat(32),
    BENEFIT_LINK_ENCRYPTION_KEY: Buffer.alloc(32, 7).toString("base64url"),
    BENEFIT_NETWORK_DAILY_LIMIT: "3",
    BENEFIT_CLAIM_MINUTE_LIMIT: "6"
  };
}

test("accepts a complete production environment", () => {
  assert.deepEqual(validateProductionEnvironment(validEnvironment()), []);
  assert.deepEqual(validateRuntimeEnvironment(validEnvironment()), []);
});

test("fails closed when a benefit secret is missing", () => {
  const environment = validEnvironment();
  delete environment.OPUS8_INTEGRATION_SECRET;
  const errors = validateProductionEnvironment(environment);
  assert.ok(errors.some((error) => error.name === "OPUS8_INTEGRATION_SECRET"));
});

test("fails closed when paid access is disabled or its internal secret is missing", () => {
  const environment = validEnvironment();
  environment.PAID_ARTICLES_ENABLED = "false";
  delete environment.PAID_ACCESS_INTERNAL_SECRET;
  const names = validateProductionEnvironment(environment).map((error) => error.name);
  assert.ok(names.includes("PAID_ARTICLES_ENABLED"));
  assert.ok(names.includes("PAID_ACCESS_INTERNAL_SECRET"));
});

test("rejects a Turnstile hostname that differs from the public host", () => {
  const environment = validEnvironment();
  environment.TURNSTILE_EXPECTED_HOSTNAME = "attacker.example";
  const errors = validateProductionEnvironment(environment);
  assert.ok(errors.some((error) => error.name === "TURNSTILE_EXPECTED_HOSTNAME"));
});

test("rejects invalid encryption keys and unsafe proxy trust", () => {
  const environment = validEnvironment();
  environment.BENEFIT_LINK_ENCRYPTION_KEY = "not-a-key";
  environment.TRUST_PROXY = "false";
  const names = validateProductionEnvironment(environment).map((error) => error.name);
  assert.ok(names.includes("BENEFIT_LINK_ENCRYPTION_KEY"));
  assert.ok(names.includes("TRUST_PROXY"));
});

test("preflight diagnostics never contain secret values", () => {
  const environment = validEnvironment();
  environment.OPUS8_INTEGRATION_SECRET = "leak-probe-" + "x".repeat(32);
  environment.BENEFIT_LINK_ENCRYPTION_KEY = "invalid-leak-probe";
  const output = formatPreflightErrors(validateProductionEnvironment(environment));
  assert.doesNotMatch(output, /leak-probe/);
});

test("deployment workflow and Caddy keep the benefit path protected", () => {
  const workflow = readFileSync(`${repositoryRoot}.github/workflows/deploy.yml`, "utf8");
  const caddy = readFileSync(`${repositoryRoot}deploy/caddy/Caddyfile`, "utf8");
  for (const name of [
    "TURNSTILE_SITE_KEY",
    "TURNSTILE_SECRET_KEY",
    "OPUS8_INTEGRATION_BASE_URL",
    "OPUS8_INTEGRATION_KEY_ID",
    "OPUS8_INTEGRATION_SECRET",
    "BENEFIT_CLAIM_HMAC_SECRET",
    "BENEFIT_LINK_ENCRYPTION_KEY",
    "PAID_ACCESS_INTERNAL_SECRET"
  ]) {
    assert.match(workflow, new RegExp(`secrets\\.${name}`));
  }
  assert.match(workflow, /production-preflight\.mjs/);
  assert.match(workflow, /TRUST_PROXY/);
  for (const action of workflow.matchAll(/^\s*uses:\s*([^\s]+)$/gm)) {
    assert.match(action[1], /@[0-9a-f]{40}$/i);
  }
  assert.match(caddy, /@benefitApi path \/api\/benefits\/webmaster\*/);
  assert.match(caddy, /header @benefitApi Cache-Control "no-store"/);
  assert.match(caddy, /@readerApi path \/api\/reader\/\*/);
  assert.match(caddy, /header @readerApi Cache-Control "private, no-store"/);
  assert.match(caddy, /handle \/api\/reader\/\*[\s\S]*reverse_proxy paid-access:8080/);
  // [H1] /internal/* must be explicitly blocked with a 404 response.
  // A missing route is NOT safe — it could fall through to a catch-all.
  // Requiring an explicit `respond 404` is the correct defence-in-depth.
  assert.match(caddy, /handle \/internal\/\*[\s\S]*?respond 404/);
  assert.match(caddy, /header_up X-Forwarded-For \{remote_host\}.*paid-access|paid-access[\s\S]*header_up X-Forwarded-For \{remote_host\}/);
  assert.match(caddy, /frame-src[^\n]*https:\/\/challenges\.cloudflare\.com/);
  assert.match(caddy, /connect-src[^\n]*https:\/\/challenges\.cloudflare\.com/);
  assert.match(caddy, /redir @legacyTopics \/benefit\/\?\{query\} permanent/);
});
