# Metrics

Every metric is in the CloudWatch namespace `Chintan`, emitted as EMF records in the Lambda logs through `backend/internal/obs` (`Count`, `CountWithRollup`, `Duration`, `Emit`). Every distinct dimension set is a billed metric identity, so counters keep one or two low-cardinality dimensions and a counter whose healthy value is zero often has none. Eleven names are alarmed in `infrastructure/template.yaml` (`alarms.md` has each alarm's threshold and what to do). Ten — `InboxKeyRefused`, `ProviderKeyRejected`, `SpendCapRejections`, `CaptureStageFailures`, `CaptureOrphaned`, `PushSendFailures`, `ExpiredNotesFailed`, `WorkerMessagesDiscarded`, `NoteCleanInvokeFailures`, `ProviderTimedOut` — are emitted with a rollup (`CountWithRollup`; `EmitWithRollup` for `ExpiredNotesFailed`), so the alarm reads the dimensionless identity (`TestEveryAlarmedMetricIsRolledUp`); `CaptureReaped` is alarmed on one dimension value and so is emitted with plain `Count` and listed below like any other; `RouterTitleMatchedExistingNote` is the one unalarmed counter emitted with a rollup, so its total survives the split the owner reads it by. Every other emitted name is on this page with its reader; `TestEveryEmittedMetricIsAlarmedOrListed` (`backend/internal/obs/rollup_test.go`) enforces it, so a new counter is either alarmed, or added here with who reads it, or not added.

### API

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `ApiRequests` | `{Status=2xx…5xx}` | `handler/metrics.go` | One per request by status class; the owner, in the console, beside the gateway 4xx/5xx alarms. |
| `ApiLatency` | `{Status}` | `handler/metrics.go` | Request duration in ms; nobody today — the request log carries `duration_ms` per route, which is where latency is read. |
| `NotesListTruncated` | none | `service/notes.go` | A shelf listing hit the drain ceiling, so its order is over an incomplete set; the owner, when a list looks wrong (healthy value zero). |

### Captures and pipeline

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `CapturesCreated` | `{Stage=created}` | `service/capture.go` | A recording was accepted; the owner, in the console, as the volume every failure counter is read against. |
| `CaptureStageEntered` | `{Stage}` | `pipeline/status.go` | Each stage transition; nobody today — consecutive records give one capture's stage durations when it felt slow (the recipe at the end), and `chintanctl latency` gives a month's. |
| `DuplicateDelivery` | `{Status}` | `pipeline/status.go` | Lambda delivered a capture already past that stage; nobody today (healthy value zero). |
| `CaptureSpendCapped` | `{Stage}` | `pipeline/status.go` | A capture stopped by the daily cap, by stage; the owner, beside the spend-cap alarm, to see where the cap bit. |
| `CapturePeaksMissing` | `{Stage}` | `pipeline/status.go` | The client uploaded no waveform peaks; nobody today; kept because it is the only sign a browser skipped the peaks upload. |
| `CaptureRejectedOversize` | `{Stage=uploaded}` | `pipeline/pipeline.go` | An upload over the size ceiling was refused; the owner, when a recording never appears (healthy value zero). |
| `CaptureOrphanObjectRemoved` | `{Stage=uploaded}` | `pipeline/pipeline.go` | An object arrived for a capture already deleted and was removed; the owner, as a storage-hygiene check (healthy value zero). |
| `CaptureRetranscribedForNote` | none | `pipeline/pipeline.go` | The destination note asked for another language and the audio was transcribed again (`note-screen.md`, "Transcribe again"); the owner, as a cost signal. |
| `CaptureDestinationPurged` | none | `pipeline/pipeline.go` | The destination note was gone when the capture resumed, so it asks for a new one; the owner, when a capture sits at `needs_target`. |
| `CaptureReaped` | `{Outcome=finished\|failed}` | `reconcile/reconcile.go` | What the quarter-hourly `reconcile-stuck` task did with a capture pending past the stuck rule: `finished` is the second run ending it on the pipeline's own terms — appended, or failed on its own verdict — and `failed` is the task giving up on it with the fixed sentence (the `capture-reaped` alarm reads `failed`); the owner, when a filing row says "Filing did not finish." |
| `TranscribedLanguage` | `{Outcome}` | `pipeline/transcribe.go` | Whether the language detected matched the one sent; the owner, when judging the language hint. |
| `AppendResumedWithoutRewriting` | `{Stage=appending}` | `pipeline/append.go` | A retried append found its paragraph already written and did not write it twice; nobody today (healthy value zero). |
| `AppendReplacedParagraph` | `{Stage=appending}` | `pipeline/append.go` | A re-transcribed capture replaced its earlier paragraph in place (`note-screen.md`, "Transcribe again"); the owner, with `CaptureRetranscribedForNote`. |
| `CapturesMoved` | `{Stage}` | `service/capture_move.go` | A capture was moved to another note, by the stage it was in; nobody today; kept because a move is the one user action that rewrites two notes. |
| `CaptureMoveRolledBack` | `{Stage}` | `service/capture_move.go` | A move failed half-way and was undone; the owner, when a note looks wrong after a move (healthy value zero). |
| `CapturesDeleted` | `{Stage}` | `service/capture_edit.go` | A capture was deleted, by stage; nobody today; kept as the counterpart of `CapturesCreated`. |

### Routing

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `RouterTitleMatchedExistingNote` | `MatchedBy` = `title`, `alias`, `tag`, `prefix_title`, `prefix_transcript`, `spoken_name`; emitted with a rollup, so the dimensionless total is also published | `pipeline/route.go` | The router or a rescue rule filed a recording into a note it named. `title`, `alias` and `tag` are the prompt's own misses — the model said "new" for a listed name — and are the routing prompt's failure rate; the other three are the code's rescues of a name the model did not file by. The owner, in the console, when judging a routing prompt change (`routing.md`, "The decision line"). |
| `RouterNewNoteKind` | `{Kind=note\|checklist}` | `pipeline/route.go` | How often the model answers `checklist` for a new note; the owner, when judging a prompt change without a battery run. |
| `RouterRetried` | `{Reason}` | `pipeline/route.go` | A routing call was retried and why; the owner, when a prompt change makes the model's answer stop parsing. |
| `RouterTimedOut` | `{Attempt}` | `pipeline/route.go` | A routing attempt hit its deadline; the owner, beside `ProviderTimedOut` (which counts the transcribe, cleanup and clean-note stages, not routing) and `AskTimedOut`. |
| `RouterSpansDiscarded` | `{Reason=missing_field\|malformed\|too_long\|empty_content\|not_derived}` | `provider/openai_router.go` | A span the model returned was thrown away and why; the owner, when judging a routing prompt change. |

### Cleanup and checklists

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `CaptureCleanupBypassed` | `{Stage=cleaning}` | `pipeline/clean.go` | The cleanup model call was skipped and the dictation stored as spoken (`prompts.md` §Cleanup); the owner, as a cost and quality signal. |
| `ChecklistItemsExtracted` | `{Outcome=items\|none}` | `pipeline/clean.go` | Whether a recording into a checklist yielded items (`checklists.md`); the owner, when judging the items prompt. |
| `ChecklistItemsDiscarded` | `{Reason=unusable}` | `pipeline/clean.go` | The items answer was unusable and the dictation was kept as text; the owner, when judging the items prompt. |
| `ChecklistItemsMerged` | `{Outcome=joined\|deduped\|reopened}` | `pipeline/append.go` | What the merge did with each body that landed (`checklists.md`, "Merging into what the list has"); the owner, when a list gains or loses a line unexpectedly. |

### Clean-note and regenerate

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `NoteCleanRequested` | `{Mode,Trigger=user\|auto\|…}` | `service/note_clean.go`, `pipeline/clean_note.go` | A whole-note clean was asked for, by mode and trigger; the owner, as the denominator for `NoteCleanOutcome`. |
| `NoteCleanOutcome` | `{Outcome=ok\|empty\|too_long\|unusable\|output_too_long\|provider\|superseded}` | `pipeline/clean_note.go` | How each clean ended (`prompts.md` §Whole-note); the owner, when judging a clean-note prompt change. |
| `NoteCleanCoalesced` | `{Trigger=user}` | `service/note_clean.go` | A clean request joined one already in flight instead of starting another; nobody today; kept because it is the only sign of a double-tap on Clean. |
| `NoteRegenerateRequested` | `{Trigger=user}` | `service/note_regenerate.go` | A regeneration was asked for; the owner, as the denominator for `CaptureRegenerated`. |
| `NoteRegenerateSkipped` | `{Reason=in_flight}` | `pipeline/regenerate.go` | A regeneration was skipped because one was running (`regenerate.md`); the owner, when a Regenerate seems to do nothing. |
| `CaptureRegenerated` | `{Outcome}` | `pipeline/regenerate.go` | Each capture re-run by a regeneration, by its final status (`regenerate.md`); the owner, after a regeneration, to see what failed. |

### Ask

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `AskRequested` | `{Trigger=user}` | `service/ask.go` | A question was asked; the owner, as the denominator for `AskOutcome`. |
| `AskOutcome` | `{Outcome}` | `pipeline/ask.go` | How each answer ended (`prompts.md` §Ask); the owner, when judging the Ask prompt. |
| `AskRetried` | `{Reason}` | `pipeline/ask.go` | An Ask call was retried and why; the owner, with `AskOutcome`. |
| `AskTimedOut` | `{Attempt}` | `pipeline/ask.go` | An Ask attempt hit its deadline; the owner, with `AskOutcome`. |

### Push

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `PushSent` | `{Kind}` | `pipeline/notify.go` | A push notification was delivered, by kind; the owner, as the denominator for the `PushSendFailures` alarm. |
| `PushSubscriptionsPruned` | none | `pipeline/notify.go` | A subscription the push service said is gone (404/410) was deleted (`push.md`); nobody today; kept because it is the only record of a browser dropping its subscription. |

### Providers and spend

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `ProviderCalls` | `{Provider,Op}` | `breaker/breaker.go` | One per paid provider call; the owner, in the console, against the provider's own dashboard and the usage screen. |
| `ProviderCostMicros` | `{Provider,Op}` | `breaker/breaker.go` | The metered cost of each call in microdollars; the owner, to check the usage meter (`usage-accounting.md`) against the console. |
| `ProviderRateLimited` | `{Provider}` + rollup | `pipeline/status.go`, `pipeline/clean_note.go`, `pipeline/ask.go` | A provider answered 429; emitted with a rollup like its alarmed siblings but deliberately unalarmed (ordinary throttling clears on its own); the owner, when captures slow down. |
| `UsageRecordFailures` | `{Op}` | `breaker/breaker.go`, `handler/router.go` | A usage row could not be written, so the usage screen under-counts (`usage-accounting.md`); the owner, when the usage screen disagrees with the provider's bill (healthy value zero). |
| `PriceWildcardUsed` | `{Provider}` | `cmd/worker/main.go` | A model has no price row of its own and is priced at the provider wildcard; the owner, after changing a model, to add the price row. |

### Retention

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `ExpiredNotesPurged` | none | `purge/purge.go` | Notes the weekly sweep deleted past their purge deadline (`retention.md`); the owner, as the counterpart of the `ExpiredNotesFailed` alarm. |

### Durations

| Name | Dimensions | Emitted by | What it says, who reads it |
|---|---|---|---|
| `CaptureQueueDelay` | `{Source=app\|device}` | `pipeline/pipeline.go` | Upload to first worker invocation, ms; the owner, with `chintanctl latency`, when a capture felt slow. |
| `CapturePipelineDuration` | `{Outcome}` | `pipeline/pipeline.go` | One invocation's run, ms, by the status it ended in; the owner, when sizing the stage deadlines (`pipeline-deadlines.md`). |
| `CaptureEndToEnd` | `{Source}` | `pipeline/pipeline.go` | Upload to terminal status, ms; the owner, as the number the capture screen's wait is judged by. |
| `ProviderLatency` | `{Provider,Op,Outcome}` | `breaker/breaker.go` | One provider call, ms; the owner, when sizing the stage deadlines (`pipeline-deadlines.md`). |

To see a metric: CloudWatch → Metrics → `Chintan` in the console, or the EMF records in the Lambda log groups; the agent role cannot read metrics, so a review that needs numbers asks the owner.

To read the records themselves — a capture's stage transitions, or the log lines around an alarm — tail the worker's log group for the stack, or query it (the API's is `/aws/lambda/chintan-api-<instance>-<environment>`):

```bash
aws logs tail /aws/lambda/chintan-worker-dev-prod --since 1h --follow --filter-pattern '{ $.capture_id = "<id>" }'
aws logs start-query --log-group-name /aws/lambda/chintan-worker-dev-prod --start-time "$(( $(date +%s) - 86400 ))" --end-time "$(date +%s)" --query-string 'fields @timestamp, msg, stage, capture_id | filter capture_id = "<id>" | sort @timestamp asc'
aws logs get-query-results --query-id <the id the line above printed>
```
