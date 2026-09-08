import { fileURLToPath } from "node:url";

const SIMPLE_SECRET_NAMES = [
  "COOKIE_SECRET",
  "VISITOR_HASH_SALT",
  "ADMIN_PASSWORD_HASH",
  "POSTGRES_PASSWORD",
  "PAID_ACCESS_INTERNAL_SECRET",
  "TURNSTILE_SECRET_KEY",
  "OPUS8_INTEGRATION_SECRET",
  "BENEFIT_CLAIM_HMAC_SECRET"
];

export function validateProductionEnvironment(environment = process.env) {
  const errors = [];
  const add = (name, message) => errors.push({ name, message });
  const value = (name) => String(environment[name] ?? "");

  for (const name of ["DEPLOY_HOST", "DEPLOY_USER", "DEPLOY_PATH"]) {
    if (!value(name).trim()) add(name, "is required");
  }
  if (!value("DEPLOY_PASSWORD") && !value("DEPLOY_KEY")) {
    add("DEPLOY_PASSWORD|DEPLOY_KEY", "one SSH credential is required");
  }
  errors.push(...validateRuntimeEnvironment(environment));
  return errors;
}

export function validateRuntimeEnvironment(environment = process.env) {
  const errors = [];
  const add = (name, message) => errors.push({ name, message });
  const value = (name) => String(environment[name] ?? "");

  if (!value("PREVIEW_DOMAIN").match(validHostnamePattern())) {
    add("PREVIEW_DOMAIN", "must be a DNS hostname without a scheme or path");
  }
  if (value("TRUST_PROXY") !== "true") {
    add("TRUST_PROXY", "must be true so client network limits use the trusted proxy chain");
  }
  if (!validRedisUrl(value("REDIS_URL"))) {
    add("REDIS_URL", "must be an absolute redis:// or rediss:// URL");
  }
  if (value("PAID_ARTICLES_ENABLED") !== "true") {
    add("PAID_ARTICLES_ENABLED", "must be true for the paid-access production release");
  }
  if (value("PAID_ACCESS_INTERNAL_URL") !== "http://paid-access:8080") {
    add("PAID_ACCESS_INTERNAL_URL", "must target the private paid-access service");
  }
  if (value("PAID_ACCESS_INTERNAL_SECRET") === value("BENEFIT_CLAIM_HMAC_SECRET")) {
    add("PAID_ACCESS_INTERNAL_SECRET", "must be distinct from the benefit HMAC secret");
  }

  const minimumLengths = {
    COOKIE_SECRET: 32,
    VISITOR_HASH_SALT: 32,
    POSTGRES_PASSWORD: 12,
    PAID_ACCESS_INTERNAL_SECRET: 32,
    TURNSTILE_SECRET_KEY: 20,
    OPUS8_INTEGRATION_SECRET: 32,
    BENEFIT_CLAIM_HMAC_SECRET: 32
  };
  for (const name of SIMPLE_SECRET_NAMES) {
    const secret = value(name);
    if (secret.length < minimumLengths[name] || secret.length > 4_096 || /[\r\n]/.test(secret)) {
      add(name, `must contain ${minimumLengths[name]} to 4096 characters without line breaks`);
    }
  }
  if (!/^\$2[aby]\$\d{2}\$[./A-Za-z0-9]{53}$/.test(value("ADMIN_PASSWORD_HASH"))) {
    add("ADMIN_PASSWORD_HASH", "must be a valid bcrypt hash");
  }

  const storageDriver = value("STORAGE_DRIVER");
  if (!["local", "oss", "r2"].includes(storageDriver)) {
    add("STORAGE_DRIVER", "must be local, oss, or r2");
  }
  if (storageDriver === "oss") {
    requireNames(environment, errors, [
      "ALIYUN_OSS_REGION",
      "ALIYUN_OSS_BUCKET",
      "ALIYUN_OSS_ACCESS_KEY_ID",
      "ALIYUN_OSS_ACCESS_KEY_SECRET"
    ]);
  }
  if (storageDriver === "r2") {
    requireNames(environment, errors, [
      "R2_ACCOUNT_ID",
      "R2_ACCESS_KEY_ID",
      "R2_SECRET_ACCESS_KEY"
    ]);
  }

  if (!/^[A-Za-z0-9_-]{10,256}$/.test(value("TURNSTILE_SITE_KEY"))) {
    add("TURNSTILE_SITE_KEY", "is invalid");
  }
  if (value("TURNSTILE_EXPECTED_HOSTNAME").toLowerCase() !== value("PREVIEW_DOMAIN").toLowerCase()) {
    add("TURNSTILE_EXPECTED_HOSTNAME", "must equal PREVIEW_DOMAIN");
  }
  if (value("TURNSTILE_EXPECTED_ACTION") !== "webmaster_benefit_claim") {
    add("TURNSTILE_EXPECTED_ACTION", "must equal webmaster_benefit_claim");
  }
  validateInteger(environment, errors, "TURNSTILE_TIMEOUT_MS", 100, 10_000);

  if (!validHttpsOrigin(value("OPUS8_INTEGRATION_BASE_URL"))) {
    add("OPUS8_INTEGRATION_BASE_URL", "must be an HTTPS origin without a path or credentials");
  }
  if (!/^[A-Za-z0-9._-]{3,64}$/.test(value("OPUS8_INTEGRATION_KEY_ID"))) {
    add("OPUS8_INTEGRATION_KEY_ID", "contains an invalid key ID");
  }
  validateInteger(environment, errors, "OPUS8_INTEGRATION_TIMEOUT_MS", 100, 30_000);

  if (!validBase64UrlKey(value("BENEFIT_LINK_ENCRYPTION_KEY"))) {
    add("BENEFIT_LINK_ENCRYPTION_KEY", "must be a canonical base64url-encoded 256-bit key");
  }
  validateInteger(environment, errors, "BENEFIT_NETWORK_DAILY_LIMIT", 1, 50);
  validateInteger(environment, errors, "BENEFIT_CLAIM_MINUTE_LIMIT", 1, 100);

  return errors;
}

export function formatPreflightErrors(errors) {
  return errors.map(({ name, message }) => `ERROR ${name}: ${message}`).join("\n");
}

function requireNames(environment, errors, names) {
  for (const name of names) {
    if (!String(environment[name] ?? "").trim()) errors.push({ name, message: "is required" });
  }
}

function validateInteger(environment, errors, name, minimum, maximum) {
  const parsed = Number(environment[name]);
  if (!Number.isInteger(parsed) || parsed < minimum || parsed > maximum) {
    errors.push({ name, message: `must be an integer from ${minimum} to ${maximum}` });
  }
}

function validHostnamePattern() {
  return /^(?=.{1,253}$)(?!-)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i;
}

function validRedisUrl(input) {
  try {
    const url = new URL(input);
    return ["redis:", "rediss:"].includes(url.protocol) && Boolean(url.hostname);
  } catch {
    return false;
  }
}

function validHttpsOrigin(input) {
  try {
    const url = new URL(input);
    return url.protocol === "https:"
      && !url.username
      && !url.password
      && url.pathname === "/"
      && !url.search
      && !url.hash;
  } catch {
    return false;
  }
}

function validBase64UrlKey(input) {
  if (!/^[A-Za-z0-9_-]{43}$/.test(input)) return false;
  const decoded = Buffer.from(input, "base64url");
  return decoded.length === 32 && decoded.toString("base64url") === input;
}

function runCli() {
  const errors = process.argv.includes("--runtime")
    ? validateRuntimeEnvironment(process.env)
    : validateProductionEnvironment(process.env);
  if (errors.length === 0) {
    process.stdout.write("OK production-preflight\n");
    return;
  }
  process.stderr.write(`${formatPreflightErrors(errors)}\n`);
  process.exitCode = 1;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) runCli();
