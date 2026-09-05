# Per-tenant usage accounting

Status: implemented for recording and self-service reading (2026-09-04);
the instance's AWS cost beside it (2026-09-04, D6b); per-provider split,
API request counting, storage summary, the tenant's share of the AWS cost and
the `chintanctl usage` admin listing (2026-09-05).

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
capture, each under 1 KB. At today's rate (~50 captures/day) that is well
inside the table's on-demand noise.

### API requests (2026-09-05)

Every authenticated request the tenant makes adds one to `api_requests` on the
same month and day rows: two unconditional `ADD`s, from the API's per-route
wrapper (`handler.router.counted`) after the handler has answered. Health
routes are public and never pass through it; a 401 is never counted. A count
that could not be written is logged (`UsageRecordFailures{Op=api_request}`)
and the response goes out unchanged — a request must not fail over a row
that exists only to describe it. The month row created by a request alone
carries the GSI1 keys too, so a tenant who only read is still in the listing.

Cost: two `UpdateItem`s per request, each under 1 KB, on the request path.
At a few hundred requests a day that is noise; if it ever is not, the answer
is to count once per invocation in a buffer and flush, not to stop counting.

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
- `days[].api_requests` and `api.requests` are the counters above; a day with
  requests and no calls is still a day.
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
  read, capped, and says when it is approximate.
