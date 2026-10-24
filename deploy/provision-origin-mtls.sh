#!/usr/bin/env bash
set -euo pipefail
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
# Only public, bounded inputs cross sudo. No key or application environment is read.
[[ "${RELEASE_SHA:-}" =~ ^[0-9a-f]{40}$ ]]
case "${MTLS_OPERATION:-}" in
  inspect|create-health-csr) test -z "${CA_BASE64:-}${HEALTH_CERT_BASE64:-}" ;;
  install-health-material)
    for value in "${CA_BASE64:-}" "${HEALTH_CERT_BASE64:-}"; do
      test "${#value}" -le 32768
      [[ "$value" =~ ^[A-Za-z0-9+/]+={0,2}$ ]]
    done ;;
  *) exit 1 ;;
esac
SUDO=()
if [ "$(id -u)" != 0 ]; then SUDO=(sudo -n); fi
"${SUDO[@]}" env RELEASE_SHA="$RELEASE_SHA" MTLS_OPERATION="$MTLS_OPERATION" CA_BASE64="${CA_BASE64:-}" HEALTH_CERT_BASE64="${HEALTH_CERT_BASE64:-}" /bin/bash -s <<'ROOT_SCRIPT'
set -euo pipefail
export LC_ALL=C
umask 077
CA=/etc/caddy/certs/origin-pull-client-ca.crt
CERT=/etc/freedompost/origin-health-client.crt
KEY=/etc/freedompost/origin-health-client.key
STAGE=/etc/freedompost/.origin-mtls-provision
fail() { printf 'Origin mTLS provisioning: %s\n' "$1" >&2; exit 1; }
safe_dir() {
  local mode
  [ ! -L "$1" ] && [ -d "$1" ] && [ "$(stat -c %u "$1")" = 0 ] || fail 'unsafe certificate directory';
  mode=$(stat -c %a "$1")
  (( (8#$mode & 0022) == 0 )) || fail 'certificate directory is writable by another user'
}
safe_file() { [ ! -L "$1" ] && { [ ! -e "$1" ] || [ -f "$1" ]; } || fail 'unsafe certificate file'; }
key_valid() {
  safe_file "$1"
  [ -s "$1" ] && [ "$(stat -c '%u:%a' "$1")" = '0:600' ] || fail 'health key must be root-owned with mode 600'
  openssl pkey -in "$1" -passin pass: -check -noout >/dev/null 2>&1 || fail 'invalid health private key'
}
for file in "$CA" "$CERT" "$KEY"; do safe_file "$file"; done
if [ "$MTLS_OPERATION" = inspect ]; then
  for file in "$CA" "$CERT"; do
    printf 'certificate_path=%s\n' "$file"
    if [ -s "$file" ]; then
      openssl x509 -in "$file" -noout -subject -issuer -serial -dates -fingerprint -sha256 2>/dev/null || printf 'certificate_status=invalid\n'
    else printf 'certificate_status=absent\n'; fi
  done
  if [ -e "$KEY" ]; then printf 'health_key_owner_mode=%s\n' "$(stat -c '%u:%a' "$KEY")"; else printf 'health_key_status=absent\n'; fi
  if [ -f "$STAGE/install.state" ] && [ ! -L "$STAGE/install.state" ]; then
    # Only our fixed status vocabulary is reported, never arbitrary file contents.
    if grep -qx 'complete' "$STAGE/install.state"; then printf 'installation_status=complete\n'; else printf 'installation_status=incomplete\n'; fi
  fi
  for service in docker caddy; do
    printf 'service=%s state=' "$service"
    systemctl is-active "$service" 2>/dev/null || true
  done
  exit 0
fi
safe_dir /etc
[ ! -L /etc/freedompost ] || fail 'unsafe certificate directory'
if [ ! -e /etc/freedompost ]; then install -d -m 755 /etc/freedompost; fi
safe_dir /etc/freedompost
[ ! -L "$STAGE" ] || fail 'unsafe staging directory'
if [ ! -e "$STAGE" ]; then install -d -m 700 "$STAGE"; fi
safe_dir "$STAGE"
safe_file "$STAGE/lock"
exec 9>"$STAGE/lock"
flock -n 9 || fail 'another certificate operation is running'
for file in "$STAGE/health.key" "$STAGE/health.csr" "$STAGE/install.state"; do safe_file "$file"; done
tmp=$(mktemp -d "$STAGE/work.XXXXXXXX")
created=()
adjacent_files=()
cleanup() {
  local status=$?
  trap - EXIT
  if [ "$status" != 0 ] && [ "${install_started:-0}" = 1 ]; then
    # Existing files were checked before mutation and never enter this list.
    for file in "${created[@]}"; do rm -f -- "$file"; done
    printf 'rolled-back\n' >> "$STAGE/install.state"
  fi
  for file in "${adjacent_files[@]}"; do rm -f -- "$file"; done
  rm -rf -- "$tmp"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
source_key="$STAGE/health.key"
if [ -e "$KEY" ]; then source_key="$KEY"; fi
if [ "$MTLS_OPERATION" = create-health-csr ]; then
  [ ! -e "$CERT" ] || [ -e "$KEY" ] || fail 'certificate exists without its formal key'
  if [ ! -e "$source_key" ]; then
    openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$tmp/health.key" >/dev/null 2>&1
    chmod 600 "$tmp/health.key"
    ln "$tmp/health.key" "$source_key"
  fi
  key_valid "$source_key"
  openssl req -new -sha256 -key "$source_key" -subj '/CN=freedompost-origin-health' -addext 'extendedKeyUsage=clientAuth' -out "$tmp/health.csr" 2>/dev/null
  openssl req -in "$tmp/health.csr" -verify -noout >/dev/null 2>&1
  # The CSR is public. Its key remains server-local, including on retries.
  mv -f "$tmp/health.csr" "$STAGE/health.csr"
  cat "$STAGE/health.csr"
  exit 0
fi
safe_dir /etc/caddy
safe_dir /etc/caddy/certs
key_valid "$source_key"
decode_cert() {
  printf '%s' "$1" | base64 --decode | tr -d '\r' > "$tmp/input.pem" || fail 'invalid base64 certificate'
  [ "$(grep -c '^-----BEGIN CERTIFICATE-----$' "$tmp/input.pem")" = 1 ] || fail 'expected one public PEM certificate'
  ! grep -q 'PRIVATE KEY' "$tmp/input.pem" || fail 'private keys are not accepted'
  openssl x509 -in "$tmp/input.pem" -out "$2" 2>/dev/null || fail 'invalid public certificate'
}
decode_cert "$CA_BASE64" "$tmp/ca.crt"
decode_cert "$HEALTH_CERT_BASE64" "$tmp/health.crt"
openssl x509 -in "$tmp/ca.crt" -noout -ext basicConstraints 2>/dev/null | grep -q 'CA:TRUE' || fail 'issuer must be a CA'
openssl verify -CAfile "$tmp/ca.crt" "$tmp/ca.crt" >/dev/null 2>&1 || fail 'invalid CA certificate'
openssl verify -purpose sslclient -CAfile "$tmp/ca.crt" "$tmp/health.crt" >/dev/null 2>&1 || fail 'invalid health chain or client purpose'
openssl x509 -in "$tmp/health.crt" -noout -ext extendedKeyUsage 2>/dev/null | grep -q 'TLS Web Client Authentication' || fail 'health certificate must explicitly permit clientAuth'
for file in "$tmp/ca.crt" "$tmp/health.crt"; do
  openssl x509 -in "$file" -checkend 604800 -noout >/dev/null 2>&1 || fail 'certificate expires within seven days'
done
cert_public=$(openssl x509 -in "$tmp/health.crt" -pubkey -noout)
key_public=$(openssl pkey -in "$source_key" -passin pass: -pubout 2>/dev/null)
[ "$cert_public" = "$key_public" ] || fail 'health certificate does not match the server key'
same_cert() {
  [ "$(openssl x509 -in "$1" -outform DER 2>/dev/null | openssl dgst -sha256)" = "$(openssl x509 -in "$2" -outform DER | openssl dgst -sha256)" ]
}
for pair in "$CA:ca.crt" "$CERT:health.crt"; do
  dest=${pair%:*}; source=${pair#*:}
  if [ -e "$dest" ]; then same_cert "$dest" "$tmp/$source" || fail 'refusing to replace existing different certificate material'; fi
done
# Record intent before the first formal file. Each link is atomic and cannot
# overwrite; ordinary errors roll back only files created by this transaction.
printf 'installing revision=%s\n' "$RELEASE_SHA" > "$STAGE/install.state"
install_started=1
atomic_install() {
  local source="$1" dest="$2" mode="$3" adjacent
  [ ! -e "$dest" ] || return 0
  printf 'planned=%s\n' "$dest" >> "$STAGE/install.state"
  adjacent=$(mktemp "${dest}.new.XXXXXXXX")
  adjacent_files+=("$adjacent")
  install -m "$mode" "$source" "$adjacent"
  if ! ln "$adjacent" "$dest"; then rm -f "$adjacent"; return 1; fi
  created+=("$dest")
  rm -f "$adjacent"
}
atomic_install "$source_key" "$KEY" 600
atomic_install "$tmp/health.crt" "$CERT" 644
atomic_install "$tmp/ca.crt" "$CA" 644
printf 'complete\n' >> "$STAGE/install.state"
printf 'Origin mTLS material installed; services unchanged.\n'
ROOT_SCRIPT
