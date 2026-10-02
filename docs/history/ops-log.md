Dated operator actions against the live account, newest last. Appendable; nothing here describes the system as it is — `README.md` → Operate and `docs/ops/` do.

# Ops log

## 2026-10-01 — backup and restore rehearsal (staging, test tenant)

Target: the `dev` instance's staging stack, test tenant `b83133f0-70d1-7018-4054-0266597d6df2` (the only tenant on staging). Reads ran as the agent role (`--profile chintan`); the one write, step 4, ran under the administrator session. The procedure is `docs/ops/backup-restore.md` → "Back up and restore one tenant". Result, 20:29–20:31 UTC:

| Step | Outcome |
|---|---|
| 1 reconcile | report-only findings on QA leftovers (`capture_size_unknown`, `missing_object`, `stuck_capture`); no `unlisted_note` or `unindexed_capture` |
| 2 backup | 51 index items, 63 objects, 236.4 KiB; manifest written |
| 3 restore dry run | `verified 63 objects (236.4 KiB) against the manifest`; `OVERWRITES live data: tenant … has 51 index items and 63 objects in the target`; nothing written |
| 4 restore --apply | `restored 51 index items and 63 objects (236.4 KiB)` |
| 5 reconcile, second backup | the same findings as step 1; `items.jsonl` and `objects.jsonl` byte-identical to backup 1, `objects/` identical (`diff -rq`); only `backup.json` differs, by its timestamp |

The same day both tables were scanned for `NOTE#`/`CAPTURE#` rows without a `data` blob (staging 0 of 20, prod 0 of 205) and the 21-key legacy decode fallback in `cmd/chintanctl/enumerate.go` was deleted (PR9-50).

## 2026-10-01 — rollback rehearsal

Deploy Backend run 36928400126, `fail_worker_smoke=staging`, main at `a471540d`: staging deployed, health and ready smokes passed, the worker smoke failed on request; `rolled back: chintan-api-dev-staging live -> version 112` and `rolled back: chintan-worker-dev-staging live -> version 112` four seconds later; the production job was skipped and Deploy Frontend did not run.

## 2026-10-01 — account state

- `scripts/bootstrap-agent.sh --apply` (administrator): `chintan-agent-permissions` v1 (2026-08-07) → v2 from the repository's `permissions.json` (alarms, SNS, SQS, events, budget, Logs `StartQuery`); the trust policy names `chintan-agent-cli` alone, and `trust.json` says the same. Verified as the agent: `describe-alarms` → 8 alarms live on the account; `chintan-captures-dlq-dev-prod` → `ApproximateNumberOfMessages` 0. (PR10-1, D12)
- `scripts/setup.sh --region us-west-2 --apply` (administrator): `chintan-bootstrap` `UPDATE_COMPLETE` 20:26 UTC, the one change `GitHubActionsRole` (the version-prune grant on `chintan-*:*` added, the dead SSM read removed, #219); `AWS_ACCOUNT_ID`, `BUILD_ROLE_ARN`, `CFN_DEPLOY_ROLE_ARN` rewritten to the same values; Pages unchanged; the VAPID pair left alone. (PR10-2)
- `gh secret delete AWS_ROLE_ARN` and `AWS_REGION` on `vppillai/chintan`: nothing under `.github/` read either. Secrets: `ALARM_EMAIL`, `AWS_ACCOUNT_ID`. (PR10-3, T65)
- The live evals (D14) ran with the instance key from SSM, `-count=3`; outcomes in `prompt-evals.md` beside this file. (PR10-4)
- `scripts/dev/protect-main.sh --apply`: ruleset `main` (id 24337213) active, requiring a pull request, squash only and `CI passed` up to date; the `production` environment has no required reviewer. (PR9-42)
