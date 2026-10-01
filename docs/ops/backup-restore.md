# Backup, restore and rollback: the rehearsed procedures

`chintanctl backup` and `restore` (README → Operate → `chintanctl`) had been tested only against fakes until 2026-10-01 (platform review PR9-50, decision D13). This page is the procedure as it was actually run, so the next run is a repeat and not a first.

## Backup and restore one tenant (rehearsed 2026-10-01)

Target: the `dev` instance's **staging** stack, test tenant `b83133f0-70d1-7018-4054-0266597d6df2` (the only tenant on staging; the owner's tenant lives on prod and is never a rehearsal target). Reads ran as the agent role (`--profile chintan`); the one write, step 4, ran under the administrator session, because the agent is not used for writes to a stack.

```bash
cd backend && go build -o /tmp/chintanctl ./cmd/chintanctl
C=/tmp/chintanctl; T=b83133f0-70d1-7018-4054-0266597d6df2

# 1. What the table and the bucket disagree about, before anything is written.
$C reconcile --instance dev --environment staging
# 2. Back up the one tenant. backup.json is written last: its presence means the backup finished.
$C backup  --instance dev --environment staging --tenant $T --out ~/backup-1
# 3. Dry run: hashes every local body against the manifest, lists what it would overwrite, writes nothing.
$C restore --instance dev --environment staging --in ~/backup-1
# 4. The write. --yes skips the typed confirmation for a tenant with live data in the target.
$C restore --instance dev --environment staging --in ~/backup-1 --apply --yes
# 5. Same findings as step 1; then a second backup must equal the first.
$C reconcile --instance dev --environment staging
$C backup  --instance dev --environment staging --tenant $T --out ~/backup-2
sha256sum ~/backup-{1,2}/items.jsonl ~/backup-{1,2}/objects.jsonl && diff -rq ~/backup-1/objects ~/backup-2/objects
```

Result on 2026-10-01 (20:29–20:31 UTC):

| Step | Outcome |
|---|---|
| 1 reconcile | report-only findings on QA leftovers (`capture_size_unknown`, `missing_object`, `stuck_capture`); **no `unlisted_note` or `unindexed_capture`**, i.e. no row written before attribute promotion |
| 2 backup | 51 index items, 63 objects, 236.4 KiB; manifest written |
| 3 restore dry run | `verified 63 objects (236.4 KiB) against the manifest`; `OVERWRITES live data: tenant … has 51 index items and 63 objects in the target`; nothing written |
| 4 restore --apply | `restored 51 index items and 63 objects (236.4 KiB)` |
| 5 reconcile, second backup | the same "Would:" summary as step 1; `items.jsonl` and `objects.jsonl` byte-identical to backup 1, `objects/` identical (`diff -rq`); only `backup.json` differs, by its timestamp |

Restore writes the tenant back into the same instance (table and bucket derived from the stack, or `--table`/`--bucket` to point at another); it has no "into another tenant" mode, so a rehearsal is a round trip of the same test tenant. A disaster restore is steps 3–4 against the rebuilt stack.

The reconcile result also closed PR9-50: both tables were scanned the same day for `NOTE#`/`CAPTURE#` rows without a `data` blob (staging 0 of 20, prod 0 of 205), and the 21-key legacy decode fallback in `cmd/chintanctl/enumerate.go` was deleted; a row without the blob is now an error, not a guess.

## Rollback rehearsal (D13)

`scripts/deploy.sh` rolls every Lambda alias it moved back when a smoke fails (README → Operate → Rollback). It had never run against AWS (platform review PR9-5: `rolled back:` in 0 of 100 deploy jobs). To rehearse it without a bad build:

```bash
gh workflow run deploy-backend.yaml -f fail_worker_smoke=staging
gh run watch   # the Staging job: `rolled back: chintan-worker-… live -> version N`, then the job fails; Production is skipped
```

`fail_worker_smoke=staging` reaches `deploy.sh` as `FAIL_WORKER_SMOKE` on the staging job only, and `deploy.sh` honours it only when `ENVIRONMENT` is `staging`, so the rehearsal has no path to prod. Afterwards staging serves the previous versions; the next push to `main` (or a plain `gh workflow run deploy-backend.yaml`) moves it forward again.

| Date | Run | Result |
|---|---|---|
| 2026-10-01 | — | mechanism added (PR10-5); first dispatch is the owner's, after merge — record its run id and the `rolled back:` lines here |

## Account state, dated

- 2026-10-01 — `scripts/bootstrap-agent.sh --apply` (administrator): `chintan-agent-permissions` v1 (2026-08-07) → v2 from the repository's `permissions.json` (alarms, SNS, SQS, events, budget, Logs `StartQuery`); the trust policy names `chintan-agent-cli` alone, and `trust.json` now says the same. Verified as the agent: `describe-alarms` → 8 alarms; `chintan-captures-dlq-dev-prod` → `ApproximateNumberOfMessages` 0. (PR10-1, D12)
- 2026-10-01 — `scripts/setup.sh --region us-west-2 --apply` (administrator): `chintan-bootstrap` `UPDATE_COMPLETE` 20:26 UTC, the one change `GitHubActionsRole` (the version-prune grant on `chintan-*:*` added, the dead SSM read removed, #219); `AWS_ACCOUNT_ID`, `BUILD_ROLE_ARN`, `CFN_DEPLOY_ROLE_ARN` rewritten to the same values; `production` still requires `vppillai`; Pages unchanged; the VAPID pair left alone. (PR10-2)
- 2026-10-01 — `gh secret delete AWS_ROLE_ARN` and `AWS_REGION` on `vppillai/chintan`: nothing under `.github/` read either. Secrets now: `ALARM_EMAIL`, `AWS_ACCOUNT_ID`. (PR10-3, T65)
- 2026-10-01 — the live evals (D14) ran with the instance key from SSM, `-count=3`; outcomes in `docs/design/prompts.md` → "Baseline of 2026-10-01". (PR10-4)
