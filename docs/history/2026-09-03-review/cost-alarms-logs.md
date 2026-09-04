# Chintan — alarms, logs and cost profile (read-only)

Date: 2026-09-03 (UTC). Account `<account>`, region us-west-2. Repo HEAD `1365195` (the review branch; both stacks were redeployed to Lambda version 15 at 14:44Z today).

Access: `ssh ubuntu@orb`. Configuration reads ran as `chintan-agent` (`aws --profile chintan`). The reads that role is denied — `cloudwatch:DescribeAlarms`/`DescribeAlarmHistory`/`GetMetricStatistics`/`ListMetrics`, `ce:GetCostAndUsage`, `cloudtrail:DescribeTrails`, `budgets:*`, `sns:ListSubscriptionsByTopic`, `xray:GetSamplingRules` — used the box's own session, read-only, with the owner's permission. Nothing was created, modified or deleted; no SSM value was read; no log line containing user content exists (by design) and none was found. Cost Explorer was called **3 times** ($0.03).

This report does not repeat [`aws-live-audit.md`](aws-live-audit.md) (inventory, stacks, IAM, Cognito) or [`review-2026-09-03.md`](review.md) §2–§4. It adds what those could not read: alarm states and history, metric volumes, the custom-metric bill, and a line-item cost attribution.

---

## Ranked summary

Idle = the account as it is today (zero user traffic). Daily = 5 captures/day, 30 s each, ~100 API requests per capture (the progress card polls 4 lists every 4 s for the ~2-minute pipeline). Prices are the published us-west-2 rates; every number below is derived from a measured quantity in §1–§5.

| # | Change | Saves / month (idle) | Saves / month (daily) | Effort | Where |
|---|---|---|---|---|---|
| 1 | **Trim prod alarms 10 → 5 and drop every `OKActions`.** The account has 12 alarms (10 chintan + 2 passbook) against 10 free; chintan's ten push it $0.20/month over. Five have never left OK; two fired only during development. 12 of the 18 e-mails ever sent were OK/creation noise. | **$0.20** | $0.20 | 30 min, template only | `template.yaml` `Api5xxRateAlarm`, `ApiFunctionThrottlesAlarm`, `WorkerFunctionErrorsAlarm`, `WorkerFunctionThrottlesAlarm`, `ProviderRateLimitedAlarm`, 8 × `OKActions` |
| 2 | **Delete the CloudTrail trail** (review §5.5), or at least make it single-region without log-file validation. The trail's S3 PUTs are the **largest single line in the account**: ~870 objects/day (408 of them hourly digest files for 17 regions where nothing happens) = ~26k PUTs/month = $0.13. Management events themselves are free. | **$0.13** (delete) / $0.07 (single-region, no digests) | same | 5 min, owner (agent is denied) | `scripts/bootstrap-agent.sh`, trail `chintan-trail` |
| 3 | **Delete the staging stack**, or disable its SQS event-source mapping. Each capture queue is long-polled 21,590 times/day by the Lambda poller with nothing in it; two queues = 1.30M requests/month vs 1M free = $0.12. One queue = 648k = $0. | **$0.12** | $0.12 | 1 h (H14 in the review: the cleanup script needs fixing first) or 3 lines (`Enabled: !If [IsProd, true, false]` on `CaptureQueueEventSourceMapping`) | `chintan-dev-staging`, `config/instances/dev-staging.yaml` |
| 4 | **Stop calling Cost Explorer from scripts and audits.** $0.01 per call: $0.09 in August, ≥ $0.10 in September so far (this review's two passes). `budgets describe-budget` returns actual + forecast for free and is already readable by the agent; the Billing console is free. | **$0.09–0.10** in audit months | same | 0 — a rule for the operator/agent | runbooks |
| 5 | **Activate the `Project`/`Instance`/`Environment` cost-allocation tags** in Billing. No saving; it is what makes the per-project view exist. ~24 h, no backfill. | $0 | $0 | 2 min, owner | Billing console |
| 6 | **Do not shorten log retention.** All eight groups ingested 2.5 MB in August (free tier 5 GB); at 14 days storage rounds to $0.0000. 14 → 7 saves < $0.0001. If anything raise it to 30 days (< $0.001/month at daily use) so the next audit is not looking at empty groups, as this one was. | $0 | $0 | — | `LogRetentionDays` |
| 7 | **Keep API Gateway access logs.** They are a second copy of every request (341 B vs the Lambda's 205 B `request` line, 24% of API-path log bytes) but the only record of requests rejected before Lambda (Aug 20, Sep 3 probes). Cost: $0.0002 per 1,000 requests. Drop only for simplicity. | $0 | $0.003 | 6 lines | `ApiStage.AccessLogSettings`, `ApiAccessLogGroup` |
| 8 | **X-Ray: switch to `PassThrough`** on all three functions. Every invocation is currently sampled (`Sampled: true` on all 13 today); August stored 1,690 traces, daily use would store ~15k/month — both inside the 100k free tier. Saves ~70 B per `REPORT` line and an IAM statement, not dollars. Turn Active on when debugging a latency problem. | $0 | $0 | 6 lines | `TracingConfig.Mode` ×3, `XRayTracing` policy |
| 9 | **EMF custom metrics: no cost action needed — the latent-cost fear is unfounded.** Custom metrics bill per metric-*hour* (`CW:MetricMonitorUsage` in metric-months, prorated); August's 44+ identities metered **0.239 metric-months** against 10 free. Daily use ≈ 2.5–7 metric-months → $0. The only way to a bill is > 10 identities active every hour of the month (a 24/7 poller or many users). Optional hygiene: drop `ApiLatency` and `CaptureStageEntered` (20 → 13 steady-state identities). Do **not** replace EMF with metric filters — they mint the same billable identities. | $0 | $0 | optional | `handler/metrics.go`, `pipeline.go:894` |
| 10 | **S3 versioning / PITR: nothing to do.** Prod bucket has 210 current objects, **0 noncurrent versions, 0 delete markers**; the 7-day `ExpireNoncurrentVersions` rule works. Daily use leaves ≤ 35 noncurrent `note.md` versions (~150 KB) in flight. PITR on 84 KB = $0.00002/month. The orphan bucket noted this morning **has since been deleted**. | $0 | $0 | — | — |
| 11 | **Budgets: delete the staging stack's notification-less budget and the manual `Master Budget `.** Budgets are free; this is clutter, not cost. | $0 | $0 | 2 lines | `MonthlyBudget` → `Condition: HasAlarmEmail` |
| 12 | **Lambda/DynamoDB provisioning is not a cost lever.** August: 247 GB-s and 2,354 requests of 400k/1M free; DynamoDB $0.0014; artifact bucket 693 MB = $0.012/month with a working 30-day expiry. Reserved concurrency 50 is an exposure (review, Medium), not a cost. | $0 | $0 | — | — |

**Totals.** Measured idle run-rate for September at today's configuration ≈ **$0.50/month + tax** (alarms $0.20, CloudTrail PUTs $0.13, SQS polling $0.12, artifact storage $0.013, plus $0.01 per Cost Explorer call), against the README's "$0.00 idle". Items 1–4 remove ≈ **$0.45–0.55** of it in both scenarios. Daily use adds only ≈ **$0.03–0.04/month** of variable AWS cost (API Gateway $0.015, DynamoDB $0.01, S3 $0.006, everything else inside free tiers); the provider bill (Groq + MiniMax, ≈ $0.30–0.70/month at this volume) is the real cost and is outside AWS. After the changes the AWS bill is ≈ $0.05/month + tax at either usage level.

Three corrections to earlier documents fall out of this: (a) the README's "$0.00 idle" is off by ≈ $0.50 — every fixed item is infrastructure-shaped (trail, poller, alarms), none is usage-shaped; (b) the template's "exactly ten alarms fits precisely" in the free tier is wrong for *this* account because passbook holds two more alarms in us-east-2; (c) the review's concern that EMF dimensions are "likely the biggest latent cost item" does not survive the proration rule — measured 0.239 metric-months.

---

## 1. Alarms

Ten metric alarms in the account carry the `chintan-` prefix, all in `chintan-dev-prod` (`EnableAlarms=true`); staging has none. No composite alarms. The account's other two alarms are passbook's, in us-east-2 (`USE2-CW:AlarmMonitorUsage` = 2.0 alarm-months in August).

### 1.1 Configuration and state (`describe-alarms`, 2026-09-03)

| Alarm | Metric | Stat / period / eval | Threshold | Missing data | State | `StateUpdatedTimestamp` (UTC) | Actions (ALARM / OK / INSUFF.) |
|---|---|---|---|---|---|---|---|
| `chintan-dev-prod-api-5xx-rate` | AWS/ApiGateway `5xx` {ApiId} | Average / 300 s / 2 of 2 | > 0.05 | notBreaching | OK | 2026-08-13 20:19:48 | 1 / 1 / 0 |
| `chintan-dev-prod-api-errors` | AWS/Lambda `Errors` {api fn} | Sum / 300 s / 1 | > 0 | notBreaching | OK | 2026-08-08 18:33:27 | 1 / 1 / 0 |
| `chintan-dev-prod-api-throttles` | AWS/Lambda `Throttles` {api fn} | Sum / 300 s / 1 | > 0 | notBreaching | OK | 2026-08-17 21:35:16 | 1 / 0 / 0 |
| `chintan-dev-prod-worker-errors` | AWS/Lambda `Errors` {worker fn} | Sum / 300 s / 1 | > 0 | notBreaching | OK | 2026-08-08 18:32:35 | 1 / 1 / 0 |
| `chintan-dev-prod-worker-throttles` | AWS/Lambda `Throttles` {worker fn} | Sum / 300 s / 1 | > 0 | notBreaching | OK | 2026-08-08 18:32:40 | 1 / 0 / 0 |
| `chintan-dev-prod-capture-dlq` | AWS/SQS `ApproximateNumberOfMessagesVisible` {captures-dlq} | Maximum / 300 s / 1 | > 0 | notBreaching | OK | 2026-08-08 18:31:11 | 1 / 1 / 0 |
| `chintan-dev-prod-expiry-dlq` | AWS/SQS `ApproximateNumberOfMessagesVisible` {expiry-dlq} | Maximum / 300 s / 1 | > 0 | notBreaching | OK | 2026-08-08 20:39:48 | 1 / 1 / 0 |
| `chintan-dev-prod-provider-key-rejected` | Chintan `ProviderKeyRejected` (no dims) | Sum / 300 s / 1 | > 0 | notBreaching | OK | 2026-08-08 20:38:38 | 1 / 1 / 0 |
| `chintan-dev-prod-provider-rate-limited` | Chintan `ProviderRateLimited` (no dims) | Sum / 300 s / 3 of 3 | > 0 | notBreaching | OK | 2026-08-08 20:39:35 | 1 / 1 / 0 |
| `chintan-dev-prod-spend-cap-tripped` | Chintan `SpendCapRejections` (no dims) | Sum / 300 s / 1 | > 0 | notBreaching | OK | 2026-08-08 19:26:20 | 1 / 1 / 0 |

All actions target the SNS topic `chintan-alarms-dev-prod`, which has one confirmed e-mail subscription. `ActionsEnabled` is true on all ten. Eight of ten carry `OKActions`; the two throttle alarms do not.

### 1.2 History (`describe-alarm-history --max-records 50`, per alarm)

| Alarm | History items | Ever in ALARM? | Episodes (UTC) | SNS actions executed |
|---|---|---|---|---|
| api-5xx-rate | 23 | **Yes, 4×** | Aug 8 20:27→20:41 (0.5, 0.5); Aug 13 06:24→06:35 (0.8, 0.32); Aug 13 19:53→20:08 (0.44, 0.64); Aug 13 20:14→20:19 (0.8, 0.44). Each cleared within 5–15 min. Created/deleted/recreated three times on Aug 8 during template iteration. | 9 (4 ALARM + 4 OK + 1 INSUFFICIENT→OK) |
| api-errors | 3 | No | — | 1 (creation INSUFFICIENT→OK) |
| api-throttles | 8 | **Yes, 2×** | Aug 17 19:33→19:38 (2 throttles); Aug 17 21:30→21:35 (3 throttles). Reserved concurrency was 5 at the time; the HEAD-1 commit raised it to 50. | 2 |
| worker-errors | 3 | No | — | 1 (creation) |
| worker-throttles | 2 | No | — | 0 (no OKActions) |
| capture-dlq | 3 | No | — | 1 (creation) |
| expiry-dlq | 3 | No | — | 1 (creation) |
| provider-key-rejected | 3 | No | — | 1 (creation) |
| provider-rate-limited | 3 | No | — | 1 (creation) |
| spend-cap-tripped | 6 | No | 3 configuration updates Aug 10 (the Metrics Insights → standard alarm change) | 1 (creation) |

**18 notifications in total; 6 were real ALARM transitions (all during the Aug 8–17 development window, while the developer was watching), 12 were OK or creation e-mails.** The alarm `api-errors` never fired although 85 5xx responses were served in August (3.7% of 2,286 requests): every 5xx was the application returning one, not the Lambda faulting — which is also why `api-5xx-rate` at threshold 0.05 fires on a single 500 in a quiet 5-minute window (1 in 10 requests = 0.1).

### 1.3 Free-tier position and cost

CloudWatch's always-free allowance is 10 standard alarms per account per month. August metered `USW2-CW:AlarmMonitorUsage` 8.033 (chintan, created Aug 8) + `USE2-CW:AlarmMonitorUsage` 2.0 (passbook) = 10.033 → $0.0033 billed. September, full month: 12 alarm-months → **$0.20/month**. Deleting two chintan alarms returns the account to $0; the template comment and `dev-staging.yaml` both assume chintan is alone in the account, which it is not. (August's other CloudWatch charge, `CW:MetricInsightAlarmUsage` $0.0137, was the Metrics Insights alarm removed on Aug 10 — that fix worked.)

### 1.4 Signal or noise, for one user

| Alarm | Verdict | Reason |
|---|---|---|
| capture-dlq | **Signal — keep** | The only terminal "a dictation was lost" signal. Never fired; 0 messages ever sent to either DLQ (`NumberOfMessagesSent` = 0 since creation). |
| provider-key-rejected | **Signal — keep** | An expired/rotated key is invisible otherwise and fails every capture. |
| spend-cap-tripped | **Signal — keep** | The breaker is the only defence against a runaway loop; silent tripping looks like a technical outage. |
| expiry-dlq | Keep for now | Goes away with the expiry Lambda if review §5.4 (EventBridge sweep) is done. |
| api-errors | Keep, drop OKActions | Crash/timeout detector for the API; never fired. Cheap insurance and the one alarm that distinguishes "Lambda died" from "app said 500". |
| api-5xx-rate | **Delete** | Fired 4× in development, never since; duplicates what the user sees instantly on the phone and what `api-errors` catches when it is a real fault. With `Average` over 300 s and one user, one 500 trips it. Confirms the review's Medium item. |
| api-throttles | **Delete** | Fired only at reserved concurrency 5 (the frontend's four parallel polls). At 50 a single user cannot reach it; an attacker meets API Gateway's 50 rps throttle first, and the budget catches the bill. |
| worker-errors | **Delete** | Fires on the first attempt of a capture SQS will retry (review); `capture-dlq` is the terminal signal. Confirms the review. |
| worker-throttles | **Delete** | Reserved 5 with SQS redelivery; a throttle is a retry, not an incident. |
| provider-rate-limited | **Delete** | 429s are retried; 3 consecutive periods never happened; a sustained one ends in `capture-dlq` anyway. |

Result: **5 chintan alarms** (+2 passbook = 7 of 10 free), no `OKActions`, e-mails cut by two-thirds. If staging ever needs alarms again there is room for three.

---

## 2. Logs

### 2.1 Groups

| Log group | Retention | `storedBytes` (API, lags) | August ingestion (`IncomingBytes`, sum of daily) | August events |
|---|---|---|---|---|
| /aws/lambda/chintan-api-dev-prod | 14 d | 0 | 1,763,419 B | 9,532 |
| /aws/lambda/chintan-worker-dev-prod | 14 d | 0 | 116,037 B | 431 |
| /aws/lambda/chintan-expiry-dev-prod | 14 d | 0 | 12,643 B | — |
| /aws/lambda/chintan-api-dev-staging | 14 d | 0 | 29,585 B | — |
| /aws/lambda/chintan-worker-dev-staging | 14 d | 0 | 3,758 B | — |
| /aws/lambda/chintan-expiry-dev-staging | 14 d | 0 | 0 (never invoked) | — |
| /aws/apigateway/chintan-dev-prod | 14 d | 920 | 562,740 B | 1,647 |
| /aws/apigateway/chintan-dev-staging | 14 d | 14 d | 7,758 B | — |
| **Total** | | | **≈ 2.50 MB** | |

At $0.50/GB that is $0.00125 — and it is inside the 5 GB always-free ingestion tier, so $0. Storage at 14 days: 2.5 MB × $0.03/GB-month ≈ $0.00007. Retention is not a cost lever at any plausible usage: daily use ingests ≈ 20 MB/month (below), $0.01 even if billed, still free.

Since Aug 18 the only Lambda events are today's: 70 events / 11,322 B in the API group (13 invocations from the review's smoke tests), 3,352 B in staging.

### 2.2 What one API invocation writes (sampled today, 13 invocations, 70 lines)

| Line kind | Lines | Bytes | Avg B/line | Share of bytes |
|---|---|---|---|---|
| Platform `START` / `END` / `REPORT` (REPORT includes a 70-B `XRAY TraceId… Sampled: true` line) | 39 | 4,699 | 66 / 52 / 243 | 41% |
| EMF metrics JSON (`ApiRequests` + `ApiLatency`, one record per routed request) | 8 | 2,447 | 305 | 22% |
| Application `INFO` (`msg:"request"` with method, route, status, bytes, duration_ms) | 18 | 3,251 | 180 (205 for `request`) | 29% |
| Cold start `INIT_START` + `"biometric unlock enabled"` | 5 | 925 | 185 | 8% |

**≈ 870 B and 5.4 lines per invocation** today (health probes); **941 B and 4.9 lines** on Aug 14 UTC (263 real requests, 1,286 events, 247,535 B — the busiest normal-use day). The API Gateway access log adds one 341-B JSON line per request (89,641 B / 263 requests that day), i.e. a second copy of method/route/status/latency — it is 24% of API-path bytes but the only record of the requests the JWT authorizer rejects before Lambda runs.

Per request, all sources: **≈ 1.28 KB.** Per 1,000 requests: 1.28 MB → **$0.0006** at $0.50/GB (and free under 5 GB). Per capture (~100 requests incl. the progress-card polling): ≈ 128 KB.

### 2.3 What one capture writes in the worker

The August logs have aged out, so this is from the daily ratios: Aug 14 UTC — 6 worker invocations, 107 events, 31,475 B → **≈ 5.2 KB and 18 lines per capture**; Aug 8 UTC — 11 invocations, 121 events, 33,052 B → 3.0 KB and 11 lines. The lines are: platform START/END/REPORT (3), `CaptureStageEntered` EMF per stage (≈ 5), `ProviderLatency` + `ProviderCostMicros/ProviderCalls` EMF per provider call (≈ 6), "provider usage" INFO per call (3, the `SlogSink`), "transcribed capture" and "capture pipeline finished" INFO (2). Roughly two-thirds of the worker's bytes are EMF. Per 1,000 captures: 5 MB → **$0.0025**.

Chatty lines worth naming, in order of bytes: (1) the per-request EMF record (305 B — a third of every API invocation; `ApiLatency` duplicates `duration_ms` in the request line); (2) the per-stage `CaptureStageEntered` EMF (5 × ~280 B per capture); (3) the `SlogSink` "provider usage" INFO which is emitted alongside an EMF record carrying the same numbers; (4) the frontend's polling itself, which multiplies (1) by ~100 per capture. None of them costs money at this scale — the cheapest way to cut log volume by 90% is the review's "one `status=all` poll per 4–8 s instead of four" (§5.6).

### 2.4 X-Ray

`TracingConfig.Mode: Active` on all six functions; the account has only the `Default` sampling rule (reservoir 1/s + 5%), which at this request rate samples **every** invocation (all 13 today: `Sampled: true`). August: `USW2-XRay-TracesStored` = 1,690, cost $0 (100k/month free; $5.00/million after). Daily use ≈ 15,000 traces/month → $0. Break-even is ~3,300 requests/day, which one user does not produce. Recommendation: `PassThrough` for simplicity (also removes the XRAY line from every REPORT and the `XRayTracing` IAM statement); it is not a saving.

---

## 3. EMF custom metrics (namespace `Chintan`)

`list-metrics --namespace Chintan` returns only identities that received data in the last ~two weeks: today that is **2** (`ApiRequests{Status=2xx}`, `ApiLatency{Status=2xx}` from the smoke tests). The August set has aged out of the listing, so the identities are enumerated from the emitters in the code (`grep obs\.(Count|CountWithRollup|Duration|Emit)`):

| Metric | Dimensions | Identities in normal use | Extra on failures |
|---|---|---|---|
| ApiRequests, ApiLatency (`handler/metrics.go:97`) | Status ∈ {2xx, 4xx, 5xx, 3xx} | 4 (2xx, 4xx × 2 metrics) | +2 (5xx) |
| CapturesCreated (`service/capture.go:267`) | Stage=created | 1 | |
| CaptureStageEntered (`pipeline.go:894`) | Stage ∈ {transcribing, cleaning, routing, appending, appended, …} | 5 | +1 (failed) |
| CapturePipelineDuration (`pipeline.go:178`) | Outcome ∈ {appended, failed} | 1 | +1 |
| ProviderLatency (`breaker.go:192,231`) | Provider × Op × Outcome | 3 (groq/transcribe, openai/clean, openai/route × ok) | +3 (error) |
| ProviderCostMicros, ProviderCalls (`meter.go:126`) | Provider × Op | 6 | |
| CaptureStageFailures (`pipeline.go:192,1024,1044`) | Stage | 0 | +1–5 |
| ProviderKeyRejected, ProviderRateLimited, SpendCapRejections (with rollup) | Provider × Op, plus dimensionless | 0 | +2 each |
| DuplicateDelivery, CaptureSpendCapped, CaptureRejectedOversize, AppendResumedWithoutRewriting, WorkerMessagesDiscarded, RouterContentDiscarded, SpendCapLookupFailures | one dim each | 0 | +1 each when they occur |
| **Steady state** | | **≈ 20** | **≈ 30–35 with a bad day** |

The handler comment records 44 identities measured after one evening before the `Route` dimension was removed; 20 + 4 API identities per extra route is consistent with that.

**Exposure.** The naive model — "every identity that receives data in a month bills $0.30 after the first 10" — would price this at 20–35 identities → **$3.00–7.50/month**, which is presumably what the review had in mind. That is not how CloudWatch bills: the usage type `CW:MetricMonitorUsage` is metered in metric-months **prorated by the hour, only for hours in which the identity receives data**, and the free tier is 10 metric-months. Measured: August's 44+ identities across six active days metered **0.239 metric-months → $0.0000**.

| Scenario | Active identities | Hours/month with data | Metric-months | Bill |
|---|---|---|---|---|
| August (measured) | 44+ | a few hours on 6 days | 0.239 | $0 |
| Daily use, captures in ~3 distinct hours/day | 20 | 90 | 2.5 | $0 |
| Daily use spread over 8 hours/day, some failures | 30 | 240 | 9.9 | $0 (at the edge) |
| A 24/7 poller (health check every few minutes) on top of daily use | 2 continuous + 20 × 8 h | 730 + 240 | 8.6 | $0 |
| 24/7 traffic that touches every identity every hour (multi-user) | 20 | 730 | 20 | $3.00 |

So the latent cost is real only if more than ten identities are active around the clock. For a one-user app the metric bill is $0 in every realistic pattern, and the guard is "never add a 24/7 poller", not "collapse dimensions". Optional hygiene if the code is being touched anyway: drop `ApiLatency` (the request log already has `duration_ms` per route, and `ApiRequests{5xx}` is the only thing an alarm could want) and replace `CaptureStageEntered` (5 identities) with the existing `CapturePipelineDuration` + `CaptureStageFailures` — 20 → 13 steady-state identities. Do **not** switch to metric filters on the logs: a metric filter publishes a custom metric with exactly the same billing, and would add a filter per metric to maintain.

---

## 4. Cost Explorer

Three calls were made (unblended, account-wide; tags are inactive so no filter is possible).

### 4.1 By service, monthly (call 1)

| Service | Jun | Jul | Aug | Sep 1–3 | Chintan? |
|---|---|---|---|---|---|
| S3 | 0.0028 | 0.0006 | **0.1386** | 0.0099 | yes — see 4.2 |
| Cost Explorer API | 0.0100 | – | **0.0900** | – (≥ $0.10 pending: 10 calls this review) | yes (audits) |
| KMS | – | – | 0.0484 | – | yes — the CMK deleted Aug 8; gone |
| Tax | – | – | 0.0300 | – | ~10% of everything |
| SQS | – | – | 0.0275 | – (inside free tier so far) | yes — idle polling |
| CloudWatch | – | – | 0.0170 | – | yes — MI alarm $0.0137 + alarm overage $0.0033 |
| API Gateway | 0.0005 | 0.0002 | 0.0023 | – | yes (2,286 requests) |
| DynamoDB | 0.0003 | 0.0001 | 0.0014 | 0.000001 | yes |
| **Total** | **0.0137** | **0.0010** | **0.3553** | **0.0099** | ≈ $0.32 of Aug is chintan; ≈ $0.008 passbook/AppStream |

### 4.2 By usage type, August (call 2) and September to date (call 3) — the lines that cost anything

| Usage type | Aug qty | Aug $ | Sep 1–3 qty | Sep $ | What it is |
|---|---|---|---|---|---|
| `USW2-Requests-Tier1` | 1,091,585 | 0.1406 | 89,726 | 0.0085 | **Shared usage-type name for S3 PUT/LIST and SQS requests.** Decomposition: SQS polling ≈ 1.06M (2 queues × 21,590/day × 24 days after Aug 8) → 60k over the 1M free → $0.024–0.028; S3 PUTs ≈ 22–24k → $0.11–0.12, matching the CloudTrail bucket's growth (713 → 22,849 objects Aug 7 → Sep 1, 885/day). |
| `USE1-APIRequest` | 9 | 0.0900 | (lags) | – | Cost Explorer `GetCostAndUsage` calls, $0.01 each |
| `us-west-2-KMS-Keys` | 0.048 key-months | 0.0484 | – | – | the removed CMK |
| `NoUsageType` (Tax) | | 0.0300 | | | |
| `USW2-CW:MetricInsightAlarmUsage` | 0.137 | 0.0137 | – | – | Metrics Insights alarm, removed Aug 10 |
| `USW2-TimedStorage-ByteHrs` | 0.514 GB-mo | 0.0118 | 0.048 | 0.0011 | S3 storage — the 693 MB Lambda artifact bucket, not the 5.6 MB of content |
| `CAN1-Requests-Tier1` / `CAN1-*` | 1,286 | 0.0071 | – | – | ca-central-1 — **passbook** (its Lambda, HTTP API and bucket are there) |
| `USW2-Requests-Tier2` | 14,133 | 0.0057 | 764 | 0.0003 | S3 GETs (deploys, exports, audits) |
| `USW2-CW:AlarmMonitorUsage` + `USE2-…` | 8.033 + 2.0 | 0.0033 | 0.639 + 0.128 | 0 (free tier applied first) | 12 alarms vs 10 free |
| `USW2-ApiGatewayHttpRequest` | 2,286 | 0.0023 | – | – | past the 12-month HTTP API free tier |
| `USW2-WriteRequestUnits` / `ReadRequestUnits` | 1,356 / 4,321 | 0.0013 | – | – | DynamoDB on-demand |
| `USW2-CW:MetricMonitorUsage` | **0.239** | 0.0000 | – | – | custom metrics, prorated (§3) |
| `USW2-XRay-TracesStored` | 1,690 | 0.0000 | – | – | §2.4 |
| `USW2-Lambda-GB-Second-ARM` / `Request-ARM` | 246.8 / 2,354 | 0.0000 | – | – | of 400k / 1M free |
| `USW2-FreeEventsRecorded` (+USE1/USE2/CAN1/EU) | 110,940 (+ ~9k) | 0.0000 | 7,374 | 0 | CloudTrail management events — free; only their S3 delivery costs |
| `USW2-CognitoEssentialsMAU` | 6 | 0.0000 | | | of 10,000 free |
| `USW2-TimedPITRStorage-ByteHrs` | 0.000 | 0.0000 | 0.000 | 0 | PITR on 84 KB |
| `USW2-DeliveryAttempts-SMTP` | 6 | 0.0000 | | | SNS e-mails (1,000 free) |

Everything else (inter-region byte lines, `Catalog-Request`, `TagStorage`, `Streams-Requests`) is < $0.0001.

### 4.3 Projected September at today's configuration, idle

| Line | Basis | $/month |
|---|---|---|
| CloudWatch alarms | 12 − 10 free = 2 × $0.10 | 0.20 |
| CloudTrail S3 PUTs | ~870/day × 30 × $0.005/1k | 0.13 |
| SQS idle polling | 2 × 21,590 × 30 = 1.295M − 1M free = 295k × $0.40/M | 0.12 |
| S3 storage | 0.55 GB-month (artifacts + content + trail) | 0.013 |
| Cost Explorer calls | 10 so far this month | 0.10 |
| Tax (~10%) | | 0.05 |
| **Total** | | **≈ 0.61** (≈ 0.51 without the CE calls) |

Budgets' own forecast is $0.261 (it extrapolates the three S3-only days and cannot see the alarm overage yet).

---

## 5. Provisioning

### 5.1 Lambda (`get-function-configuration`, `get-function-concurrency`, today)

| Function | Mem | Timeout | Arch | Reserved | Tracing | Log format | Versions kept | August use |
|---|---|---|---|---|---|---|---|---|
| chintan-api-dev-prod | 512 MB | 29 s | arm64 | 50 | Active | Text | 16 | 2,131 invocations, avg 59 ms, max 10.5 s |
| chintan-worker-dev-prod | 2048 MB | 900 s | arm64 | 5 | Active | Text | 16 | 42 invocations, avg 2.06 s, max 25.9 s |
| chintan-expiry-dev-prod | 512 MB | 300 s | arm64 | 2 | Active | Text | 11 | 24 invocations |
| chintan-api-dev-staging | 512 MB | 29 s | arm64 | 50 | Active | Text | 19 | deploy smoke only |
| chintan-worker-dev-staging | 2048 MB | 900 s | arm64 | 5 | Active | Text | 19 | 6 messages received (Aug 8) |
| chintan-expiry-dev-staging | 512 MB | 300 s | arm64 | 2 | Active | Text | 13 | never invoked |

Compute is not a cost: August used 247 GB-s of the 400,000 free. Daily use ≈ 150 captures × (2 GB × ~10 s) + 15k API × (0.5 GB × 0.06 s) ≈ 3,500 GB-s — still free. Memory and timeouts cost nothing when idle; 2048 MB for the worker buys CPU for the audio handling and is fine. 94 published versions × ~8.5 MB ≈ 800 MB of the 75 GB free code storage. The one provisioning item with a dollar attached is the review's: reserved concurrency 50 + API Gateway 50 rps on unauthenticated routes is a ~$4–5/day exposure if abused; 10 would still be 2× what one phone can generate.

### 5.2 DynamoDB

Both tables `PAY_PER_REQUEST`; prod 72 items / 83,899 B (+ GSIs 17,296 B and 16,849 B); streams on; **PITR ENABLED, 35 days**; deletion protection off (review). PITR is billed on table size: 0.084 MB × $0.20/GB-month = **$0.00002/month** — `USW2-TimedPITRStorage-ByteHrs` shows 0.000. Keep it. On-demand requests in August: 1,356 WRU + 4,321 RRU = $0.0013. Daily use (150 captures × ~10 writes; ~100 polls × 1–2 RRU per capture) ≈ 1.5k WRU + 30k RRU ≈ $0.01/month.

### 5.3 S3

| Bucket | Current objects / bytes | Noncurrent / delete markers | Storage class | Relevant lifecycle |
|---|---|---|---|---|
| chintan-content-dev-prod-<account> | 210 / 5,607,307 B (32 webm, 88 txt, 61 json, 29 md) | **0 / 0**; no key has > 1 version | STANDARD | `ExpireNoncurrentVersions` 7 d, `ExpireDeleteMarkers`, `AbortStaleMultipartUploads` 7 d, 4 tag-scoped audio-expiry rules |
| chintan-content-dev-staging-<account> | 0 | 0 | – | same |
| chintan-content-dev-<account> (orphan) | **bucket no longer exists** (`NoSuchBucket`) — deleted since this morning's audit | | | |
| chintan-lambda-<account>-us-west-2 | 126 / 693 MB | 0 | STANDARD | `ExpireOldArtifacts` 30 d (+ noncurrent 1 d) — the oldest zips expire from Sep 7 |
| chintan-cloudtrail-<account>-us-west-2 | 24,214 / 47.2 MB (11,306 digests 9.0 MB; 12,908 logs 38.2 MB) | versioning off | STANDARD | `ExpireTrailObjects` 400 d |

The per-append rewrite of `note.md` creates a noncurrent version per capture, and the 7-day rule has already removed every one from August; at daily use at most ~35 noncurrent versions (~5 KB each, ~150 KB) exist at any moment — $0.000004/month. No storage-class change is worth making on 5.6 MB (Intelligent-Tiering's monitoring fee would cost more than it saves; the audio is < 100 KB per object anyway).

### 5.4 SQS (`GetMetricStatistics`, Aug 1 – Sep 4)

| Queue | Empty receives | Messages sent | Messages received |
|---|---|---|---|
| chintan-captures-dev-prod | 560,732 | 42 | 42 |
| chintan-captures-dev-staging | 561,053 | 30 | 6 |
| chintan-captures-dlq-dev-prod | 0 | **0** | 0 |
| chintan-expiry-dlq-dev-prod | 0 | **0** | 0 |

Steady state: **21,590 empty receives per queue per day** (Aug 29 – Sep 2, identical every day) — the Lambda event-source poller's long polls. Two queues = 1.295M requests/month against the 1M always-free allowance → $0.118/month; one queue = 648k → $0. (`GetQueueAttributes` for depth is denied to the agent, but 0 messages ever sent to either DLQ settles the depth question.)

### 5.5 CloudTrail

`chintan-trail` is the **only trail in the account**: multi-region (home us-west-2), global service events on, log-file validation on, management events read+write, no data events, no Insights, logging since Aug 7, latest delivery today 17:51Z. The first copy of management events is free; the bill is the S3 delivery: 24,214 objects in 28 days —

| Prefix | Objects | Per day | Note |
|---|---|---|---|
| CloudTrail/us-west-2 | 11,332 | 405 | real activity (Lambda role assumptions, KMS decrypts, deploys, audits) |
| CloudTrail/us-east-1 | 1,022 | 36 | global services (IAM, STS, Cost Explorer) |
| CloudTrail/us-east-2 | 550 | 20 | AppStream buckets / passbook alarms |
| CloudTrail-Digest/* — **17 regions × 24/day** | 11,306 | 408 | one hourly digest per region whether or not anything happened |

≈ 870 PUTs/day ≈ 26k/month ≈ **$0.13/month**, the largest single line in the account. Storage grows 47 MB/month (≈ $0.001) and the 400-day expiry means ~350k objects will accumulate; harmless but ugly. Options: delete the trail (review §5.5; the deny policy forbids the agent, so the owner does it) → −$0.13; or keep it single-region with validation off (`update-trail --no-is-multi-region-trail --no-enable-log-file-validation`) → −$0.07 (digests + other-region logs); or just turn off validation → −$0.06.

### 5.6 Budgets and SNS

Three account-wide $10 budgets (`Master Budget ` manual; staging's with **0 notifications**; prod's with 2 notifications and one e-mail subscriber). Budgets are free; two are dead weight. The prod alarm topic has one confirmed e-mail subscription.

---

## 6. Recommendations, with the exact change

Ordered by dollars per unit of effort; savings are the same in both scenarios unless stated, because every fixed item is usage-independent and every usage-dependent item is inside a free tier.

### 6.1 Alarms: 10 → 5, no OKActions — saves $0.20/month, 30 minutes

In `infrastructure/template.yaml`: delete `Api5xxRateAlarm`, `ApiFunctionThrottlesAlarm`, `WorkerFunctionErrorsAlarm`, `WorkerFunctionThrottlesAlarm`, `ProviderRateLimitedAlarm`; remove the `OKActions:` block from the remaining five (`ApiFunctionErrorsAlarm`, `CaptureDLQDepthAlarm`, `ExpiryDLQDepthAlarm`, `ProviderKeyRejectedAlarm`, `SpendCapRejectionsAlarm`). Update the README's `enable_alarms` row ("the template declares exactly ten alarms, so one environment fits precisely") and the comment in `config/instances/dev-staging.yaml` to say five, and that the account's other project also holds alarms. Consider raising `ApiFunctionErrorsAlarm` to `Threshold: 2` if the once-a-week 500 from a provider blip should not page. With the expiry Lambda gone (§5.4 of the review) `ExpiryDLQDepthAlarm` goes too → 4.

### 6.2 CloudTrail — saves $0.13/month (delete) or $0.07 (single-region, no digests), 5 minutes, owner

The review already recommends deleting the trail with the rest of the agent-boundary apparatus. If it stays: `aws cloudtrail update-trail --name chintan-trail --no-is-multi-region-trail --no-enable-log-file-validation` and shorten `ExpireTrailObjects` from 400 to 90 days. Either way update `scripts/bootstrap-agent.sh` to match so a fresh bootstrap does not recreate the multi-region digests.

### 6.3 Staging's idle SQS poller — saves $0.12/month

Preferred (review §5.5): delete `chintan-dev-staging` (`scripts/cleanup-aws.sh --instance dev --environment staging --apply` once H14 is fixed), remove `config/instances/dev-staging.yaml`, and let the deploy workflow smoke prod's `/health/ready` before flipping the alias. Minimal alternative that keeps staging: on `CaptureQueueEventSourceMapping` add `Enabled: !If [IsProd, true, false]` (with `IsProd: !Equals [!Ref Environment, 'prod']`) — staging's smoke test never exercises the worker anyway. Longer term, review §5.2 (S3 → Lambda, no SQS) removes the poller entirely, and with it the last SQS request.

### 6.4 Cost Explorer calls — saves $0.09–0.10 in any month someone audits

Rule for scripts and agents: **never call `ce get-cost-and-usage` for a routine check.** `aws budgets describe-budget --account-id <account> --budget-name <prod budget>` returns `ActualSpend` and `ForecastedSpend` free (and the agent role already has it), the Billing console is free, and if line items are needed monthly, a Cost and Usage Report to S3 is free. Note the cost in `docs/cost-analysis.md` and the agent's runbook. This review's two passes cost $0.10 — a third of the account's real monthly bill.

### 6.5 Activate cost-allocation tags — $0, unlocks the per-project view, owner

Billing console → Cost allocation tags → activate `Project`, `Instance`, `Environment` (they are already on every resource). ~24 h; no backfill. Passbook (ca-central-1) and AppStream (us-east-2) then fall out cleanly.

### 6.6 Logs — keep 14 days (or raise to 30); keep access logs

Cutting `LogRetentionDays` 14 → 7 saves < $0.0001/month. Raising to 30 costs < $0.001/month at daily use and would have preserved the August evidence this review could not read. Keep API Gateway access logging (§2.2) unless simplifying; if dropping, remove `AccessLogSettings` from `ApiStage`, `ApiAccessLogGroup`, and the `logs:*` statement in `ChintanStackResources` that covers `/aws/apigateway/chintan-*`.

### 6.7 X-Ray → PassThrough — $0 saved, less noise

`TracingConfig: Mode: PassThrough` on the three functions (template lines 1319, 1367, 1433) and delete the `XRayTracing` inline policy. Re-enable on one function for an afternoon when a latency question comes up; the default sampling rule will trace everything at this volume.

### 6.8 EMF — leave the billing alone; optional hygiene

No cost change is warranted (§3). If the code is touched: emit `ApiRequests` only (drop `ApiLatency`), replace `CaptureStageEntered` with the existing duration/failure metrics, and keep the three rollup metrics the alarms read. Never add a scheduled health-check or a widget that polls the API around the clock — that is the only single-user path to a metric bill ($0.30/month per identity beyond ten that is active every hour).

### 6.9 Things confirmed fine — no action

S3 versioning (0 noncurrent versions, 7-day rule works), PITR ($0.00002/month), DynamoDB on-demand, Lambda sizing, the artifact bucket's 30-day expiry, Cognito Essentials (6 MAU of 10,000), SNS (18 e-mails in a month of 1,000 free). The orphan bucket from this morning's audit is already gone.

### 6.10 Budgets clutter — $0

Make `MonthlyBudget` `Condition: HasAlarmEmail` so staging stops creating a budget nobody hears; delete the hand-made `Master Budget ` (it duplicates prod's).

---

## 7. What the two scenarios cost after the changes

| | Today's config, idle | Today's config, daily use | After 6.1–6.4, idle | After 6.1–6.4, daily use |
|---|---|---|---|---|
| Alarms | 0.20 | 0.20 | 0 | 0 |
| CloudTrail S3 PUTs | 0.13 | 0.13 | 0 | 0 |
| SQS polling | 0.12 | 0.12 | 0 | 0 |
| S3 storage | 0.013 | 0.014 | 0.013 | 0.014 |
| API Gateway (15k req) | 0 | 0.015 | 0 | 0.015 |
| DynamoDB | 0 | 0.010 | 0 | 0.010 |
| S3 requests (1k PUT, few k GET) | 0 | 0.006 | 0 | 0.006 |
| Logs, metrics, X-Ray, Lambda, Cognito, SNS | 0 (free tiers) | 0 (free tiers) | 0 | 0 |
| Cost Explorer calls | 0.01 each | 0.01 each | 0 | 0 |
| Tax (~10%) | 0.05 | 0.05 | 0.001 | 0.005 |
| **AWS total** | **≈ 0.51** (+ CE calls) | **≈ 0.55** (+ CE calls) | **≈ 0.015** | **≈ 0.05** |
| Providers (Groq + MiniMax), outside AWS | 0 | ≈ 0.30–0.70 | 0 | ≈ 0.30–0.70 |

The AWS side of Chintan is a ~$0.50/month fixed-overhead bill with a ~$0.04/month usage component. The overhead is three infrastructure choices (a multi-region trail, a second SQS poller, ten alarms in an account that already had two) plus the cost of looking at the bill through the API. All four are removable without touching a byte of user data.

---

## Appendix — commands (account id redacted; no output containing user content exists)

```
# account session (denied to chintan-agent), read-only
aws cloudwatch describe-alarms --alarm-name-prefix chintan
aws cloudwatch describe-alarm-history --alarm-name <each> --max-records 50
aws cloudwatch list-metrics --namespace Chintan [--recently-active PT3H]
aws cloudwatch get-metric-statistics --namespace AWS/Logs --metric-name IncomingBytes|IncomingLogEvents --dimensions Name=LogGroupName,Value=<group> --period 86400 --statistics Sum
aws cloudwatch get-metric-statistics --namespace AWS/SQS --metric-name NumberOfEmptyReceives|NumberOfMessagesSent|NumberOfMessagesReceived --dimensions Name=QueueName,Value=<queue>
aws cloudwatch get-metric-statistics --namespace AWS/Lambda --metric-name Invocations|Duration --dimensions Name=FunctionName,Value=<fn>
aws cloudwatch get-metric-statistics --namespace AWS/ApiGateway --metric-name Count|5xx|4xx --dimensions Name=ApiId,Value=3kg2xg9khf
aws cloudwatch get-metric-statistics --namespace AWS/S3 --metric-name NumberOfObjects|BucketSizeBytes --dimensions Name=BucketName,Value=chintan-cloudtrail-… Name=StorageType,Value=AllStorageTypes|StandardStorage
aws ce get-cost-and-usage --time-period Start=2026-06-01,End=2026-09-04 --granularity MONTHLY --metrics UnblendedCost --group-by Type=DIMENSION,Key=SERVICE
aws ce get-cost-and-usage --time-period Start=2026-08-01,End=2026-09-01 --granularity MONTHLY --metrics UnblendedCost UsageQuantity --group-by Type=DIMENSION,Key=USAGE_TYPE
aws ce get-cost-and-usage --time-period Start=2026-09-01,End=2026-09-04 --granularity MONTHLY --metrics UnblendedCost UsageQuantity --group-by Type=DIMENSION,Key=USAGE_TYPE
aws ce list-cost-allocation-tags
aws cloudtrail describe-trails --include-shadow-trails; get-trail-status; get-event-selectors
aws budgets describe-budgets / describe-notifications-for-budget
aws xray get-sampling-rules
aws sns list-subscriptions-by-topic

# chintan-agent (--profile chintan)
aws logs describe-log-groups / describe-log-streams / filter-log-events (/aws/lambda/chintan-api-dev-prod, today only)
aws lambda get-function-configuration / get-function-concurrency / list-versions-by-function
aws dynamodb describe-table / describe-continuous-backups
aws s3api list-object-versions / get-bucket-lifecycle-configuration / list-objects-v2 (cloudtrail bucket keys only)
```

Prices used (us-west-2, on-demand, 2026): CloudWatch alarm $0.10/alarm-month (10 free); custom metric $0.30/metric-month prorated hourly (10 free); Logs $0.50/GB ingested (5 GB free), $0.03/GB-month stored; X-Ray $5/million traces (100k free); S3 PUT $0.005/1k, GET $0.0004/1k, $0.023/GB-month; SQS $0.40/million (1M free); Cost Explorer API $0.01/request; Lambda 1M requests + 400k GB-s free; API Gateway HTTP $1.00/million (12-month free tier expired); DynamoDB on-demand $1.25/million WRU, $0.25/million RRU, PITR $0.20/GB-month.
