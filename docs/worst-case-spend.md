# Worst-case monthly spend

**Required by §Phase 0 acceptance:** *"The documented worst-case monthly spend, under
concurrency cap + API throttle + spend breaker, is below $20."* §10.7 says the same and
adds the instruction this document is built around: **"If real usage diverges materially,
re-derive the table rather than trusting the total."**

So this is a derivation, not an assertion. Every figure below is arithmetic over inputs
that live in the repository, cited by file so a reader can check the input as well as the
sum. §8 is the procedure for re-deriving it.

**The short answer, stated before the detail because the detail qualifies it:**

| Question | Answer |
|---|---|
| Expected monthly spend at the §10.7 modelled usage | **$0.53 – $1.29** (§4) |
| Worst case of *legitimate* operation — 3× the modelled usage, no need-gating, a full reprocess pass, both instances live, no free-tier headroom | **≈ $8.40** (§5) |
| Ceiling against an unauthenticated flood at the API throttle | **$20.40 – $64.15** (§6.1) |
| Ceiling against an authenticated client in a loop | **≈ $303, plus unbounded S3 storage** (§6.2) |

**The acceptance criterion holds for the legitimate-operation bound and does not hold for
the adversarial ceilings.** The three named controls do not compose into a $20 bound, and
§7 gives the four specific reasons with the number attached to each. That is the finding,
not a caveat: the criterion as written is satisfiable, but not by the controls it names.

---

## 1. Inputs, and where each one lives

Nothing here is remembered or assumed. If a value below no longer matches its source, this
document is stale and §8 says what to do.

| Input | Value | Source |
|---|---|---|
| API function memory | 256 MB | `infrastructure/template.yaml` `ApiFunction.MemorySize` |
| API function timeout | 10 s | same, `Timeout` |
| API reserved concurrency | **5** | same, `ReservedConcurrentExecutions` |
| Worker memory | 1024 MB (`WorkerMemoryMB` default) | `infrastructure/template.yaml` `Parameters.WorkerMemoryMB` |
| Worker timeout | 300 s | same, `WorkerFunction.Timeout` |
| Worker reserved concurrency | **2** | same, `ReservedConcurrentExecutions` |
| Architecture | `arm64`, both functions | same, `Architectures` (mandatory, §10.1) |
| API stage throttle | **5 rps steady, burst 10** | same, `HttpApiStage.DefaultRouteSettings` |
| Route topology | single `$default` route → API function; **no gateway authorizer** | same, `DefaultRoute` |
| Log retention | 14 days, explicit on every log group | same, `LogRetentionDays` (default 14, from `retention.log_group_days`) |
| Table billing | `PAY_PER_REQUEST`, PITR on, TTL on | same, `Table` |
| S3 lifecycle | continuous safety copies expire at 7 days; stale multipart aborted at 7 days | same, `LifecycleConfiguration` |
| Daily spend cap, dev | **$0.50 / tenant / UTC day** | `config/instances/dev.yaml` `limits.daily_spend_usd` |
| Daily spend cap, prod | **$2.00 / tenant / UTC day** | `config/instances/prod.yaml` `limits.daily_spend_usd` |
| STT price | $0.04 / hour, 10 s minimum billed per request | `config/instances/*.yaml` `providers.stt.catalog.groq_whisper_turbo` |
| LLM price | **absent from config** — see §7.4 | `providers.llm.catalog` has no token price field (`backend/internal/config/config.go`, `LLMCatalog`) |
| Embedding price | **absent from config** — same | `EmbeddingsCatalog` |
| Audio bitrate | 24 kbps opus, mono = 3 kB/s | `capture.audio` |
| Segment target | 28 s (45 s max) | `capture.vad.target_segment_ms` |
| Modelled usage | ~20 min of **speech**/day ≈ 10 h/month ≈ 45 segments/day | §10.7 |
| CMK | none in the personal phase (I8) | `Table.SSESpecification`, bucket `AES256` |
| Alarms / SNS topics | none, by rule (§10.1) | `scripts/checks/check-no-alarms.sh` |
| VPC / NAT | none, by rule (§10.2) | `scripts/checks/check-no-vpc.sh` |
| CloudTrail | one management-event trail, objects expire at 400 days | `scripts/bootstrap-agent.sh` |

**Month length.** 31 days = **2,678,400 s** throughout. Using 31 rather than 30 overstates
every ceiling by ~3%, which is the correct direction for a bound.

**No free-tier credit is taken anywhere below.** §10.3 and G-028: the Always-Free
allowances (1M Lambda requests, 400,000 GB-s, 25 GB DynamoDB) are **account-wide and shared
with passbook**, so this project cannot claim headroom it does not own. Where a row would
be $0.00 with the allowance and non-zero without it, the non-zero figure is used and the
row says so. This is the single largest difference between the tables here and §10.7's,
whose Lambda and DynamoDB rows read $0.00 (see §7.5).

---

## 2. Unit prices

List prices, ca-central-1, at the time of writing. **These are the inputs most likely to be
wrong or stale**, so they are isolated here rather than buried in arithmetic, and rounded
*up* where there was any doubt — the safe direction for a ceiling.

| Resource | Unit price | Note |
|---|---|---|
| Lambda, arm64 | $0.0000133334 / GB-s | = $0.048 per GB-hour. ~20% below x86, which is why `arm64` is mandatory |
| Lambda requests | $0.20 / 1M | |
| API Gateway HTTP API | $1.00 / 1M requests | first 300M/month; ~5× cheaper than REST API |
| DynamoDB on-demand write | $1.25 / 1M WRU | 1 WRU per 1 KB |
| DynamoDB on-demand read | $0.25 / 1M RRU | strongly consistent: 1 RRU per 4 KB |
| DynamoDB storage | $0.25 / GB-month | |
| DynamoDB PITR | $0.20 / GB-month | on top of storage — PITR doubles the standing table cost |
| S3 Standard storage | $0.025 / GB-month | |
| S3 PUT / POST / LIST | $0.005 / 1,000 | **the dominant S3 charge here** — many small objects, one per segment |
| S3 GET | $0.0004 / 1,000 | |
| CloudWatch Logs ingest | $0.50 / GB | the reason `RetentionInDays` is mandatory is *storage*; ingest is charged regardless |
| CloudWatch Logs storage | $0.03 / GB-month | |
| Cognito | $0.00 | first 10,000 MAU free; this deployment has 1 |
| SSM Parameter Store Standard | $0.00 | including `SecureString` under `alias/aws/ssm` (§10.2) |
| KMS | $0.00 | no CMK (I8). A CMK would add ~$1/month standing — a fifth of the §10.7 target |
| CloudTrail | $0.00 | first trail, management events only |
| Resource Groups, cost-allocation tags | $0.00 | |
| GitHub Pages | $0.00 | §10.6 |
| STT (Groq `whisper-large-v3-turbo`) | $0.04 / hour | free tier 2,000 requests/day covers 45/day, so the realistic figure is the lower bound of each STT range below |
| LLM (MiniMax-M3) | **unknown** | no token price exists in config or in §7.4's schema. See §7.4 |

---

## 3. What each named control actually bounds

This section is the reason the answers in §6 differ from the acceptance criterion. Each
control is real and each one binds something; the question is *what*, and the three do not
cover the same surface.

**`ReservedConcurrentExecutions` bounds GB-seconds per unit time, not requests per month.**
A cap of 5 on a 256 MB function means at most 5 × 0.25 = **1.25 GB resident at any instant**,
whatever the request rate. Invocations beyond the cap are throttled by Lambda and cost
nothing in duration. So the cap converts an unbounded duration bill into a fixed rate:

```
api    5 × 0.25 GB = 1.25 GB  →  1.25 × 2,678,400 s = 3,348,000 GB-s/month  →  $44.64
worker 2 × 1.00 GB = 2.00 GB  →  2.00 × 2,678,400 s = 5,356,800 GB-s/month  →  $71.42
                                                                     total     $116.06
```

That is the *ceiling*, reached only if both functions are busy every second of the month.
Per day it is $3.74 — which is the more useful form, because it says a fully saturated day
costs less than a coffee and a week of saturation is noticed by the account Budget long
before it matters.

**`ThrottlingRateLimit: 5` bounds requests through the gateway.** 5 rps sustained is
13,392,000 requests/month; the burst of 10 does not raise the sustained figure, since the
token bucket refills at the rate limit. This is the control that bounds request-count
charges — API Gateway requests, Lambda invocations, and every DynamoDB write those
invocations perform.

**It does not bound S3 ingress, and that is by design.** I3 sends audio bytes to S3 by
presigned PUT precisely so they never transit the gateway or a Lambda payload. The
consequence for cost is exact: **one throttled API request buys one unthrottled S3 PUT of
arbitrary size.** Neither the object count nor the byte count is bounded by anything in
this repository. §6.2 and §7.3 carry the number.

**`limits.daily_spend_usd` bounds third-party provider spend per tenant per UTC day.** It
does not bound AWS spend at all — it is computed from Usage records (I12), which meter
provider calls. The monthly ceiling it implies is `31 × cap × tenants`, plus the overshoot
`backend/internal/breaker/breaker.go` documents rather than hides: in-flight reservations
are per-process, so concurrent calls in separate Lambda instances are mutually invisible,
and the day can exceed the cap by up to `(concurrency − 1)` calls' worth. Provider calls
run in the worker, whose concurrency is 2, so the overshoot is one in-flight call per day —
material only if a single call is expensive.

**Summary of coverage — the gaps are the point:**

| Cost surface | Bounded by | Ceiling |
|---|---|---|
| Lambda duration | reserved concurrency | $116.06/month |
| Gateway requests, Lambda invocations | API throttle | $16.07/month |
| DynamoDB writes/reads | API throttle (writes follow requests) | $20.09/month |
| CloudWatch Logs | API throttle (lines follow requests) | $3.44/month |
| Provider (STT, LLM, embeddings) | daily spend breaker | $77.50/month for dev + prod |
| **S3 object count** | **nothing** | **$66.96/month at the presign rate** |
| **S3 bytes stored** | **nothing** | **unbounded; ~$40/month per month of a saturated mobile uplink** |

---

## 4. Expected spend at the modelled usage

§10.7's basis, restated so the rows are checkable: ~20 min of **speech**/day ≈ 10 h/month,
~45 segments/day ≈ 1,350/month, a handful of sessions/day ≈ 120/month. Wall-clock recording
is longer; VAD is what keeps billed audio near the speech figure.

| Component | Arithmetic | Est. monthly |
|---|---|---|
| Frontend hosting | GitHub Pages | $0.00 |
| STT | 10 h × $0.04 = $0.40 paid; 45 req/day is far inside Groq's 2,000/day free tier | $0.00 – 0.40 |
| LLM | **not derivable — no token price in config.** §10.7's figure carried forward (§7.4) | $0.20 – 0.50 |
| Embeddings | changed blocks only; same pricing gap | < $0.05 |
| Lambda — worker | 1,350 invocations × ~10 s × 1 GB = 13,500 GB-s × $0.0000133334 | $0.18 |
| Lambda — api | ~1,000 req × 0.3 s × 0.25 GB = 75 GB-s, + 1,000 invocations | $0.00 |
| API Gateway | 1,000 × $1.00/1M | $0.00 |
| DynamoDB | ~5,000 writes × 1 KB = $0.006; storage 0.1 GB = $0.025; PITR = $0.020 | $0.05 |
| S3 | ~6,000 PUT = $0.03; 1,350 GET ≈ $0.00; 110 MB/month accumulating ≈ $0.003 | $0.04 |
| CloudWatch Logs | ~20,000 lines × 1 KB = 20 MB × $0.50/GB | $0.01 |
| Cognito / SSM / KMS / CloudTrail / Resource Groups | 1 MAU; SSM Standard; no CMK; first trail | $0.00 |
| CloudTrail delivery PUTs | ~8,600/month × $0.005/1,000 | $0.04 |
| **Total** | sum of the rows, low and high ends | **$0.53 – $1.29** |

Consistent with §10.7's $0.35–$1.05. The difference is almost entirely the Lambda and
DynamoDB rows, which §10.7 reads as $0.00 by taking the account-wide free tier that G-028
says is shared with passbook. $0.18 of worker duration is the honest floor.

**The audio arithmetic, since it is the one input a reader can sanity-check without an AWS
bill:** 24 kbps mono opus = 3 kB/s. 20 min/day = 1,200 s → 3.6 MB/day → 1,200 × 3 kB × 31 =
**112 MB/month**. §10.7 independently states "~110MB of opus segments"; the two derivations
agree, which is the point of showing it.

---

## 5. Worst case of legitimate operation — the $20 claim

This is the bound the acceptance criterion is actually about: everything going wrong that
does not involve someone attacking the endpoint. Assumptions, each pessimistic and each
plausible:

- **3× the modelled usage** — 1 hour of speech per day, every day (≈ 129 segments/day, 3,990/month)
- **need-gating never fires** — every session takes the full LLM cleanup path (§10.5.2 says a large fraction should skip; assume none do)
- **a full reprocess pass** — one `reprocess.sh` + `retranscribe.sh` sweep over the month's content, doubling STT, LLM, and embedding volume
- **both instances live** — dev and prod, with dev at ~20% of prod's AWS volume
- **no free-tier headroom** — passbook has consumed the account-wide allowance
- **no shadow mode** — §7.2 says shadow doubles STT spend and is for bounded windows; if it were left on, add the STT row again

| Component | Arithmetic | Est. monthly |
|---|---|---|
| STT | (30 h live + 30 h retranscribe) × $0.04 | $2.40 |
| LLM | **assumed, not derived** (§7.4): §10.7's $0.50 upper bound × 3 usage × 2.5 no-gating | $3.75 |
| Embeddings | $0.05 × 3 usage × 2 full reindex | $0.30 |
| Lambda — worker | 3,990 × 10 s × 1 GB = 39,900 GB-s, doubled by the reprocess pass = 79,800 GB-s | $1.06 |
| Lambda — api | 10,000 req × 0.3 s × 0.25 GB = 750 GB-s + invocations | $0.01 |
| API Gateway | 10,000 × $1.00/1M | $0.01 |
| DynamoDB | ~16,000 writes = $0.02; reads $0.01; storage 0.5 GB = $0.13; PITR $0.10 | $0.26 |
| S3 | 16,000 PUT = $0.08; GETs $0.01; 335 MB/month accumulating, ~4 GB by month 12 = $0.10 | $0.19 |
| CloudWatch Logs | 60,000 lines × 1 KB = 60 MB | $0.03 |
| CloudTrail delivery | as §4 | $0.04 |
| Second instance (dev) | +20% on the AWS rows above | $0.32 |
| **Total** | | **≈ $8.37** |

**Under $20, with ~2.4× headroom.** Two properties of that headroom are worth stating
because they are what makes the number robust rather than lucky:

1. The AWS half of the bill is **$1.92** of the $8.37. Even a 5× miss on AWS volume keeps
   the total under $20.
2. The provider half — $6.45 — is capped independently by the breaker at a *lower* figure
   than this table only if the caps are reduced (§7.1): as configured, the breaker permits
   $77.50 and therefore does not constrain this row at all. The $6.45 here is a usage
   estimate, not a bound.

That second point is why the acceptance criterion cannot rest on this table alone.

---

## 6. The adversarial ceilings

What the three controls actually permit, by who is doing it. The distinction matters: the
two actors face different controls, and I10 is worth real money in the first case.

### 6.1 An unauthenticated flood at the API throttle

There is **no authorizer on the gateway** — the `$default` route sends everything to the API
function, which authorises in-process (I10, §4). So an anonymous request costs a gateway
request, a Lambda invocation, and its duration, but reaches no table: auth fails closed
before any read. That last part is I10 paying for itself in dollars, not only in privacy.

```
requests/month = 5 rps × 2,678,400 s = 13,392,000

API Gateway     13,392,000 × $1.00/1M                        = $13.39
invocations     13,392,000 × $0.20/1M                        =  $2.68
duration        13,392,000 × d × 0.25 GB × $0.0000133334     =  varies with d
logs            13,392,000 × 0.5 KB = 6.7 GB × $0.50/GB      =  $3.35  (+$0.09 storage)
DynamoDB        auth fails before any read (I10)              =  $0.00
```

| Rejection duration `d` | Duration cost | **Total** |
|---|---|---|
| 20 ms (JWT verify, JWKS cached) | $0.89 | **$20.40** |
| 200 ms (cold start, or JWKS fetch) | $8.93 | **$28.44** |
| 1 s (concurrency fully saturated — the ceiling) | $44.64 | **$64.15** |

**Even the cheapest row is at the $20 limit, and none of it requires authentication.** The
binding input is the throttle: at 5 rps the request charges alone are $16.07/month. §7.2
carries the consequence.

### 6.2 An authenticated client in a loop

A runaway client, a stolen JWT, or a retry storm. Everything in §6.1, plus the work an
authorised request actually performs: an audit record (I13), a tenant-scoped read, a
presigned PUT the client then uses, and a worker invocation per uploaded object.

```
API Gateway requests    13,392,000 × $1.00/1M                          = $13.39
api invocations         13,392,000 × $0.20/1M                          =  $2.68
logs                    6.7 GB ingest + storage                        =  $3.44
Lambda duration, api    concurrency-saturated (§3)                     = $44.64
Lambda duration, worker concurrency-saturated (§3)                     = $71.42
worker invocations      ≤ 13,392,000 × $0.20/1M                        =  $2.68
DynamoDB writes         13,392,000 × 1 KB × $1.25/1M   (audit, I13)    = $16.74
DynamoDB reads          13,392,000 × $0.25/1M                          =  $3.35
S3 PUTs                 13,392,000 / 1,000 × $0.005                    = $66.96
provider spend          31 × ($2.00 prod + $0.50 dev)                  = $77.50
                                                                  total = $302.80
```

Plus **S3 storage, which no control in this repository bounds.** A presigned PUT carries no
size limit unless the signature includes one, and the bytes never pass the throttle (I3).
At a sustained 5 Mbps uplink — an ordinary phone — that is ~1.6 TB/month, ~$40/month of
storage *added every month*, and the 7-day lifecycle rule only reaches objects tagged as
continuous safety copies.

**Ceiling: $302.80/month plus unbounded storage growth.** Worth noting what is *absent* from
this list and would dominate it if the rules of §10.1–10.2 were relaxed: a NAT Gateway
(~$32/month standing), a CMK (~$1/month plus per-request), CloudWatch alarms past the
account-wide free 10, and unset log retention. The five load-bearing choices in §10.7 are
load-bearing.

---

## 7. Findings — where the $20 claim does not hold, and what closes each gap

Each of these is arithmetic over a value in the repository, so each has a number and a
concrete fix. **None of the fixes are applied by this document** — they touch files outside
this agent's boundary (`config/instances/*.yaml`, `infrastructure/template.yaml`) and one is
a spec change.

### 7.1 The prod daily cap permits $62/month of provider spend by itself

`config/instances/prod.yaml` sets `limits.daily_spend_usd: 2.00`, and its own comment says
*"the documented worst case under this breaker plus the concurrency cap and API throttle
must stay below $20/month (§Phase 0 acceptance)."* The arithmetic contradicts the comment:

```
prod  31 × $2.00 = $62.00/month
dev   31 × $0.50 = $15.50/month
                   $77.50/month  — 3.9× the $20 ceiling, provider spend alone
```

The caps are per tenant per UTC day, so this is the single-tenant figure; a second tenant
doubles it.

**Fix, either of:** (a) reduce the caps so their sum × 31 fits the budget — $0.25/day each
gives $15.50/month combined, still ~7× the modelled daily spend of ~$0.035; or (b) add a
*monthly* ceiling to the breaker, which is the more honest control, since the acceptance
criterion is monthly and a daily cap can only approximate it. (b) is a change to
`backend/internal/breaker` and to §7.4's schema.

### 7.2 The API throttle at 5 rps permits ~$16/month of request charges to anonymous callers

§6.1: 5 rps × 31 days = 13.4M requests = $13.39 (gateway) + $2.68 (invocations), before any
duration or logging. A single-user application has no use for 5 requests per second
sustained.

**Fix:** `ThrottlingRateLimit: 1` with `ThrottlingBurstLimit: 10` unchanged. The burst is
what covers a screen loading several resources at once; the steady rate is what an attacker
gets. At 1 rps the anonymous flood becomes 2,678,400 requests/month:

```
API Gateway  $2.68   invocations $0.54   logs $0.71   duration @20ms $0.18   = $4.11
                                                      duration @200ms $1.79  = $5.72
```

So $20.40 → **$4.11**, at no cost to a single-user application. Note what does *not* change:
the $44.64 duration ceiling in §3 is rate-independent — it depends on how long each
invocation holds a concurrency slot, not on how many arrive — and reaching it at 1 rps would
need every rejection to occupy a slot for five seconds, which a JWT verification against a
cached JWKS cannot do. The throttle is the right control for request charges and is not a
control on duration.

This is a one-line change to `infrastructure/template.yaml`, whose comment already identifies
the throttle as *"passbook's cost guard"* — the value is simply inherited from a project whose
per-request cost profile was AWS-only.

### 7.3 Nothing bounds S3 object count or bytes, because I3 routes them around the throttle

The largest single line in §6.2 is $66.96 of S3 PUT requests, and the unbounded term is
storage. This is not an oversight in the throttle — it is the direct consequence of I3
("audio bytes never transit an API Gateway request or Lambda payload"), which is correct for
latency, payload limits, and cost at normal volume, and which removes the throttle from the
byte path by construction.

**Fix, and it belongs in Phase 1 where the presign endpoint is built:** a per-tenant daily
quota on presign issuance (objects and total declared bytes), enforced from the same Usage
records the breaker reads, with `storage_bytes` metered at presign time rather than at
upload. §Phase 0 already lists `storage_bytes` as a metering unit (I12), so the unit exists
and nothing emits it yet. A presigned PUT can also be signed with a `content-length-range`
condition, which bounds a single object without a quota — cheap, and worth doing regardless.

### 7.4 The breaker cannot price two of its three provider legs

`backend/internal/breaker/breaker.go` states its own basis: *"Config carries what the
estimates are computed from: `cost_per_hour_usd` and `min_billed_seconds` per STT provider
(§7.1), and the token prices per LLM entry."* **There are no token prices.** `LLMCatalog`
holds `adapter`, `base_url`, `model`, `secret_ref`, `max_context`; `EmbeddingsCatalog` holds
`dimensions` instead. §7.4's schema does not define the field either, so this is a spec gap
that the config validator faithfully reproduces.

Consequences, in order of severity:

1. The breaker's mandatory positive estimate for an LLM call has nothing to compute from.
   Whatever it uses will be a literal in Go — which is a provider price hardcoded in code,
   **contrary to I5**.
2. I12 requires every metering event to carry a provider cost basis. LLM and embedding
   events cannot carry a true one, so the daily total the breaker reads under-reports, and
   it under-reports exactly the component §10.7 calls the largest available saving.
3. This document cannot derive the LLM row. §4 and §5 carry §10.7's asserted range instead,
   marked as assumed — the only figure in either table that is not derived.

**Fix:** add `input_cost_per_mtok` / `output_cost_per_mtok` to `LLMCatalog` and
`cost_per_mtok` to `EmbeddingsCatalog`, required by the validator exactly as
`cost_per_hour_usd` already is for STT (`config.go` line ~503 is the pattern). Then re-derive
§4's LLM row from the real numbers.

### 7.5 §10.7's Lambda and DynamoDB rows claim account-wide free tier

§10.7 reads $0.00 for Lambda and ~$0.00 for DynamoDB. Both are true only while the
account-wide Always-Free allowance has headroom, which §10.3 and G-028 say must not be
assumed in an account shared with passbook. The honest figures are small ($0.18 and $0.05 at
modelled usage), so this changes no decision — but it is the kind of $0.00 that stops being
$0.00 without anyone touching this project, and the total is printed in a README that other
people read (§10.4).

### 7.6 The worker pays 1 GB of memory to wait on the network

The worker is 1024 MB because the Phase 5 embedding matrix must be resident (G-061), and it
is also the function that blocks on STT and LLM HTTP calls. Waiting at 1 GB costs 4× waiting
at 256 MB. At modelled usage that is $0.18 versus $0.05 — immaterial today, and worth
recording because the fix is the one §10.5.1 already prescribes for LLM calls: batch
per session so the wait is paid once, not 45 times a day. Splitting the pipeline across two
memory profiles would cost more in complexity than it saves at any volume this document
covers.

---

## 8. How to re-derive this

§10.7: *"If real usage diverges materially, re-derive the table rather than trusting the
total."* The trigger for that is any of: a config change to `limits`, `capture.audio`, or
`capture.vad`; a template change to memory, timeout, concurrency, or the throttle; a new or
repointed provider; or a monthly bill that misses §4's range by more than 2×.

**1. Re-read the inputs.** Every row in §1 cites its file. The values most likely to have
moved are `limits.daily_spend_usd`, `WorkerMemoryMB`, and `ThrottlingRateLimit`.

**2. Re-check the unit prices in §2** against the current AWS price list for the deployed
region and the providers' current pages. §7.1's own STT entry warns that one catalogued
provider is "9× to 27× Groq depending on API and tier — verify against current pricing
before relying on it (G-044)"; the same caution applies to every row of §2.

**3. Replace the modelled usage with measured usage.** Actuals come from the Usage records
(I12), not from a guess:

- the usage report (`usage.sh` / `chintanctl usage`, §11.4) gives per-tenant metered spend by
  month and provider, and reconciles metered totals against the provider and AWS bills
- Cost Explorer filtered on `Project = voicenotes` gives the AWS side — provided the
  cost-allocation tag was activated on day one, since §10.3 warns activation does not
  backfill

**4. Recompute with these formulas**, which are the whole of the arithmetic above:

```
seconds_per_month        = days × 86,400
gb_seconds              = concurrency × (memory_mb / 1024) × seconds_per_month     # ceiling
gb_seconds              = invocations × duration_s × (memory_mb / 1024)            # actual
requests_ceiling        = throttle_rps × seconds_per_month
provider_ceiling        = 31 × daily_spend_usd × tenants   (+ one in-flight call, §3)
segments_per_month      = speech_seconds_per_day × 30 / target_segment_ms × 1000
audio_bytes_per_month   = speech_seconds_per_day × 30 × bitrate_bps / 8
s3_put_cost             = objects / 1,000 × $0.005
ddb_write_cost          = writes × ceil(item_kb) / 1,000,000 × $1.25
log_cost                = lines × bytes_per_line / 1e9 × $0.50
```

**5. Re-derive, do not patch.** If a row moves, recompute the table rather than adjusting the
total — the totals here are sums of visible arithmetic, and a total edited in isolation is
how a cost model stops being checkable. Then reconcile §7: the findings are numbers, and a
changed input changes them.

**6. Above $5/month recurring, stop.** §10.7: *"If any phase's design pushes recurring cost
above $5/month, stop and flag it before implementing. That is a design error, not a budget
overrun."*
