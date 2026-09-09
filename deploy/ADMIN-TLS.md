# Admin hostname and origin TLS

The admin hostname is `admin-freedompost.thinderbox.uk`. The previous
`admin.freedompost.thinderbox.uk` is not covered by `*.thinderbox.uk` certificates.

## Before rollout

1. Configure a proxied DNS record for the new hostname pointing to the production
   origin. Preserve the existing Cloudflare Access application policy and protect
   the entire new hostname, including `/api/*`, before exposing it.
2. Keep Cloudflare SSL/TLS on Full (strict). Verify its edge certificate covers
   the new hostname separately from the origin certificate.
3. Install the matching Origin CA certificate and private key at
   `/etc/caddy/certs/admin-origin.crt` and `/etc/caddy/certs/admin-origin.key`.
   Keep the private key root-owned with mode 600. Never commit either file.
4. Validate the hostname, expiry, issuer chain and key match. Back up the current
   Caddy configuration, Compose configuration and environment file securely.
5. Use `docker compose --env-file .env -f deploy/docker-compose.yml` from the
   project root. A service's `env_file` alone does not provide Compose's
   `${VARIABLE}` interpolation values. Do not print expanded configuration or
   environment files; production secrets may contain literal dollar signs.

The deployment script validates Caddy and certificate loading before replacing
the gateway. The certificate directory is mounted read-only and survives bundle
updates. Missing or invalid certificates must stop deployment.

## Acceptance checks

- Verify public HTTPS without disabling certificate validation.
- Verify unauthenticated public requests receive the intended Access challenge;
  complete the allowed user's Access login and application login in a browser.
- Verify the origin with the Cloudflare Origin CA root explicitly trusted:
  admin `/` returns 200, `/health` returns 200, and `/api/admin/session` returns
  401 without an application session.
- Verify public-site `/api/admin/session`, `/api/admin/login`, and `/admin/`
  return 404. The public reader must continue to return 200.
- Verify direct origin access cannot bypass the intended Access policy. DNS
  proxying alone is not an origin access restriction; inspect the origin
  firewall or validate Access tokens before claiming that boundary is enforced.

## 2026-09-09 handover validation

On a separate Caddy container bound only to origin loopback port 18443, the
certificate chain and hostname validated against Cloudflare's RSA Origin CA.
All seven origin HTTP checks listed above passed. Production traffic was not
switched during this check. Local deployment tests passed (12 tests), and the
Go HTTP module tests passed with the existing module cache path overridden to
the current E: workspace. The config package has no tests.

Public DNS, Access policy, application login, and production rollout remain
pending until Cloudflare account access is available. Origin checks do not
prove these public-facing acceptance criteria. Preserve the former hostname's
Access protection until migration is complete; its invalid TLS prevents relying
on an HTTPS redirect as a migration mechanism.

For rollback, restore the backed-up gateway configuration/image and environment
as a matched set, then validate and reload/recreate only the affected services.
The previous hostname was already failing public TLS; returning to that state
is containment, not a verified restoration of public admin access.
