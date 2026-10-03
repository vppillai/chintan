# Chintan — working here

A personal voice-notes PWA (Go on Lambda, DynamoDB, S3; React/Vite PWA on Pages) with one alpha user, the owner. Alpha means no backwards-compatibility burden; existing notes must never be lost. `README.md` is the system, `docs/design/` is how each part works (`docs/design/specs/` the dated design specs), `docs/api/openapi.yaml` the wire, `docs/backlog.md` the append-only ledger with `docs/backlog-open.md` its generated view, `docs/ops/` the runbooks (`alarms.md`, `metrics.md`, `backup-restore.md`), `docs/history/` the dated reports, reviews and the owner's queue (not maintained; only `morning-queue.md` and `ops-log.md` are appended to), `scripts/dev/README.md` how a round runs and `scripts/qa/README.md` the live-QA harness.

## Where things run

- Edit on the Mac, in a git worktree (`git worktree add -B <branch> <absolute path> origin/main`; relative paths bite). The main checkout is read-only: never edit, checkout or stash there.
- Toolchains exist only on the Linux VM, `ssh -o ConnectTimeout=10 ubuntu@orb`. Mirror the worktree there with `scripts/dev/mk-sync.sh <wt-dir> <suffix>` and the script it writes; never edit files on the VM. Keep each ssh call under four minutes; start longer runs detached (`ssh orb 'nohup bash -c "…" > log 2>&1 &'`) and poll the log.
- AWS from the VM only, read-only: `aws --profile chintan --region us-west-2`. The VM's default profile is the owner's; do not use it unless the task says so. Never print a secret, token or key; pass one through the environment inside a single ssh call.

## Gates (run on the VM before pushing)

- Go: `cd backend && export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH GOTOOLCHAIN=auto && gofmt -l . && go vet ./... && go test -race ./... && golangci-lint run --timeout=5m ./...` — gofmt must print nothing.
- Frontend: `cd frontend && export PATH=$HOME/temp/node/bin:$HOME/.bun/bin:$PATH && bun install --frozen-lockfile && bun run typecheck && bun run lint && node node_modules/vitest/vitest.mjs run && bun run build && bun run check-bundle`.
- Playwright, for the specs you touch: `CI=1 bunx playwright test --project=chromium <spec>`. `CI=1` is required: without it Playwright attaches to another stream's preview server. WebKit does not launch on the VM; CI covers it.
- Infra and scripts: `cfn-lint infrastructure/*.yaml`; `shellcheck --severity=warning` and `shfmt -d -i 4 -ci` on every script; `actionlint` on the workflows.
- Wire changes: update `docs/api/openapi.yaml`, add a conformance scenario, then `CHINTAN_UPDATE_FIXTURES=1 go test ./internal/handler/ -run Contract` and the frontend typecheck; copy the generated fixtures back, never hand-edit them. Re-run after any merge from main.

## The merge rule

A pull request squash-merges into `main` when its one required check, `CI passed`, is green; nobody reviews and nobody can merge red (the ruleset is `scripts/dev/protect-main.sh`). Merging deploys: staging first, its smoke test and rollback are the production gate, then prod; the frontend follows to Pages. Do not merge unless the task says so. Small imperative commits with bodies; rebase onto `origin/main` before the final push; `gh pr create --base main` with what / why / how verified; drive CI green with `gh pr checks <n> --watch`.

## Conventions

- WHY comments in full sentences. Tests beside the code; every bug fix gets a test that fails without it.
- Only `frontend/src/styles/tokens.css` holds literal colours and font sizes. Real controls, 44 px targets, both themes (Ink & Paper, Nocturne), reduced motion.
- User-facing error sentences are fixed strings. No user content in logs. The owner's tenant is not for tests.
- `docs/backlog.md` is append-only with `Correction:` rows and a generated view; the rules are `scripts/dev/README.md` → "The round", step 5.
- Update any `docs/design/*.md` sentence your change makes false.

## Gotchas

- Mac: `grep` is ugrep (use `/usr/bin/grep` for `--include`); there is no `timeout`; zsh treats a bare `=word` as a path. VM: `~/.local/bin/node` is a shim that execs Bun; `awk` is mawk.
- The VM's `gh` is a different account from the Mac's, which is the repository admin. Admin reads and writes (rulesets, environments, `scripts/setup.sh`) use the Mac's `gh`; when a script on the VM needs it, pipe the token over stdin: `gh auth token | ssh orb 'read -r T; GH_TOKEN=$T …'`.
- Agent and driver limits (three agents per Mac, detached drivers, the sleeping Mac): `scripts/dev/README.md` → "The round", steps 2 and 4.
- A stacked pull request shows no CI run while it conflicts with main; merge or rebase onto main first.
- PR bodies go in a uniquely named scratch file per stream, never a shared one.
