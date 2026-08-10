# Chintan v2 — AWS cost analysis

Region: **us-west-2**. Date: **2026-08-08**. Scope: one instance stack
(`infrastructure/template.yaml`) plus the shared bootstrap stack
(`infrastructure/bootstrap.yaml`), for a **single user**.

Cost effectiveness is a product requirement that was never verified during the
v2 build. This document closes that gap: it enumerates every cost-bearing
resource, prices each against the live AWS price list, models three usage
scenarios, and ranks the available reductions.

---

## Decisions left to the owner

These are **not** implemented. Each is a real trade-off, not an oversight.

| # | Change | Saves | What you lose |
|---|---|---|---|
| 1 | ~~`TokenVaultKey: EnableKeyRotation: false`~~ — **taken**, and superseded | **$1.00/mo, the whole idle bill** | The customer-managed KMS key is gone entirely; the refresh-token vault is now AES-256-GCM under an SSM SecureString. Rotation is no longer a $1/month/version question. See [§5.1](#51-kms-key-rotation--taken-and-superseded). |
| 2 | Worker `MemorySize: 2048` → `512` | **$0.00 today** | Nothing measurable. The saving is genuinely zero because Lambda stays inside the free tier in every modelled scenario. See [§5.2](#52-worker-memory--tradeoff). |
| 3 | Replace the `SpendCapRejections` Metrics Insights alarm with a plain alarm | $0.30/mo **only when `DailySpendCapMicros ≠ 0`** | Nothing, if a dimensionless companion metric is emitted. Costs a code change. See [§5.3](#53-spend-cap-alarm--tradeoff). |
| 4 | Drop `Status` from the `ApiLatency` dimension set | $0.00 today | Latency broken down by status class. Reduces the ceiling by 90 metric identities but needs a second `Emit` call. See [§5.4](#54-apilatency-dimensions--tradeoff). |

**Not recommended:** turning off `AdvancedSecurityMode: ENFORCED`. It costs
**$0.02/month** for one user. See [§5.5](#55-cognito-advanced-security--do-not-cut).

---

## 1. Headline

> **Idle monthly cost: ~$0.09**, and it was measured, not derived.
> It was $1.00, all of it one customer-managed KMS key. That key has been
> removed: the refresh-token vault is now sealed with AES-256-GCM under a key
> held as an SSM SecureString, which is encrypted by the AWS-managed `aws/ssm`
> key and is free. Lambda, DynamoDB, S3 storage, SQS, API Gateway, the topic and
> the budget really are **$0.00** idle.

### What this section claimed, and what the bill said

This section read **"Idle monthly cost: $0.00"** and was wrong. Two days of a
genuinely unused deployment billed $0.019, and the run rate hid two charges that
had not finished accruing:

| Driver | Measured | Why it was missed |
|---|---|---|
| `CW:MetricInsightAlarmUsage` | $0.0040/day ≈ **$0.12/mo** | The two spend-cap alarms were Metrics Insights queries, which have **no free tier**. The analysis counted alarms as free without separating the two billing forms. |
| `CW:AlarmMonitorUsage` | **$0.00 then, ~$1.00/mo projected** | CloudWatch's always-free allowance is **ten alarm-months**. This template declares ten alarms, so prod alone fits *exactly* — and staging's identical ten fall entirely outside it. On a young month only 1.68 alarm-months had accrued, so the meter read zero and looked settled. |
| S3 `PutObject` | $0.0028/day ≈ **$0.09/mo** | CloudTrail delivering log files. Genuinely unavoidable while the trail exists, and largely proportional to API activity rather than to app usage. |

Both CloudWatch charges are now fixed rather than documented:

- `SpendCapRejections` emits through `obs.CountWithRollup`, so its alarm is an
  ordinary standard-resolution alarm like `ProviderKeyRejected` and
  `ProviderRateLimited` always were. No Metrics Insights alarm remains.
- Alarms are behind the `EnableAlarms` parameter and **off in staging**
  (`config/instances/dev-staging.yaml`), which puts the alarm count back at ten
  — the exact free allowance. Staging is still gated by its deploy smoke test.

**The lesson worth keeping:** a free tier that is not yet exhausted reads
identically to a cost that does not exist. Anything counted as "free" here needs
the allowance written next to it and the projected full-month consumption
compared against it — not a spot reading from a partial month.

Light and heavy use barely move it, because AWS's always-free tiers absorb
essentially all of a single user's consumption:

| Scenario | AWS/month | Third-party/month | Total |
|---|---|---|---|
| **Idle** (0 captures) | **~$0.09** | $0.00 | **~$0.09** |
| **Light** (30 captures × 2 min) | **$0.13** | $0.69 | **$0.82** |
| **Heavy** (300 captures × 5 min) | **$0.25** | $16.80 | **$17.05** |

The idle figure is CloudTrail's log delivery and nothing else. The use rows add
$0.09 to their previous values for the same reason: the earlier table treated
CloudTrail as free because its *management events* are, which is true, and its S3
writes are not.

### The one that scales with use: custom metrics

Idle cost is settled. The charge that actually threatens the free tier appears
only once the app is *used*, which is why two days of idle billing said nothing
about it.

CloudWatch bills **$0.30 per custom metric per month**, prorated hourly, with an
always-free allowance of **ten**. A metric's identity is its name *plus its
dimension set*, so a dimension multiplies metrics — it does not annotate one.

`ApiRequests` and `ApiLatency` were dimensioned by `Route` × `Status`. Measured
on the live instance after a single evening: **44 identities in the `Chintan`
namespace, 28 of them these two metrics** — and only twelve of twenty-eight
routes had been exercised. Fully exercised that is 28 routes × ~4 status classes
× 2 metrics ≈ **224 identities against an allowance of ten**, growing by four
more every time a route is added.

`Route` is no longer a dimension. Nothing is lost: `obs/middleware.go` already
logs `route`, `status` and `duration_ms` on every request, so latency by route is
a Logs Insights query over data that is already being written — and one that can
group by exact status, which the metric never could.

That leaves roughly **24 identities**, and — the point — a number bounded by the
dimension values rather than by the size of the API:

| Metric | Dimensions | Identities |
|---|---|---|
| `ApiRequests`, `ApiLatency` | `Status` | ~8 |
| `ProviderCalls`, `ProviderCostMicros`, `ProviderLatency` | `Provider`, `Op` | ~9 |
| `CaptureStageEntered` | `Stage` | 4 |
| `RateLimited` | `Route` — but only two routes take a limiter | 2 |
| `ProviderKeyRejected`, `ProviderRateLimited`, `SpendCapRejections` | dimensioned + rollup | on failure only |
| `CapturesCreated`, `CapturePipelineDuration`, `RouterContentDiscarded` | none | 3 |

**Twenty-four is still more than ten**, and pretending otherwise is how the
previous version of this document went wrong. What saves it is hourly proration:
the bill is `identities × hours-with-data / 730`, so ten metric-months is reached
at roughly **300 active hours a month — about ten hours a day** of traffic. A
single user dictating notes is far below that. Before the change the same
threshold was ~5.5 hours a day and heading toward half an hour.

If it ever does get close, the next cut is `ApiLatency`: nothing alarms on it and
`duration_ms` is already in the request log.

The README's claim of **$1–10/month** is *numerically* right at the bottom of
its range and *structurally* wrong. It says "nearly all of the cost is
per-recording" and itemises Lambda $0.20–2, DynamoDB $0.25–1, S3 $0.50–5.
In reality **Lambda, DynamoDB and S3 are all $0.00** at this scale. The entire
idle bill used to be one fixed charge the README did not mention at all — the
vault CMK — and removing it takes the idle figure to zero.

At heavy use the AWS bill is **6% of total spend**. The providers are the cost
story; the infrastructure is not.

### The second index, and what it costs

`gsi2` orders notes by `updated_at` so the list can answer "most recently
touched", which the base table cannot: its sort key is `NOTE#<id>` and a note id
leads with its **creation** instant. It is billed like any GSI — storage for the
projected attributes, plus a write unit on every note write that changes an
indexed attribute.

**Idle cost: $0.00, unchanged.** Concretely, on the measured footprint:

- **Storage.** The projection is `INCLUDE`, deliberately without the `data`
  blob, so an indexed note is its promoted attributes only — roughly 600 bytes
  for a note with a snippet. The whole table is **27,405 bytes** across 39
  items; the index adds well under 50 KB against a **25 GB** free allowance.
- **Writes.** One extra WRU per note write. A note is written on create, on
  edit, and once per capture appended to it — so the heavy scenario's 300
  captures add ~300 index writes, or **$0.0002** at $0.625/M WRU, inside the
  free tier either way.
- **Reads get cheaper, not dearer.** The active list previously ran a filtered
  base-table query, and DynamoDB applies `Limit` **before** `FilterExpression`,
  so a page of active notes could cost several round trips through archived
  ones. The shelf is now part of the partition key, so each page is one query
  over exactly the items it wants and the retry loop only runs for the archived
  list.

The one real cost is operational rather than monetary, and it is not on the rate
card: **adding a GSI does not index the rows already in the table.** DynamoDB
backfills a new index only from items that already carry its key attributes, so
every note written before `gsi2` existed is absent from it, and the notes list
reads empty until each is rewritten. `chintanctl reindex` is that rewrite and is
part of the same change; it must be run once per instance immediately after the
stack update.

### The custom-metric hypothesis: **refuted**

The brief suspected custom metrics were the single largest recurring cost and
"entirely self-inflicted". The metric surface *is* large and self-inflicted —
**20 metric names expanding to a ceiling of 317 billable metric identities**
([§3](#3-the-custom-metric-surface)) — but it is **not** the largest cost, for
two reasons that only show up when you read the billing rules rather than the
rate card:

1. **Custom metrics are prorated hourly.** From the CloudWatch pricing page:
   *"All custom metrics and Detailed Monitoring charges are prorated by the hour
   and charges are incurred only when metrics are sent to CloudWatch in a given
   hour."* A metric identity that receives data in 40 hours of a 730-hour month
   costs 40/730 × $0.30 = **$0.016**, not $0.30.
2. **The free tier is 10 metric-months**, i.e. $3.00/month of allowance. A
   single user active ~40 hours/month touching ~12 identities per active hour
   consumes **0.66 metric-months** — 7% of the allowance.

So the surface is a **latent** risk, not a present cost. It only becomes the
dominant line item if the app acquires continuous traffic. Concretely: if every
one of the 317 identities received data every hour, the bill would be
**$95.10/month** — nearly ten times the entire budget. The gap between $0.00
and $95.10 is traffic pattern, not configuration.

Two things currently keep it at $0.00, and both are worth knowing about because
either could be undone by accident:

- **Frontend polling is bounded.** `frontend/src/api/queries.ts` stops polling
  after `CAPTURE_POLL_MAX_MS` (10 minutes) and backs off 2s → 20s. A tab left
  open does *not* generate hourly traffic. If that ever changes to an unbounded
  poll, metrics start billing in all 730 hours and this analysis inverts.
- **The free tier absorbs the rest.** It is shared account-wide, and this
  account also runs the `passbook-*` stacks. A second Chintan instance
  (staging) roughly doubles metric-hours *and* pushes alarms past the free 10.

---

## 2. Every cost-bearing resource

Verified against the deployed account (`338186951935`) where possible.

### 2.1 `infrastructure/template.yaml`

| Resource | Bills on | Idle cost |
|---|---|---|
| *(removed)* `TokenVaultKey` (KMS CMK) | was **$1/key-version-month**, no free tier | **$0.00** |
| `/chintan/<instance>/token_vault_key` (SSM SecureString) | Standard parameters and their `aws/ssm` decrypts are free | $0.00 |
| `UserPool` + `AdvancedSecurityMode: ENFORCED` | → **Plus tier**, $0.020/MAU, **zero free MAU** | $0.00 (0 MAU) |
| `UserPoolDomain` | no SKU exists in the price list | $0.00 |
| `DynamoDBTable` (on-demand) | $0.25/GB-mo, first 25 GB free; measured **27,405 bytes** | $0.00 |
| ↳ `PointInTimeRecoverySpecification` | $0.20/GB-mo, no minimum; 27 KB → $0.0000054 | $0.00 |
| ↳ `gsi1` GSI | GSI storage + RRU/WRU, same free tier | $0.00 |
| ↳ `gsi2` GSI (notes by update time) | same; adds a second index write per note write and ~1 KB per note of projected storage | $0.00 |
| ↳ `StreamSpecification: NEW_AND_OLD_IMAGES` | $0.02/100,000 stream read request units — but **GetRecords calls made by a Lambda trigger are not billed**, and an idle table writes no records at all | $0.00 |
| `ContentBucket` | $0.023/GB-mo; measured **5.9 MB** | $0.00 |
| ↳ `VersioningConfiguration: Enabled` | noncurrent versions billed as storage | $0.00 |
| `CaptureQueue` + `CaptureDLQ` + `ExpiryDLQ` | $0.40/M requests, first 1M free; idle queues are free | $0.00 |
| `ApiLambdaFunction` (512 MB, arm64) | $0.0000133334/GB-s, 400,000 GB-s free | $0.00 |
| `WorkerLambdaFunction` (2048 MB, arm64) | same | $0.00 |
| `ExpiryLambdaFunction` (512 MB, arm64) | same; invoked only when TTL expires a record, which on an idle instance is never | $0.00 |
| ↳ `ReservedConcurrentExecutions: 5` | **free** — only *provisioned* concurrency bills | $0.00 |
| ↳ `Version` / `Alias` ×3 | code storage $0.0000000309/GB-s | ~$0.00 |
| ↳ `TracingConfig: Mode: Active` (X-Ray) | $5.00/M traces, **100,000 free** | $0.00 |
| `HttpApi` | $1.00/M requests, **no fixed charge** | $0.00 |
| `ApiStage` → `DetailedMetricsEnabled: true` | **billed as custom metrics**, ~36 identities | $0.00 idle |
| `ApiLogGroup` / `WorkerLogGroup` / `ExpiryLogGroup` | $0.50/GB ingest, $0.03/GB-mo storage, 5 GB free | $0.00 |
| `ApiAccessLogGroup` | **vended logs**, same $0.50/GB at this volume | $0.00 |
| 9 × `AWS::CloudWatch::Alarm` (standard) | $0.10/alarm-month, **10 free** | $0.00 — but see the note below; this is now the tightest allowance in the stack |
| `SpendCapRejectionsAlarm` (Metrics Insights) | $0.10 **per metric analyzed**, **no free tier** | **not deployed** — `HasSpendCap` is false at the default `DailySpendCapMicros: 0` |
| `AlarmTopic` + subscription | $0.50/M requests, 1M free; idle topic free | $0.00 |
| `MonthlyBudget` | **budget monitoring is free**; only *action-enabled* budgets bill | $0.00 |
| EMF custom metrics (namespace `Chintan`) | $0.30/metric-month, hourly prorated, 10 free | $0.00 idle |

**The alarm allowance is now the thing to watch.** The stack went from 6
standard alarms to 9 — `chintan-…-expiry-dlq`, `chintan-…-provider-key-rejected`
and `chintan-…-provider-rate-limited`. Nine is still inside the free ten, so the
idle figure does not move, but the headroom is one alarm rather than four, and
the free tier is **account-wide**. Two consequences follow, and neither is
hypothetical on this account:

- A **staging instance** deploys the same nine alarms. Two instances is 18
  alarms, of which 8 are billable: **$0.80/month**. That was $0.20/month at six
  alarms each, so the second instance now costs four times what it did.
- Setting `DailySpendCapMicros` adds `SpendCapRejectionsAlarm`, which is a
  Metrics Insights query alarm and takes **no** part of the free ten (§5.3).

The cheapest way to buy the headroom back, if it is ever wanted, is §5.3: give
`SpendCapRejections` the same dimensionless rollup these two new metrics carry
and demote its alarm to a plain one. That is a saving rather than a cost, and
the code it needs now exists — `obs.EmitWithRollup`.

### 2.2 `infrastructure/bootstrap.yaml`

| Resource | Idle cost |
|---|---|
| Artifact S3 bucket (versioned, lifecycle-expired) — measured **136 MB** | $0.0031 |
| `CfnDeployRole`, agent role (IAM) | $0.00 — IAM is free |

### 2.3 Outside both templates

Found in the account, not declared by either template:

| Resource | Idle cost |
|---|---|
| `chintan-trail` — multi-region CloudTrail, management events only, no data events | $0.00 — the first copy of management events is free per account |
| `chintan-cloudtrail-…` bucket — measured **1.4 MB** | $0.00 |

No NAT gateways, VPCs, load balancers, or provisioned capacity of any kind
exist in this architecture. There is no data-transfer-out charge worth
modelling: S3 egress to the internet is the only path, and it is far under the
100 GB/month free allowance.

---

## 3. The custom-metric surface

CloudWatch bills **per unique metric name + dimension-set value combination**.
From the pricing page: *"CloudWatch treats each unique combination of dimensions
as a separate metric, even if the metrics have the same metric name."*

EMF is billed on **three** dimensions simultaneously — *"Embedded metrics
generate costs by the number of logs ingested, number of logs archived, and
number of custom metrics generated"* — so each identity costs ingest **and**
storage **and** $0.30/metric-month.

**20 metric names → 317 identity ceiling.** Every emitter, enumerated:

| Metric name | Dimensions | Value domain | Ceiling |
|---|---|---|---|
| `ApiRequests` | Route, Status | 30 routes × 4 status classes | 120 |
| `ApiLatency` | Route, Status | same | 120 |
| `CaptureStageFailures` | Stage | 12 statuses ∪ 3 stage strings | 15 |
| `CapturePipelineDuration` | Outcome | 12 capture statuses | 12 |
| `DuplicateDelivery` | Status | 12 capture statuses | 12 |
| `ProviderLatency` | Provider, Op, Outcome | 3 pairs × {ok, error} | 6 |
| `CaptureStageEntered` | Stage | transcribing, routing, cleaning, appending | 4 |
| `RouterContentDiscarded` | Reason | missing_field, empty_content, not_derived | 3 |
| `CaptureSpendCapped` | Stage | transcribe, route, cleanup | 3 |
| `SpendCapRejections` | Provider, Op | groq/transcribe, openai/route, openai/cleanup | 3 |
| `ProviderCostMicros` | Provider, Op | same 3 pairs | 3 |
| `ProviderCalls` | Provider, Op | same 3 pairs | 3 |
| **`ProviderKeyRejected`** | **Provider, and a dimensionless rollup** | **groq, openai, ∅** | **3** |
| **`ProviderRateLimited`** | **Provider, and a dimensionless rollup** | **groq, openai, ∅** | **3** |
| `RateLimited` | Route | the 2 `perIP()` webauthn routes | 2 |
| `CapturesCreated` | Stage | `created` | 1 |
| `WorkerMessagesDiscarded` | Reason | `unparseable` | 1 |
| `CaptureRejectedOversize` | Stage | `uploaded` | 1 |
| `AppendResumedWithoutRewriting` | Stage | `appending` | 1 |
| `SpendCapLookupFailures` | Reason | `resolver_error` | 1 |
| | | **Total ceiling** | **317** |

Two corrections to the inventory above, found while adding the two new rows and
worth stating rather than quietly folding in. `ApiServerErrors` (30) and
`CapturePipelineOutcomes` (4) were listed as emitters but were removed by S2 and
S3 in the same commit this document first described, so the table was counting
34 identities that no longer exist. Three emitters were missing from it:
`CaptureRejectedOversize`, `AppendResumedWithoutRewriting` and
`SpendCapLookupFailures`, one identity each. Net of those and of the six added
below, the ceiling moves **342 → 317** and the name count **17 → 20**.

### The two new metrics, and why each costs three identities rather than two

`ProviderKeyRejected` and `ProviderRateLimited` are emitted by
`pipeline.handleProviderError` when a provider answers 401/403 or 429. Each is
published under **two** dimension sets in one EMF record — `["Provider"]` and
the empty set — which is `obs.EmitWithRollup`.

The empty set is not redundancy, it is what makes the alarms cheap. A
CloudWatch alarm must name a dimension set, and a metric dimensioned only by
`Provider` has none an alarm can name without naming every provider value —
the exact problem §5.3 records for `SpendCapRejections`, which is why that one
is a Metrics Insights query alarm at **$0.10 per metric analysed with no free
tier**. The dimensionless rollup costs **one metric identity, inside the free
ten**, and lets both new alarms be plain standard-resolution alarms at
**$0.00**. Taking the same trade for `SpendCapRejections` would remove its
$0.30/month; that is still §5.3's call to make, not this commit's.

`Provider` is the only dimension on purpose. Adding `Op` would triple both
ceilings to answer a question nobody asks: a revoked key is revoked for every
op that uses it, and `ProviderLatency` already carries the Op breakdown.

Plus **~36** more from `DetailedMetricsEnabled: true` on the API stage
(6 HTTP API metrics × ~6 method+resource combinations), which are billed at the
same custom-metric rate.

**Realistically touched** by one user in a busy month: **~53** identities
(~18 route/status pairs × 2 metrics, plus the ~17 pipeline and provider
identities on the happy path).

The cardinality discipline in `obs.Emit`'s doc comment and in
`handler/metrics.go`'s `routeOf` is genuinely good — no tenant id, capture id,
or raw path ever reaches a dimension, and status is bucketed to a class rather
than an exact code. Without that the ceiling would be unbounded rather than 317.
The remaining problem is not cardinality but **redundancy**, addressed in §5.

---

## 4. Three scenarios

Modelling assumptions: single user; measured storage as of 2026-08-08; worker
wall-clock dominated by provider round-trips (~30 s for a 2-min capture, ~70 s
for a 5-min one); user active in ~40 distinct hours/month (light) and ~150
(heavy); audio ~1 MB/min.

| Line item | Idle | Light (30 × 2 min) | Heavy (300 × 5 min) |
|---|---|---|---|
| **KMS** — no customer-managed key | **$0.000** | **$0.000** | **$0.000** |
| KMS requests via `aws/ssm` (20,000/mo free) | $0.000 | $0.000 | $0.000 |
| **SSM** Standard parameters (free, no SKU) | $0.000 | $0.000 | $0.000 |
| **Cognito** — Plus, $0.020/MAU, no free tier | $0.000 | $0.020 | $0.020 |
| **Lambda** — 400,000 GB-s + 1M req free | $0.000 | $0.000 | $0.000 |
| ↳ *(GB-s consumed)* | *0* | *~1,860* | *~42,000* |
| **API Gateway** HTTP @ $1.00/M | $0.000 | $0.001 | $0.010 |
| **DynamoDB** RRU/WRU + storage | $0.000 | $0.002 | $0.019 |
| ↳ PITR @ $0.20/GB-mo | $0.000 | $0.000 | $0.000 |
| **S3** storage @ $0.023/GB-mo | $0.003 | $0.005 | $0.038 |
| ↳ requests (PUT $0.005/1k, GET $0.004/10k) | $0.000 | $0.009 | $0.087 |
| **SQS** (1M req free) | $0.000 | $0.000 | $0.000 |
| **SNS** (1M req free) | $0.000 | $0.000 | $0.000 |
| **X-Ray** (100,000 traces free) | $0.000 | $0.000 | $0.000 |
| **CloudWatch** alarms — 9 standard, 10 free | $0.000 | $0.000 | $0.000 |
| **DynamoDB Streams** — Lambda-trigger GetRecords not billed | $0.000 | $0.000 | $0.000 |
| **CloudWatch** custom metrics (10 free, hourly) | $0.000 | $0.000 | $0.000 |
| ↳ *(metric-months consumed of 10)* | *0* | *~0.7* | *~4.1* |
| **CloudWatch** logs ingest + storage (5 GB free) | $0.000 | $0.000 | $0.000 |
| **Budgets** — monitoring free | $0.000 | $0.000 | $0.000 |
| **CloudTrail** — 1st mgmt-event copy free | $0.000 | $0.000 | $0.000 |
| **AWS total** | **$0.00** | **$0.04** | **$0.16** |

### Third-party spend

Modelled with `internal/meter`'s `DefaultPrices`
(`groq/*`: 2 µ$/audio-second; `openai/*`: 1 µ$/input token, 4 µ$/output token):

| Provider | Light | Heavy |
|---|---|---|
| Groq STT — 60 / 1,500 audio-minutes @ $0.0072/min | $0.43 | $10.80 |
| LLM route + cleanup — ~4.75k / ~11k tokens per capture | $0.26 | $6.00 |
| **Third-party total** | **$0.69** | **$16.80** |

**Do these estimates look right? No — they are conservative by roughly 3–10×.**

- `groq/*` at 2 µ$/second is **$0.43/audio-hour**. Groq's published Whisper
  pricing is roughly $0.04–0.11 per audio-hour depending on model, so the table
  overstates STT by **~4–10×**.
- `openai/*` at $1/$4 per Mtok is priced as an OpenAI-class model, but the
  deployed configuration is `LLM_BASE_URL: https://api.minimax.io/v1`,
  `LLM_MODEL: MiniMax-M3` — MiniMax list pricing is several times cheaper. The
  provider key is still `"openai"` (set in `pipeline.go`), so the row matches
  and nothing is mispriced *mechanically*; the number is just high.

Erring high is the correct direction for a spend **breaker** — it trips early
rather than late, and `DefaultPrices`' own doc comment already says these are
"estimates for budgeting, not billing" and expects a per-instance override. But
two consequences should be understood:

1. `DailySpendCapMicros` will bind at roughly a third to a tenth of the real
   dollar spend the owner thinks they are authorising.
2. The `ProviderCostMicros` metric — and anything built on it — overstates
   actual cost by the same factor.

Neither is a defect. Both are worth a one-line correction once a real invoice
exists.

---

## 5. Reductions, ranked by saving-per-regret

### Implemented (SAFE)

These are in the accompanying commit. Together they remove **up to 66 of the
342 metric identities plus all ~36 detailed-metric identities** — about 27% of
the ceiling — for no loss of information that is not derivable from what
remains. Present-day dollar saving is **$0.00**, because all of it currently
sits inside the free tier. The value is **headroom**: it is what stops a future
traffic change from turning a $0 line item into a $100 one.

#### S1. `DetailedMetricsEnabled: true` → `false` — **SAFE**

Removes ~36 billable custom-metric identities.

*What is lost:* per-route, per-method gateway metrics. In this architecture that
is nearly nothing, because **API Gateway only sees 5 routes** — `GET /v1/health`,
`OPTIONS /{proxy+}`, the two WebAuthn login routes, and `$default`. All 25
other application routes arrive as `$default`, since routing happens inside the
Lambda. The per-route breakdown you are paying for is therefore mostly a single
bucket labelled `$default`, while the EMF `ApiRequests` / `ApiLatency` metrics
already carry the true registered-route breakdown for free.

*Verified safe:* `Api5xxRateAlarm` uses `Namespace: AWS/ApiGateway`,
`MetricName: 5xx`, `Dimensions: [ApiId]`. Per the API Gateway docs, the `ApiId`
and `ApiId, Stage` dimension sets are published by default at no charge; only
`ApiId, Method, Resource, Stage` requires detailed metrics and *"will incur
additional charges"*. The alarm keeps receiving datapoints.

#### S2. Drop `ApiServerErrors` — **SAFE**

Removes up to 30 identities.

*What is lost:* nothing. It carries the **identical dimension set** to
`ApiRequests` and is emitted only when `status >= 500` — it is exactly
`ApiRequests` filtered to `Status="5xx"`, which CloudWatch can do at query time
at no cost. No alarm, test, dashboard, or document references it.

#### S3. Drop `CapturePipelineOutcomes` — **SAFE**

Removes up to 4 identities.

*What is lost:* almost nothing. `CapturePipelineDuration` is emitted on the same
code path with the same `Outcome` dimension, so its **SampleCount per outcome**
already counts pipeline completions. The one distinction that disappears is that
`Duration` is emitted before the error branch and so also counts errored and
conceded runs, whereas `Outcomes` counted only clean completions. That
difference is recoverable from `CaptureStageFailures` and `DuplicateDelivery`,
both of which are kept. No alarm, test, or document references it.

#### S4. Drop the `Method` dimension from the API metrics — **SAFE**

Removes 0 identities, shrinks every EMF record.

*What is lost:* nothing whatsoever. The route pattern registered by
`rt.handle` **already begins with the method** — `"GET /v1/notes"`,
`"POST /v1/captures"`. `Method` is functionally determined by `Route`, so it
never multiplied the identity count; it only added bytes to every metric log
line and implied a breakdown that does not exist. This is a correctness tidy-up
with a rounding-error cost benefit, listed for completeness.

### Left for the owner (TRADEOFF)

#### 5.1 KMS key rotation — TAKEN, and superseded

This was the largest single lever in the analysis, and the answer turned out not
to be "how often do we rotate" but "why is there a customer-managed key at all".

KMS bills **per key *version***, not per key: *"$1 per customer managed KMS key
version in US West (Oregon)"*, with rotation adding a further $1/month for each
of the first two rotations and capping there. On a single-user instance whose
entire idle bill was that one key, the schedule ran $1.00 → $2.00 → $3.00/month.

**What the CMK actually bought.** Not encryption at rest: DynamoDB already
encrypts the table with an AWS-owned key, free. It bought *separation* — a
principal that can read the table still cannot use a refresh token without a
distinct `kms:Decrypt`. That is real, though narrower than it looks, because the
Lambda role holds both `dynamodb:GetItem` and the decrypt right, so compromising
that role defeats it either way. What it genuinely protects is a `chintanctl
backup`, a PITR restore, and someone reading the table in the console.

**What replaced it.** A 32-byte key in an SSM SecureString, with the token
sealed by AES-256-GCM. The separation is preserved exactly: reading the vault
needs `ssm:GetParameter` on one path plus `kms:Decrypt` on `alias/aws/ssm`, and
neither of those comes with `dynamodb:GetItem`. A SecureString is encrypted
under the AWS-managed `aws/ssm` key, and only *customer-managed* keys carry the
$1/month/version charge — so the same property now costs nothing.

**What it cost to do.** Rotation is no longer automatic; it is `put-parameter`
at the same path, and every sealed blob records the parameter version that
sealed it so an old entry is identifiable rather than indistinguishable from
corruption. And the migration is a one-way break: blobs sealed by the CMK
cannot be opened by the new box. The service detects exactly that case, discards
the entry, and asks the user to enrol again — once.

*Historic note, left because the reasoning is still the right shape for the next
version of this question:* disabling a security control to save money is the
change to slip into a cost-reduction commit. If the answer is "the tokens roll
over weekly, rotation buys me nothing", the saving is real and immediate.

#### 5.2 Worker memory — TRADEOFF

`MemorySize: 2048` is **provably oversized**, and the saving from fixing it is
**provably zero**. Both halves matter.

*Oversized:* the brief's premise is correct and the code confirms it.
`provider/groq_stt.go` states *"Transcribe streams the recording into a
multipart upload without ever holding [it] … through an io.Pipe, so peak
allocation is one copy buffer regardless of how long the recording is"*, and
`pipeline.go` passes a **presigned GET URL** rather than bytes, with the comment
*"v1 pulled the whole object into the Lambda heap and re-POSTed it, which made
the heap — rather than the microphone — the real cap"*. The worker's live heap
is a transcript and some JSON: single-digit MB. 2 GB is ~100× the working set.

*Zero saving:* Lambda cost is memory × duration, so 2048 → 512 is a 4× cut on
GB-seconds. But the heavy scenario consumes **~42,000 GB-s against a 400,000
GB-s perpetual free tier** — the function is 10× inside the free allowance, so
4× less of $0.00 is still $0.00. Cutting memory saves nothing until captures
increase roughly tenfold.

*The risk is small but real:* Lambda scales vCPU with memory (~1 vCPU at
1,769 MB). This worker is I/O-bound — its wall clock is Groq and LLM round-trips,
which do not care about local CPU — so less memory should not lengthen it
meaningfully. "Should not" is not "measured".

**Recommendation: leave it, or drop to 1024 if it offends.** There is no cost
argument for touching it today, and a change with nonzero risk and zero reward
is a bad trade. Revisit if usage grows an order of magnitude.

#### 5.3 Spend-cap alarm — TRADEOFF

`SpendCapRejectionsAlarm` is a **Metrics Insights query alarm**, and those are
priced differently from the alarms around it in two ways that both cut against
it:

- **$0.10 per metric *analyzed by the query*, per month** — not $0.10 per alarm.
  `SELECT SUM(SpendCapRejections) FROM SCHEMA("Chintan", Provider, Op)` matches
  3 Provider×Op combinations today, so **$0.30/month**, and it grows by $0.10
  every time a provider or op is added.
- **The 10-alarm free tier does not apply.** The CloudWatch pricing page limits
  it to *"Standard resolution alarms that list metrics directly and don't use a
  Metrics Insights query"*. This alarm bills from the first metric.

**Today this costs $0.00**, because the alarm is behind `Condition: HasSpendCap`
and the default `DailySpendCapMicros` is `0`. It only starts billing when the
owner sets a real cap — which is exactly when they are trying to save money.

The template's comment explaining *why* it is a Metrics Insights alarm is
correct and worth preserving: `obs.Emit` declares one dimension set
`["Provider","Op"]`, so no dimensionless rollup of `SpendCapRejections` exists
to alarm on, and a no-dimension alarm would sit permanently `OK`.

*The fix, if wanted:* emit a second, dimensionless `SpendCapRejectionsTotal`
alongside the dimensioned metric in `breaker.go`, and point a plain
standard-resolution alarm at it. That costs 1 extra metric identity (~$0.00,
inside the free tier) and $0.00 in alarm charges instead of $0.30. It is left
out of this commit because it changes metric emission *and* alarm semantics for
a saving that is zero under the default configuration.

#### 5.4 `ApiLatency` dimensions — TRADEOFF

`ApiRequests` and `ApiLatency` share one `Emit` call and therefore one dimension
set, `{Route, Status}` after S4. Latency broken down by status class is largely
noise — you want p99 per route, and a 4xx's latency is rarely interesting.
Dropping `Status` from `ApiLatency` alone would cut its ceiling from 120 to 30,
removing **90 identities** — by far the largest remaining reduction available.

*Why it is not implemented:* the two metrics currently ride in one EMF record,
which is efficient and simple. Splitting them means two `Emit` calls, two log
lines per request, and roughly double the EMF ingest bytes — trading log volume
for metric identities. Since both are $0.00 today, the trade is not obviously
worth making, and it is a design decision about what the owner wants to be able
to ask CloudWatch later.

### Do not cut

| Item | Why |
|---|---|
| **DynamoDB PITR** | Costs **$0.0000054/month** on a 27 KB table. Removing it saves nothing measurable and gives up 35-day point-in-time restore for every note and capture. There is no cost argument here at all. |
| **S3 versioning** | Costs ~$0.00 on 5.9 MB, and the lifecycle rules (`ExpireDeleteMarkers`, `NoncurrentVersionExpiration`, `AbortStaleMultipartUploads`) already stop noncurrent versions accumulating. It is the only protection against an overwrite or a bad delete. |
| **X-Ray active tracing** | 100,000 traces/month free; heavy use is ~15,000. Costs **$0.00** and there is no cheaper setting than free. Reducing the sampling rate would save nothing. |
| **`LogRetentionDays: 14`** | Already explicit and already short — the template comment notes it is set *"so groups never default to never-expire"*, which is the expensive failure mode. Storage is 47 KB. Nothing to win. |
| **API access logs** | Vended logs at $0.50/GB with 5 GB free; actual volume is ~1 MB/month. The format is already minimised to route key and status with an explicit "nothing here may carry transcript content" constraint. They are the only record of who called what. |
| **The 9 standard alarms** | $0.10 each with **10 free** → $0.00. One alarm of headroom remains; see §2.1. |
| **Consolidating alarms** | Would **increase** cost. A composite alarm is $0.50/month and a Metrics Insights alarm is $0.10/metric with **no** free tier, versus $0.10/alarm **inside a free 10** today. The current design of plain, directly-listed metric alarms is the cheapest option available. |
| **`DuplicateDelivery`** | Closes a real defect — commit `88b2402` *"Let a duplicate delivery bow out, instead of dead-lettering itself"* — and `TestConcedingEmitsADuplicateDeliveryCounter` asserts it is emitted. It is the evidence the fix works. Up to 12 identities, $0.00. |
| **`ReservedConcurrentExecutions: 5`** | Reserved concurrency is **free**; only *provisioned* concurrency bills ($0.0000077778/GB-s ARM). It is also the blast-radius cap on a runaway loop, which is itself a cost control. |

### 5.5 Cognito advanced security — DO NOT CUT

The brief asked whether `AdvancedSecurityMode: ENFORCED` is worth its tier cost.
**It costs $0.02/month. Keep it.**

The mechanics are worth stating precisely, because the direction of the answer
is right for a surprising reason:

- `AdvancedSecurityMode: ENFORCED` maps to the **Plus** feature tier
  ($0.020/MAU), not Essentials ($0.015/MAU) or Lite ($0.0055/MAU).
- **Plus has zero free MAU.** Lite and Essentials include 10,000 free MAU/month;
  the pricing page says flatly *"There is no free tier for the Plus tier."*
  So enabling it does move the pool off the free tier **entirely** — the concern
  in the brief is factually correct.
- But "off the free tier" for **one user** means **$0.020/month**. Falling back
  to Essentials saves **two cents** and gives up compromised-credential
  detection and risk-based adaptive authentication.

*The scaling cliff worth knowing:* because Plus has no allowance, its cost is
purely linear from user one. At 1,000 MAU, Plus is $20.00/month where Essentials
is still $0.00. For this single-user app that is irrelevant; for a clone that
grows, it is the single steepest line in the model.

*Aside, verified:* the deployed pool `us-west-2_mhEsaNtml` currently reports
`UserPoolTier: ESSENTIALS` with `UserPoolAddOns: None` — the v2 template's
`ENFORCED` setting has not been deployed yet, so this is a prospective $0.02,
not a current charge. TOTP MFA (`SOFTWARE_TOKEN_MFA`) is available on all three
tiers and does not force the upgrade.

---

## 6. Evidence from the deployed account

Read-only queries against account `338186951935`, 2026-08-08. The v1 stack
`chintan-dev-prod` is deployed; v2 is not.

**Actual billed cost, whole months:**

| Month | Total |
|---|---|
| June 2026 | **$0.0137** |
| July 2026 | **$0.0010** |

Itemised, July: API Gateway $0.0002, DynamoDB $0.0001, S3 $0.0006. CloudWatch,
Lambda, SNS, SQS and KMS all billed **$0.00**.

**Measured footprint:**

| | |
|---|---|
| DynamoDB `chintan-dev-prod` | 39 items, **27,405 bytes** |
| `chintan-content-dev-…` | 107 objects, **5.9 MB** |
| `chintan-lambda-…` (bootstrap artifacts) | 17 objects, **136 MB** |
| `chintan-cloudtrail-…` | 829 objects, **1.4 MB** |
| `/aws/lambda/chintan-api-dev-prod` | **46,813 bytes**, 14-day retention |
| Custom metrics in namespace `Chintan` | **0** (v2 not deployed) |
| CloudWatch alarms in us-west-2 | **0** (v2 adds 9) |
| Customer-managed KMS keys | **0 in the template as of this revision** — the vault CMK was removed |

The vault CMK was one day old when this was first measured, which is why no KMS
charge appears in the June or July figures. It would have been the one line item
materially different in the August bill, and it was the entire idle cost — which
is why it was removed rather than tuned.

**Retained keys still bill.** `TokenVaultKey` carried `DeletionPolicy: Retain`,
so deleting it from the template orphans the live key rather than destroying it,
and an orphaned CMK bills the same $1/month as an attached one. Removing the
resource is therefore only half the saving; the key must also be scheduled for
deletion by hand. `scripts/teardown.sh` now finds keys in this state by their
`Project` tag and prints the command, because an unaliased CMK has no name to
search for.

---

## 7. Sources

All prices are the verbatim `us-west-2` rate from the AWS Price List bulk API,
which is the same data the pricing pages resolve their placeholders against.

| Source | Used for |
|---|---|
| [Price List — CloudWatch, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCloudWatch/current/us-west-2/index.json) | metrics $0.30/$0.10/$0.05/$0.02, alarms $0.10/$0.30/$0.50, logs $0.50 & $0.25/GB ingest, $0.03/$0.018/$0.006 GB-mo storage, free tiers of 10 metrics / 10 alarms / 5 GB |
| [Price List — Cognito, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonCognito/current/us-west-2/index.json) | Lite $0.0055, Essentials $0.015, Plus $0.020 per MAU; legacy ASF $0.05 |
| [Price List — Lambda, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSLambda/current/us-west-2/index.json) | arm64 $0.0000133334/GB-s, $0.20/M requests, 400,000 GB-s + 1M request free tier, provisioned concurrency $0.0000077778/GB-s |
| [Price List — KMS, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/awskms/current/us-west-2/index.json) | **$1 per customer managed KMS key version**, $0.03/10,000 requests, 20,000 free |
| [Price List — DynamoDB, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonDynamoDB/current/us-west-2/index.json) | $0.625/M WRU, $0.125/M RRU, $0.25/GB-mo storage (25 GB free), **PITR $0.20/GB-mo** |
| [Price List — S3, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonS3/current/us-west-2/index.json) | $0.023/GB-mo, $0.005/1,000 PUT, $0.004/10,000 GET |
| [Price List — SQS, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSQueueService/current/us-west-2/index.json) | $0.40/M standard requests, first 1M free |
| [Price List — SNS, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonSNS/current/us-west-2/index.json) | $0.50/M requests, first 1M free |
| [Price List — API Gateway, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonApiGateway/current/us-west-2/index.json) | HTTP API $1.00/M requests, no fixed charge |
| [Price List — X-Ray, us-west-2](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AWSXRay/current/us-west-2/index.json) | $5.00/M traces stored (100,000 free), $0.50/M accessed (1M free) |
| [CloudWatch pricing](https://aws.amazon.com/cloudwatch/pricing/) | *"prorated by the hour and charges are incurred only when metrics are sent"*; *"each unique combination of dimensions as a separate metric"*; alarm free tier excludes Metrics Insights query alarms |
| [Reducing CloudWatch costs](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch_billing.html) | EMF billed on ingest + archive + custom metrics; Metrics Insights alarms billed per metric analyzed |
| [Cognito pricing](https://aws.amazon.com/cognito/pricing/) | *"There is no free tier for the Plus tier"*; 10,000 free MAU for Lite/Essentials; free tier is indefinite |
| [Cognito Plus plan features](https://docs.aws.amazon.com/cognito/latest/developerguide/feature-plans-features-plus.html) | advanced security / threat protection requires Plus |
| [KMS pricing](https://aws.amazon.com/kms/pricing/) | *"first and second rotation … adds $1/month … capped at the second rotation"* |
| [HTTP API CloudWatch metrics](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-metrics.html) | `ApiId` dimension published by default; only `ApiId, Method, Resource, Stage` needs detailed metrics and *"will incur additional charges"* |
| [AWS Budgets pricing](https://aws.amazon.com/aws-cost-management/aws-budgets/pricing/) | budget monitoring free; only action-enabled budgets bill |
| [AWS Free Tier](https://aws.amazon.com/free/) | always-free allowances persist in 2026 alongside the new-account credit model |
