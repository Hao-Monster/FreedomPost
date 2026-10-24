# Origin access boundary

## Release status and prerequisites

This change is local code and test preparation. It does **not** configure
Cloudflare, install production certificates, or change the cloud security group.
Those are deployment blockers until separately authorized, completed and verified.
Do not dispatch production deployment simply because CI passes.

The public reader and admin hostname share Caddy. Every HTTPS listener requires a
client certificate signed by a CA dedicated exclusively to this FreedomPost
origin. Cloudflare Access still protects the complete admin hostname, and the
application still requires an administrator session. mTLS prevents unauthenticated
direct origin requests from bypassing the Cloudflare path; it does not replace
Access, WAF rules or application authorization.

Use custom zone-level or hostname-level Authenticated Origin Pulls (AOP). The
default Cloudflare AOP CA is shared across accounts and does not establish this
site's boundary. Never add that shared CA or unrelated organization CAs to this
trust store. The health certificate is an intentional privileged maintenance
identity, so its key must not be exposed to visitors or app containers.

## Host materials and rollout order

1. Create a dedicated private CA through the operator's certificate procedure.
   Keep the CA signing key offline, outside the server and repository. Sign two
   different client certificates with explicit `clientAuth` extended key usage:
   one for Cloudflare and one for host health checks. Do not use test fixtures.
2. After separate authorization, upload only the Cloudflare client certificate
   and its key to custom AOP, and enable it for both public and admin hostnames.
   Preserve Full (strict), the current server certificates, Access application
   coverage/MFA and WAF rules. Confirm the selected AOP scope covers both hosts.
3. Install the CA public certificate at
   `/etc/caddy/certs/origin-pull-client-ca.crt`. Keep the existing
   `/etc/caddy/certs/admin-origin.crt` and `admin-origin.key` in place. Caddy
   mounts this directory read-only. Never place the CA signing key, Cloudflare
   client key or health client key in that directory.
4. Install the health leaf certificate at
   `/etc/freedompost/origin-health-client.crt` and its unencrypted private key at
   `/etc/freedompost/origin-health-client.key`. The private key must be
   `root:root`, mode `0600`. Both files remain outside container mounts, `.env`,
   release archives and GitHub Secrets. OpenSSL and noninteractive sudo must be
   available for the deployment account's existing root operations.
5. Separately authorize and verify cloud security-group ingress: allow TCP
   80/443 only from Cloudflare's current official IPv4 **and** IPv6 ranges.
   Preserve management SSH access. Inspect effective rules and Docker port
   publishing; do not assume UFW INPUT rules protect Docker forwarding. The
   deployment script no longer adds worldwide allow rules, but it does not
   remove historical rules or configure the cloud security group.
6. Only after recording these prerequisites may an explicitly authorized
   protected-main/full-SHA production deployment run. Before snapshot, bundle
   extraction, environment changes or container operations, the script validates
   health-key ownership/mode, certificate chain/client purpose, matching key and
   at least seven days of validity for the CA and health certificate. Missing or
   invalid material stops the release. There is no disable flag or localhost
   exemption.

Changing the CA or rotating certificates is a separate credential operation,
not a side effect of deploying code. Track expiration for both client leaf
certificates and the CA. The server cannot inspect the private Cloudflare leaf;
its validity and successful AOP traffic must be checked at Cloudflare separately.

## Acceptance

- With the host health certificate and key, loopback origin HTTPS must validate
  the server trust chain and hostname. Admin `/` is 200 and
  `/api/admin/session` is 401 without a session; public admin API/UI paths stay
  404. API readiness and public content must pass their existing checks.
- Without a client certificate, origin TLS must fail even from loopback. The
  release gate requires this rejection for both public and admin hosts. HTTP
  cannot deliver app content; it only redirects or serves certificate challenges.
- From an external address outside Cloudflare, test direct 80/443 connectivity
  with a trusted server CA. The security group should reject it. Never use
  `curl -k`. A TLS client-certificate rejection proves mTLS; it does **not**
  prove the network allowlist was applied. An IP change/DNS proxy record alone
  proves neither boundary.
- Through Cloudflare, verify public content and the admin Access challenge with
  valid edge HTTPS. An allowed administrator must complete both Access and app
  login. This browser acceptance cannot be replaced by local fixture tests.
- Release records include `origin_auth=custom-aop-required` and the CA's public
  SHA256 fingerprint. Do not log client keys or expanded environment values.

The benefit-campaign workflow also uses the health client certificate. Its
read-only health request must succeed before any campaign database mutation.

## Rollback

Certificate files are persistent host configuration, outside code snapshots.
Preserve their permissions and the cloud ingress restriction during rollback.
Preserve current administrator credentials according to
[administrator credential governance](../docs/admin-credentials-governance.md).

The first hardened release's snapshot contains a pre-mTLS gateway. **Do not use
that snapshot's nginx image or Caddyfile for routine rollback**: restoring it
would reopen the origin bypass. Keep the hardened gateway and its configuration
running while selecting the previous `api-go`/`paid-access` image tags in a
reviewed Compose override; verify their route compatibility before applying it.
Do not restore the snapshot's entire `deploy` directory or `.env`. If the gateway
itself fails, correct its certificate/configuration problem while retaining
`require_and_verify`; do not temporarily disable client authentication. A failed
first rollout is a stop condition for a separately reviewed recovery, not an
authorization to reopen origin access.

Later rollbacks between verified mTLS releases may use the normal image snapshot
after confirming that both target gateway and configuration retain all three
HTTPS client-auth policies. Certificate/CA rollback is separate and must remain
consistent with Cloudflare's selected AOP certificate and the health identity.

## Local verification

`npm run test:deploy-preflight` runs preflight rejection/order tests using real
OpenSSL and disposable Go-generated fixtures. Only Linux key ownership/mode is
mocked in the portable guard test. CI/Deploy install a checksum-pinned official
Caddy 2.11.7 binary and run real TLS tests without Docker. On a workstation set
`CADDY_BIN` to a verified 2.11.7 executable before running the tests; otherwise
the real-Caddy test is explicitly skipped locally and fails if missing in CI.

The real TLS test uses the checked-in Caddyfile with temporary server identities,
paths, loopback ports and stub upstreams. It exercises all three HTTPS policies,
absent/wrong-CA/expired/server-only client certificates, valid independent AOP
and maintenance identities, SNI/Host mismatch, and admin/public route isolation.
No production material is read and no installed Docker daemon is started.

References: [Cloudflare AOP](https://developers.cloudflare.com/ssl/origin-configuration/authenticated-origin-pull/),
[custom AOP](https://developers.cloudflare.com/ssl/origin-configuration/authenticated-origin-pull/set-up/zone-level/),
[official source ranges](https://www.cloudflare.com/ips/),
[Caddy client authentication](https://caddyserver.com/docs/caddyfile/directives/tls),
[Docker forwarding rules](https://docs.docker.com/engine/network/firewall-iptables/).
