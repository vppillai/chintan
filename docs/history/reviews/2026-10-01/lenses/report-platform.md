# Platform review — lens: platform (OPS-n) — 1 Oct 2026

**Main commit reviewed: `c636993b`** (#207 "Install the Web Push VAPID pair automatically", merged 2026-10-01 05:15Z). The checkout was at `53bf8a23` when the review opened and `merge-seq.sh` fast-forwarded it to `c636993b` ten seconds later (reflog: `pull -q --ff-only origin main` 22:15:37 -0700); every file:line below is at `c636993b`. Read-only throughout: `git log/show`, `gh` reads, `aws describe/get/list` from the agent role on orb, tool runs on a private orb mirror (`~/temp/chintan-r9-platform`). Nothing was edited, approved, re-run or posted.

Evidence tags: **measured** (numbers from the Actions API, AWS describe calls or tool output), **traced** (read in code/config), **reproduced** (run on orb).

## Summary

| Severity | Count | Ids |
|---|---|---|
| High | 1 | OPS-1 |
| Medium | 5 | OPS-2, OPS-3, OPS-4, OPS-5, OPS-6 |
| Low | 9 | OPS-7 … OPS-15 |

The platform is in better shape than sixty fast merges would suggest: the deploy path, the IAM shape, the data-protection settings and the CI gates are each sound in isolation and the deployed state matches the repository. The debt is in the **governance around them** (nothing enforces the gates; the human prod approval is a script), in **CI economics** (one uncached download is the whole wall-clock tail; identical trees are tested twice), and in a few **blind spots** (user-visible capture failures never alarm; the agent role cannot see the DLQ).

---

## Findings, ranked

### OPS-1 — `main` is unprotected and the production gate is self-approved by a script (High; owner decision)
- **Where:** repository settings (`gh api repos/vppillai/chintan/branches/main/protection` → 404 "Branch not protected"; `rulesets` → `[]`); environment `production` requires reviewer `vppillai` with `prevent_self_review: false`; `.agent-tools/gate.sh:14-18` posts `state=approved` with the comment "approved by the agent under the owner's standing instruction"; `.agent-tools/merge-seq.sh:26-32` merges after 40 × 30 s of *pending* checks (or an empty check list) because the loop falls through to `gh pr merge`.
- **Evidence (measured):** Deploy Backend prod-gate wait over the last 40 runs: p50 **14 s**, p95 27 s, 39/40 under one minute; approvals on runs 36808317767 and 36818907164 carry the gate.sh comment. All 60 merged PRs since 29 Sept have 0 reviews recorded; `gh pr merge` is accepted by GitHub regardless of check state.
- **Why it is debt:** `ci.yaml:3` ("every gate that must be green before a change reaches main") and README *Operate* ("waits for your approval on production") describe controls that exist only as a convention in an out-of-repo script. One slow Playwright run (>20 min; p95 is 13 min, max 28.6 min this week) or a tooling slip merges red or untested code, and the prod environment then adds 14 s, not a review. The next change costs nothing today and everything on the day it goes wrong.
- **Fix (pick one, S):** (a) *Truthfully automatic:* add a ruleset on `main` requiring the 11 CI checks and a PR; remove the required reviewer from `production` (the staging smoke + rollback is then the real gate), delete `gate.sh`, update README/ci.yaml wording. (b) *Truthfully human:* keep the reviewer, stop scripting the approval, and accept merges queue on you. Either way `merge-seq.sh` should fail, not merge, when the check loop times out.

### OPS-2 — The Playwright browser download is uncached and is the entire CI tail (Medium)
- **Where:** `.github/workflows/ci.yaml:353-358` (`bunx playwright install --with-deps chromium webkit`, no cache step).
- **Evidence (measured, 249 CI runs 27 Sept–1 Oct):** Playwright job p50 5.7 min / p95 13.0 min = 47 % of all CI minutes and 100 % of run wall clock (every other job finishes inside 2.2 min). Step "Install Chromium and WebKit": p50 49 s, **p95 9.0 min**, 17 runs over 5 min, worst 28.6 min (run 36743441385), 25.2 min (36744330807), 23.1 min (36757623160) — cdn.playwright.dev stalls. The `e2e` step itself is a steady 4.7 min p50.
- **Why:** ~400 MB fetched per run; the stalls are what make a PR wait a quarter of an hour, and they are the runs that would push `merge-seq.sh` past its 20-minute fall-through (OPS-1).
- **Fix (S):** `actions/cache` on `~/.cache/ms-playwright` keyed on the Playwright version from `bun.lock` (the `--with-deps` apt packages still install; they are quick). Expected effect: p95 wall clock from 13 min to ~7 min.

### OPS-3 — The same tree is tested two or three times; docs-only PRs pay the full matrix twice (Medium)
- **Where:** `ci.yaml:19-24` triggers on `pull_request` *and* `push: main` with no `paths`/`paths-ignore`; `deploy-backend.yaml:105-119` runs `go test -race` again as its own `Test` job.
- **Evidence (measured):** 51 of 74 push-to-main CI runs this week have a git tree identical to a merged PR head already tested (squash merges change the sha, not the tree); push-main runs are 1037 job-min = 30 % of all CI minutes, ~715 of them re-testing identical content. 12 of the last 60 merged PRs were docs-only (#144, 145, 161, 164, 165, 171, 173, 174, 184, 189, 200, 202) and each ran the full 11-job matrix (12–22 job-min) and then again on main. Mean 14.0 job-min per CI run, 3488 job-min in five days. The `bun audit` advisory on 30 Sept (brace-expansion, transitive) blocked docs-only merges for 9 h for the same reason.
- **Fix (S):** `paths-ignore: ['docs/**', '**.md']` on both triggers; once OPS-1(a) makes the PR checks required, drop the push-main trigger (the squash-merged tree is the tested tree) and drop Deploy Backend's `Test` job (it re-runs what CI just proved on the same sha; `build` can `needs: discover` instead). Keep a `workflow_dispatch` for an ad-hoc main run.

### OPS-4 — A provider outage is invisible to the operator: capture failures never alarm (Medium)
- **Where:** `backend/internal/pipeline/status.go:255-287` — after a provider error the capture is `markFailed` and the pipeline **returns nil**; `pipeline.go:374-376` only returns an error (→ Lambda retry → DLQ → alarm) for infrastructure failures. `CaptureStageFailures`, `ProviderTimedOut`, `ProviderRateLimited`, `CaptureSpendCapped`, `PushSendFailures`, `UsageRecordFailures` are emitted (`obs.Count`, no rollup) and no alarm names them (`infrastructure/template.yaml:2194-2438`: 7 alarms; the only provider alarm is `ProviderKeyRejected`).
- **Evidence (traced + deployed state):** the 7 deployed prod alarms match the template exactly and none is structurally dead (every `Chintan` alarm reads a `CountWithRollup` series; `TestEveryAlarmedMetricIsRolledUp` pins that). Staging has 0 alarms by design, so a broken staging scheduled task is also silent.
- **Why:** Groq or MiniMax down for an hour = every capture ends "failed" with a Retry button and nobody is paged; the DLQ alarm (the one the README leans on) never sees it because the pipeline reports success to Lambda.
- **Fix (S):** emit `CaptureStageFailures` with `CountWithRollup` and add one alarm (`Sum ≥ 3 in 15 min`, `notBreaching`) to `AlarmTopic`; optionally the same for `ProviderTimedOut`. Keep staging alarm-free.

### OPS-5 — The deployed agent guardrail is stale, so the account's own health is unreadable to the review role (Medium; owner action)
- **Where:** IAM policy `chintan-agent-permissions` deployed **v1 (2026-08-07)**; repo `infrastructure/agent-policies/permissions.json` has since added `ActionsThatTakeNoResourceToScopeTo` (cloudwatch:DescribeAlarms/GetMetricData, sqs:ListQueues, events:ListRules, lambda:ListEventSourceMappings, logs query), `CapturePipelineQueues`, `AlarmTopics`, `ProjectAlarms`, `ProjectBudget`, drift detection. Boundary v5 and deny v2 do match the repo. Repo `trust.json` names the account root; the deployed trust names only `user/chintan-agent-cli` (deployed is tighter — fix the repo, not AWS).
- **Evidence (measured):** this review was denied `cloudwatch:DescribeAlarms`, every `sns:*` read, `sqs:GetQueueAttributes`, `events:ListRules`, `lambda:ListEventSourceMappings` — exactly the statements missing from v1. Consequence: **DLQ depth (prod and staging) and the alarm e-mail's confirmation state could not be verified** (the stack holds a full subscription ARN, which is consistent with confirmed, not proof).
- **Fix (S):** `scripts/bootstrap-agent.sh --apply` once with elevated credentials (the README's documented step), and set `trust.json` to the single CLI user so the repo stops being looser than the deployment. Then one `aws sqs get-queue-attributes` on `chintan-captures-dlq-dev-prod` to close the DLQ question.

### OPS-6 — The worker smoke counted a skip as a pass for four deploys, and the rollback has never run for real (Medium)
- **Where:** `scripts/deploy.sh:766-785` (worker smoke; AccessDenied → `warn` and fall through to `ok smoke passed` at :797); rollback `rollback_and_fail` :680-694.
- **Evidence (measured, 50 deploy jobs since 29 Sept):** every job prints `ok smoke passed`. Runs 36750865586, 36752745743, 36760149631, 36762701169 (8 jobs, 30 Sept 17:27–19:07Z) printed `warning: worker smoke skipped: this role may not invoke …:live; redeploy infrastructure/bootstrap.yaml` and *then* `ok smoke passed`. The first real worker invoke returning ok is run 36798850608 (1 Oct 00:58Z); since then 8 jobs (incl. prod runs 36808317767, 36818907164) show `smoke: invoke chintan-worker-dev-prod:live with the smoke task` → `ok smoke passed`. **Deployed bootstrap == repo byte-for-byte** (contains `lambda:InvokeFunction` on `function:chintan-worker-*:live`), so the permission is live. `rolled back:` appears in 0/100 jobs; the only rollback text is the advisory `==> ROLLBACK:` hint. Also 44/50 jobs logged `AccessDenied … UpdateTerminationProtection` as a warning until 19:07Z 30 Sept.
- **Why:** a smoke that says "passed" when it did not run teaches the log reader to ignore it; and R7-4b's one real promise — prod never stays on failed code — has only been exercised by the self-test, never against AWS (alias moves under IAM conditions are where such code fails).
- **Fix (S, plus one owner-approved staging run):** print `warning: smoke INCOMPLETE` and exit non-zero on AccessDenied for `prod` (it is a configuration fault, not an unknown); then exercise the rollback once on **staging** with a deliberately failing worker (e.g. a `workflow_dispatch` input that makes the smoke task return an error) and record the run id in the backlog.

### OPS-7 — `go 1.26` without a patch pin: the local build path ships 19 known stdlib CVEs; CI is reproducible by luck (Low)
- **Where:** `backend/go.mod:14` (`go 1.26`, no `toolchain` line); `scripts/build-lambda.sh:77`; `ci.yaml:45-48` (`go-version-file`).
- **Evidence (reproduced + measured):** on orb with `GOTOOLCHAIN=auto` the module resolves to **go1.26.0** and `govulncheck ./...` reports 19 reachable stdlib advisories (crypto/x509 ×7, crypto/tls ×3, net/http ×2, …), all fixed in ≤ go1.26.6. CI run 36818138175 and the prod build job of 36808317767 resolved `1.26` to **go1.26.8** from the runner cache and report 0 vulnerabilities — so the prod artifact is clean today, but which patch builds it is decided by the runner image, not the repo. `scripts/bootstrap.sh` (disaster recovery) would build with 1.26.0.
- **Fix (S):** `toolchain go1.26.8` in go.mod (Dependabot gomod bumps it from then on). Separately, setup-go's "cache hit" restores a 15 MB key and `go vet` still prints `go: downloading github.com/aws/…` in four jobs per run (~30–40 s each) — check that the module cache path is what is being saved.

### OPS-8 — Deploy-role breadth, and one dead grant (Low)
- **Where:** `infrastructure/bootstrap.yaml` CfnDeployRole: `route53:ChangeResourceRecordSets` on `hostedzone/*`, `cognito-idp:CreateUserPool`/`UpdateUserPoolDomain`, `kms:CreateKey`, `acm:RequestCertificate` on `*` with no tag gate; GitHubActionsRole can `cloudformation:UpdateStack` on `stack/chintan-*/*`, which names `chintan-bootstrap` itself (mitigated: its IAM writes are `CalledVia: cloudformation` and cfn-deploy denies touching the guardrail roles); GitHubActionsRole `ssm:GetParameter/GetParameters/DescribeParameters` on `/chintan/*` (bootstrap.yaml ~:1178, "for deploy verification", since 2026-08-06) is **dead** — the boundary's `DenyReadingProviderSecretValues` denies it to every non-Lambda principal and nothing in `deploy.sh` reads SSM.
- **Rounds 7–8 widened exactly one thing:** `lambda:InvokeFunction` on `function:chintan-worker-*:live` (scoped; needed by the smoke). Lambda roles are tight (per-table, per-bucket, per-parameter, boundary attached, worker may invoke only itself) and match the template.
- **Fix (S–M, not urgent):** drop the dead SSM statement; scope Route 53 to `DnsZoneId` when the custom-domain feature is first used; consider `NotResource: stack/chintan-bootstrap/*` on the CI role.

### OPS-9 — OpenAPI under-documents the statuses shared helpers can return (Low)
- **Where:** `backend/internal/handler/errors.go:139-142` (500 fallback on every route; only `DELETE …/permanent` documents 500), `idempotency.go:70-106` (400/409/413/503 on every `idempotent()` route; undocumented on PUT /settings, …/restore, …/regenerate, …/retry, POST /export), `body.go:75,147` (413 on six JSON routes), nil-guard 503s on GET /tags, /search, /usage, /ask/{id}, /export/{id}, POST /export and the three inbox routes.
- **Evidence (reproduced):** 43 operations in both the document and `routes.go`; 165 documented (route,status) pairs, every one covered by a conformance scenario (`openapi_conformance_test.go:470-1048`); fixtures regenerated as CI does → **no diff** (current). The gap is one-directional: conformance proves documented ⇒ reachable, never reachable ⇒ documented.
- **Fix (M):** a `components.responses` set for the helper statuses referenced by every route (or a `default` problem+json response), and a conformance assertion that each route's helper chain is in the document.

### OPS-10 — No Content-Security-Policy on the Pages site (Low; owner decision)
- **Where:** `frontend/index.html` (no `http-equiv="Content-Security-Policy"`; two inline scripts at :46-65 and :66-81), GitHub Pages cannot set headers.
- **Why:** modest risk — same-origin bundle, no third-party script, API and Cognito on known origins — but a meta CSP (`script-src 'self' 'sha256-…' 'sha256-…'; connect-src` the API/Cognito/S3 origins `ci-build-site.sh` already knows; `object-src 'none'`; `base-uri 'self'`) is the one XSS backstop a static host allows. M.

### OPS-11 — Script hygiene drift (Low)
- `scripts/invite-user.sh:163,171` passes the temporary password on **argv** (`--temporary-password "$TEMP_PASSWORD"`, visible in `ps`) while `vapid-keys.sh:144-165` deliberately avoids argv for the same class of secret; `--delete` has no confirmation (:131).
- Dry-run/confirm drift: `bootstrap-agent.sh` and `clean-instance-orphans.sh` accept `--apply` but not `--dry-run`; the confirmation bypass is `--yes` (teardown, cleanup-aws, vapid-keys), env `ASSUME_YES` (clean-instance-orphans) or `--i-understand-this-deletes-data`.
- Docs: README:139 `--rotate --apply` blocks on the typed prompt (needs `--yes`, undocumented); `docs/design/async-updates.md:128` names `scripts/push-keys.sh` (does not exist); `docs/design/home.md:35` `scripts/make-icons.mjs` (lives in `frontend/scripts/`). Every README script invocation otherwise matches real names and flags; `--help` on all ten scripts is offline and matches.
- Otherwise clean (reproduced): `shellcheck --severity=warning` 0, default severity 11 informational (all deliberate idioms), `shfmt -d` 0, no `set -x`, `vapid-keys.sh` never prints the private key (dry-run prints `<generated>`, apply writes 0600 JSON via `--cli-input-json`). S.

### OPS-12 — Lambda versions are never pruned; every path-filtered merge publishes two (Low)
- **Where:** `scripts/deploy.sh:696-734` publishes a version and moves `live` on every apply; the code key is `function-<sha>.zip` so CloudFormation always sees a `Modify` even for identical bytes.
- **Evidence (measured):** prod `live` = v96 (97 versions), staging v103/v104 (101 versions), ≈4.5 MB each ≈ 0.9 GB of the 75 GB regional quota; five prod deploys on 30 Sept alone. The artifact bucket is bounded (170 zips, 1.08 GB, 30-day lifecycle) — the function store is not.
- **Fix (S):** keep the last N (say 10) versions after a successful smoke; needs `lambda:DeleteFunction` on `function:chintan-*:*` for the CI role outside the `CalledVia` condition.

### OPS-13 — The 4xx-flood alarm cannot be tripped by the case it describes (Low)
- **Where:** `template.yaml:2229-2258` (`AWS/ApiGateway 4xx ≥ 1000 / 5 min`, description: "the open inbox routes are being flooded"); `:2013-2016` throttles each inbox route to 1 rps / burst 10.
- **Traced:** gateway 429s count as 4xx, so the three inbox routes can produce at most ≈ 3 × 300 + 30 ≈ 930 4xx per 5 min — under the threshold. A bad-key flood is in fact caught by `InboxKeyRefused` (>24 / 15 min), so this alarm is redundant for inbox traffic and only fires for unauthenticated floods on `$default`. S: lower to ~300 or reword.

### OPS-14 — Small observations (Low, batch)
- One flaky test: `TestConcurrentCompleteCaptureAppendsExactlyOnce` (`backend/internal/pipeline`) — the only re-runs this week (3: runs 36345333589, 36532423081, 36533711342). Every other CI failure was real.
- Duplicate manual "Master Budget" ($10) beside the stack's budget; unused SSM `/chintan/dev/token_vault_key`; `Project` is not an activated cost-allocation tag (the README says to activate it); API access log `correlationId` duplicates `requestId` and carries no `sourceIp`, so an inbox flood has no address to act on.
- Content buckets have no bucket policy (no `aws:SecureTransport` deny) and SSE-S3; fine for one user, note for the day there are two.
- Cognito: ESSENTIALS tier has no threat protection (the template's `ThreatProtectionAvailable` branch is dormant), MFA optional, one invited user still in `FORCE_CHANGE_PASSWORD`.
- `web_push`/VAPID (#207): the deploy role gained nothing; the pair is written by the operator's own credentials; `doctor.sh` reports presence. The prod template on AWS differs from main by one comment (the #207 wording) — nothing else.

### OPS-15 — Agent tooling (`.agent-tools/`) (Low; owner decision on OPS-1 decides its fate)
- **Safe parts (traced):** `stack-merge.sh` and `merge-seq.sh` push only with `--force-with-lease`, never to `main`; squash merges; worktrees pruned; `resolve-known.py` auto-resolves only append-only files and takes main's fixtures (CI regenerates and diffs them, so a wrong pick fails loudly).
- **Unsafe parts:** `gate.sh` is the self-approval in OPS-1; `merge-seq.sh:26-32` merges after a 20-minute pending timeout or an empty check list; `resolve-known.py` "keep both" on `frontend/src/styles/index.css` and `Icon.tsx` can land duplicate CSS/exports that lint may not catch; 14 unquoted expansions (SC2086) and 2 unused vars (reproduced).
- **Move into the repo?** No. They hard-code Mac paths and an environment id, pull into the owner's checkout, and encode a policy (auto-approve) the repo's README contradicts. If the owner chooses OPS-1(a), `gate.sh` is deleted and the rest stays personal tooling; if OPS-1(b), `gate.sh` must stop being used.

---

## Checked and sound (no finding)

- **CI pipeline integrity:** every action SHA-pinned, every tool versioned; each custom check ships a `--self-test` that proves it can fail; the contract job checks both directions and the committed fixtures are current (reproduced). Frontend: `bun audit` 0 at any level, 1405 vitest tests green in 13 s, typecheck/lint/knip clean, main chunk 163 kB gz. Queue wait p50 2 s; only one flaky test.
- **Deploy path:** change set printed and refused on stateful replacement (`deploy.sh:149-172`), alias publish with rollback, staging-first with smoke; 40/40 Deploy Backend runs green, prod stack events show 5 clean updates on 30 Sept and no rollbacks; Deploy Frontend p50 56 s with 0 failures and 2 expected concurrency cancels; release tagging works. Deployed bootstrap == repo; deployed prod/staging templates == repo (one comment).
- **S3 lifecycle after R7-3 (measured against the live rules):** every sub-90-day rule is tag-gated (`capture-audio` or `export`); the only untagged rules are 90-day noncurrent, abort-multipart and expired-delete-marker; only the audio upload is tagged (`upload.go:206`) and `MarkProcessed` preserves tags — **no rule can expire a note version before 90 days**; transcripts are untagged so Regenerate is safe.
- **IAM:** Lambda roles scoped per table/bucket/parameter with the boundary attached; build role can only upload two key prefixes and DescribeStacks; OIDC subjects per environment; cfn-deploy denies touching the guardrails and itself.
- **Data protection:** DynamoDB PITR + deletion protection + TTL, termination protection on all three stacks, versioned/encrypted/BPA buckets, CloudTrail with validation and an agent-write deny, DLQ with 14-day retention and an alarm.
- **Public inbox:** three `AuthorizationType: NONE` routes exactly as the router declares (`TestPublicRoutesMatchTheGatewayRouteTable`), gateway-throttled 1 rps/10 burst each, device keys sha256-hashed with constant-time compare, optional expiry (WH-A), neighbourhood only (WH-B), per-device daily limit, refusals counted by reason and alarmed; body caps enforced in the handler.
- **Service worker:** `registerType: 'prompt'`, no `skipWaiting` at install, update on `visibilitychange` ≤ 1/30 min, precache of hashed assets only, runtime cache touches same-origin navigations only — **never `/v1/`, never Cognito or S3** — and one reload on `controllerchange`. End to end: deploy → new `sw.js` → waiting worker → prompt → reload. CORS on the gateway and the bucket is one explicit origin with credentials.
- **Logs:** 14-day retention on all six groups, 12.8 MB stored in total; log-hygiene gate in CI.
- **Cognito client:** no secret, SRP + USER_AUTH + refresh, revocation on, `PreventUserExistenceErrors`, callback pinned to the instance path, 60-min tokens / 30-day refresh, passkeys on prod.

## Cost (read-only, Cost Explorer; measured)

| Service | Aug | Sep |
|---|---|---|
| Cost Explorer API calls | 0.090 | 0.170 (17 calls × $0.01, 6 by this review) |
| S3 | 0.133 | 0.165 |
| DynamoDB | 0.001 | 0.058 |
| API Gateway | 0.002 | 0.033 (33 k requests) |
| Tax | 0.030 | 0.050 |
| KMS / CloudWatch / SQS | 0.071 | 0.003 |
| **Total** | **0.328** | **0.476** |

September is $0.48 against the 30 Sept baseline of ~$0.37, and the growth is Cost Explorer calls (reviews) plus tax; the workload run-rate is ≈ $0.26/month, 2.6 % of the $10 budget (actual MTD $0.46, forecast $0.41, both notifications OK). DynamoDB and API Gateway grew with QA traffic, not with a leak. **Provider spend** (the instance `SPEND#<day>` counters in the prod table; older rows TTL out): $0.63 across the 18 September days still present, peak $0.20 on 29 Sept (the eval battery) — about $1/month pace, below the $1.55 baseline. Nothing grew.

## What is good (keep it)

1. `scripts/deploy.sh` is the best file in the repository: printed change set, stateful-replacement refusal, alias rollback, self-tests, honest failure reporting; and the deployed stacks match the repo.
2. The CI gates are pinned, versioned and self-proving; the contract gate catches a one-sided rename on purpose, and the fixtures are current.
3. IAM is genuinely least-privilege where it matters (Lambda roles, build role, boundary) and the guardrail denies are layered.
4. The service worker and the inbox are designed, not defaulted: prompt updates, no API caching, hashed keys, throttles, refusal metrics.
5. Cost is tiny, watched by a budget and a daily cap, and the lifecycle rules now keep 90 days of note history without over-keeping audio.

## Owner decisions needed

- OPS-1: automatic-and-truthful (ruleset + no reviewer) or human-and-truthful (reviewer, no gate.sh).
- OPS-5: run `scripts/bootstrap-agent.sh --apply` once (elevated) and check the DLQ depth.
- OPS-6: spend one staging run to prove the rollback.
- OPS-10: whether a meta CSP is worth the build-time plumbing.
