#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -eq 0 ]; then
  SUDO=""
else
  SUDO="sudo"
fi

echo "=== Cleaning disk space ==="
$SUDO journalctl --vacuum-size=50M >/dev/null 2>&1 || true
if command -v apt-get >/dev/null 2>&1; then
  $SUDO apt-get clean || true
fi
if [ ! -f /swapfile ] && [ "$(awk '/MemTotal/ { print $2 }' /proc/meminfo 2>/dev/null || echo 0)" -lt 1800000 ]; then
  $SUDO fallocate -l 512M /swapfile || $SUDO dd if=/dev/zero of=/swapfile bs=1M count=512
  $SUDO chmod 600 /swapfile
  $SUDO mkswap /swapfile
  $SUDO swapon /swapfile || true
fi

echo "=== Extracting deploy bundle into $DEPLOY_PATH ==="
$SUDO mkdir -p "$DEPLOY_PATH"
$SUDO chown "$(id -u):$(id -g)" "$DEPLOY_PATH"
tar -xzf /tmp/freedompost-deploy.tar.gz -C "$DEPLOY_PATH"
rm -f /tmp/freedompost-deploy.tar.gz
cd "$DEPLOY_PATH"

if [ ! -f .env ]; then
  {
    printf '%s\n' "NODE_ENV=production"
    printf '%s\n' "PORT=3000"
    printf '%s\n' "HOST=0.0.0.0"
    printf '%s\n' "LOG_LEVEL=info"
    printf 'PREVIEW_DOMAIN=%s\n' "$PREVIEW_DOMAIN"
    printf 'ORIGIN_TEST_HOST=%s\n' "$DEPLOY_HOST"
    printf 'PUBLIC_SITE_URL=https://%s\n' "$PREVIEW_DOMAIN"
    printf 'VITE_PUBLIC_SITE_URL=https://%s\n' "$PREVIEW_DOMAIN"
    printf '%s\n' "COOKIE_SECURE=true"
    printf '%s\n' "TRUST_PROXY=true"
    printf 'REDIS_PASSWORD=%s\n' "$REDIS_PASSWORD"
    printf 'REDIS_URL=redis://:%s@redis:6379\n' "$REDIS_PASSWORD"
    printf '%s\n' "PAID_ARTICLES_ENABLED=true"
    printf '%s\n' "PAID_ACCESS_INTERNAL_URL=http://paid-access:8080"
    printf 'PAID_ACCESS_INTERNAL_SECRET=%s\n' "$PAID_ACCESS_INTERNAL_SECRET"
    printf '%s\n' "PAID_ACCESS_WECHAT_IMAGE_URL=/images/contact-wechat.jpg"
    printf '%s\n' "API_BODY_LIMIT_BYTES=104857600"
    printf '%s\n' "UPLOAD_MAX_BYTES=524288000"
    printf 'COOKIE_SECRET=%s\n' "$COOKIE_SECRET"
    printf 'VISITOR_HASH_SALT=%s\n' "$VISITOR_HASH_SALT"
    printf '%s\n' "ADMIN_USERNAME=admin"
    printf 'ADMIN_PASSWORD=%s\n' "$ADMIN_PASSWORD"
    printf '%s\n' "POSTGRES_DB=freedompost"
    printf '%s\n' "POSTGRES_USER=freedompost"
    printf 'POSTGRES_PASSWORD=%s\n' "$POSTGRES_PASSWORD"
    printf 'DATABASE_URL=postgres://freedompost:%s@postgres:5432/freedompost\n' "$POSTGRES_PASSWORD"
    printf '%s\n' "FREEDOMPOST_REPOSITORY=postgres"
    printf 'STORAGE_DRIVER=%s\n' "$STORAGE_DRIVER"
    printf '%s\n' "LOCAL_STORAGE_ROOT=runtime/local-storage"
    printf '%s\n' "PUBLIC_UPLOAD_BASE_URL=/api/uploads"
    printf 'TURNSTILE_SITE_KEY=%s\n' "$TURNSTILE_SITE_KEY"
    printf 'TURNSTILE_SECRET_KEY=%s\n' "$TURNSTILE_SECRET_KEY"
    printf 'TURNSTILE_EXPECTED_HOSTNAME=%s\n' "$PREVIEW_DOMAIN"
    printf '%s\n' "TURNSTILE_EXPECTED_ACTION=webmaster_benefit_claim"
    printf '%s\n' "TURNSTILE_TIMEOUT_MS=3000"
    printf 'OPUS8_INTEGRATION_BASE_URL=%s\n' "$OPUS8_INTEGRATION_BASE_URL"
    printf 'OPUS8_INTEGRATION_KEY_ID=%s\n' "$OPUS8_INTEGRATION_KEY_ID"
    printf 'OPUS8_INTEGRATION_SECRET=%s\n' "$OPUS8_INTEGRATION_SECRET"
    printf '%s\n' "OPUS8_INTEGRATION_TIMEOUT_MS=5000"
    printf 'BENEFIT_CLAIM_HMAC_SECRET=%s\n' "$BENEFIT_CLAIM_HMAC_SECRET"
    printf 'BENEFIT_LINK_ENCRYPTION_KEY=%s\n' "$BENEFIT_LINK_ENCRYPTION_KEY"
    printf '%s\n' "BENEFIT_NETWORK_DAILY_LIMIT=3"
    printf '%s\n' "BENEFIT_CLAIM_MINUTE_LIMIT=6"
  } > .env
  chmod 600 .env
fi

set_env() {
  key="$1"
  value="$2"
  env_tmp="$(mktemp)"
  awk -v key="$key" -v value="$value" '
    BEGIN { found = 0 }
    $0 ~ "^" key "=" {
      print key "=" value
      found = 1
      next
    }
    { print }
    END {
      if (!found) {
        print key "=" value
      }
    }
  ' .env > "$env_tmp"
  cat "$env_tmp" > .env
  rm -f "$env_tmp"
}

set_env_if_present() {
  key="$1"
  value="${2:-}"
  if [ -n "$value" ]; then
    set_env "$key" "$value"
  fi
}

set_env "PREVIEW_DOMAIN" "$PREVIEW_DOMAIN"
set_env "ORIGIN_TEST_HOST" "$DEPLOY_HOST"
set_env "PUBLIC_SITE_URL" "https://$PREVIEW_DOMAIN"
set_env "VITE_PUBLIC_SITE_URL" "https://$PREVIEW_DOMAIN"
set_env "COOKIE_SECURE" "true"
set_env "TRUST_PROXY" "true"
set_env "REDIS_URL" "redis://redis:6379"
set_env "PAID_ARTICLES_ENABLED" "true"
set_env "PAID_ACCESS_INTERNAL_URL" "http://paid-access:8080"
set_env "PAID_ACCESS_INTERNAL_SECRET" "$PAID_ACCESS_INTERNAL_SECRET"
set_env "PAID_ACCESS_WECHAT_IMAGE_URL" "/images/contact-wechat.jpg"
set_env "COOKIE_SECRET" "$COOKIE_SECRET"
set_env "VISITOR_HASH_SALT" "$VISITOR_HASH_SALT"
set_env "ADMIN_PASSWORD" "$ADMIN_PASSWORD"
set_env "POSTGRES_PASSWORD" "$POSTGRES_PASSWORD"
set_env "DATABASE_URL" "postgres://freedompost:$POSTGRES_PASSWORD@postgres:5432/freedompost"
set_env "STORAGE_DRIVER" "$STORAGE_DRIVER"
set_env "TURNSTILE_SITE_KEY" "$TURNSTILE_SITE_KEY"
set_env "TURNSTILE_SECRET_KEY" "$TURNSTILE_SECRET_KEY"
set_env "TURNSTILE_EXPECTED_HOSTNAME" "$PREVIEW_DOMAIN"
set_env "TURNSTILE_EXPECTED_ACTION" "webmaster_benefit_claim"
set_env "TURNSTILE_TIMEOUT_MS" "3000"
set_env "OPUS8_INTEGRATION_BASE_URL" "$OPUS8_INTEGRATION_BASE_URL"
set_env "OPUS8_INTEGRATION_KEY_ID" "$OPUS8_INTEGRATION_KEY_ID"
set_env "OPUS8_INTEGRATION_SECRET" "$OPUS8_INTEGRATION_SECRET"
set_env "OPUS8_INTEGRATION_TIMEOUT_MS" "5000"
set_env "BENEFIT_CLAIM_HMAC_SECRET" "$BENEFIT_CLAIM_HMAC_SECRET"
set_env "BENEFIT_LINK_ENCRYPTION_KEY" "$BENEFIT_LINK_ENCRYPTION_KEY"
set_env "BENEFIT_NETWORK_DAILY_LIMIT" "3"
set_env "BENEFIT_CLAIM_MINUTE_LIMIT" "6"
set_env "GO_API_WEIGHT" "${GO_API_WEIGHT:-0}"
set_env "TS_API_WEIGHT" "${TS_API_WEIGHT:-100}"

if [ "$STORAGE_DRIVER" = "oss" ]; then
  set_env "ALIYUN_OSS_REGION" "$ALIYUN_OSS_REGION"
  set_env "ALIYUN_OSS_BUCKET" "$ALIYUN_OSS_BUCKET"
  set_env "ALIYUN_OSS_ACCESS_KEY_ID" "$ALIYUN_OSS_ACCESS_KEY_ID"
  set_env "ALIYUN_OSS_ACCESS_KEY_SECRET" "$ALIYUN_OSS_ACCESS_KEY_SECRET"
  set_env_if_present "ALIYUN_OSS_ENDPOINT" "${ALIYUN_OSS_ENDPOINT:-}"
  set_env_if_present "ALIYUN_OSS_PUBLIC_BASE_URL" "${ALIYUN_OSS_PUBLIC_BASE_URL:-}"
  set_env_if_present "ALIYUN_OSS_PREFIX" "${ALIYUN_OSS_PREFIX:-}"
fi
if [ "$STORAGE_DRIVER" = "r2" ]; then
  set_env "R2_ACCOUNT_ID" "$R2_ACCOUNT_ID"
  set_env "R2_BUCKET" "${R2_BUCKET:-freedompost}"
  set_env "R2_ACCESS_KEY_ID" "$R2_ACCESS_KEY_ID"
  set_env "R2_SECRET_ACCESS_KEY" "$R2_SECRET_ACCESS_KEY"
  set_env_if_present "R2_ENDPOINT" "${R2_ENDPOINT:-}"
  set_env "R2_PUBLIC_BASE_URL" "${R2_PUBLIC_BASE_URL:-https://r2pic.openal.uk}"
  set_env "R2_PREFIX" "${R2_PREFIX:-freedompost/uploads}"
fi
chmod 600 .env

if [ "$(awk '/MemTotal/ { print $2 }' /proc/meminfo)" -lt 1800000 ] && ! grep -q '^/swapfile ' /proc/swaps; then
  if [ ! -f /swapfile ]; then
    $SUDO fallocate -l 512M /swapfile || $SUDO dd if=/dev/zero of=/swapfile bs=1M count=512
  fi
  $SUDO chmod 600 /swapfile
  $SUDO mkswap /swapfile
  $SUDO swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' | $SUDO tee -a /etc/fstab >/dev/null
fi

if ! command -v curl >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    $SUDO apt-get update
    $SUDO apt-get install -y curl ca-certificates
  elif command -v dnf >/dev/null 2>&1; then
    $SUDO dnf install -y curl ca-certificates
  elif command -v yum >/dev/null 2>&1; then
    $SUDO yum install -y curl ca-certificates
  else
    echo "curl is required to install Docker"
    exit 1
  fi
fi

if ! command -v docker >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    $SUDO rm -f /etc/apt/sources.list.d/docker.list /etc/apt/sources.list.d/docker-ce.list
    $SUDO rm -f /etc/apt/keyrings/docker.asc
    $SUDO apt-get update
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl docker.io
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y docker-compose-v2 || $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y docker-compose
  elif command -v dnf >/dev/null 2>&1; then
    $SUDO dnf install -y dnf-plugins-core || $SUDO dnf install -y yum-utils
    $SUDO dnf config-manager --add-repo https://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo
    $SUDO dnf install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  elif command -v yum >/dev/null 2>&1; then
    $SUDO yum install -y yum-utils
    $SUDO yum-config-manager --add-repo https://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo
    $SUDO yum install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  else
    curl --retry 5 --retry-delay 5 -fsSL https://get.docker.com -o /tmp/get-docker.sh
    $SUDO sh /tmp/get-docker.sh
  fi
fi

if command -v systemctl >/dev/null 2>&1; then
  $SUDO systemctl enable --now docker
elif command -v service >/dev/null 2>&1; then
  $SUDO service docker start
fi

$SUDO mkdir -p /etc/docker
cat <<'JSON' | $SUDO tee /etc/docker/daemon.json >/dev/null
{
  "registry-mirrors": [
    "https://docker.m.daocloud.io",
    "https://hub-mirror.c.163.com",
    "https://mirror.baidubce.com"
  ]
}
JSON

if command -v systemctl >/dev/null 2>&1; then
  $SUDO systemctl restart docker
elif command -v service >/dev/null 2>&1; then
  $SUDO service docker restart
fi

docker_cmd() {
  if [ -n "$SUDO" ]; then
    $SUDO docker "$@"
  else
    docker "$@"
  fi
}

compose() {
  if docker_cmd compose version >/dev/null 2>&1; then
    docker_cmd compose "$@"
  elif command -v docker-compose >/dev/null 2>&1; then
    if [ -n "$SUDO" ]; then
      $SUDO docker-compose "$@"
    else
      docker-compose "$@"
    fi
  else
    echo "Docker Compose is required"
    exit 1
  fi
}

docker_cmd version
compose version

if command -v ufw >/dev/null 2>&1; then
  $SUDO ufw allow 80/tcp || true
  $SUDO ufw allow 443/tcp || true
fi
if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
  $SUDO firewall-cmd --permanent --add-service=http || true
  $SUDO firewall-cmd --permanent --add-service=https || true
  $SUDO firewall-cmd --reload || true
fi
if command -v iptables >/dev/null 2>&1; then
  $SUDO iptables -C INPUT -p tcp --dport 80 -j ACCEPT 2>/dev/null || $SUDO iptables -I INPUT -p tcp --dport 80 -j ACCEPT || true
  $SUDO iptables -C INPUT -p tcp --dport 443 -j ACCEPT 2>/dev/null || $SUDO iptables -I INPUT -p tcp --dport 443 -j ACCEPT || true
fi

for attempt in 1 2 3; do
  compose --env-file .env -f deploy/docker-compose.yml pull postgres redis && break
  if [ "$attempt" -eq 3 ]; then
    exit 1
  fi
  sleep "$((attempt * 20))"
done

compose --env-file .env -f deploy/docker-compose.yml up -d postgres redis
docker_cmd image prune --force || true
docker_cmd system df || true

echo "=== Building pre-compiled lightweight containers ==="
compose --env-file .env -f deploy/docker-compose.yml build paid-access nginx api-go

# Wait for PostgreSQL to finish initializing and recovery
for attempt in $(seq 1 30); do
  if compose --env-file .env -f deploy/docker-compose.yml exec -T postgres pg_isready -U freedompost >/dev/null 2>&1; then
    echo "=== PostgreSQL is ready ==="
    break
  fi
  echo "Waiting for PostgreSQL ($attempt/30)..."
  sleep 1
done

echo "=== Running Go database migrations ==="
compose --env-file .env -f deploy/docker-compose.yml run --rm api-go -migrate

echo "=== Starting production services ==="
compose --env-file .env -f deploy/docker-compose.yml up -d --force-recreate --remove-orphans paid-access nginx api-go

# ── Deployment gate: Go API must be healthy ────────────────────────────
echo "=== Verifying Go API deployment health gates ==="
for attempt in $(seq 1 60); do
  if compose --env-file .env -f deploy/docker-compose.yml exec -T api-go /fp-api -health-check \
    && curl -kfsS --max-time 10 --connect-timeout 5 --resolve "$PREVIEW_DOMAIN:443:127.0.0.1" "https://$PREVIEW_DOMAIN/health" >/dev/null \
    && curl -kfsS --max-time 10 --connect-timeout 5 --resolve "$PREVIEW_DOMAIN:443:127.0.0.1" -D /tmp/benefit-health.headers "https://$PREVIEW_DOMAIN/api/benefits/webmaster" >/dev/null \
    && grep -qi '^cache-control:.*no-store' /tmp/benefit-health.headers; then
    echo "=== Go API health check passed (attempt $attempt) ==="
    echo "=== Production Deployment 100% SUCCESS ==="
    exit 0
  fi
  echo "Attempt $attempt failed, retrying in 2s..."
  sleep 2
done

# ── Gate failed: dump diagnostics ─────────────────────────────────────
compose --env-file .env -f deploy/docker-compose.yml ps
compose --env-file .env -f deploy/docker-compose.yml logs --tail=120 api-go paid-access nginx
exit 1
