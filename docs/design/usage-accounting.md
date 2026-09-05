# Per-tenant usage accounting

Status: implemented for recording and self-service reading (2026-09-04);
the instance's AWS cost beside it (2026-09-04, D6b); per-provider split,
API request counting, storage summary, the tenant's share of the AWS cost and
the `chintanctl usage` admin listing (2026-09-05); storage over time, from a
daily snapshot (2026-09-05, O4).

## Why this exists, and what it is not

The instance has one spend cap and one counter: `INSTANCE / SPEND#<day>`,
written by the breaker before and after every priced provider call, compared
against `DailySpendCapMicros`. That answers "is this instance over budget
today" and nothing else. The 2026-09-03 review removed the earlier per-tenant
`USAGE` rows because they existed to enforce a second cap nobody needed, and
dragged a resolver interface and a usage sink along with them.

What the owner wants now is different: **visibility** of what each user's
captures cost, per month, for an eventual admin view and, later, billing.
No enforcement, no dashboard yet, and no second copy of the cap machinery.

So this is accounting only:

- It never refuses a call. The breaker's cap logic is untouched.
- It is never read on the capture path.
- Its failure never fails a capture. The breaker logs `failed to record
  tenant usage`, bumps `UsageRecordFailures`, and the `provider usage` log
  line — which the breaker has always written — remains the record of last
  resort.

## Data model

Both rows live in the tenant's own partition, next to the tenant's notes and
captures:

| pk | sk | keeps | TTL |
|---|---|---|---|
| `USER#<tenant>` | `USAGE#<yyyy-mm>` | the month's totals | none — billing history |
| `USER#<tenant>` | `USAGE#<yyyy-mm-dd>` | one day's totals | 400 days |

Putting them in `USER#` rather than in `INSTANCE` is deliberate. The
partition walk that `chintanctl export`, `backup` and `erase` already do
carries them without learning a new kind (they show up as an unknown sort
key and are copied verbatim, which is the tool's documented behaviour), and a
tenant that is erased takes its attribution with it — the money is still
counted in `INSTANCE / SPEND#`, which is the row that does not belong to any
tenant.

The month row sorts before its days (`USAGE#2026-09` < `USAGE#2026-09-01`),
so `GET /v1/usage?month=2026-09` is one `Query` with
`begins_with(sk, "USAGE#2026-09")` and no second read.

### Attributes

Flat, not nested. DynamoDB cannot `ADD` into a map path that does not exist,
so a nested `ops.transcribe.cost_micros` would need a read or a two-step
write per call. Flat attributes are one unconditional `UpdateItem` with a
single `ADD` clause listing every counter the call moves — atomic per row,
so two captures finishing at once cannot lose an increment.

```
type            "usage"
tenant_id       <tenant>
period          "2026-09" | "2026-09-04"
granularity     "month" | "day"
ttl             (day rows only)

cost_micros     N   reconciled cost, microdollars
calls           N
audio_seconds   N   3 decimals
input_tokens    N
output_tokens   N

op_<op>_cost_micros      N   the same five, per operation:
op_<op>_calls            N   <op> ∈ transcribe | route | cleanup | clean_note | ask
op_<op>_audio_seconds    N   (only the units the operation consumes)
op_<op>_input_tokens     N
op_<op>_output_tokens    N

provider_<name>_cost_micros    N   the same five again, per provider:
provider_<name>_calls          N   <name> ∈ groq | openai — the name the price
provider_<name>_audio_seconds  N   table is keyed on, lowercased, letters and
provider_<name>_input_tokens   N   digits only
provider_<name>_output_tokens  N

api_requests    N   authenticated API requests (2026-09-05; see below)

gsi1pk          "USAGE#<yyyy-mm>"     month row only — see admin listing
gsi1sk          "TENANT#<tenant>"     month row only
```

The op and provider splits are written by the same single `ADD` as the row
totals: one `UpdateItem` per row moves cost, calls, the units, and all three
copies of each. A new operation or provider appears as new attributes on the
next call and the readers recover both splits by attribute-name prefix, so
`clean_note` and `ask` needed no change here.

A future admin query reads this cheaply: every number it wants is a
top-level attribute on one item, readable with a `ProjectionExpression`, and
the per-op split is recovered by attribute-name prefix without a schema.

### Write path

`breaker.Do` settles a call — reserves, runs it, reconciles the reservation
against what the provider reported, writes the `provider usage` log line —
and then, when built `WithUsage`, calls `usage.Recorder.Record` once with the
tenant, the UTC day the `SPEND#` counter used, the provider, the op, the
reconciled cost and the reconciled quantities. `usage.Dynamo.Record` turns
that into two `UpdateItem`s, month then day.

The two writes are not one transaction. A failure between them leaves a
month one call ahead of its days — visible to a reader, recoverable from the
log — and a `TransactWriteItems` would double the write units of every
provider call to prevent it. Two plain `ADD`s is the right trade for an
accounting row.

The tenant is an explicit field on `breaker.Estimate`, set by every pipeline
stage, rather than read off the context: the context's tenant is a logging
convenience (`obs.WithTenant`) and a cost record should not depend on it.

Cost per capture: two `UpdateItem`s per provider call, i.e. four to six per
capture — eight for a recording made into a note whose transcript carries a
spoken instruction, which adds one routing-priced span call — each under 1 KB. At today's rate (~50 captures/day) that is well
inside the table's on-demand noise.

### API requests (2026-09-05)

Every authenticated request the tenant makes adds one to `api_requests` on the
**day** row: one unconditional `ADD`, from the API's per-route wrapper
(`handler.router.counted`) after the handler has answered. The month's
`api.requests` is the sum of its day rows on read, so the month row is not
written per request; the day's first request (the `ADD` that made the day's
count 1) writes it once, an `ADD` of zero carrying the GSI1 keys, so a tenant
who only read is still in the `chintanctl usage` listing. Health routes are
public and never pass through the wrapper; a 401 is never counted. A count
that could not be written is logged (`UsageRecordFailures{Op=api_request}`)
and the response goes out unchanged — a request must not fail over a row
that exists only to describe it.

What the number is: every authenticated request, the app's own polling
included — `GET /v1/captures` every 1.5–4 s while a recording is processing,
`GET /v1/ask/{id}` every 1–2 s while a question is pending — so it measures
traffic to the API, not actions taken. On 2026-09-04 polls were about 40% of
it.

Cost: one `UpdateItem` per request, under 1 KB, on the request path. It was
two until 2026-09-05; measured around the deploy that introduced the counter,
the pair took the `GET /v1/captures` poll from a 4 ms warm p50 to 14 ms (prod
API access log, `duration_ms`, 579 warm polls before vs 28 after), which was
most of the time of the requests it counted. Months up to that date carry a
month-row count from the two-write period; it is ignored on read. The day
rows' 400-day retention bounds the sum: a month older than that reads zero
requests. If one write is ever too many, the answer is to count once per
invocation in a buffer and flush, not to stop counting.

### Read path

`GET /v1/usage?month=yyyy-mm` (default: the current UTC month) answers the
caller's own month:

```json
{
  "month": "2026-09",
  "cost_micros": 40791, "calls": 118, "audio_seconds": 1391.2,
  "input_tokens": 84210, "output_tokens": 15332,
  "ops": {
    "transcribe": {"cost_micros": 20230, "calls": 50, "audio_seconds": 1391.2},
    "route":      {"cost_micros": 9497,  "calls": 18, "input_tokens": 23188, "output_tokens": 2201},
    "cleanup":    {"cost_micros": 11842, "calls": 50, "input_tokens": 61022, "output_tokens": 13131}
  },
  "days": [{"date": "2026-09-04", "cost_micros": 40791, "calls": 118, "…": "…"}]
}
```

A month with no rows is zeros with empty `ops` and `days`, not 404: a new
user's screen is not an error. Microdollars throughout, the unit the cap and
`daily_spend_cap_micros` already use, so a "You" screen can show spend against
budget without converting.

Since D6b the same response carries `aws` — see the next section — and since
2026-09-05 four more members, all required so the client never guesses:

```json
{
  "providers": {"groq": {"cost_micros": 20230, "calls": 50, "audio_seconds": 1391.2},
                "openai": {"cost_micros": 20561, "calls": 68, "input_tokens": 84210, "output_tokens": 15332}},
  "days":  [{"date": "2026-09-04", "cost_micros": 40791, "calls": 118, "api_requests": 12, "…": "…"}],
  "api":   {"requests": 312},
  "storage": {"recordings": 41, "audio_seconds": 1391.2, "audio_bytes": 9123456, "notes": 12, "approximate": false}
}
```

- `providers` is the `provider_<name>_*` split; a provider with no call in
  the month is absent, and the object is empty rather than null.
- `days[].api_requests` are the day counters above and `api.requests` is their
  sum; a day with requests and no calls is still a day.
- `storage` is computed when the request is served, from the tenant's capture
  and note index rows: recordings and their summed duration and uploaded size
  (`CaptureIndex.AudioBytes`, stamped by the worker from the S3 notification
  that started the pipeline — a recording processed before 2026-09-05 reads
  zero), and the active notes. Each walk is capped at 2,000 rows; hitting the
  cap sets `approximate` and the numbers are a floor. Nothing is stored for
  it: a running total would be one more counter to keep in step with every
  delete and move.

## The AWS cost beside it (D6b)

The provider figure is what the user's speech cost in Groq and MiniMax. It is
not what the instance costs: Lambda, DynamoDB, S3, Cognito, CloudWatch and
the rest are billed by AWS to the account, and until now the only place that
number existed was the console. `GET /v1/usage` now answers it too, as
`aws`, so the You screen can put "AWS this month" under the provider line:

```json
{
  "month": "2026-09",
  "cost_micros": 40791, "...": "...",
  "aws": {"month_micros": 2345678, "as_of": "2026-09-04T06:15:09Z", "budget_micros": 10000000}
}
```

`aws` is `null` — the key present, the value null — when nothing has been
recorded for the month: the stack has no budget, or the task below has not
run since the month began. Null and zero are different answers and the
frontend is meant to tell them apart.

### Where the number comes from

The stack's own `AWS::Budgets::Budget` (`MonthlyBudget`, declared whenever
there is an alarm address). AWS keeps a budget's `CalculatedSpend.ActualSpend`
current — the account's month-to-date actual cost, refreshed up to three
times a day — and `budgets:DescribeBudget` is free. Cost Explorer would give
the same figure by service, and charges $0.01 per request for it; nothing in
this system calls Cost Explorer, and the worker role has no `ce:` grant.

The budget is account-scoped on purpose (its comment in the template says
why: a tag-filtered budget reads zero until the tag is activated for cost
allocation). So the figure is **the account's** bill. On an account that runs
only this instance that is the instance's cost; on a shared account it is an
upper bound, which is still the honest number to show.

### The task

A third worker task, `{"task":"aws-cost"}` (`internal/awscost`), from
`AwsCostRule` — `rate(1 day)`, same target and retry story as the weekly
sweep. It:

1. reads `MONTHLY_BUDGET_NAME` from the environment — `!Ref MonthlyBudget`,
   which is the budget's generated name, or `''` when the stack has none —
   and, with no name, logs at INFO that there is nothing to read and returns;
2. calls `DescribeBudget(AccountId, BudgetName)`;
3. converts `ActualSpend.Amount` (a decimal string in USD) to microdollars
   digit by digit — exact by construction, truncated below the sixth decimal,
   which is below what AWS bills — and `BudgetLimit.Amount` the same way;
4. writes the reading under the **current UTC month**.

The row: `pk = INSTANCE`, `sk = AWSCOST#<yyyy-mm>`, attributes `type =
"aws_cost"`, `month`, `month_micros`, `budget_micros` (absent when the
budget has no limit), `as_of` (RFC 3339, when it was read). `PutItem`, not
`ADD`: the latest reading is the only one anybody wants, so the daily
overwrite is the point and a retried invocation is harmless. No TTL —
twelve rows a year is billing history.

It lives on `INSTANCE` for the same reason `SPEND#<day>` does. The number
belongs to the account, not to a tenant; chintanctl's per-tenant export,
backup and erase must neither carry it nor delete it; and a tenant leaving
does not un-spend it.

Only a failed AWS call is returned as an error for Lambda to retry (and
dead-letter to `CaptureDLQ`, under the existing alarm). A budget that has no
`CalculatedSpend` yet — one created hours ago — or one whose unit is not USD
is logged and dropped: retrying cannot change either, and the API keeps
answering null until a later run succeeds.

### Month boundaries and staleness

The row is keyed by the wall-clock UTC month at the moment of the read, which
is also Budgets' calendar. The last reading of a month is therefore whatever
the last daily run saw, up to a day before the month ended; the first reading
of the next month is small. `as_of` is on the wire precisely so the screen can
say "as of the 4th" rather than imply the figure is live. If a month-end
figure ever matters (billing), the fix is a second read of the previous
period on the 1st, not a shorter interval.

### The tenant's share (2026-09-05)

`aws.month_micros` stays instance-level: every tenant sees the same figure.
Beside it, `aws.share_micros` is that figure × the tenant's own provider cost
for the month ÷ the instance's provider spend for the month, rounded to the
nearest microdollar, and `aws.share_basis` is `"provider_cost"` — the one
apportionment rule there is, named on the wire so a different one can be told
apart later. The denominator is the sum of the breaker's `INSTANCE /
SPEND#<day>` counters for the month (`usage.Reader.InstanceSpend`), read with
one `begins_with` Query; it is the number the cap is enforced against, so the
shares of every tenant add up to the bill. Both members are null when the
instance spent nothing at the providers — there is then nothing to apportion
by — and the counters expire after ninety days, so a month older than that
reads null as well. The rule is a proxy (a tenant that caused 40% of the
provider spend caused roughly 40% of the Lambda seconds behind it), which is
why the basis is named rather than implied.

### The spend cap stays, and leaves the You screen (U13b)

`daily_spend_cap_micros` is unchanged server-side: the breaker still refuses a
provider call that would take the day's `SPEND#` over it, the API still
reports the instance value read-only on Settings, and the template still sets
it. It is a **runaway guard** — set at roughly a hundred times a normal day's
spend so that a retry loop, a stuck recording or a leaked key cannot run up a
bill overnight — not a budget anybody plans against. Showing "cap $X/day" next
to a month's spend of a few cents made it read as one, so the You screen no
longer shows it; a single line on About is enough for an operator to confirm
the guard is on. Nothing about that is an API change, which is why this
section is a paragraph and not a migration.

## The admin listing

Nothing cross-tenant is exposed on the API. The operator's listing is
`chintanctl usage --instance <name> [--month yyyy-mm] [--tenant <id>]…`
(2026-09-05), read-only:

1. **Which tenants had usage in month M** is a `Query` on GSI1:
   `gsi1pk = "USAGE#<M>"`. The month rows have carried these keys since the
   accounting shipped, so there was no backfill and no template change. GSI1
   is an `INCLUDE` projection of capture attributes, so the query returns
   keys only (tenant id from `gsi1sk`); the command then does one `GetItem`
   per tenant for the numbers. For tens of tenants that is the right cost. If
   it ever becomes hundreds, the alternative is an `ADMIN#USAGE#<M>` aggregate
   row updated by the same breaker `ADD` — a third `UpdateItem` per call — or
   a second GSI projecting the counters; both are additive.
2. It prints tenant, cost, calls, audio minutes, API requests and the per-op
   cost, as a table or `--json`, and recovers the per-op split by attribute
   prefix as the API does. `--tenant` reads the named rows and skips the
   index.
3. **An API route** for the same listing would need an admin notion this API
   does not have: every route is tenant-scoped by the Cognito `sub`. The
   smallest honest version is a Cognito group (`admins`) checked in the
   middleware, and a `GET /v1/admin/usage?month=` route returning what the
   command prints. Nothing about the rows changes for that.
4. **Billing** would read the month rows and nothing else. The day rows exist
   for charts and expire.

## Deliberately not built

- No per-capture usage rows. The `provider usage` log line has the capture's
  correlation id and the day/month rows have the totals; a per-capture row
  is a third copy of a number for a question nobody has asked.
- No enforcement from these rows. The cap is `SPEND#`, instance-wide, and
  stays that way; a per-tenant cap is exactly the machinery step 3 removed.
- No backfill. The log group's retention is 14 days and the rows are cheap;
  history starts at the deploy that ships this.
- No admin endpoint, no admin UI. The API for the caller's own month is what
  a "You" screen needs; `chintanctl usage` is the operator's view, and the
  design above is what an admin page would add.
- No Cost Explorer, no per-service AWS breakdown. One free `DescribeBudget` a
  day answers the question that was asked; the per-tenant share is a
  proportion of it, not a second reading.
- No stored storage totals. The summary is computed from the index rows on
  read, capped, and says when it is approximate. What *is* stored is one
  reading a day of that summary, added onto the month — see the next section
  — which is a different thing: a counter of what was held, not a copy of
  what is held.

## Storage over time (O4, 2026-09-05)

`storage` on `GET /v1/usage` is the footprint when the request is served. The
owner deleted every note on the 5th and it read zero, although the bucket had
held the audio all month and S3 had billed the account for every day of it.
Storage is priced in byte-months; a point-in-time figure cannot say what a
month of it cost. So the same footprint is now also **measured once a day and
added onto the month**:

```
storage_byte_days      N   Σ over the month's snapshots of audio_bytes that day
storage_note_days      N   Σ over the month's snapshots of active notes that day
storage_snapshot_day   S   yyyy-mm-dd of the last snapshot the month row took
```

on the tenant's `USAGE#<yyyy-mm>` row, and on `USAGE#<yyyy-mm-dd>` the day's
own reading (`storage_byte_days`, `storage_note_days`, a SET, not a sum).

### The task

A fourth worker task, `{"task":"storage-snapshot"}` (`internal/storagesnap`),
from `StorageSnapshotRule` — `rate(1 day)`, the same target, retry story and
IAM as `AwsCostRule`. It:

1. **names the tenants** — the one hard question in a single-table design
   with no Scan grant. It queries GSI1 for `gsi1pk = USAGE#<month>` for the
   current month and the twelve before it (`usage.Dynamo.TenantsWithUsage`,
   the admin listing's query) and takes the union. Every authenticated API
   request writes that month row, and so does this task, so a tenant who has
   been snapshotted once is named by this month's row tomorrow and stays in
   the set for as long as month rows are kept, which is for good. **The gap:**
   a tenant with notes in the bucket and no usage row in thirteen months —
   nobody today, since request counting shipped on 2026-09-05 and the
   snapshot names everyone it has ever measured — is not counted until they
   touch the app. Closing it would need a tenant registry row or a Scan; the
   registry is the right shape if it is ever needed, written at first sign-in.
2. **measures** each tenant with `service.StorageService.Summarize`, the walk
   `GET /v1/usage` makes — capped at 2,000 rows each, `Approximate` when it
   bites, logged but not stored.
3. **adds** the reading (`usage.Dynamo.AddStorageDay`). A tenant with nothing
   stored is skipped rather than written as zeros: zeros would keep a departed
   tenant in the month rows for ever, and there is nothing to bill.

### Idempotent per day

The month write is `ADD storage_byte_days :bytes, storage_note_days :notes
SET storage_snapshot_day = :day …` under
`attribute_not_exists(storage_snapshot_day) OR storage_snapshot_day < :day`.
A second run on the same day — Lambda's retry after a fault, a rule that fired
twice, an operator invoking it by hand — fails the condition and adds nothing;
the failure is the answer (`added = false`), not an error. The day row is then
written regardless, a plain SET of today's footprint over today's footprint,
so a run that stored the month and died before the day row is finished by the
retry. The writer builds its own expressions with fresh maps per call; the
shared `update` helper adds into its caller's maps, which is a defect being
fixed separately, and this writer does not depend on it.

The order is month first, day second, so a fault between the two leaves the
billable figure right and the chart short by a day, rather than the reverse.

### On the wire

`storage.byte_days` and `storage.note_days` (required; 0 until the month's
first snapshot) and `days[].storage_byte_days` (required; 0 on a day the task
did not run). **No price is attached server-side.** The You screen prices
byte-days as GB-months — divide by the days in the month — at
`S3_STANDARD_USD_PER_GB_MONTH = 0.023` (`frontend/src/features/settings/usage.ts`),
names the rate on screen and calls it an estimate: the bill is in the
account's region and storage class, after the free tier, and includes request
and transfer costs the figure knows nothing about. A month of personal
recordings is a few hundred microdollars, so the screen says "under $0.001"
rather than rounding a real cost to nothing.

### Cost

Thirteen index queries, one `Summarize` per tenant (two bounded partition
reads) and two `UpdateItem`s per tenant, once a day. For tens of tenants that
is noise. If it ever is not, the walk is what to bound — snapshot fewer
tenants a day, not fewer days a tenant.
