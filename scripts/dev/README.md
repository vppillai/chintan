# scripts/dev — running a round

The scripts an agent round is driven with: the worktree mirror, the merge
drivers, the conflict resolver and the ruleset on `main`. `CLAUDE.md` at the
root is the one-page version: toolchains, gates, the merge rule, the gotchas.
CI shellchecks and shfmt-checks every `*.sh` here.

## The round, in order

1. **Brief.** One shared brief for every stream: the environment, the gates
   (`CLAUDE.md`), the branch prefix `rN/<slug>`, the backlog heading and the
   id range each stream owns, and the owner decisions being applied, quoted by
   their D-ids from the latest `docs/history/reviews/<date>/` ruling.
2. **Streams.** Each stream is one agent, one worktree
   (`git -C <main checkout> worktree add -B rN/<slug> <wt-root>/wt-rN-<slug> origin/main`),
   one pull request. The main checkout is never edited. Streams own disjoint
   files; at most one edits the backend and one the frontend at a time, and at
   most three agents run at once on one Mac.
   Toolchains live on the Linux VM: `mk-sync.sh wt-rN-<slug> rN<slug>` writes
   the sync script, which mirrors the worktree to `orb:~/temp/chintan-rN<slug>`.
3. **Adversarial review.** Each pull request gets one reviewer agent that
   edits nothing, runs the gates in its own mirror and returns
   `approve | changes-requested` with `must_fix` items; the implementer fixes
   and the reviewer re-reads.
4. **Driver merge.** `merge-seq.sh "<pr>:<wt-dir>:<be|fe|infra>" …` merges in
   order: rebase if conflicting (`resolve-known.py` settles the append-only
   files and regenerates `docs/backlog-open.md`), wait for `CI passed`,
   squash-merge, pull main, drop the worktree. It STOPs — never merges — on a
   failing or still-pending check. `stack-merge.sh "<bottom>,<upper>…:<kind>"`
   merges stacked pull requests bottom-up, retargeting the uppers to `main`
   first and rebuilding each onto the fresh main. Run a driver detached
   (`nohup … > <scratchpad>/log 2>&1 &`) and poll its log; a Mac that sleeps
   kills it.
5. **Queue.** The round closes with a docs pull request: backlog rows
   appended under the round's heading (`docs/backlog.md` is append-only; a
   later `Correction:` row replaces an earlier row's truth, and
   `python3 scripts/backlog-view.py` regenerates `docs/backlog-open.md`, which
   CI checks), the morning queue for the owner, and the review ledger's
   outcome rows.

## The gate

`protect-main.sh` is the merge rule (README → Operate → Release flow) as code:
the ruleset on `main` and the `production` environment, no reviewers, no
bypass, no force-push, no deletion. The status check is strict — the head must be at main's current tip — because
nothing else tests the tree that lands: no push-to-main CI run, no deploy-time
test. The cost is one CI run (~9 minutes, Playwright's wall clock) per queued
pull request each time main moves under it; `merge-seq.sh` pays it by updating
the branch and polling again.
Dry-run by default; `--apply` creates or updates both and prints before/after.
Run it after every edit to it. It needs the Mac's `gh` (the repository admin);
the VM's `gh` is a different account.

## Paths

Every path is an argument or an environment variable with a default that
reproduces the owner's layout (`<wt-root>/chintan` is the main checkout, the
worktrees are its siblings):

| Variable | Default | Used by |
|---|---|---|
| `CHINTAN_WT_ROOT` | three directories above this script | `merge-seq.sh`, `stack-merge.sh`, `mk-sync.sh` |
| `CHINTAN_REPO` | `$CHINTAN_WT_ROOT/chintan` | `merge-seq.sh`, `stack-merge.sh` |
| `CHINTAN_CI_POLLS` | `40` (× 30 s) | `merge-seq.sh` |
| `CHINTAN_SYNC_OUT` | the current directory | `mk-sync.sh` |
| `CHINTAN_ORB` | `ubuntu@orb` | `mk-sync.sh` |
| `CHINTAN_GH_REPO` | the checkout's origin | `protect-main.sh` |

`resolve-known.py` takes no paths: it runs inside the worktree being rebased,
keeps both sides of `docs/backlog.md`, `docs/history/reviews/README.md`,
`frontend/src/styles/index.css` and `frontend/src/components/Icon.tsx`
(dropping lines that appear on both sides and a second table header), takes
main's contract fixtures (CI regenerates and diffs them, so a wrong pick fails
loudly), and exits 2 on anything else so the driver STOPs.
