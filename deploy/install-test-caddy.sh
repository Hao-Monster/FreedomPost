#!/usr/bin/env bash
set -euo pipefail
# GitHub Ubuntu amd64 runners only. This installs an isolated test executable,
# never a service. SHA512 is pinned from the official v2.11.7 release checksums.
test "$(uname -sm)" = 'Linux x86_64'
test -n "${RUNNER_TEMP:-}"
test -n "${GITHUB_ENV:-}"
test_dir=$(mktemp -d "$RUNNER_TEMP/freedompost-caddy.XXXXXX")
curl -fsSL --retry 3 --max-time 120 \
  https://github.com/caddyserver/caddy/releases/download/v2.11.7/caddy_2.11.7_linux_amd64.tar.gz \
  -o "$test_dir/caddy.tar.gz"
printf '%s  %s\n' a7a433a1b133efc3c8d10eb0b99d52a24b5ef5c322dc77f5282182b1c0402139ab83f3a99f0c52409df77d20123fb0b523edad8a66d8f5e49136197bf61ef0e7 "$test_dir/caddy.tar.gz" | sha512sum -c -
tar -xzf "$test_dir/caddy.tar.gz" -C "$test_dir" caddy
"$test_dir/caddy" version | grep -q '^v2\.11\.7 '
printf 'CADDY_BIN=%s/caddy\n' "$test_dir" >> "$GITHUB_ENV"
