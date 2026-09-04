# Per-tenant usage accounting

Status: implemented for recording and self-service reading (2026-09-04).
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
