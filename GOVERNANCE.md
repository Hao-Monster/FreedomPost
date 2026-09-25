# Repository and production governance

## Branches and pull requests

The permanent remote branches are protected `main` and unprotected `codex/development`. Only `codex/development` is kept locally. Development is sequential in this checkout. Main accepts scoped squash PRs with the current base tested; direct pushes, force pushes and deletion remain blocked.

After a squash merge, preserve the development branch by merging `origin/main` into it (the identical merged tree should produce no code changes). Do not reset away commits or user edits. New PRs contain only the next scope's changes. Never deploy this branch.

## Releases

Merging does not deploy. Dispatch Deploy on main with the full current main SHA. Production jobs use the production environment restricted to main, and reject stale SHA input. Existing deployment serialization remains enabled. A dispatch from a feature branch must never obtain the production environment.

Production SSH authentication is stored only in the production environment. Repository-level DEPLOY_KEY and DEPLOY_PASSWORD were removed after uploading the verified existing SSH key to that environment. The obsolete one-time migration workflow is disabled. Other application secrets have not been migrated; do not claim all secrets are environment-scoped.

## Recovery inventory (2026-09-25)

- Before-governance refs are preserved in an external verified Git bundle; user untracked files remain untouched.
- TLS source: a47b847 and 34fa624; restore admin hostname, Origin CA mount, public admin API isolation, and literal secret handling first.
- Editor/image and affiliate recovery: 7606a6a, d36c613, 8eb435a, 76d22b9; reconcile with merged editor PRs 57-60.
- Chatwoot/readers/orders: final changes through ea7ee2e, including cookie secret preservation.
- Channel/WeCom and credential governance: remaining changes through 8b94d1f; exclude completed one-time secret-migration workflows.
- PR 56 remains an inventory reference until every necessary feature has a replacement and verification evidence. PR 19 must be checked for supersession; do not reinstate its weaker deployment checks.

## Integration evidence

- PR 61: release/branch governance; both CI jobs passed and merged.
- PR 62: TLS configuration and trusted certificate gates; both CI jobs passed and merged.
- PR 63: image preservation, storage origin handling and affiliate aggregation; both CI jobs passed and merged. The historic heading fallback was superseded by the current serializer after a save-request test reproduced duplicate partially formatted headings and passed with the reconciliation (75 admin tests).
- Remaining integration uses the final 8b94d1f tree for the dependent reader/order/Chatwoot/channel/configuration features. It excludes obsolete secret migration experiments. Compared with 8b94d1f, retained differences are current editor fixes/tests, release governance, trusted TLS verification, diagnostic gating, and trailing blank-line cleanup.
- PR 19 was closed after verifying the same stable patch ID as merged PR 20; its branch and two other patch-equivalent remote branches were removed. All prior local refs are archived in the verified external before-governance.bundle.
- Local Go tests do not establish real database or race-detector coverage; the required GitHub Go job runs PostgreSQL integration, race checks, vet and vulnerability checks.
- Integration and CI do not establish live channel delivery or authenticated Access acceptance. Those are separate production acceptance requirements; no deployment occurs on merge.

Release acceptance requires public reader availability, actual admin hostname TLS, Access entry and authenticated admin UI, public admin API isolation, and checks relevant to changed features. Report source/build/API evidence separately from authenticated browser and message-recipient evidence.
