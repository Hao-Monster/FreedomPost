# Repository and production governance

## Branches and pull requests

The permanent remote branches are protected `main` and unprotected `codex/development`. Only `codex/development` is kept locally. Development is sequential in this checkout. Main accepts scoped squash PRs with the current base tested; direct pushes, force pushes and deletion remain blocked.

After a squash merge, preserve the development branch by merging `origin/main` into it (the identical merged tree should produce no code changes). Do not reset away commits or user edits. New PRs contain only the next scope's changes. Never deploy this branch.

## Releases

Merging does not deploy. Dispatch Deploy on main with the full current main SHA. Production jobs use the production environment restricted to main, and reject stale SHA input. Existing deployment serialization remains enabled. A dispatch from a feature branch must never obtain the production environment.

The environment restriction does not itself restrict repository-level secrets. Old deployment-capable branches/workflows must be retired; moving repository secrets to environment scope requires securely providing their values and must not be represented as complete merely by adding an environment field.

## Recovery inventory (2026-09-25)

- Before-governance refs are preserved in an external verified Git bundle; user untracked files remain untouched.
- TLS source: a47b847 and 34fa624; restore admin hostname, Origin CA mount, public admin API isolation, and literal secret handling first.
- Editor/image and affiliate recovery: 7606a6a, d36c613, 8eb435a, 76d22b9; reconcile with merged editor PRs 57-60.
- Chatwoot/readers/orders: final changes through ea7ee2e, including cookie secret preservation.
- Channel/WeCom and credential governance: remaining changes through 8b94d1f; exclude completed one-time secret-migration workflows.
- PR 56 remains an inventory reference until every necessary feature has a replacement and verification evidence. PR 19 must be checked for supersession; do not reinstate its weaker deployment checks.

Release acceptance requires public reader availability, actual admin hostname TLS, Access entry and authenticated admin UI, public admin API isolation, and checks relevant to changed features. Report source/build/API evidence separately from authenticated browser and message-recipient evidence.
