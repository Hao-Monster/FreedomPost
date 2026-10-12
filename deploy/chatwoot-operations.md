# Chatwoot order delivery and direct conversation release

The public loader and Go order sender must use the same `CHATWOOT_BASE_URL`
and **public website token**. An administrator API token does not replace the
website token. The browser's signed `cw_conversation` cookie authenticates the
visitor message; never put this cookie in logs or release artifacts.

## Order handoff v2 (2026-10-12)

The confirmed contract is in `docs/order-support-handoff-v2.md`. This section
describes source behavior, not proof that production has been upgraded.

- Modern clients confirm the protected order-owner cookie before creation and
  persist an idempotency key before the first POST. Order creation and support
  delivery are separate, owner-authorized operations.
- A delivery is bound to a verified Chatwoot visitor through authenticated
  capability and persistent CAS. The receiver deduplicates the stable order UUID
  in PostgreSQL. Neither a browser flag nor a Redis lock substitutes for this.
- Orders POST only to `/api/v1/widget/freedompost_handoff/messages`. A legacy
  receiver cannot accept this dedicated route, so a rollback between capability
  and delivery fails without creating an untracked ordinary message. Never
  fall back to `/api/v1/widget/messages` for a v2 order.
- A saved receipt is not yet a visible message. The SDK confirms the current
  identity/generation has read and rendered that exact message before the page
  displays success. Display recovery never resends a saved message.
- Recovery is bounded to three foreground attempts/30 seconds. Closing the page
  does not leave a background sender. Reopening can recover only while the
  browser retains the order owner and request key. Lost ownership must not create
  a replacement order or expose the old order.
- Normal order handling does not use the clipboard. An explicit copy button is
  only a user-selected fallback; errors and retry-original-order remain visible
  outside the closed dialog.
- Deploy the compatible Chatwoot backend/schema/SDK first, then website migration
  `0014_order_support_handoff.sql` and the website. The preflight requires
  `supportHandoffProtocol: 1`, pinned upstream version and full revision metadata;
  each send still verifies authenticated receiver capability at runtime.

## Legacy clients (no Idempotency-Key)

- `POST /api/orders` first commits the order, then sends one message through
  `/api/v1/widget/messages` using the visitor's cookie.
- `sent` means Chatwoot returned a saved, public, incoming message with a
  positive ID and the expected content. A proxy HTTP 200 alone is insufficient.
- `pending` means the cookie was absent. It does **not** mean a background job
  will retry. `unavailable` means the send was not confirmed. Both states retain
  legacy behavior and do not roll back the order. New storefront clients use
  the v2 state machine instead.
- There is no automatic retry after an ambiguous timeout, to avoid duplicate
  messages. Before manually recovering an old order, check its current
  conversation for the exact order code and send it at most once if absent.

The October 4 incident was an outdated production website token: the public
loader's inbox returned HTTP 200, while the API container's configured inbox
returned HTTP 404. `deploy/chatwoot-preflight.mjs` now compares the built loader
with deployment configuration and checks that the inbox serves a widget before
the release connects to the production host.

## Release order and verification

1. Release the patched Chatwoot web image through the separate
   `freedompost-chatwoot` repository's manual widget workflow. Its
   `directConversation` option defaults to false and preserves native messaging.
2. Merge this website change through the required main-branch PR checks.
3. Run `Deploy` manually on protected main with its full commit SHA. The
   production environment's website token must match the checked-in loader.
4. Check the workflow, server `.release-sha`, public loader's
   `directConversation: true`, and the patched Chatwoot release metadata.
5. In a real browser, verify one bubble click opens messages and input, prior
   history is present, closing/reopening preserves a draft, and a newly created
   test order appears exactly once without manual paste. Record the order code
   and successful API status without recording signed cookies.

Do not deploy the new SDK cache key before the patched Chatwoot SDK is live.
The v2 release adds ownership/request and delivery tables without changing
prices, payment state, the legacy channel bridge, or existing order rows. It
introduces no background retry queue. Preserve the previously shipped QR welcome
rule and verify one QR on actual new conversation creation, not just widget open.

## Website rollback material

Before replacing files or pruning images, the deployment records a snapshot at
`<DEPLOY_PATH>.rollback/<UTC timestamp>-<release SHA prefix>/`:

- `config.tar.gz`: previous `.env` and `deploy/` configuration, mode 0600;
- `images.yml`: compose overrides referencing preserved, tagged nginx, api-go,
  and paid-access images;
- `replaced-by-sha`: the release that superseded this snapshot.

The successful release writes `.release-record` with the full SHA, snapshot
path, and timestamp. Keep snapshots on the host; they contain credentials.
The v2 additive tables and receiver idempotency records must remain during an
application rollback; do not drop them or erase delivery history. A rollback uses the saved
configuration and `images.yml` with `docker compose up -d --no-build --no-deps
nginx api-go paid-access`, through an authorized release workflow, followed by
the existing TLS and health gates. Preserve the corrected Chatwoot website
token if rolling back application code: restoring the stale token also restores
the order-send incident. The Chatwoot web image has its own isolated rollback
workflow and does not require restoring its database.
