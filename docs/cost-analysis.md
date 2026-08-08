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
| 1 | `TokenVaultKey: EnableKeyRotation: false` | **$2.00/mo** at steady state | Automatic annual re-keying of the refresh-token CMK. This is the single biggest lever on the bill — it is **two thirds of the idle cost** — but it is a security control, so a bot should not switch it off on cost grounds. See [§5.1](#51-kms-key-rotation--tradeoff). |
| 2 | Worker `MemorySize: 2048` → `512` | **$0.00 today** | Nothing measurable. The saving is genuinely zero because Lambda stays inside the free tier in every modelled scenario. See [§5.2](#52-worker-memory--tradeoff). |
| 3 | Replace the `SpendCapRejections` Metrics Insights alarm with a plain alarm | $0.30/mo **only when `DailySpendCapMicros ≠ 0`** | Nothing, if a dimensionless companion metric is emitted. Costs a code change. See [§5.3](#53-spend-cap-alarm--tradeoff). |
| 4 | Drop `Status` from the `ApiLatency` dimension set | $0.00 today | Latency broken down by status class. Reduces the ceiling by 90 metric identities but needs a second `Emit` call. See [§5.4](#54-apilatency-dimensions--tradeoff). |

**Not recommended:** turning off `AdvancedSecurityMode: ENFORCED`. It costs
**$0.02/month** for one user. See [§5.5](#55-cognito-advanced-security--do-not-cut).

---

## 1. Headline

> **Idle monthly cost: $1.00.**
> Of which **$1.00 is the KMS customer-managed key.** Everything else in the
> stack — every alarm, every metric, every log group, every table, bucket,
> queue, topic, API and budget — costs **$0.00** when the app is idle.

Light and heavy use barely move it, because AWS's always-free tiers absorb
essentially all of a single user's consumption:

| Scenario | AWS/month | Third-party/month | Total |
|---|---|---|---|
| **Idle** (0 captures) | **$1.00** | $0.00 | **$1.00** |
| **Light** (30 captures × 2 min) | **$1.04** | $0.69 | **$1.73** |
| **Heavy** (300 captures × 5 min) | **$1.16** | $16.80 | **$17.96** |

The README's claim of **$1–10/month** is *numerically* right at the bottom of
its range and *structurally* wrong. It says "nearly all of the cost is
per-recording" and itemises Lambda $0.20–2, DynamoDB $0.25–1, S3 $0.50–5.
In reality **Lambda, DynamoDB and S3 are all $0.00** at this scale, and the
entire idle bill is one fixed charge the README does not mention at all.

At heavy use the AWS bill is **6% of total spend**. The providers are the cost
story; the infrastructure is not.

### The custom-metric hypothesis: **refuted**

The brief suspected custom metrics were the single largest recurring cost and
"entirely self-inflicted". The metric surface *is* large and self-inflicted —
**17 metric names expanding to a ceiling of 342 billable metric identities**
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
one of the 342 identities received data every hour, the bill would be
**$102.60/month** — ten times the entire budget. The gap between $0.00 and
$102.60 is traffic pattern, not configuration.

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
| `TokenVaultKey` (KMS CMK) | **$1/key-version-month**, no free tier | **$1.00** |
| `TokenVaultKeyAlias` | aliases are free | $0.00 |
| `UserPool` + `AdvancedSecurityMode: ENFORCED` | → **Plus tier**, $0.020/MAU, **zero free MAU** | $0.00 (0 MAU) |
| `UserPoolDomain` | no SKU exists in the price list | $0.00 |
| `DynamoDBTable` (on-demand) | $0.25/GB-mo, first 25 GB free; measured **27,405 bytes** | $0.00 |
| ↳ `PointInTimeRecoverySpecification` | $0.20/GB-mo, no minimum; 27 KB → $0.0000054 | $0.00 |
| ↳ `gsi1` GSI | GSI storage + RRU/WRU, same free tier | $0.00 |
| `ContentBucket` | $0.023/GB-mo; measured **5.9 MB** | $0.00 |
| ↳ `VersioningConfiguration: Enabled` | noncurrent versions billed as storage | $0.00 |
| `CaptureQueue` + `CaptureDLQ` | $0.40/M requests, first 1M free; idle queues are free | $0.00 |
| `ApiLambdaFunction` (512 MB, arm64) | $0.0000133334/GB-s, 400,000 GB-s free | $0.00 |
| `WorkerLambdaFunction` (2048 MB, arm64) | same | $0.00 |
| ↳ `ReservedConcurrentExecutions: 5` | **free** — only *provisioned* concurrency bills | $0.00 |
| ↳ `Version` / `Alias` ×2 | code storage $0.0000000309/GB-s | ~$0.00 |
| ↳ `TracingConfig: Mode: Active` (X-Ray) | $5.00/M traces, **100,000 free** | $0.00 |
| `HttpApi` | $1.00/M requests, **no fixed charge** | $0.00 |
| `ApiStage` → `DetailedMetricsEnabled: true` | **billed as custom metrics**, ~36 identities | $0.00 idle |
| `ApiLogGroup` / `WorkerLogGroup` | $0.50/GB ingest, $0.03/GB-mo storage, 5 GB free | $0.00 |
| `ApiAccessLogGroup` | **vended logs**, same $0.50/GB at this volume | $0.00 |
| 6 × `AWS::CloudWatch::Alarm` (standard) | $0.10/alarm-month, **10 free** | $0.00 |
| `SpendCapRejectionsAlarm` (Metrics Insights) | $0.10 **per metric analyzed**, **no free tier** | **not deployed** — `HasSpendCap` is false at the default `DailySpendCapMicros: 0` |
| `AlarmTopic` + subscription | $0.50/M requests, 1M free; idle topic free | $0.00 |
| `MonthlyBudget` | **budget monitoring is free**; only *action-enabled* budgets bill | $0.00 |
| EMF custom metrics (namespace `Chintan`) | $0.30/metric-month, hourly prorated, 10 free | $0.00 idle |

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

**17 metric names → 342 identity ceiling.** Every emitter, enumerated:

| Metric name | Dimensions | Value domain | Ceiling |
|---|---|---|---|
| `ApiRequests` | Route, Method, Status | 30 routes × 4 status classes | 120 |
| `ApiLatency` | Route, Method, Status | same | 120 |
| `ApiServerErrors` | Route, Method, Status | 30 routes × {5xx} | 30 |
| `CaptureStageFailures` | Stage | 12 statuses ∪ 3 stage strings | 15 |
| `CapturePipelineDuration` | Outcome | 12 capture statuses | 12 |
| `DuplicateDelivery` | Status | 12 capture statuses | 12 |
| `ProviderLatency` | Provider, Op, Outcome | 3 pairs × {ok, error} | 6 |
| `CapturePipelineOutcomes` | Outcome | 4 terminal statuses | 4 |
| `CaptureStageEntered` | Stage | transcribing, routing, cleaning, appending | 4 |
| `RouterContentDiscarded` | Reason | missing_field, empty_content, not_derived | 3 |
| `CaptureSpendCapped` | Stage | transcribe, route, cleanup | 3 |
| `SpendCapRejections` | Provider, Op | groq/transcribe, openai/route, openai/cleanup | 3 |
| `ProviderCostMicros` | Provider, Op | same 3 pairs | 3 |
| `ProviderCalls` | Provider, Op | same 3 pairs | 3 |
| `RateLimited` | Route | the 2 `perIP()` webauthn routes | 2 |
| `CapturesCreated` | Stage | `created` | 1 |
| `WorkerMessagesDiscarded` | Reason | `unparseable` | 1 |
| | | **Total ceiling** | **342** |

Plus **~36** more from `DetailedMetricsEnabled: true` on the API stage
(6 HTTP API metrics × ~6 method+resource combinations), which are billed at the
same custom-metric rate.

**Realistically touched** by one user in a busy month: **~53** identities
(~18 route/status pairs × 2 metrics, plus the ~17 pipeline and provider
identities on the happy path).

The cardinality discipline in `obs.Emit`'s doc comment and in
`handler/metrics.go`'s `routeOf` is genuinely good — no tenant id, capture id,
or raw path ever reaches a dimension, and status is bucketed to a class rather
than an exact code. Without that the ceiling would be unbounded rather than 342.
The remaining problem is not cardinality but **redundancy**, addressed in §5.

---

## 4. Three scenarios

Modelling assumptions: single user; measured storage as of 2026-08-08; worker
wall-clock dominated by provider round-trips (~30 s for a 2-min capture, ~70 s
for a 5-min one); user active in ~40 distinct hours/month (light) and ~150
(heavy); audio ~1 MB/min.

| Line item | Idle | Light (30 × 2 min) | Heavy (300 × 5 min) |
|---|---|---|---|
| **KMS** — 1 CMK @ $1/key-version-mo | **$1.000** | **$1.000** | **$1.000** |
| KMS requests (20,000/mo free) | $0.000 | $0.000 | $0.000 |
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
| **CloudWatch** alarms — 6 standard, 10 free | $0.000 | $0.000 | $0.000 |
| **CloudWatch** custom metrics (10 free, hourly) | $0.000 | $0.000 | $0.000 |
| ↳ *(metric-months consumed of 10)* | *0* | *~0.7* | *~4.1* |
| **CloudWatch** logs ingest + storage (5 GB free) | $0.000 | $0.000 | $0.000 |
| **Budgets** — monitoring free | $0.000 | $0.000 | $0.000 |
| **CloudTrail** — 1st mgmt-event copy free | $0.000 | $0.000 | $0.000 |
| **AWS total** | **$1.00** | **$1.04** | **$1.16** |

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

#### 5.1 KMS key rotation — TRADEOFF

**The largest single lever in the entire analysis: $2.00/month, two thirds of
the idle bill.**

KMS bills **per key *version***, not per key: *"$1 per customer managed KMS key
version in US West (Oregon)"*. With `EnableKeyRotation: true`, the AWS KMS
pricing page states *"the first and second rotation of the key adds $1/month
(prorated hourly) in cost. This price increase is capped at the second
rotation, and any subsequent rotations will not be billed."*

So the idle bill is on a schedule:

| | Monthly |
|---|---|
| Year 1 (today) | $1.00 |
| Year 2 (after 1st rotation) | $2.00 |
| Year 3 onward (capped) | **$3.00** |

The good news is that it is **capped** — it does not compound indefinitely. The
bad news is that $3.00/month is 30% of the top of the README's stated budget,
for a single-user app, forever.

*What removing it costs you:* automatic annual re-keying of the CMK that seals
Cognito refresh tokens for biometric unlock. Note what KMS rotation does and
does not do: it generates new key material for **new** encryptions and retains
old material to decrypt existing ciphertext. It does not re-encrypt the vault.
For a single-user token vault whose contents turn over every
`RefreshTokenValidityDays` (default 7) anyway, the marginal security value of
annual rotation is modest.

**This is left to the owner deliberately.** It is a security control, and
disabling a security control to save $2 is the owner's call to make, not a
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
| **The 6 standard alarms** | $0.10 each with **10 free** → $0.00. |
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
| CloudWatch alarms in us-west-2 | **0** (v2 adds 6) |
| Customer-managed KMS keys | 1 — `alias/chintan-dev/token-vault`, created **2026-08-07** |

The KMS key is one day old, which is why no KMS charge appears in the June or
July figures. **It is the one line item that will be materially different in the
August bill**, and it is the entire idle cost.

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
