# Per-tenant usage accounting

Status: implemented for recording and self-service reading (2026-09-04);
the instance's AWS cost beside it (2026-09-04, D6b).
Admin listing: designed, not built.

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

op_<op>_cost_micros      N   the same five, per pipeline stage:
op_<op>_calls            N   <op> ∈ transcribe | route | cleanup
op_<op>_audio_seconds    N   (only the units the stage consumes)
op_<op>_input_tokens     N
op_<op>_output_tokens    N

gsi1pk          "USAGE#<yyyy-mm>"     month row only — see admin listing
gsi1sk          "TENANT#<tenant>"     month row only
```

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

Since D6b the same response carries one more member, `aws` — see the next
section; the example above omits it.

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

### No per-user split — deliberately

AWS cost is instance-level. Every tenant sees the same `aws` object, and it is
not their share of anything: apportioning the account's bill by provider
share (a tenant that caused 40% of the provider spend caused roughly 40% of
the Lambda seconds) or flat (per active tenant) is a policy decision, and it
belongs in the admin view designed above, not in the self-service endpoint. A
future admin listing has everything it needs for either: the per-tenant month
rows for the shares and this row for the total.

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

## The admin listing, when it is wanted

Nothing cross-tenant is exposed today. When it is:

1. **Which tenants had usage in month M** is a `Query` on the existing GSI1:
   `gsi1pk = "USAGE#<M>"`. The month rows already carry these keys, so there
   is no backfill and no template change. GSI1 is an `INCLUDE` projection of
   capture attributes, so the query returns keys only (tenant id from
   `gsi1sk`); the listing then does one `GetItem` per tenant for the numbers.
   For an admin page over tens of tenants that is the right cost. If it ever
   becomes hundreds, the alternative is an `ADMIN#USAGE#<M>` aggregate row
   updated by the same breaker `ADD` — a third `UpdateItem` per call — or a
   second GSI projecting the counters; both are additive.
2. **Who may call it** needs an admin notion this API does not have: today
   every route is tenant-scoped by the Cognito `sub`. The smallest honest
   version is a Cognito group (`admins`) checked in the middleware, and a
   `GET /v1/admin/usage?month=` route that lists `{tenant_id, totals}`.
   Nothing about the rows changes for that.
3. **Billing** would read the month rows and nothing else. The day rows exist
   for charts and expire.

## Deliberately not built

- No per-capture usage rows. The `provider usage` log line has the capture's
  correlation id and the day/month rows have the totals; a per-capture row
  is a third copy of a number for a question nobody has asked.
- No enforcement from these rows. The cap is `SPEND#`, instance-wide, and
  stays that way; a per-tenant cap is exactly the machinery step 3 removed.
- No backfill. The log group's retention is 14 days and the rows are cheap;
  history starts at the deploy that ships this.
- No admin endpoint, no UI. The API for the caller's own month is what a
  "You" screen needs; the design above is what an admin page would add.
- No Cost Explorer, no per-service AWS breakdown, no per-tenant AWS share.
  One free `DescribeBudget` a day answers the question that was asked.
