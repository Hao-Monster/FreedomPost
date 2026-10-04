# Chatwoot order delivery and direct conversation release

The public loader and Go order sender must use the same `CHATWOOT_BASE_URL`
and **public website token**. An administrator API token does not replace the
website token. The browser's signed `cw_conversation` cookie authenticates the
visitor message; never put this cookie in logs or release artifacts.

## Current behavior

- `POST /api/orders` first commits the order, then sends one message through
  `/api/v1/widget/messages` using the visitor's cookie.
- `sent` means Chatwoot returned a saved, public, incoming message with a
  positive ID and the expected content. A proxy HTTP 200 alone is insufficient.
- `pending` means the cookie was absent. It does **not** mean a background job
  will retry. `unavailable` means the send was not confirmed. Both states retain
  the existing copy-and-send fallback and do not roll back the order.
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
This release does not change prices, payment state, order schema, or the legacy
channel bridge. It introduces no background retry queue.

## Website rollback material

Before replacing files or pruning images, the deployment records a snapshot at
`<DEPLOY_PATH>.rollback/<UTC timestamp>-<release SHA prefix>/`:

- `config.tar.gz`: previous `.env` and `deploy/` configuration, mode 0600;
- `images.yml`: compose overrides referencing preserved, tagged nginx, api-go,
  and paid-access images;
- `replaced-by-sha`: the release that superseded this snapshot.

The successful release writes `.release-record` with the full SHA, snapshot
path, and timestamp. Keep snapshots on the host; they contain credentials.
There are no new database migrations in this change. A rollback uses the saved
configuration and `images.yml` with `docker compose up -d --no-build --no-deps
nginx api-go paid-access`, through an authorized release workflow, followed by
the existing TLS and health gates. Preserve the corrected Chatwoot website
token if rolling back application code: restoring the stale token also restores
the order-send incident. The Chatwoot web image has its own isolated rollback
workflow and does not require restoring its database.
