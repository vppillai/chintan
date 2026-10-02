# The worker

The second Lambda: everything that takes longer than an HTTP request or
runs on a schedule. Code: `backend/cmd/worker/main.go` (the entry point,
`Handler`, `sniff`, `scheduled`), `backend/internal/pipeline/worker.go`
(`Worker.Handle`, the `Invocation` envelope), `pipeline/invoker.go` (how
the API sends work), the function and its wiring in
`infrastructure/template.yaml` (`WorkerLambdaFunction`,
`WorkerLambdaLiveAlias`, `WorkerLambdaEventInvokeConfig`, `CaptureDLQ`, the
three `AWS::Events::Rule`s).

## Why a second function

API Gateway's HTTP API caps an integration at 30 seconds, so the API
function's `Timeout` is 29 s and nothing that waits on a speech or language
model can run inside a request. The worker's ceiling is Lambda's own:
`Timeout: 900`, `MemorySize: 2048`, `arm64`, `ReservedConcurrentExecutions:
5` — five, because each invocation is a provider round-trip and five is the
most this instance should have in flight at once. Everything reaches it
asynchronously through the `live` alias, which is also what the API's
invoker and the bucket's notification name, so a rollback that moves the
alias moves every way into the worker at once.

## How work arrives

There is no queue in front of the worker. Three sources, one handler:

- **S3.** The content bucket notifies the alias on `ObjectCreated` for
  keys under `tenants/` ending in `/audio.webm`, `/audio.m4a`,
  `/audio.mp3`, `/audio.ogg` or `/audio.wav` — the five extensions
  `normalizeAudioContentType` (`service/capture.go`) can give an upload, so
  every recording the client or a device uploads, and none of the objects
  the worker writes afterwards.
- **The API**, through `pipeline.Invoker`: `InvokeCapture` with a reason
  (`inbox-text`, `retry`, `retranscribe`, `target`), `InvokeCleanNote`,
  `InvokeAsk`, `InvokeRegenerateNote`. Each is one `lambda:Invoke` with
  `InvocationType: Event`; Lambda answers 202 once the event is queued and
  anything else is an error to the caller. The pipeline uses the same
  invoker to hand an auto-clean to a fresh invocation after an append
  (`CleanInvoker`, `NoteCleanInvokeFailures` when that hand-off fails).
- **EventBridge**, three rules with a constant JSON input (below).

The payload is the `Invocation` envelope: `tenant_id`, `capture_id`,
`reason`, `task`, `note_id`, `mode`, `requested_at`, `ask_id`,
`capture_ids`, `correlation_id`. The correlation id is the API request's,
so the worker's log lines join the request's into one trace
(api-conventions.md).

`Handler` sniffs the payload rather than reading configuration, so a
function wired to the wrong trigger is inert rather than wrong: a `task`
field names a task; a `Records` array whose first record says
`eventSource: aws:s3` is a recording; neither is the API naming a capture;
any other event source is logged and dropped
(`TestSniffDistinguishesTheThreeInvocations`).

## Tasks

| `task` | Sender | Schedule | Handler |
|---|---|---|---|
| *(none: a capture)* | S3, or the API with a `reason` | — | `Pipeline.RunUpload` / `Run` |
| `clean-note` | the API, or the worker itself after an append | — | `Worker.handleCleanNote` (prompts.md) |
| `ask` | the API | — | `Worker.handleAsk` (ask.md) |
| `regenerate-note` | the API or `chintanctl regenerate` | — | `Worker.handleRegenerateNote` (regenerate.md) |
| `sweep-expired` | `ExpirySweepRule` | `rate(7 days)` | `purge.Sweeper.Sweep` (retention.md) |
| `aws-cost` | `AwsCostRule` | `rate(1 day)` | `awscost.Collector.Run` (usage-accounting.md) |
| `storage-snapshot` | `StorageSnapshotRule` | `cron(0 3 * * ? *)` | `storagesnap.Snapshotter.Run` (usage-accounting.md) |
| `smoke` | `scripts/deploy.sh` | each deploy | a no-op |

The storage snapshot has a fixed hour because a `rate` schedule fires at
the minute its rule is created, and a reading taken near midnight UTC
would credit a day's storage to its neighbour from one run to the next;
03:00 is far from the boundary and from the cost reading. Each scheduled
task's name is a constant in its package (`purge.Task`, `awscost.Task`,
`storagesnap.Task`) and `TestHandlerDispatchesEveryTaskConstant` fails
when the map in `cmd/worker` misses one. A task nobody knows is refused
with `ErrUnknownTask` rather than dropped, so a deploy out of step with its
sender is retried and dead-lettered where a person sees it
(`TestHandlerRefusesAnUnknownTaskWithAnError`).

The smoke task is the deploy's proof that the function starts: `setup()`
runs in `main` before `lambda.Start` — the environment, the secrets and the
clients a bad deploy breaks first — so a container that answers the task
with `nil` has passed it, and the task itself reads and writes nothing.
`scripts/deploy.sh` invokes the alias synchronously with
`{"task":"smoke"}` after the health probes and rolls the alias back on a
`FunctionError` (`TestHandlerSmokeTaskIsANoOp`).

## The capture path

`Worker.Handle` resolves the payload to capture references
(`parseInvocation`; an S3 key is matched against `audioKeyPattern`, the
mirror of `keys.CaptureAudio`), refuses an object over
`service.MaxCaptureBytes` before anything that costs money
(`RejectOversizedCapture`), and runs each capture through the stages
`transcribing` → `routing` or the chosen target → `cleaning` →
`appending` → `appended`. Every stage persists its artefact and its status
(`setStatus`, `CaptureStageEntered`), so a retry resumes at the first
stage whose object is missing rather than transcribing again
(`cleanForNote` is where a resumed run picks up once `raw.txt` exists).
The terminal statuses are `appended`, `no_content`, `failed`,
`spend_capped` and `needs_target` (`model.IsTerminalStatus`); the stage
deadlines inside the 900 s are pipeline-deadlines.md's; routing.md owns
the routing stage.

## Retries, exactly-once and the dead-letter queue

The return value is the whole protocol: `nil` means done — finished,
failed on its own terms, or conceded to another delivery — and an error
means an infrastructure fault interrupted the work. On an error Lambda
retries the same payload twice (`MaximumRetryAttempts: 2`) within six
hours (`MaximumEventAgeInSeconds: 21600`) and then writes it, with the
error, to `CaptureDLQ` through the `OnFailure` destination, where it is
kept fourteen days (`MessageRetentionPeriod: 1209600`) under
`CaptureDLQDepthAlarm`. What a message there means and how it is read and
recovered is `docs/ops/alarms.md`'s. A provider failing is not an
infrastructure fault: the
capture ends `failed` with a Retry button and never reaches the queue
(`handleProviderError`).

Two deliveries of one capture can overlap — Lambda is at-least-once, and
the API's Retry is allowed once a row has gone `CaptureStuckAfter`
(15 min) without a write. Every row write is a compare-and-set on
`version` (data-model.md); the delivery that loses reloads the row and
stops (`concede`, `DuplicateDelivery`). The append itself is guarded
twice more: a deterministic token, `appendToken(captureID, cleanKey)`, is
taken with `ClaimCaptureAppend` under `appendClaimCondition` — free, or
held by a claim older than `repository.AppendClaimLease` (20 min) that
never finished — and the capture's marker in the note body says whether
the paragraph landed. Marker present means the bookkeeping is all that is
left; absent means the invocation fails and the attempt after the lease
appends once. The lease is 20 min because it has to outlast the longest
a live holder can write, which is the function's 900 s
(`TestAppendClaimLeaseOutlastsALiveWorker`,
`TestRetryAfterAFailedFinishAppendsExactlyOnce`,
`TestRetryOfAnAppendThatDiedBeforeWritingWaitsForTheLease`).
append-vs-autosave.md owns the body write the append shares with the
editor.

## The spend cap

Every paid call the worker makes goes through `breaker.Do`, which reserves
against the instance's `SPEND#<day>` counter before the provider is
contacted and refuses with `ErrSpendCapExceeded` past
`DailySpendCapMicros`. A capture stopped that way ends `spend_capped` with
the fixed verdict `daily provider spend cap reached`
(`CaptureSpendCapped` by stage); a clean-note run and an ask record the
same sentence. usage-accounting.md owns the counter and the cap.

## What it emits

`CaptureStageEntered`, `DuplicateDelivery`, `CaptureSpendCapped`,
`WorkerMessagesDiscarded` (an unparseable payload, logged and let go
rather than retried into the queue), `CaptureStageFailures`, the provider
and duration metrics, and the per-task counters: every name, its
dimensions and who reads it are in `docs/ops/metrics.md`. The worker also
sends one push per finished capture (push.md).
