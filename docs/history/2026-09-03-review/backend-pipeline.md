# Chintan backend review — processing / data layer

Scope: `backend/cmd/worker`, `backend/cmd/chintanctl`, `backend/internal/{pipeline,provider,provider/fake,repository,repository/memory,purge,breaker,meter,cleanup,obs}`. Every non-test file read in full; tests read where they bear on a finding. Infrastructure parameters cross-checked against `infrastructure/template.yaml`.

Test run (linux box, `go test -cover`):

```
internal/pipeline    ok  79.4%
internal/provider    ok  77.4%
internal/repository  ok  62.6%
internal/purge       ok  84.3%
internal/breaker     ok  94.0%
cmd/chintanctl       ok  51.5%
cmd/worker           ok   6.1%
```

Non-test LOC in scope: ~10,200 (chintanctl 3,365; repository 2,144; pipeline 1,499; memory 814; provider 761; obs 380; worker 319; purge 261; breaker 233; fake 192; meter 147; cleanup 60). Test LOC: ~9,400.

## Summary

The code is careful, heavily commented, and mostly does what its comments say. The conditional-write discipline (versioned PutItem, ETag-conditional S3 writes, positive TTL-identity filter in purge) is real, not decorative, and the DynamoDB fake evaluating actual condition expressions is better than most projects manage.

Three things are wrong enough to matter:

1. **The append is not recoverable after an interruption.** The claim lease (20 min) was deliberately set longer than the SQS visibility timeout (16 min). The consequence the authors did not follow through: the one automatic redelivery arrives while the previous attempt's claim is still unexpired, `append()` treats that as "somebody else owns it", returns `nil`, and the message is deleted. Any failure after `ClaimCaptureAppend` (a DynamoDB throttle in `refreshNoteIndex`, five version conflicts, a Lambda timeout) leaves the capture in `appending` forever with the text already in the note body. The redelivery test passes because it simulates a third delivery SQS will never make.
2. **Provider spend metering is never persisted.** The worker wires `meter.SlogSink{}`; `DynamoUsageSink` has no production caller; `chintanctl usage` reads `USAGE#` rows nobody writes. The spec's stated reason for shipping metering ("cannot be reconstructed retroactively") is exactly what is lost — the only record is 14-day CloudWatch logs.
3. **Infrastructure faults inside the provider call are recorded as permanent capture failures**, with the raw AWS SDK error string stored on the capture and served to the client. A throttled spend-counter `UpdateItem`, an expired presigned URL, a Groq 5xx, a network timeout and a 429 all take the same path as a revoked API key: `failed`, no retry.

Beyond that: `restore` overwrites live data unconditionally; the LLM cost model never charges output tokens (~5x understatement on cleanup); routing failures silently create new notes; and roughly 1,500–2,000 lines exist to serve tenancy, metering and migration concerns that a single-user deployment does not have.

The SQS+worker split is justified for the *work* (a 20-minute recording exceeds API Gateway's 30 s), but SQS specifically is not — and the SQS-specific coupling (visibility timeout vs. lease) is where the critical bug lives. Details in the Architecture assessment.

---

## Critical

### C1. An interrupted append strands the capture in `appending` forever

**Where:** `internal/pipeline/pipeline.go:688-700`, `internal/repository/dynamo.go:1037-1047`, `internal/repository/memory/memory.go:418-427`, `internal/repository/store.go:80-98`, `infrastructure/template.yaml:795,800` (VisibilityTimeout 960, maxReceiveCount 3).

**What's wrong.** `append()` computes `resumingOwnAttempt` and calls `ClaimCaptureAppend`. The claim is only grantable when `AppendToken == ""` or `(AppendedAt == 0 && AppendClaimedAt < now-20min)`. When the claim is not grantable the pipeline does this:

```go
if !claimed {
    *capture = current
    return current, nil          // pipeline.go:694-700
}
```

A `nil` error means `Worker.Handle` does not add the message to `BatchItemFailures` (worker.go:91-99), so SQS deletes it.

Timeline for any failure after the claim (delivery 1 at T0):
- `appendToNote` succeeds; `refreshNoteIndex` fails (DynamoDB throttle, or five `ErrVersionConflict`s), or `CompleteCaptureAppend` fails, or the Lambda hits its 900 s timeout. `Run` returns an error → `BatchItemFailure` → message returns to the queue at T0+960 s. (For an S3-write failure the claim is released explicitly at pipeline.go:706; those are fine.)
- Delivery 2 at T0+960 s: `AppendClaimedAt` is 16 minutes old, lease is 20 minutes → not claimable → `claimed=false` → `return current, nil` → message deleted. Capture stays `appending`, `AppendedAt == 0`, note body contains the paragraph, note index (snippet, `updated_at`) not refreshed, GSI2 order stale.
- Delivery 3 never happens.

The comment at pipeline.go:174-179 ("If the owner dies mid-flight the queue's visibility timeout redelivers its message, and the append claim's lease lets that redelivery take the append over — so conceding drops nothing") is false precisely because store.go:83-89 insists the lease outlast the visibility timeout. The two constraints are mutually exclusive: either the redelivery arrives with a stale lease (and the code's own `resumingOwnAttempt` + body check protects against the duplicate) or it arrives with a live lease and is discarded.

`RetryCapture` (service/capture.go:276-300) does not reset a capture whose `Status == appending` and `Error == ""`; it re-enqueues as-is, so a user retry inside the 20-minute window hits the same concede. After 20 minutes a retry does recover it (the body-contains check works), but the UI treats `appending` as pending, not retryable.

**Why it matters.** This is the exact failure class the design document calls out as the reason for v2 ("duplicate note content after a gateway timeout"); the fix over-corrected into a stall. The trigger is an ordinary DynamoDB fault on the write path, not an exotic race.

**Evidence the test suite masks it.** `append_redelivery_test.go:59-104` runs delivery 2 at +960 s, asserts only that the text count is 1, then fabricates a delivery 3 at +1920 s and asserts `appended` there. SQS does not redeliver a message that was acknowledged. `TestAppendClaimLeaseOutlastsTheQueueVisibilityTimeout` (line 24) pins the inequality that causes the stall.

**Fix.** Make the claim re-entrant for the same deterministic token: add `OR append_token = :token` to `appendClaimCondition` (dynamo.go:1020-1022) and the `claimable` predicate in both stores. The existing ETag-conditional loop plus the `resuming` body check already make a re-entered append safe (a lost `PutIfMatch` re-reads and finds the text). Replace the `strings.Contains` heuristic with an exact marker — e.g. store the note ETag returned by the successful `PutIfMatch` on the claim, or append an HTML comment `<!-- chintan:capture:<id> -->` and check for that — so a user edit inside the window cannot cause a second append. Then delete the lease-vs-visibility constraint and its test; the lease only has to be long enough to reject a *concurrent* second delivery, not the sequential one. Alternatively, and minimally: when `!claimed && current.AppendToken == token && current.AppendedAt == 0`, return an error so SQS redelivers instead of acknowledging.

---

## High

### H1. Usage metering rows are never written; `chintanctl usage` and `DynamoUsageSink` are dead

**Where:** `cmd/worker/main.go:124-130` (`meter.SlogSink{}`), `internal/pipeline/usage_sink.go` (no non-test caller), `internal/meter/meter.go:134-147` (`MultiSink`, no non-test caller), `cmd/chintanctl/usage.go:117` (scans `USAGE#`).

**What's wrong.** The breaker's sink is the structured log. `NewDynamoUsageSink` is constructed only in `usage_sink_test.go`. `MultiSink`, whose only purpose is to fan out to both, is referenced only in `meter_test.go`. `chintanctl usage` therefore always prints "no USAGE# records in range" on a real instance; README and the spec (§4.6) both describe the rows as the point of the feature.

**Why it matters.** Log retention is 14 days (`log_retention_days` default). The design's stated rationale — "this data cannot be reconstructed retroactively" — is defeated. `TestUsageRecordUsesTheKeyShapeTheCLIReads` asserts compatibility between two components that are never connected in the binary; `cmd/worker` is at 6% coverage so nothing checks `setup()`.

**Fix.** Either `breaker.New(counter, meter.MultiSink{meter.SlogSink{}, pipeline.NewDynamoUsageSink(dynamoClient, tableName)}, ...)` in `setup()`, plus a test that constructs the worker's breaker and asserts a `PutItem` with `USAGE#`; or delete `usage_sink.go`, `MultiSink`, and `chintanctl usage` (~310 LOC code, ~350 LOC tests) and let the spend counter and CloudWatch EMF be the record. For one user the second option is honest.

### H2. Transient infrastructure and provider faults are recorded as permanent failures, with raw error text stored on the capture

**Where:** `internal/pipeline/pipeline.go:345-347, 585-587, 636-637` (every error from `Breaker.Do` goes to `handleProviderError`), `pipeline.go:940-986` (only spend-cap, 401/403 and 429 are classified; everything else falls to `markFailed`), `pipeline.go:1004-1007` (`capture.Error = cause.Error()`), `internal/breaker/breaker.go:156-159` (`"breaker: reserve: %w"` on counter failure), `internal/provider/groq_stt.go:118-124` (presigned fetch failures are untyped `fmt.Errorf`).

**What's wrong.** `Breaker.Do` wraps three very different failure sources into one error return: the spend-counter `UpdateItem`, the provider HTTP call, and `ErrSpendCapExceeded`. The pipeline handles the last one, treats 401/403 and 429 specially for *metrics*, and then marks the capture `failed` for all of them — including:
- `ProvisionedThroughputExceededException` / any DynamoDB fault on `SPEND#` (`breaker.go:158`)
- `provider: fetch source: status 403` (presigned URL expired: `audioURLTTL` is 60 min, a Lambda can legitimately run 15) or any S3 hiccup fetching the audio
- Groq/MiniMax 5xx, connection reset, `context deadline exceeded`
- 429, which the code's own comment says "usually resolves itself within minutes" — and then never retries.

`capture.Error` receives `cause.Error()` verbatim — e.g. `breaker: reserve: pipeline: spend counter update: operation error DynamoDB: UpdateItem, https response error StatusCode: 400, RequestID: …` — and `GET /v1/captures/{id}` serialises it. The spec (§4.2) says "Infrastructure error strings are logged, never serialised to the client."

**Why it matters.** The design's rule "provider errors are the capture's own verdict, infrastructure errors are retried" is right; the implementation cannot tell them apart because the breaker collapses them. A 30-second MiniMax outage turns every capture in flight into a `failed` card the user must find and retry by hand.

**Fix.** Return counter failures from `Breaker.Do` as a distinct sentinel (`ErrCounterUnavailable`) and treat them as retryable in the pipeline. Type the Groq source-fetch failure. In `handleProviderError`, treat `StatusError` with 5xx or 429, and any non-`StatusError` transport error, as retryable (return the error so SQS/async-invoke redelivers; the pipeline already resumes from the last persisted stage so the retry is cheap). Store a fixed user-facing sentence on `capture.Error` (as `ErrProviderKeyRejected` already does) and log the real error.

### H3. `chintanctl restore` overwrites live data unconditionally and is only half a restore

**Where:** `cmd/chintanctl/restore.go:183-207`, `cmd/chintanctl/awsports.go:61-70` (`Put` is an unconditional `PutItem`), `awsports.go:243-259` (object `Put` carries no tags).

**What's wrong.**
- Every item in `items.jsonl` is `PutItem`'d over whatever is in the table. A note edited since the backup is reverted to the old `version`; the API's optimistic-concurrency then compares against that old number, so the clobber is invisible.
- Items and objects created after the backup are left in place, so the result is neither the backup nor the current state.
- Restored audio objects are written without the retention/`chintan-processed` tags `PresignPut`/`MarkProcessed` set, so they never expire.
- `erase` demands the tenant id typed; `restore` needs only `--apply`.

**Why it matters.** The README sells this as the answer to "nothing backed up S3 content". A restore that runs against a live instance to recover one lost object silently rolls back every note. DynamoDB PITR plus S3 versioning (7-day noncurrent retention) already cover the realistic single-user recovery cases; this tool adds a sharper knife.

**Fix.** Refuse when the target partition is non-empty unless `--force`; default the item write to `ConditionExpression: attribute_not_exists(pk)` and report skipped items; add typed confirmation matching `erase`. Or delete `restore` (314 LOC) and document "restore = PITR + S3 version recovery".

---

## Medium

### M1. Routing failure silently files the dictation in a new note; note creation is not atomic with the capture write

**Where:** `pipeline.go:456-466` (any non-spend-cap router error → `RouteNew`), `pipeline.go:523-529` (`CreateNote` then `persist`).

A transient LLM 5xx or timeout on the *router* is not retried; the transcript goes to a fresh "Voice note <timestamp>" instead of the note the user named. If the cleanup call then fails in the same outage, an empty note remains. Separately, `CreateNote` succeeding and `persist` losing (conflict → concede, or a crash) leaves an orphaned empty note per delivery. Fix: treat router transport/5xx errors as retryable (H2); create the note idempotently (deterministic note id derived from capture id, or `attribute_not_exists` put) or create it lazily at append time.

### M2. A missing destination note is an infrastructure fault that dead-letters

**Where:** `pipeline.go:282-285`. `GetNote` returning `ErrNotFound` (user permanently deleted the target between routing and append, or between `SetCaptureTarget` and the worker) is returned as an error → three redeliveries → DLQ → alarm. Should be `markFailed`/`needs_target`, as the archived case at line 286-288 already is.

### M3. LLM cost metering never charges output tokens

**Where:** `breaker.go:59-62` (`Result` carries one unit), `pipeline.go:580-583, 631-634` (only `InputTokens` reported), `meter.go:107` (`UnitOutputTokens` priced at 4x input but never used).

Cleanup output is roughly the size of its input, so cleanup cost is understated by about 5x (1 + 4). Routing is understated less (short JSON reply). The daily cap therefore enforces against a number that is a fraction of the bill. Fix: `Result` carries `[]struct{Unit; Quantity}` or the callback returns cost directly; charge both units.

### M4. `thinking: {type: disabled}` breaks the "any OpenAI-compatible endpoint" claim

**Where:** `openai_cleanup.go:75-83`. OpenAI's API rejects unknown top-level parameters with 400; other compatible servers vary. Pointing `LLM_BASE_URL` at OpenAI makes every cleanup fail permanently (via H2). Gate the field on the model name or an env flag.

### M5. Capture size limit is far above the STT provider's

**Where:** `service/capture.go:53` (`MaxCaptureBytes = 256 << 20`), `pipeline/worker.go:77`, Groq's documented file limit (25 MB free / 100 MB dev tier).

A 30–256 MB upload passes the worker's oversize gate, is streamed S3→Lambda→Groq, and is refused with 413 → permanent failure (H2), after paying the transfer. Realistic 20-minute Opus recordings are ~10–20 MB so this rarely bites, but the gate is not protecting what it claims to.

### M6. `reconcile --apply` can delete an object whose row is milliseconds old

**Where:** `cmd/chintanctl/reconcile.go:157-200`, `awsports.go:28-59` (Query without `ConsistentRead`), `service/notes.go:51-71` (note objects are written before the row).

The dual-write order is S3 first, row second — by design. `reconcile` lists the bucket, then classifies any object whose owner has no row as `orphan_object` and deletes it under `--apply`. The partition scan is an eventually-consistent Query. Running it while a note is being created (or a capture uploaded before its row is visible) deletes live data. Fix: `ConsistentRead: true` and skip objects with `LastModified` inside the last N minutes.

### M7. "Terminal" is defined three different ways

**Where:** `service/capture_status.go:19-26` (appended, no_content, failed, spend_capped), `cmd/chintanctl/reconcile.go:266-277` (appended, failed, no_content, needs_target), `service/capture.go:282-289` (`RetryCapture`'s own switch: appended, no_content, needs_target terminal; spend_capped retryable).

`reconcile` reports every `spend_capped` capture as stuck and never reports a `needs_target` one; the pipeline's `Run` would happily re-run a `needs_target` capture if asked. One function, exported from `model`.

### M8. `erase`, "delete forever", oversize rejection and the TTL cascade leave bytes for 7 days

**Where:** `infrastructure/template.yaml:1033-1036, 1053-1054` (versioned bucket; noncurrent versions expire after 7 days), `cmd/chintanctl/erase.go:190-195`, `repository/s3.go:164-178`, `service/notes.go` `deleteObject`.

`DeleteObject` on a versioned bucket writes a delete marker. `erase` reports "removed N objects (X GiB)"; the README says "Deletes one tenant everywhere". The audio is recoverable and billable for a week. Acceptable, but document it in `erase`'s output and the README, or issue version-specific deletes there.

### M9. Every append rewrites the whole note body and leaves a noncurrent copy

**Where:** `pipeline.go:750-791`. Read-concat-`PutIfMatch` is O(body) per append and, on the versioned bucket, keeps one superseded copy per append for 7 days. Fine at a few notes a day; worth knowing before anyone dictates into one note for a year.

### M10. The in-memory `Objects.Delete` is stricter than S3

**Where:** `memory/memory.go:782-793` returns `ErrNotFound`; `s3.go:164-178` returns `nil` for a missing key (S3 `DeleteObject` is idempotent). `TestARedeliveredRecordDoesNotFail` (purge_test.go:251) therefore proves tolerance of an error S3 never produces, and any code path that *relies* on `ErrNotFound` from a delete would pass tests and misbehave in production. Align the double with S3.

### M11. `RetryCapture` cannot rescue an `appending` capture inside the lease window

**Where:** `service/capture.go:276-300`. Consequence of C1; listed so it is not forgotten when C1 is fixed. `resumeStatusFor` should also clear `AppendToken`/`AppendClaimedAt` when `AppendedAt == 0`, or the retry should be refused with a message.

---

## Low

- **L1. Doc drift.** Spec §4.4 says `pk=TENANT#<id>`; code is `USER#` (`dynamo.go:78`, `spend.go:49`, `usage_sink.go:76`, `purge.go:59`, `enumerate.go:66`). `cursor.go:33` says the table's keys are `pk, sk, gsi1pk, gsi1sk`; `gsi2` exists.
- **L2. Dead interface method.** `Store.UpdateCaptureStatus` (`store.go:135`, `dynamo.go:979`, `memory.go:365`) has no callers.
- **L3. Dead fields.** `provider.Audio.Body`/`SizeBytes` (`stt.go:14-26`) are only exercised by `fake.STT`; production always passes a URL. `MultiSink` (see H1).
- **L4. Unused env.** `ALLOWED_ORIGIN` is injected into both worker functions (`template.yaml:1339,1407`) and never read by `cmd/worker`.
- **L5. UTC in a user-facing title.** `fallbackNoteTitle` (`pipeline.go:1036`) renders the fallback note title in UTC.
- **L6. Rune split.** `noteFileName` (`export.go:357-359`) slices the slug at byte 80, which can split a multibyte rune.
- **L7. Prompt-injection surface.** Cross-note routing is well defended: `note_id` must be in the candidate list (`openai_router.go:45`), `content` must be a word-subsequence of the transcript (`openai_router.go:87-102`), titles are sanitised and bounded, candidate fields are pipe-stripped (`routing/prompt.go:118-130`). The *cleanup* output has no derivation check (by design for `polished`), so spoken instructions can make the model emit arbitrary text — including its own system prompt — into the current note. Single user, own note, low. A subsequence check for `faithful` mode would close it cheaply.
- **L8. Resume heuristic.** `strings.Contains(existingContent, text)` (`pipeline.go:761`) is defeated by a user edit to the just-appended paragraph inside the retry window. Fold into the C1 fix (marker or stored ETag).
- **L9. Eventually consistent reads.** `GetItem` is used without `ConsistentRead` everywhere (`dynamo.go:596, 794, 1032, 1090`). The conditional writes make this safe; `Run`'s early-exit terminal check (`pipeline.go:159`) can see a stale non-terminal status and start a stage that then loses its write — a wasted provider call in the worst case, and only within seconds of another write.
- **L10. Backup is not a snapshot.** `runBackup` walks the table then the bucket with no coordination; a capture completing during the walk can appear in one and not the other. `reconcile` after `restore` would flag it. Document.
- **L11. Logging hygiene** is good: provider bodies are drained and never logged (`groq_stt.go:100-104`, `openai_cleanup.go:106-110`), transcripts pass through `obs.Redact`, chintanctl logs keys and counts only. The one leak is H2's `capture.Error`, which is API output rather than a log line.
- **L12. Cursor direction encoding** (`cursor.go`, 120 LOC) is correct and thoroughly reasoned; it is also more machinery than a single tenant's few hundred notes will ever exercise.

---

## Over-engineering / deletable

Ranked by LOC that could go without loss for the actual deployment (one user, a few captures a day, a $10/month budget alarm already in place).

| Area | Files | Code LOC | Test LOC | Why it is more than needed |
|---|---|---|---|---|
| Per-tenant spend caps, cap resolver, settings field, SpendGate | `breaker.go` (`CapResolver`, `WithCapResolver`, `capFor`), `cmd/worker/main.go` `tenantSpendCaps`, `model.Settings.DailySpendCapMicros`, `service/spend.go` | ~150 | ~400 | Two caps (tenant and instance) and a "lower of the two" rule for one tenant. A single instance-wide daily cap — one `ADD` and a compare — is ~60 LOC. |
| Usage sink + CLI | `pipeline/usage_sink.go`, `meter.MultiSink`, `chintanctl/usage.go` | ~325 | ~350 | Dead in production (H1). |
| Pre-promotion / legacy-row compatibility | `dynamo.go:358-391, 461-475, 649-657, 724-758, 866-879, 964-977`; `pipeline.go:1040-1048`; `enumerate.go:252-304` fallbacks | ~150 | — | Compatibility with items written before attributes were promoted and before ids were time-ordered. For one table, run the migration once and delete the branches. |
| `reindex` + `ReindexNotes` | `chintanctl/reindex.go`, `dynamo.go:517-572` | ~160 | ~135 | One-time migration step for adding gsi2, now permanent. Would vanish with GSI2 (see Architecture). |
| `restore` | `chintanctl/restore.go` | 314 | ~100 | H3; PITR + S3 versioning cover the single-user cases. |
| `erase` | `chintanctl/erase.go` | 217 | ~120 | For one tenant, `erase` is `teardown`. Keep only if a second user is imminent. |
| TTL purge cascade as a second Lambda | `internal/purge`, expiry function, stream mapping, ExpiryDLQ, two alarms | 261 | 407 | Exists to cascade S3 deletes when TTL expires an archived note after 30 days. A weekly EventBridge sweep calling the existing `PurgeNoteArtifacts` from the API Lambda is ~40 LOC and removes a function, a mapping, a DLQ and stream permissions. |
| Verbatim DynamoDB-JSON codec in chintanctl | `chintanctl/ports.go` `AttrValue` + `awsports.go:86-174` | ~200 | — | `attributevalue`/`types` already round-trip items exactly; the custom codec exists to avoid decoding through `map[string]any`, which is a non-problem if you marshal the SDK types directly. |
| Duplicate status aliases / terminal predicates | `service/capture_status.go:9-15`, `reconcile.go:266-277`, `RetryCapture` switch | ~40 | — | M7. |
| Dead code | `Store.UpdateCaptureStatus`, `Audio.Body/SizeBytes`, `sortCapturesNewestFirst`, `ALLOWED_ORIGIN` on the worker | ~60 | — | L2–L4. |

Rough total: **~1,800 LOC of production code and ~1,500 LOC of tests** could be deleted or collapsed, out of ~10,200 / ~9,400 in scope. Removing GSI2 (below) adds ~250 more.

Abstractions with one implementation that are still worth keeping: `repository.Store`/`Objects` (the memory doubles earn them), `provider.STT/LLM/Router` (fakes earn them), `breaker.Counter` (the fixture's `memCounter` earns it). `meter.Sink`/`Pricer` are one implementation each and could be concrete.

---

## Architecture assessment

**Is the async worker warranted?** Yes, for the work: Groq on a 20-minute recording plus two LLM calls routinely exceeds API Gateway's fixed 30-second integration limit, and the spec's account of v1's duplicate-append-on-504 is credible. A synchronous pipeline is not an option.

**Is SQS warranted?** No. At a few captures a day there is no backpressure to absorb and no fan-out. What SQS costs here is precisely the machinery where the critical bug lives:

- `VisibilityTimeout` must exceed the Lambda timeout (960 > 900), which forces a 16-minute redelivery interval, which forced the 20-minute `AppendClaimLease`, which makes the redelivery arrive inside a live lease (C1).
- `ReportBatchItemFailures`, `BatchSize: 1`, the per-record `break`, two message shapes (`Message` vs. raw S3 event), the correlation-id message attribute, `s3:TestEvent` handling, the DLQ and its alarm — ~150 LOC in `worker.go`/`queue.go` plus template.

The two alternatives both remove that coupling:

1. **S3 `ObjectCreated` → Worker Lambda directly** (Lambda async invocation: two automatic retries at ~1 and ~2 minutes, `OnFailure` destination for the dead-letter case), and the API's `retry`/`target` paths call `lambda.Invoke` with `InvocationType=Event`. Retry intervals of 1–2 minutes are a far better match for a 20-minute lease than 16. `Worker.Handle` becomes "one capture ref in, `Run`, return error or nil".
2. **Single Lambda with async self-invoke.** Also works, but the API and worker genuinely want different memory/timeout (512 MB/29 s vs. 2 GB/900 s), so keeping two functions from one binary — which the repo already does — is the right shape.

Recommendation: option 1, and make the append re-entrant for its own token (C1 fix) regardless of transport. If SQS is kept for durability reasons, then fix C1, drop `TestAppendClaimLeaseOutlastsTheQueueVisibilityTimeout`, and let idempotency rest on a marker in the note body rather than on lease arithmetic.

**Data model.** Single-table, `pk=USER#<tenant>`, notes and captures as typed sort keys, S3 for bodies: correct and simple. Item sizes are small (bodies are in S3; the `data` blob doubles each item but stays in the low KB). Hot-partition concerns do not apply at one tenant. Two things are heavier than needed:

- **GSI2 exists only to order ≤ a few hundred notes by `updated_at`.** `decideTarget` (`pipeline.go:537-556`) already drains up to 500 notes from the base table and sorts them in Go. `ListNotes` could do the same and GSI2, `NoteIndexKeys`, `ReindexNotes`, `chintanctl reindex`, the two-step deploy procedure and its README section all disappear (~250 LOC plus a GSI). Revisit if a tenant ever has thousands of notes.
- **GSI1 (note → captures)** is justified only by `ListCapturesByNote` for the note-detail page and the purge cascade; with the base-table `ListCaptures` (already paginated) and a client-side filter it too could go at this scale, but it is cheap and harmless. Keep.

The DynamoDB fake that evaluates real condition expressions and pins projections to the template is the strongest piece of test infrastructure in the repo and is what makes the repository layer trustworthy despite the eventual-consistency and legacy-branch caveats above.

**Providers.** Groq adapter is well built: streamed multipart via `io.Pipe`, `verbose_json` with both granularities, typed status errors, bounded response decode, no body logging. The OpenAI-compatible adapter is fine except for the MiniMax-specific `thinking` field (M4) and the single-unit cost result (M3). Prompt construction fences the transcript and neutralises the fence string in both prompts; the router's output verifier (id must be offered, content must be a subsequence of the transcript) is genuinely good defence and is what makes spoken injection unable to reach other notes.

**Breaker.** Fails open on cap-lookup failure (documented, instance cap defaults to 0), fails closed on counter failure (H2 turns that into a permanent capture failure, which is the wrong closed). Redelivery cannot bypass it — each attempt reserves and reconciles again — and concurrent over-reservation errs toward refusal. The reconcile-then-record ordering is right. Its real problem is scale of concept, not correctness: two caps, a resolver interface and a sink interface for a system whose only real cost control is the AWS Budgets alarm.

---

## Test quality

**Behaviour tests that earn their keep**
- `internal/repository/dynamofake_test.go`: a fake that parses and evaluates the store's condition and filter expressions, honours `Limit`-before-filter, `ExclusiveStartKey`, GSI projections read from the template, and DynamoDB's attribute-shape validation (the empty-Binary outage). Tests against it fail for the reasons production would fail.
- `internal/pipeline/*`: every test drives `Pipeline.Run` or `Worker.Handle` over the memory store and provider fakes — real state transitions, not mock call counts. `foreignWriteBeforeFirstPut` (duplicate_delivery_test.go) is a deterministic race injection rather than a goroutine lottery.
- `internal/provider/groq_stt_test.go`: `httptest` servers assert multipart field set, filename, `Authorization`, and — `TestGroqSTTDoesNotBufferTheWholeRecording` — that the body is streamed.
- `internal/purge/purge_test.go`: the TTL-identity filter is tested in both directions (`TestAUserDeleteIsNotTreatedAsAnExpiry`).
- `cmd/chintanctl/backup_restore_test.go`: round-trips through the fakes and asserts refusal on tampered manifests.

**Tests that pass for the wrong reason**
- `append_redelivery_test.go:59-104` (see C1): models a delivery SQS will not make; never asserts status after the delivery that is actually last. Rewriting it to assert `StatusAppended` after delivery 2 turns it red.
- `append_redelivery_test.go:24` locks in the inequality that causes C1.
- `usage_sink_test.go:81` "uses the key shape the CLI reads" — for a sink no production code constructs (H1).
- `purge_test.go:251` `TestARedeliveredRecordDoesNotFail` passes against a memory double whose `Delete` returns `ErrNotFound`; S3 returns success, so the test exercises a branch production never takes (M10).
- `breaker/tenant_cap_test.go` and `pipeline/tenant_cap_test.go` test a two-cap policy that one tenant cannot exercise meaningfully; they are correct but they guard complexity rather than requirements.

**Gaps on risky paths**
- `cmd/worker` at 6%: `setup()` wiring is untested, which is how H1 shipped. A test constructing the breaker as `setup()` does and asserting the sink type would have caught it.
- No test for an infrastructure error surfacing *through* `Breaker.Do` (counter failure, presigned-fetch failure, transport timeout) — H2's classification is untested. `TestAnOrdinaryProviderFaultRaisesNeitherCounter` covers only the metric, and asserts the capture is `failed`, i.e. it enshrines the bug.
- No test for `route` losing its `persist` after `CreateNote` (M1's orphan), nor for `GetNote` → `ErrNotFound` in `run()` (M2).
- No test that LLM output tokens are priced (M3) — `TestDoReconcilesUnderAndOverEstimates` reconciles a single unit.
- No test for `restore` into a populated target (H3), nor for `reconcile --apply` racing a write (M6).
- No test for a Lambda-context deadline interrupting `appendToNote`/`refreshNoteIndex` (the `ctx.Done()` branch at pipeline.go:784-786).
- `internal/repository` at 62.6%: WebAuthn/vault paths are largely untested; out of this review's scope but noted.
- No integration test against DynamoDB Local or LocalStack; everything rests on the fake's fidelity. Given how much the fake models, one nightly run against DynamoDB Local would convert "the fake agrees with us" into "the service agrees with us".
