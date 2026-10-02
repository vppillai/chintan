# Usage accounting and the spend cap

What a tenant's recordings cost, what the instance costs, and the one
ceiling that stops a runaway bill. Code: the pricing (`internal/meter`), the
breaker that owns every provider call (`internal/breaker`), the daily counter
it enforces against (`internal/pipeline/spend.go`), the API's courtesy check
(`internal/service/spend.go`), the per-tenant rows (`internal/usage`), the
two daily readings (`internal/awscost`, `internal/storagesnap`), the read
(`internal/handler/usage.go`, `GET /v1/usage` in `docs/api/openapi.yaml`),
the screen (`frontend/src/screens/UsageScreen.tsx`,
`features/settings/UsageSection.tsx`, `usage.ts`) and the operator's listing
(`cmd/chintanctl/usage.go`).

## What is metered

Every call to a paid provider goes through `breaker.Do`; there is no other
way to reach one. A call is an `Estimate` — provider, model, `meter.Op`,
`meter.Quantities`, tenant — and the breaker reserves, runs it, reconciles
the reservation against what the provider reported, writes the `provider
usage` log line, attributes the call to its tenant and emits
`ProviderCostMicros`, `ProviderCalls` and `ProviderLatency`.

| | Values | Set in |
|---|---|---|
| Units (`meter.Unit`) | `audio_seconds`, `input_tokens`, `output_tokens` | the provider's response, else the estimate |
| Ops (`meter.Op`) | `transcribe`, `route`, `cleanup`, `clean_note`, `ask` | each pipeline stage |
| Providers | `groq` (speech), `openai` (the language model) | `cfg.STTProvider` in `pipeline/pipeline.go`; `LLMProvider: "openai"` in `cmd/worker/main.go` |

The provider name is the price table's key, not the vendor's. The language
model is whatever OpenAI-compatible endpoint `LLM_BASE_URL` names, and the
template sets it to `https://api.minimax.io/v1` with `LLM_MODEL: MiniMax-M3`
(`provider/openai_cleanup.go` carries the same default), so `openai` on the
wire and in the rows is MiniMax. The screen says so: `PROVIDER_LABELS` in
`usage.ts` renders the key as "Language model (MiniMax)" (`LLM_VENDOR`),
leading with the role because an instance pointed elsewhere keeps the key.

Three more things are counted outside the breaker: every authenticated API
request (`handler.router.counted`), the instance's AWS bill (`awscost`) and
what each tenant stores (`service.StorageService` on read, `storagesnap`
once a day). Each has its section below.

## How a call is priced

`meter.PriceTable` maps `provider/model` to a price per unit in microdollars
(1,000,000 = $1). `Resolve` and `CostMicros` share one lookup: the model's
own row, else the provider's `*` wildcard, else nothing. Each unit is rounded
**up** to a whole microdollar (`CostMicros`; a fraction that rounded to zero
would let a high call rate accumulate spend the cap never sees) and a call is
the sum over its units (`Cost`), so a completion pays for the tokens it read
and the tokens it wrote. `meter.DefaultPrices` ships four rows:

| Row | Price | Why the row |
|---|---|---|
| `groq/whisper-large-v3-turbo` | $0.040 per hour of audio (11.11 µ$/s) | the model the template deploys |
| `groq/*` | $0.111 per hour | the dearer Whisper, so an unrecognised Groq model over-reserves |
| `openai/minimax-m3` | 0.30 µ$ per input token, 1.20 µ$ per output token | the model the template deploys |
| `openai/*` | 1 µ$ in, 4 µ$ out | three to four times MiniMax, so an unrecognised model over-reserves |

An unpriced model would make the cap enforce nothing, so the worker checks
both configured models at start-up (`checkPriced`, `cmd/worker/main.go`):
`ResolvedNone` refuses to start, naming the file to add a row to;
`ResolvedWildcard` starts, warns once and counts `PriceWildcardUsed{Provider}`
(`docs/ops/metrics.md`). The rows are pinned by
`TestDefaultPricesCoverBothProvidersAndBothTokenKinds` and
`TestDefaultPricesResolveTheDeployedModelsExactly`; the rounding by
`TestPriceTableRoundsUp` and `TestCostSumsEveryUnitTheCallConsumed`.

## The spend cap and the breaker

One cap, one counter, for the whole instance. The cap is
`DAILY_SPEND_CAP_MICROS`, the template's `DailySpendCapMicros` (default
`5000000`, five dollars a day — about a hundred times a single user's
heaviest day, so a retry loop or a leaked login stops at five dollars rather
than at the monthly budget e-mail; `0` counts without enforcing). The counter
is the row `pk INSTANCE`, `sk SPEND#<yyyy-mm-dd>`, attribute `spend_micros`,
written by `pipeline.DynamoCounter.Add` as one atomic `ADD` that returns the
new total, with a TTL of `spendRetention` (90 days). It lives on `INSTANCE`,
not in a tenant's partition, because the money belongs to the account:
`chintanctl`'s per-tenant export, backup and erase must neither carry it nor
delete it, and a tenant leaving does not un-spend it. There is deliberately
no per-tenant cap: the thing that stops a runaway is the ADD-and-compare,
and a second cap would be a second copy of this machinery.

`breaker.Do`, in order:

1. **Reserve.** `ADD` the estimated cost to today's counter (UTC).
2. **Refuse** when the cap is set and the total exceeds it: release the
   reservation, count `SpendCapRejections` (emitted with a rollup so
   `SpendCapRejectionsAlarm` — installed only when the cap is non-zero and
   alarms are on, `SpendCapAlarmEnabled` = `HasSpendCap` and `AlarmsEnabled`
   — is a plain alarm), log `daily spend cap reached` and return
   `ErrSpendCapExceeded`. A redelivery cannot slip past: every attempt
   reserves first.
3. **Run** the call. A provider error releases the reservation, so an outage
   does not consume the day's budget.
4. **Reconcile** the difference between the estimate and what the provider
   reported, write the `provider usage` log line (provider, model, op, cost,
   the day's total, the quantities — never any text), attribute the call to
   its tenant (below) and emit the metrics.

What the cap does downstream. A capture whose stage is refused ends as
`spend_capped` with the error `daily provider spend cap reached`
(`spendCappedVerdict`, `pipeline/status.go`) and counts
`CaptureSpendCapped{Stage}`; routing does not retry past the cap
(`TestRoutingDoesNotRetryPastTheSpendCap`); a whole-note clean and an Ask end
with their own `spend_capped` verdicts (`cleanNoteSpendCapped`,
`askSpendCapped`) and are stopped before the model is called
(`TestCleanNoteTaskIsStoppedByTheSpendCapBeforeTheModelIsCalled`,
`TestAskTaskIsStoppedByTheSpendCapBeforeTheModelIsCalled`). The filing row
reads "Daily spending cap reached" (`filing/model.ts`) with Retry, and
`POST /v1/captures/{id}/retry` runs a `spend_capped` capture again at once;
no push is sent for it (`push.md`).

**The gate.** The API never calls a provider, so it cannot spend; it reads
the same counter against the same cap through `service.SpendGate.Capped`
(`service/spend.go`) — an `ADD` of zero, so there is no second read path
that could disagree with the breaker — and answers 429 before a recording is
uploaded that nothing would transcribe. The handlers that cost a provider
call check it first: `beginCapture`, `retryCapture`, `retranscribeCapture`
and the inbox's `inboxCapture`, audio and text routes through
`refusedByCap`; `cleanNote`, `regenerateNote` and `beginAsk` read
`Spend.Capped` themselves.
The error is `service.ErrSpendCapped`, written as a problem whose `type` ends
in `#spend-capped` (`handler/errors.go`, the `SpendCapped` response in
`openapi.yaml`): the gateway's throttling 429 and this one share a title, and
only the first may be retried. A counter that cannot be read is a 500 and the
request is refused (`refusedByCap` fails rather than guesses). The client reads the type as
`ApiError.isSpendCapped`; the recorder keeps the audio
(`machine.ts`, the `spend-capped` failure) because the cap resets at
midnight UTC. `GET /v1/settings` reports `daily_spend_cap_micros` read-only;
no screen shows the number — it is an operator's guard, not a budget a
person plans against — and About carries one sentence that a cap exists.
Tests: `breaker_test.go` (`TestDoRefusesToCallProviderOverCap`,
`TestDoReleasesReservationWhenProviderFails`,
`TestConcurrentCallsAccountExactly`, `TestZeroCapCountsWithoutEnforcing`) and
`service/spend_test.go` (`TestSpendGateClosesOnceTheCounterReachesTheCap`,
`TestSpendGateReadsTheCounterWithoutSpendingAgainstIt`,
`TestSpendGateSurfacesACounterFailureRatherThanClosingTheDoor`).

## The tenant's rows

Accounting, not enforcement: nothing here refuses a call, nothing here is
read on the capture path, and a failure here never fails a capture. Both
rows live in the tenant's own partition, so `chintanctl export`, `backup` and
`erase` carry them in the partition walk without learning a new kind, and an
erased tenant takes its attribution with it while the money stays counted on
`INSTANCE / SPEND#`.

| pk | sk | keeps | TTL |
|---|---|---|---|
| `USER#<tenant>` | `USAGE#<yyyy-mm>` | the month's totals; `gsi1pk USAGE#<yyyy-mm>`, `gsi1sk TENANT#<tenant>` for the listing | none — billing history |
| `USER#<tenant>` | `USAGE#<yyyy-mm-dd>` | one day's totals | `dayRetention`, 400 days |

The month row sorts before its days, so a month is one `Query` with
`begins_with(sk, "USAGE#<yyyy-mm>")`. Attributes are flat, because DynamoDB
cannot `ADD` into a map path that does not exist: one unconditional
`UpdateItem` with a single `ADD` moves every counter a call touches, atomic
per row, so two captures finishing at once cannot lose an increment.

```
type "usage" · tenant_id · period "<YYYY-MM>" | "<YYYY-MM-DD>" · granularity "month" | "day" · ttl (day rows)

cost_micros  calls  audio_seconds (3 decimals)  input_tokens  output_tokens
op_<op>_<counter>              the same five per op
provider_<name>_<counter>      the same five per provider; <name> is the price
                               key lowercased to letters and digits (providerName)
api_requests                   day rows; the month's figure is their sum
storage_byte_days  storage_note_days  storage_snapshot_day   see Storage
```

**A provider call** (`usage.Dynamo.Record`, called from `breaker.record`)
is two `UpdateItem`s, month then day — four to six per capture, eight when
a recording made into a note carries a spoken instruction and pays a
routing-priced call, each under 1 KB. They are not a transaction: a failure
between them leaves the month one call ahead of its days, which a reader can
see and the log line can repair, and a `TransactWriteItems` would double the
write units of every provider call to prevent it. A failed record is logged
(`failed to record tenant usage`) and counted as `UsageRecordFailures{Op}`;
the `provider usage` line is the record of last resort. The tenant is an
explicit field on `breaker.Estimate`, set by every stage, rather than read
off the context, because the context's tenant is a logging convenience
(`obs.WithTenant`) and a cost record must not depend on it; a call with no
tenant is logged and skipped rather than written to an anonymous row.
`TestRecordWritesMonthAndDayRowsAsOneADDEach` and
`TestRecordCountsTheCallUnderItsProvider` hold the shape.

**An API request** (`handler.router.counted` → `usage.Dynamo.CountRequest`)
is one `ADD` of `api_requests` on the **day** row after the handler has
answered. The day's first request — the one whose `ADD` made the count 1 —
also writes the month row, an `ADD` of zero carrying the GSI1 keys, so a
tenant who only read is still in the listing; every later request of the day
is the one write. One write, not two, because this runs on the request path
of every authenticated call, polls included, and the counter must not be
most of the time of the requests it counts. Health routes are public and
never reach the wrapper; a 401 is not counted; a count that fails is logged
with `UsageRecordFailures{Op=api_request}` and the response goes out
unchanged. The number measures traffic, not actions: `GET /v1/captures`
while a recording is processing and `GET /v1/ask/{id}` while a question is
pending are in it, and the screen says so. The month's `api.requests` is the
sum of its day rows on read, so a month older than 400 days reads zero
requests.
`TestCountRequestWritesTheDayRowAndTheMonthRowOnlyOnTheDaysFirstRequest`.

## The instance's AWS cost

The provider figure is what speech cost at Groq and MiniMax. Lambda,
DynamoDB, S3, Cognito and CloudWatch are billed by AWS to the account, and
`GET /v1/usage` answers that too, as `aws`, from the stack's own
`AWS::Budgets::Budget` (`MonthlyBudget`, limit `MonthlyBudgetUSD`, default
$10; it exists only with an alarm address, `HasAlarmEmail`). AWS keeps a
budget's `CalculatedSpend.ActualSpend` current up to three times a day and
`budgets:DescribeBudget` is free; Cost Explorer gives the same figure by
service at $0.01 a request, and nothing here calls it. The grant is
`budgets:ViewBudget` on that one budget's ARN, on the worker's role only
(`WorkerReadsBudgetPolicy`). The budget is account-scoped on purpose (a
tag-filtered budget reads zero until the tag is activated for cost
allocation), so the figure is the account's bill: the instance's cost on a
dedicated account, an upper bound on a shared one.

The daily `aws-cost` task (`awscost.Collector.Run`) reads
`MONTHLY_BUDGET_NAME` — `!Ref MonthlyBudget`, or `''` when the stack has no
budget, in which case it logs at INFO and returns — calls `DescribeBudget`,
converts `ActualSpend.Amount` and `BudgetLimit.Amount` to microdollars digit
by digit (`usdToMicros`: exact by construction, truncated below the sixth
decimal; `TestUSDToMicrosIsExact`) and `PutItem`s the row `pk INSTANCE`,
`sk AWSCOST#<yyyy-mm>` — `type "aws_cost"`, `month`, `month_micros`,
`budget_micros` (absent when the budget has no limit), `as_of` (RFC 3339).
`PutItem` rather than `ADD` because the latest reading is the only one
anybody wants, so a retry is harmless; no TTL, because twelve rows a year is
billing history. Only a failed AWS call is returned for Lambda to retry
(`TestRunRetriesOnlyOnAnAWSFault`); a budget with no spend figure yet, or
one not in USD, is logged and dropped, and the API keeps answering `null`
for the month. The row is keyed by the UTC month at the moment of the run
(`TestRunKeysTheRowByTheUTCMonth`), which is Budgets' calendar too, so the
last reading of a month is whatever the last daily run saw; `as_of` is on
the wire so the screen can say how old the figure is.

**The tenant's share.** `aws.month_micros` is the same for every tenant.
`aws.share_micros` is that figure × the tenant's provider cost for the month
÷ the instance's provider spend for the month, rounded half up in exact
integer arithmetic (`shareOf`, `handler/usage.go`); `aws.share_basis` is
`"provider_cost"`, named on the wire so a different rule can be told apart.
The denominator is the sum of the month's `SPEND#<day>` counters
(`usage.Dynamo.InstanceSpend`), the number the cap is enforced against, so
every tenant's share adds up to the bill. Both are `null` when the instance
spent nothing at the providers. The fraction is clamped to one: the tenant's
month row never expires while the counters expire after 90 days, so a month
two or three back has a denominator missing days the numerator still counts
(`TestShareOfIsClampedToTheWholeMonth`). The rule is a proxy — a tenant that
caused 40 % of the provider spend caused roughly 40 % of the Lambda seconds
behind it — which is why the basis is named rather than implied.

## Storage

`storage` on `GET /v1/usage` is the footprint when the request is served:
`service.StorageService.Summarize` walks the tenant's capture index rows
(recordings, their summed `DurationMS` and `AudioBytes`, the size S3 reported
when the pipeline started) and active notes, each walk capped at
`maxStorageRows` (2,000); hitting the cap sets `approximate` and the numbers
are a floor. Nothing is stored for it: a running total would be one more
counter to keep in step with every delete and move.

A footprint cannot say what a month of storage cost — a tenant who deletes
everything on the 30th reads zero on the 31st though S3 billed every day —
and storage is priced in byte-months, so the daily `storage-snapshot` task
(`storagesnap.Snapshotter.Run`) measures the same footprint and **adds** it
onto the month: `storage_byte_days += audio_bytes`, `storage_note_days +=
notes` on `USAGE#<yyyy-mm>`, and the day's own reading as a `SET` on
`USAGE#<yyyy-mm-dd>` (`usage.Dynamo.AddStorageDay`). The month write is
conditioned on `attribute_not_exists(storage_snapshot_day) OR
storage_snapshot_day < :day` and sets the day in the same write, so a second
run on the same day — a Lambda retry, a rule that fired twice, an operator —
fails the condition and adds nothing; the failure is the answer (`added =
false`), not an error (`TestAddStorageDayAddsToTheMonthOnceADay`,
`TestRunIsIdempotentWithinADay`). The writer builds its own expressions
rather than calling the shared `update` helper, because the month write
needs the condition and the `SET` beside its `ADD` and the helper offers
neither. The day row is written regardless, so a
run that stored the month and died is finished by the retry; month first,
day second, so a fault between them leaves the billable figure right and the
chart short by a day rather than the reverse. A tenant with nothing stored
is skipped rather than written as zeros, which would keep a departed tenant
in the month rows for ever.

Which tenants exist is the one hard question in a single table with no Scan
grant: the task queries GSI1 for `gsi1pk = USAGE#<month>` over the current
month and the twelve before it (`lookbackMonths` = 13,
`usage.Dynamo.TenantsWithUsage`) and takes the union. Every authenticated
request writes that month row, and so does this task, so a tenant once
snapshotted stays in the set for as long as month rows are kept, which is
for good. The gap: a tenant with notes in the bucket and no usage row in
thirteen months is not counted until they touch the app. Closing it needs a
tenant registry row written at first sign-in, or a Scan.

No price is attached server-side. The screen prices byte-days as GB-months —
divide by the days in the month — at `S3_STANDARD_USD_PER_GB_MONTH` (0.023,
`usage.ts`) per binary gigabyte (`BYTES_PER_GB` = 1024³, what the bill calls
a GB), names the rate on screen and calls it an estimate: the bill is in the
account's region and storage class, after the free tier, with request and
transfer costs the figure knows nothing about. A figure under 500 µ$ reads
"under $0.001" (`formatEstimatedDollars`) rather than rounding a real cost
to nothing.

The run costs thirteen index queries, one `Summarize` per tenant (two
bounded partition reads) and two `UpdateItem`s per tenant, once a day. If
that ever matters, the walk is what to bound: snapshot fewer tenants a day,
not fewer days a tenant.

## The scheduled tasks

Both are EventBridge rules invoking the worker's live alias with a `task`
payload; `cmd/worker/main.go`'s `scheduled` map dispatches on it (the weekly
`sweep-expired` beside them is `retention.md`'s). Each returns an error only
for a fault a retry can fix, so Lambda retries it and a run that exhausts
its attempts lands in `CaptureDLQ` under the dead-letter alarm (README
"Alarms").

| Task | Rule | Schedule | Writes |
|---|---|---|---|
| `aws-cost` | `AwsCostRule` | `rate(1 day)` | `INSTANCE / AWSCOST#<yyyy-mm>` |
| `storage-snapshot` | `StorageSnapshotRule` | `cron(0 3 * * ? *)` | `storage_*` on every tenant's month and day rows |

`AwsCostRule` is not conditional on `HasAlarmEmail`: without a budget the
task has nothing to read and says so, which is one free invocation a day
and one fewer conditional to keep in step. `StorageSnapshotRule` is a fixed
hour rather than `rate(1 day)` because a rate schedule fires at the minute
of the rule's creation, and a rule created within seconds of midnight UTC
would take its reading on either side of the day boundary from one run to
the next, attributing a day's storage to its neighbour; 03:00 UTC is far
from the boundary and from the cost reading. Both tasks stamp the UTC day or
month at the moment they run.

## Reading: `GET /v1/usage`

`getUsage` (`handler/usage.go`) answers the caller's own month —
`?month=yyyy-mm`, default the current UTC month, the calendar the rows are
kept in — by joining `usage.Dynamo.Month` (one `Query`), `AWSCost` and
`InstanceSpend`, and `StorageService.Summarize`. A month with no rows is
zeros with empty `ops`, `providers` and `days`, not 404: a new user's screen
is not an error. 503 when the instance has no usage store. Microdollars
throughout, the unit the cap uses. Every member is required on the wire so
the client never guesses (`Usage` in `openapi.yaml`; the contract fixtures
hold it):

```json
{
  "month": "<YYYY-MM>",
  "cost_micros": 40791, "calls": 118, "audio_seconds": 1391.2,
  "input_tokens": 84210, "output_tokens": 15332,
  "ops": {
    "transcribe": {"cost_micros": 20230, "calls": 50, "audio_seconds": 1391.2},
    "route":      {"cost_micros": 9497,  "calls": 18, "input_tokens": 23188, "output_tokens": 2201},
    "cleanup":    {"cost_micros": 11064, "calls": 50, "input_tokens": 61022, "output_tokens": 13131}
  },
  "providers": {
    "groq":   {"cost_micros": 20230, "calls": 50, "audio_seconds": 1391.2},
    "openai": {"cost_micros": 20561, "calls": 68, "input_tokens": 84210, "output_tokens": 15332}
  },
  "days": [
    {"date": "<YYYY-MM-DD>", "cost_micros": 40791, "calls": 118, "audio_seconds": 1391.2,
     "input_tokens": 84210, "output_tokens": 15332, "api_requests": 312, "storage_byte_days": 9123456}
  ],
  "api": {"requests": 312},
  "storage": {"recordings": 41, "audio_seconds": 1391.2, "audio_bytes": 9123456, "notes": 12,
              "approximate": false, "byte_days": 27370368, "note_days": 36},
  "aws": {"month_micros": 2345678, "as_of": "<YYYY-MM-DD>T06:15:09Z", "budget_micros": 10000000,
          "share_micros": 2345678, "share_basis": "provider_cost"}
}
```

- `ops` and `providers` are recovered from the `op_*` and `provider_*`
  attributes by prefix (`splitOf`), so a new op or provider appears as new
  attributes on the next call and needs no reader change. A key with no call
  in the month is absent; the objects are empty, never null.
- `days` has one entry per day with any usage, any request or a snapshot, in
  date order; a day with requests and no calls is still a day.
- `aws` is `null` — key present, value null — when nothing is recorded for
  the month: no budget, or the task has not run since the month began. Null
  and zero are different answers and the screen tells them apart.

## What `/usage` shows

The route is `/usage` (`ROUTES.usage`), its own screen (`UsageScreen.tsx`,
"Usage" with a "‹ You" link, loaded lazily so Home never pays for it) reached
from one row on You; `UsageSection.tsx` is the card, "Usage this month",
fetched by `useUsage` (`api/queries/settings.ts`, `staleTime` 60 s). It reads
top to bottom the way a bill does:

- **Head.** The month as an eyebrow (`monthLabel`), the providers' figure
  large (`formatDollars`: three decimals under a dollar because a month costs
  cents, two from a dollar up), then "118 calls · 23.2 min of audio" or "No
  recordings have been processed this month yet."
- **Three cells.** *Providers*: the figure again with one line per provider,
  biggest first (`providerRows`), and an "Earlier this month" line for any
  remainder the provider rows do not account for (`unattributed`, derived
  from the data). *AWS*: `month_micros` with `asOfLabel` ("as of 3 hours
  ago", coarse on purpose), "· of $10.00 budget" when the budget has a limit,
  and "Your estimated share: $… (by provider spend)" when `share_micros` is
  set; "Not recorded yet" when `aws` is null. *Total*: providers plus the
  share when there is one ("providers + your AWS share"), else plus the
  instance figure ("providers + instance AWS"); the cell is left out when
  `aws` is null (`combinedMicros`, `totalBasis`) rather than repeat the
  providers' figure under a heading that promises more.
- **Stages.** One row per op that ran, in pipeline order then the two
  hand-asked calls — Transcribe, Route, Clean up, Clean note, Ask (`OPS`,
  `opRows`); an op the screen has no name for is shown by its key.
- **The strip.** `Sparkline`: one bar per calendar day of the month, cost as
  height, today in the accent, empty for days with no row and for the days
  still to come, so the strip also shows how far into the month it is
  (`dayBars`); a dot per day on its own scale for that day's API requests,
  so a day of reading with no cost still leaves a mark. Every bar carries a
  `<title>`; the figure is described in words for a screen reader.
- **Facts.** "API requests · every call the app made, polling included",
  "Recordings stored" (count · minutes · MB, "· approx." when the walk
  capped), "Notes", and "Stored this month" in GB·days with the estimate and
  the rate named.

The foot says what the two figures are: providers are what transcription and
the language model charged for this account; AWS is what it costs to run the
instance for everyone, updated once a day, and the share is estimated from
this account's part of the provider spend. Tests: `usage.test.ts` (the
arithmetic and the calendar on a fixed clock), `UsageScreen.test.tsx`.

## The operator's listing

Nothing cross-tenant is on the API: every route is scoped by the Cognito
`sub`, and an admin route would need an admin notion the API does not have.
`chintanctl usage --instance <name> [--month yyyy-mm] [--tenant <id>]…` is
the operator's view, read-only: a `Query` on GSI1 for `gsi1pk = USAGE#<M>`
names the tenants (the index projects none of the counters, so a hit is its
keys), then each tenant's month row and day rows in one prefix read; it
prints tenant, cost, calls, audio minutes, API requests (the sum of the days,
as the API reads it) and the per-op cost, as a table or `--json`; `--tenant`
skips the index. `TestUsageListsTheMonthRowsInternalUsageWrites` seeds rows
through the real writer so the two spellings of the row cannot drift.

## Deliberately not built

- **No per-tenant cap.** `SPEND#` is instance-wide and the tenant rows are
  accounting only; a second cap is a second copy of the breaker's machinery
  for a question a single-user instance does not ask.
- **No per-capture usage rows.** The `provider usage` log line has the
  capture's correlation id and the rows have the totals; a third copy of the
  number answers nothing new.
- **No Cost Explorer, no per-service AWS breakdown.** One free
  `DescribeBudget` a day answers "what does the instance cost"; the share is
  a proportion of it, not a second reading.
- **No stored storage totals.** The footprint is computed on read and says
  when it is approximate; what is stored is one reading a day of it, which
  is a counter of what was held, not a copy of what is held.
- **No backfill.** The log group's retention is `LogRetentionDays` (default
  14) and the rows are cheap; history starts at the deploy that writes them.

History: `docs/backlog.md` (U13, U13b, D6b, O4, S15, S21, S26, N11).
