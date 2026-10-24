#!/usr/bin/env bash
set -euo pipefail

# Validate before any credential/configuration reads or privileged operations.
# Require the same canonical spelling as production-preflight.mjs so appending
# .rollback always selects a sibling of the Docker build context.
if [[ ! "${DEPLOY_PATH:-}" =~ ^(/[A-Za-z0-9_.-]+)+$ ]] \
  || [[ "/${DEPLOY_PATH:-}/" == */./* || "/${DEPLOY_PATH:-}/" == */../* ]]; then
  printf 'DEPLOY_PATH must be a non-root canonical absolute path without trailing slashes, dot segments or unsafe characters\n' >&2
  exit 1
fi
# Missing directories are valid on first install. Reject existing symlink
# aliases (including parent components) without creating any directories.
if ! canonical_deploy_path=$(realpath -m -- "$DEPLOY_PATH" 2>/dev/null) \
  || [ "$canonical_deploy_path" != "$DEPLOY_PATH" ]; then
  printf 'DEPLOY_PATH must not use a filesystem alias\n' >&2
  exit 1
fi

if [ "$(id -u)" -eq 0 ]; then
  SUDO=""
else
  SUDO="sudo"
fi

# BEGIN admin credential guard functions
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
    $SUDO docker-compose "$@"
  else
    echo "Docker Compose is required" >&2
    return 1
  fi
}

admin_credentials_error() {
  printf 'Admin credential gate: %s\n' "$1" >&2
  return 1
}

validate_admin_password_hash() {
  [[ "$1" =~ ^\$2[ab]\$(0[4-9]|[12][0-9]|3[01])\$[./A-Za-z0-9]{53}$ ]]
}

read_admin_env_file() {
  local contents line key value literal hash_count=0 username_count=0
  ADMIN_FILE_HASH=""
  ADMIN_FILE_USERNAME="admin"
  contents=$($SUDO awk '/^[[:space:]]*(ADMIN_PASSWORD_HASH|ADMIN_USERNAME)[[:space:]]*=/' "$1" 2>/dev/null) \
    || { admin_credentials_error 'cannot read existing credential configuration'; return 1; }
  while IFS= read -r line; do
    [[ "$line" =~ ^[[:space:]]*(ADMIN_PASSWORD_HASH|ADMIN_USERNAME)[[:space:]]*=(.*)$ ]] || continue
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    literal=false
    case "${value:0:1}" in
      "'")
        [[ ${#value} -ge 2 && "${value: -1}" == "'" ]] \
          || { admin_credentials_error 'unsupported credential quoting'; return 1; }
        value="${value:1:${#value}-2}"
        literal=true
        ;;
      '"')
        [[ ${#value} -ge 2 && "${value: -1}" == '"' ]] \
          || { admin_credentials_error 'unsupported credential quoting'; return 1; }
        value="${value:1:${#value}-2}"
        ;;
    esac
    if [ "$literal" = false ]; then
      value="${value//\$\$/\$}"
    fi
    case "$key" in
      ADMIN_PASSWORD_HASH) hash_count=$((hash_count + 1)); ADMIN_FILE_HASH="$value" ;;
      ADMIN_USERNAME) username_count=$((username_count + 1)); ADMIN_FILE_USERNAME="${value:-admin}" ;;
    esac
  done <<< "$contents"
  if [ "$hash_count" -ne 1 ] || [ "$username_count" -gt 1 ] || ! validate_admin_password_hash "$ADMIN_FILE_HASH"; then
    admin_credentials_error 'existing credentials are missing, duplicated or invalid'
    return 1
  fi
}

parse_admin_container_credentials() {
  local line hash_count=0 username_count=0
  ADMIN_CONTAINER_HASH=""
  ADMIN_CONTAINER_USERNAME="admin"
  while IFS= read -r line; do
    case "$line" in
      ADMIN_PASSWORD_HASH=*) hash_count=$((hash_count + 1)); ADMIN_CONTAINER_HASH="${line#*=}" ;;
      ADMIN_USERNAME=*) username_count=$((username_count + 1)); ADMIN_CONTAINER_USERNAME="${line#*=}"; ADMIN_CONTAINER_USERNAME="${ADMIN_CONTAINER_USERNAME:-admin}" ;;
    esac
  done <<< "$1"
  if [ "$hash_count" -ne 1 ] || [ "$username_count" -gt 1 ] || ! validate_admin_password_hash "$ADMIN_CONTAINER_HASH"; then
    admin_credentials_error 'container credentials are missing, duplicated or invalid'
    return 1
  fi
}

get_single_admin_container() {
  local containers container oneoff count=0
  ADMIN_CONTAINER_ID=""
  containers=$(compose --env-file "$DEPLOY_PATH/.env" -f "$DEPLOY_PATH/deploy/docker-compose.yml" ps -aq api-go 2>/dev/null) \
    || { admin_credentials_error 'cannot identify the existing API container'; return 1; }
  while IFS= read -r container; do
    [ -n "$container" ] || continue
    oneoff=$(docker_cmd inspect --format '{{index .Config.Labels "com.docker.compose.oneoff"}}' "$container" 2>/dev/null) \
      || { admin_credentials_error 'cannot classify the API container'; return 1; }
    case "$oneoff" in
      True|true) continue ;;
      False|false) ;;
      *) admin_credentials_error 'API container identity is ambiguous'; return 1 ;;
    esac
    count=$((count + 1))
    ADMIN_CONTAINER_ID="$container"
  done <<< "$containers"
  if [ "$count" -ne 1 ]; then
    admin_credentials_error 'expected exactly one existing API service container'
    return 1
  fi
}

read_admin_container_credentials() {
  local credentials
  credentials=$(docker_cmd inspect --format '{{range .Config.Env}}{{if or (eq (index (split . "=") 0) "ADMIN_PASSWORD_HASH") (eq (index (split . "=") 0) "ADMIN_USERNAME")}}{{println .}}{{end}}{{end}}' "$1" 2>/dev/null) \
    || { admin_credentials_error 'cannot read API container credentials'; return 1; }
  parse_admin_container_credentials "$credentials"
}

prepare_admin_credentials() {
  local existing_containers rotation="${ROTATE_ADMIN_PASSWORD-false}"
  case "$rotation" in
    true|false) ;;
    *) admin_credentials_error 'ROTATE_ADMIN_PASSWORD must be true or false'; return 1 ;;
  esac
  validate_admin_password_hash "${ADMIN_PASSWORD_HASH:-}" \
    || { admin_credentials_error 'target must be an unquoted bcrypt $2a$/$2b$ hash'; return 1; }
  if [ ! -f "$DEPLOY_PATH/.env" ]; then
    if [ "$rotation" != true ]; then
      admin_credentials_error 'initial credentials require explicit password rotation'
      return 1
    fi
    if command -v docker >/dev/null 2>&1; then
      existing_containers=$(docker_cmd ps -aq --filter 'label=com.docker.compose.service=api-go' 2>/dev/null) \
        || { admin_credentials_error 'cannot verify an empty deployment'; return 1; }
      if [ -n "$existing_containers" ]; then
        admin_credentials_error 'existing API containers have no credential configuration'
        return 1
      fi
    fi
    ADMIN_USERNAME=admin
    ADMIN_CREDENTIAL_MODE=bootstrap
    return 0
  fi
  read_admin_env_file "$DEPLOY_PATH/.env" || return 1
  get_single_admin_container || return 1
  read_admin_container_credentials "$ADMIN_CONTAINER_ID" || return 1
  if [ "$ADMIN_FILE_HASH" != "$ADMIN_CONTAINER_HASH" ] || [ "$ADMIN_FILE_USERNAME" != "$ADMIN_CONTAINER_USERNAME" ]; then
    admin_credentials_error 'stored and running credentials differ; reconcile before deployment'
    return 1
  fi
  ADMIN_USERNAME="$ADMIN_CONTAINER_USERNAME"
  ADMIN_CREDENTIAL_MODE=preserve
  if [ "$rotation" = true ]; then
    ADMIN_CREDENTIAL_MODE=rotate
  elif [ "$ADMIN_PASSWORD_HASH" != "$ADMIN_CONTAINER_HASH" ]; then
    admin_credentials_error 'password change requires explicit rotation; existing credentials were preserved'
    return 1
  fi
}

verify_admin_credentials() {
  if [ "$1" != "$ADMIN_PASSWORD_HASH" ] || [ "$2" != "$ADMIN_USERNAME" ]; then
    admin_credentials_error 'container credentials do not match the selected deployment credentials'
    return 1
  fi
}

verify_candidate_admin_credentials() {
  local credentials
  credentials=$(compose --env-file .env -f deploy/docker-compose.yml run --rm --no-deps --entrypoint /bin/sh api-go -c \
    'printf "ADMIN_PASSWORD_HASH=%s\nADMIN_USERNAME=%s\n" "$ADMIN_PASSWORD_HASH" "${ADMIN_USERNAME:-admin}"' 2>/dev/null) \
    || { admin_credentials_error 'candidate credential check could not run'; return 1; }
  parse_admin_container_credentials "$credentials" || return 1
  verify_admin_credentials "$ADMIN_CONTAINER_HASH" "$ADMIN_CONTAINER_USERNAME"
}

verify_running_admin_credentials() {
  get_single_admin_container || return 1
  read_admin_container_credentials "$ADMIN_CONTAINER_ID" || return 1
  verify_admin_credentials "$ADMIN_CONTAINER_HASH" "$ADMIN_CONTAINER_USERNAME"
}
# END admin credential guard functions

# BEGIN origin client certificate guard functions
# Fixed host paths; no environment switch may disable origin authentication.
ORIGIN_CLIENT_CA=/etc/caddy/certs/origin-pull-client-ca.crt
ORIGIN_HEALTH_CERT=/etc/freedompost/origin-health-client.crt
ORIGIN_HEALTH_KEY=/etc/freedompost/origin-health-client.key

origin_credentials_error() {
  printf 'Origin mTLS gate: %s\n' "$1" >&2
  return 1
}

verify_origin_client_material() {
  local ca="$1" cert="$2" key="$3" cert_public key_public file
  for file in "$ca" "$cert" "$key"; do
    $SUDO test -r "$file" \
      || { origin_credentials_error 'required host certificate material is missing or unreadable'; return 1; }
  done
  [ "$($SUDO stat -c '%u:%a' "$key")" = '0:600' ] \
    || { origin_credentials_error 'health client key must be root-owned with mode 600'; return 1; }
  $SUDO openssl verify -purpose sslclient -CAfile "$ca" "$cert" >/dev/null 2>&1 \
    || { origin_credentials_error 'health client certificate chain or client purpose is invalid'; return 1; }
  $SUDO openssl x509 -in "$cert" -noout -ext extendedKeyUsage 2>/dev/null | grep -q 'TLS Web Client Authentication' \
    || { origin_credentials_error 'health certificate must explicitly permit client authentication'; return 1; }
  for file in "$ca" "$cert"; do
    $SUDO openssl x509 -in "$file" -checkend 604800 -noout >/dev/null 2>&1 \
      || { origin_credentials_error 'origin client CA or health certificate expires within seven days'; return 1; }
  done
  cert_public=$($SUDO openssl x509 -in "$cert" -pubkey -noout 2>/dev/null) \
    || { origin_credentials_error 'cannot read health certificate public key'; return 1; }
  key_public=$($SUDO openssl pkey -in "$key" -pubout -passin pass: 2>/dev/null) \
    || { origin_credentials_error 'cannot read unencrypted health client key'; return 1; }
  [ -n "$cert_public" ] && [ "$cert_public" = "$key_public" ] \
    || { origin_credentials_error 'health certificate and private key do not match'; return 1; }
}

origin_curl() {
  $SUDO curl --noproxy '*' --cacert deploy/trust/cloudflare-origin-ca-rsa.pem \
    --cert "$ORIGIN_HEALTH_CERT" --key "$ORIGIN_HEALTH_KEY" "$@"
}

verify_origin_rejects_anonymous() {
  local status code=0
  status=$(curl --noproxy '*' --cacert deploy/trust/cloudflare-origin-ca-rsa.pem \
    --max-time 10 --connect-timeout 5 --resolve "$1:443:127.0.0.1" \
    -sS -o /dev/null -w '%{http_code}' "https://$1/" 2>/dev/null) || code=$?
  # Positive probes already proved the listener and server certificate work.
  # Require a TLS failure, not a timeout, refused connection or HTTP rejection.
  [ "$status" = 000 ] && { [ "$code" -eq 35 ] || [ "$code" -eq 56 ]; }
}
# END origin client certificate guard functions

# BEGIN managed storage guard functions
managed_storage_error() {
  printf 'Managed storage gate: %s\n' "$1" >&2
  return 1
}

verify_managed_storage_configuration() {
  local driver="${STORAGE_DRIVER:-}" name value bucket public_bucket contents line key literal
  local -A saved=() effective=()
  case "$driver" in
    local|r2|oss) ;;
    *) managed_storage_error 'STORAGE_DRIVER must be local, r2 or oss'; return 1 ;;
  esac
  # Match the credential guard's read-only parsing; never source .env. Only
  # storage settings are read, and quoted values/Compose dollar escaping remain literal.
  if [ -f "$DEPLOY_PATH/.env" ]; then
    contents=$($SUDO awk '/^[[:space:]]*(MANAGED_R2_BUCKET|MANAGED_R2_PREFIX|MANAGED_OSS_BUCKET|MANAGED_OSS_ENDPOINT|MANAGED_OSS_PREFIX|MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED|R2_BUCKET|ALIYUN_OSS_BUCKET)[[:space:]]*=/' "$DEPLOY_PATH/.env" 2>/dev/null) \
      || { managed_storage_error 'cannot read existing storage configuration'; return 1; }
    while IFS= read -r line; do
      [[ "$line" =~ ^[[:space:]]*(MANAGED_R2_BUCKET|MANAGED_R2_PREFIX|MANAGED_OSS_BUCKET|MANAGED_OSS_ENDPOINT|MANAGED_OSS_PREFIX|MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED|R2_BUCKET|ALIYUN_OSS_BUCKET)[[:space:]]*=(.*)$ ]] || continue
      key="${BASH_REMATCH[1]}"
      [ -z "${saved[$key]+present}" ] \
        || { managed_storage_error 'existing storage configuration contains duplicate keys'; return 1; }
      value="${BASH_REMATCH[2]}"
      value="${value#"${value%%[![:space:]]*}"}"
      value="${value%"${value##*[![:space:]]}"}"
      literal=false
      case "${value:0:1}" in
        "'")
          [[ ${#value} -ge 2 && "${value: -1}" == "'" ]] \
            || { managed_storage_error 'unsupported storage configuration quoting'; return 1; }
          value="${value:1:${#value}-2}"
          literal=true
          ;;
        '"')
          [[ ${#value} -ge 2 && "${value: -1}" == '"' ]] \
            || { managed_storage_error 'unsupported storage configuration quoting'; return 1; }
          value="${value:1:${#value}-2}"
          ;;
      esac
      if [ "$literal" = false ]; then value="${value//\$\$/\$}"; fi
      saved[$key]="$value"
    done <<< "$contents"
  fi
  for name in MANAGED_R2_BUCKET MANAGED_R2_PREFIX MANAGED_OSS_BUCKET MANAGED_OSS_ENDPOINT MANAGED_OSS_PREFIX MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED; do
    value="${!name:-}"
    value="${value:-${saved[$name]:-}}"
    effective[$name]="$value"
    if [[ "$value" == *$'\n'* || "$value" == *$'\r'* ]]; then
      managed_storage_error 'managed configuration must not contain line breaks'; return 1
    fi
  done
  case "${effective[MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED]}" in
    ''|true|false) ;;
    *) managed_storage_error 'private-origin confirmation must be true or false when supplied'; return 1 ;;
  esac
  for name in r2 oss; do
    if [ "$name" = r2 ]; then
      bucket="${effective[MANAGED_R2_BUCKET]}"
      # Public bucket writers only run for the active driver; historical
      # drivers retain their stored bucket even when runner inputs differ.
      if [ "$driver" = r2 ]; then public_bucket="${R2_BUCKET:-freedompost}";
      else public_bucket="${saved[R2_BUCKET]:-freedompost}"; fi
    else
      bucket="${effective[MANAGED_OSS_BUCKET]}"
      if [ "$driver" = oss ]; then public_bucket="${ALIYUN_OSS_BUCKET:-}";
      else public_bucket="${saved[ALIYUN_OSS_BUCKET]:-}"; fi
    fi
    if [ "$driver" = "$name" ] || [ -n "$bucket" ]; then
      [[ "$bucket" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] \
        || { managed_storage_error 'a valid dedicated private bucket is required'; return 1; }
      [ -n "$public_bucket" ] && [ "$bucket" != "$public_bucket" ] \
        || { managed_storage_error 'managed and public buckets must be distinct'; return 1; }
      [ "${effective[MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED]}" = true ] \
        || { managed_storage_error 'confirm private origin access before releasing remote managed storage'; return 1; }
    fi
  done
}

write_managed_storage_configuration() {
  local name
  # Empty runner inputs must never erase persistent private storage settings.
  for name in MANAGED_R2_BUCKET MANAGED_R2_PREFIX MANAGED_OSS_BUCKET MANAGED_OSS_ENDPOINT MANAGED_OSS_PREFIX MANAGED_MEDIA_PRIVATE_ORIGIN_CONFIRMED; do
    set_env_if_present "$name" "${!name:-}"
  done
}
# END managed storage guard functions

# BEGIN rollback directory guard functions
prepare_rollback_directory() {
  local root="${DEPLOY_PATH}.rollback"
  test ! -L "$root"
  test ! -e "$root" || test -d "$root"
  $SUDO mkdir -p "$root"
  $SUDO chown "$(id -u):$(id -g)" "$root"
  $SUDO chmod 700 "$root"
  # Refuse to reuse an existing snapshot, including a symlink at this path.
  (umask 077; mkdir -m 700 "$ROLLBACK_DIR")
}
# END rollback directory guard functions

[[ "${RELEASE_SHA:-}" =~ ^[0-9a-f]{40}$ ]]
verify_origin_client_material "$ORIGIN_CLIENT_CA" "$ORIGIN_HEALTH_CERT" "$ORIGIN_HEALTH_KEY"
verify_managed_storage_configuration
prepare_admin_credentials
printf 'Admin credential mode: %s\n' "$ADMIN_CREDENTIAL_MODE"
# Keep the running images tagged before the normal image-prune/build steps.
# The snapshot contains credentials, stays on this host and is never uploaded.
ROLLBACK_DIR="${DEPLOY_PATH}.rollback/$(date -u +%Y%m%dT%H%M%SZ)-${RELEASE_SHA:0:12}"
prepare_rollback_directory
if [ -f "$DEPLOY_PATH/.env" ]; then
  (
    umask 077
    $SUDO tar -czf "$ROLLBACK_DIR/config.tar.gz" -C "$DEPLOY_PATH" .env deploy
    $SUDO chmod 600 "$ROLLBACK_DIR/config.tar.gz"
    printf 'services:\n' > "$ROLLBACK_DIR/images.yml"
    for service in nginx api-go paid-access; do
      if [ "$service" = api-go ]; then
        container="$ADMIN_CONTAINER_ID"
      else
        containers=$(compose --env-file "$DEPLOY_PATH/.env" -f "$DEPLOY_PATH/deploy/docker-compose.yml" ps -aq "$service")
        container="${containers%%$'\n'*}"
      fi
      test -n "$container"
      previous_image=$($SUDO docker inspect --format '{{.Image}}' "$container")
      rollback_tag="freedompost-rollback-${service}:$(basename "$ROLLBACK_DIR")"
      $SUDO docker tag "$previous_image" "$rollback_tag"
      printf '  %s:\n    image: %s\n' "$service" "$rollback_tag" >> "$ROLLBACK_DIR/images.yml"
    done
    printf '%s\n' "$RELEASE_SHA" > "$ROLLBACK_DIR/replaced-by-sha"
    echo "Rollback snapshot: $ROLLBACK_DIR"
    )
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
# Generated trees must be replaced, not overlaid: removed routes otherwise
# survive in the next gateway image. Keep prior and failed outputs for recovery.
install_static_release() (
  local app target backup="$ROLLBACK_DIR/static"
  local -a replaced=()
  restore_static_release() {
    local result="$1" app target
    trap - EXIT
    if [ "$result" -ne 0 ]; then
      for app in "${replaced[@]}"; do
        target="$DEPLOY_PATH/apps/$app/dist"
        if [ -e "$target" ] || [ -L "$target" ]; then
          if ! mv "$target" "$backup/$app/failed"; then
            printf 'Static recovery requires manual restore from %s\n' "$backup/$app/previous" >&2
            continue
          fi
        fi
        if [ -d "$backup/$app/previous" ]; then
          mv "$backup/$app/previous" "$target" \
            || printf 'Static recovery requires manual restore from %s\n' "$backup/$app/previous" >&2
        fi
      done
    fi
    exit "$result"
  }
  trap 'restore_static_release "$?"' EXIT
  # Reject aliases before moving either generated tree; never move user data.
  if [ -e "$backup" ] || [ -L "$backup" ]; then
    printf 'Static backup already exists: %s\n' "$backup" >&2
    exit 1
  fi
  test ! -L "$DEPLOY_PATH/apps"
  for app in public-reader admin; do
    test ! -L "$DEPLOY_PATH/apps/$app"
    test ! -L "$DEPLOY_PATH/apps/$app/dist"
    test ! -e "$DEPLOY_PATH/apps/$app/dist" || test -d "$DEPLOY_PATH/apps/$app/dist"
  done
  for app in public-reader admin; do
    target="$DEPLOY_PATH/apps/$app/dist"
    mkdir -p "$backup/$app" "$DEPLOY_PATH/apps/$app"
    if [ -e "$target" ]; then mv "$target" "$backup/$app/previous"; fi
    replaced+=("$app")
  done
  tar -xzf /tmp/freedompost-deploy.tar.gz -C "$DEPLOY_PATH"
  test -f "$DEPLOY_PATH/apps/public-reader/dist/reader/index.html"
  test -f "$DEPLOY_PATH/apps/public-reader/dist/reader-shell/index.html"
  test -f "$DEPLOY_PATH/apps/admin/dist/index.html"
)
install_static_release
rm -f /tmp/freedompost-deploy.tar.gz
cd "$DEPLOY_PATH"

# This hostname is a production contract, independent of caller-supplied values.
test "${ADMIN_DOMAIN:-}" = 'admin-freedompost.thinderbox.uk'
test "${ADMIN_ORIGIN:-}" = 'https://admin-freedompost.thinderbox.uk'
$SUDO openssl verify -CAfile deploy/trust/cloudflare-origin-ca-rsa.pem -verify_hostname "$ADMIN_DOMAIN" /etc/caddy/certs/admin-origin.crt
$SUDO openssl x509 -in /etc/caddy/certs/admin-origin.crt -checkend 604800 -noout
if [ -f .env ]; then
  $SUDO chown "$(id -u):$(id -g)" .env
  chmod 600 .env
fi

if [ ! -f .env ]; then
  {
    printf '%s\n' "NODE_ENV=production"
    printf '%s\n' "PORT=3000"
    printf '%s\n' "HOST=0.0.0.0"
    printf '%s\n' "LOG_LEVEL=info"
    printf 'PREVIEW_DOMAIN=%s\n' "$PREVIEW_DOMAIN"
    printf 'ADMIN_DOMAIN=%s\n' "${ADMIN_DOMAIN:-admin-freedompost.thinderbox.uk}"
    printf 'ADMIN_ORIGIN=%s\n' "${ADMIN_ORIGIN:-https://admin-freedompost.thinderbox.uk}"
    printf 'ORIGIN_TEST_HOST=%s\n' "$DEPLOY_HOST"
    printf 'PUBLIC_SITE_URL=https://%s\n' "$PREVIEW_DOMAIN"
    printf 'VITE_PUBLIC_SITE_URL=https://%s\n' "$PREVIEW_DOMAIN"
    printf '%s\n' "COOKIE_SECURE=true"
    printf '%s\n' "TRUST_PROXY=true"
    printf 'REDIS_PASSWORD=%s\n' "$REDIS_PASSWORD"
    printf 'REDIS_URL=redis://:%s@redis:6379\n' "$REDIS_PASSWORD"
    printf 'CHATWOOT_BASE_URL=%s\n' "$CHATWOOT_BASE_URL"
    printf 'CHATWOOT_WEBSITE_TOKEN=%s\n' "$CHATWOOT_WEBSITE_TOKEN"
    printf 'CHATWOOT_API_TOKEN=%s\n' "$CHATWOOT_API_TOKEN"
    printf 'CHATWOOT_ACCOUNT_ID=%s\n' "$CHATWOOT_ACCOUNT_ID"
    printf 'CHATWOOT_INBOX_ID=%s\n' "$CHATWOOT_INBOX_ID"
    printf 'CHATWOOT_TIMEOUT_MS=%s\n' "$CHATWOOT_TIMEOUT_MS"
    printf 'CHANNEL_BRIDGE_ENABLED=%s\n' "${CHANNEL_BRIDGE_ENABLED:-false}"
    printf 'CHANNEL_BRIDGE_PROVIDER=%s\n' "${CHANNEL_BRIDGE_PROVIDER:-wecom}"
    printf 'CHANNEL_BRIDGE_ALLOW_UNPREFIXED_REPLIES=%s\n' "${CHANNEL_BRIDGE_ALLOW_UNPREFIXED_REPLIES:-false}"
    printf 'CHATWOOT_WEBHOOK_SECRET=%s\n' "${CHATWOOT_WEBHOOK_SECRET:-}"
    printf 'WECOM_BASE_URL=%s\n' "${WECOM_BASE_URL:-https://qyapi.weixin.qq.com}"
    printf 'WECOM_CORP_ID=%s\n' "${WECOM_CORP_ID:-}"
    printf 'WECOM_CORP_SECRET=%s\n' "${WECOM_CORP_SECRET:-}"
    printf 'WECOM_AGENT_ID=%s\n' "${WECOM_AGENT_ID:-}"
    printf 'WECOM_CALLBACK_TOKEN=%s\n' "${WECOM_CALLBACK_TOKEN:-}"
    printf 'WECOM_ENCODING_AES_KEY=%s\n' "${WECOM_ENCODING_AES_KEY:-}"
    printf 'WECOM_RECEIVE_ID=%s\n' "${WECOM_RECEIVE_ID:-}"
    printf 'WECOM_OPERATOR_USER_ID=%s\n' "${WECOM_OPERATOR_USER_ID:-}"
    printf '%s\n' "PAID_ARTICLES_ENABLED=true"
    printf '%s\n' "PAID_ACCESS_INTERNAL_URL=http://paid-access:8080"
    printf 'PAID_ACCESS_INTERNAL_SECRET=%s\n' "$PAID_ACCESS_INTERNAL_SECRET"
    printf '%s\n' "PAID_ACCESS_WECHAT_IMAGE_URL=/images/contact-wechat.jpg"
    printf '%s\n' "API_BODY_LIMIT_BYTES=104857600"
    printf '%s\n' "UPLOAD_MAX_BYTES=524288000"
    printf 'COOKIE_SECRET=%s\n' "$COOKIE_SECRET"
    printf 'VISITOR_HASH_SALT=%s\n' "$VISITOR_HASH_SALT"
    printf 'ADMIN_USERNAME=%s\n' "$ADMIN_USERNAME"
    printf 'ADMIN_PASSWORD_HASH=%s\n' "$ADMIN_PASSWORD_HASH"
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
  local key value env_tmp
  key="$1"
  # Compose interpolates dollar signs even in service env_file values.
  value="${2//\$/\$\$}"
  env_tmp="$(mktemp .env.XXXXXX)"
  if ! ENV_KEY="$key" ENV_VALUE="$value" awk '
    BEGIN {
      key = ENVIRON["ENV_KEY"]
      value = ENVIRON["ENV_VALUE"]
      prefix = key "="
      pattern = "^[[:space:]]*" key "[[:space:]]*="
      found = 0
    }
    $0 ~ pattern {
      print prefix value
      found = 1
      next
    }
    { print }
    END {
      if (!found) {
        print prefix value
      }
    }
  ' .env > "$env_tmp"; then
    rm -f "$env_tmp"
    return 1
  fi
  mv "$env_tmp" .env
}

remove_env_key() {
  local key env_tmp
  key="$1"
  env_tmp="$(mktemp .env.XXXXXX)"
  if ! awk -v key="$key" '$0 !~ ("^[[:space:]]*" key "[[:space:]]*=") { print }' .env > "$env_tmp"; then
    rm -f "$env_tmp"
    return 1
  fi
  mv "$env_tmp" .env
}

set_env_if_present() {
  key="$1"
  value="${2:-}"
  if [ -n "$value" ]; then
    set_env "$key" "$value"
  fi
}

set_env "PREVIEW_DOMAIN" "$PREVIEW_DOMAIN"
set_env "ADMIN_DOMAIN" "${ADMIN_DOMAIN:-admin-freedompost.thinderbox.uk}"
set_env "ADMIN_ORIGIN" "${ADMIN_ORIGIN:-https://admin-freedompost.thinderbox.uk}"
set_env "ORIGIN_TEST_HOST" "$DEPLOY_HOST"
set_env "PUBLIC_SITE_URL" "https://$PREVIEW_DOMAIN"
set_env "VITE_PUBLIC_SITE_URL" "https://$PREVIEW_DOMAIN"
set_env "COOKIE_SECURE" "true"
set_env "TRUST_PROXY" "true"
if [ -n "${REDIS_PASSWORD:-}" ]; then
  set_env "REDIS_PASSWORD" "$REDIS_PASSWORD"
  set_env "REDIS_URL" "redis://:${REDIS_PASSWORD}@redis:6379"
fi
set_env "CHATWOOT_BASE_URL" "$CHATWOOT_BASE_URL"
set_env "CHATWOOT_WEBSITE_TOKEN" "$CHATWOOT_WEBSITE_TOKEN"
set_env "CHATWOOT_API_TOKEN" "$CHATWOOT_API_TOKEN"
set_env "CHATWOOT_ACCOUNT_ID" "$CHATWOOT_ACCOUNT_ID"
set_env "CHATWOOT_INBOX_ID" "$CHATWOOT_INBOX_ID"
set_env "CHATWOOT_TIMEOUT_MS" "$CHATWOOT_TIMEOUT_MS"
set_env "CHANNEL_BRIDGE_ENABLED" "${CHANNEL_BRIDGE_ENABLED:-false}"
set_env "CHANNEL_BRIDGE_PROVIDER" "${CHANNEL_BRIDGE_PROVIDER:-wecom}"
set_env "CHANNEL_BRIDGE_ALLOW_UNPREFIXED_REPLIES" "${CHANNEL_BRIDGE_ALLOW_UNPREFIXED_REPLIES:-false}"
set_env_if_present "CHATWOOT_WEBHOOK_SECRET" "${CHATWOOT_WEBHOOK_SECRET:-}"
set_env "WECOM_BASE_URL" "${WECOM_BASE_URL:-https://qyapi.weixin.qq.com}"
set_env_if_present "WECOM_CORP_ID" "${WECOM_CORP_ID:-}"
set_env_if_present "WECOM_CORP_SECRET" "${WECOM_CORP_SECRET:-}"
set_env_if_present "WECOM_AGENT_ID" "${WECOM_AGENT_ID:-}"
set_env_if_present "WECOM_CALLBACK_TOKEN" "${WECOM_CALLBACK_TOKEN:-}"
set_env_if_present "WECOM_ENCODING_AES_KEY" "${WECOM_ENCODING_AES_KEY:-}"
set_env_if_present "WECOM_RECEIVE_ID" "${WECOM_RECEIVE_ID:-}"
set_env_if_present "WECOM_OPERATOR_USER_ID" "${WECOM_OPERATOR_USER_ID:-}"
set_env "PAID_ARTICLES_ENABLED" "true"
set_env "PAID_ACCESS_INTERNAL_URL" "http://paid-access:8080"
set_env "PAID_ACCESS_INTERNAL_SECRET" "$PAID_ACCESS_INTERNAL_SECRET"
set_env "PAID_ACCESS_WECHAT_IMAGE_URL" "/images/contact-wechat.jpg"
set_env "COOKIE_SECRET" "$COOKIE_SECRET"
set_env "VISITOR_HASH_SALT" "$VISITOR_HASH_SALT"
# Remove legacy plaintext credential aliases from existing servers. The
# bcrypt hash above is the sole production source of truth.
remove_env_key "ADMIN_PASSWORD"
remove_env_key "HASH_PASSWORD"
# Preserve the existing username line, including literal quoting and spaces.
# Bootstrap writes admin above; password rotation must not rewrite the username.
set_env "ADMIN_PASSWORD_HASH" "$ADMIN_PASSWORD_HASH"
set_env "POSTGRES_PASSWORD" "$POSTGRES_PASSWORD"
set_env "DATABASE_URL" "postgres://freedompost:$POSTGRES_PASSWORD@postgres:5432/freedompost"
set_env "STORAGE_DRIVER" "$STORAGE_DRIVER"
write_managed_storage_configuration
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

chmod 600 .env
if grep -qE '^(ADMIN_PASSWORD|HASH_PASSWORD)=' .env; then
  echo "legacy admin credential variables remain in .env" >&2
  exit 1
fi

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
DAEMON_JSON_TMP="$(mktemp)"
cat <<'JSON' > "$DAEMON_JSON_TMP"
{
  "registry-mirrors": [
    "https://docker.m.daocloud.io",
    "https://hub-mirror.c.163.com",
    "https://mirror.baidubce.com"
  ]
}
JSON
if ! $SUDO cmp -s "$DAEMON_JSON_TMP" /etc/docker/daemon.json 2>/dev/null; then
  $SUDO cp "$DAEMON_JSON_TMP" /etc/docker/daemon.json
  if command -v systemctl >/dev/null 2>&1; then
    $SUDO systemctl restart docker
  elif command -v service >/dev/null 2>&1; then
    $SUDO service docker restart
  fi
fi
rm -f "$DAEMON_JSON_TMP"

docker_cmd version
compose version

# Ingress is provisioned separately: allow Cloudflare IPv4/IPv6 ranges in the
# cloud security group. Never reopen published Docker ports to all sources.

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
verify_candidate_admin_credentials

# Validate certificate loading before replacing the running public gateway.
compose --env-file .env -f deploy/docker-compose.yml run --rm --no-deps nginx caddy validate --config /etc/caddy/Caddyfile

# Wait for PostgreSQL to finish initializing and recovery
for attempt in $(seq 1 30); do
  if compose --env-file .env -f deploy/docker-compose.yml exec -T postgres pg_isready -U freedompost >/dev/null 2>&1; then
    echo "=== PostgreSQL is ready ==="
    break
  fi
  if [ "$attempt" -eq 30 ]; then
    echo "=== ERROR: PostgreSQL failed to become ready within 30s ===" >&2
    exit 1
  fi
  echo "Waiting for PostgreSQL ($attempt/30)..."
  sleep 1
done

echo "=== Running Go database migrations ==="
compose --env-file .env -f deploy/docker-compose.yml run --rm api-go -migrate

echo "=== Starting production services ==="
compose --env-file .env -f deploy/docker-compose.yml up -d --force-recreate --remove-orphans paid-access nginx api-go
verify_running_admin_credentials

# ── Deployment gate: Go API must be healthy ────────────────────────────
echo "=== Verifying Go API deployment health gates ==="
for attempt in $(seq 1 60); do
  if compose --env-file .env -f deploy/docker-compose.yml exec -T api-go /fp-api -health-check \
    && origin_curl -fsS --max-time 10 --connect-timeout 5 --resolve "$PREVIEW_DOMAIN:443:127.0.0.1" "https://$PREVIEW_DOMAIN/health" >/dev/null \
    && origin_curl -fsS --max-time 10 --connect-timeout 5 --resolve "$ADMIN_DOMAIN:443:127.0.0.1" "https://$ADMIN_DOMAIN/" >/dev/null \
    && origin_curl -sS --max-time 10 --connect-timeout 5 --resolve "$ADMIN_DOMAIN:443:127.0.0.1" -o /dev/null -w '%{http_code}' "https://$ADMIN_DOMAIN/api/admin/session" | grep -q '^401$' \
    && origin_curl -sS --max-time 10 --connect-timeout 5 --resolve "$PREVIEW_DOMAIN:443:127.0.0.1" -o /dev/null -w '%{http_code}' "https://$PREVIEW_DOMAIN/api/admin/session" | grep -q '^404$' \
    && benefit_headers=$(origin_curl -fsS --max-time 10 --connect-timeout 5 --resolve "$PREVIEW_DOMAIN:443:127.0.0.1" -D - -o /dev/null "https://$PREVIEW_DOMAIN/api/benefits/webmaster") \
    && grep -qi '^cache-control:.*no-store' <<< "$benefit_headers" \
    && verify_origin_rejects_anonymous "$ADMIN_DOMAIN" \
    && verify_origin_rejects_anonymous "$PREVIEW_DOMAIN"; then
    echo "=== Go API health check passed (attempt $attempt) ==="
    printf '%s\n' "$RELEASE_SHA" > .release-sha
    origin_ca_fingerprint=$($SUDO openssl x509 -in "$ORIGIN_CLIENT_CA" -noout -fingerprint -sha256)
    printf 'release_sha=%s\nrollback_dir=%s\ndeployed_at=%s\ncredential_mode=%s\norigin_auth=custom-aop-required\norigin_ca_fingerprint=%s\n' "$RELEASE_SHA" "$ROLLBACK_DIR" "$(date -u +%FT%TZ)" "$ADMIN_CREDENTIAL_MODE" "$origin_ca_fingerprint" > .release-record
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
