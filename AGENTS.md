# FreedomPost repository governance

- Remote `main` is the only protected integration and release branch.
- `codex/development` is the only local development branch and the only remote PR branch. Work sequentially; never switch this shared checkout during another task's active edits.
- Before work, fetch origin, inspect status and compare with current main. Preserve untracked files and user changes. Do not create additional branches or worktrees without explicit authorization changing this policy.
- One scoped PR at a time, targeting main. Wait for required checks, review the diff, squash merge, then merge origin/main back into the development branch before starting the next scope. Never force-push or delete unpreserved work to synchronize.
- Feature deployments are prohibited. Production releases are manual, from protected main, with an explicit full SHA. A merged PR is not a deployed release.
- Keep previously shipped TLS, Access isolation, editor, order and channel features when integrating changes. Compare against the last production release, not just the PR base.
- Never use `curl -k` as evidence that production TLS is valid. Admin acceptance uses `admin-freedompost.thinderbox.uk`, trusted origin TLS and authenticated Cloudflare Access browser verification.
- Production changes require a version/configuration record and a usable rollback point. Do not deploy unrelated feature recovery together with emergency TLS repair.
