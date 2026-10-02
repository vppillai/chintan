# Pipeline deadlines

How long each stage of the worker may wait on a provider, and what happens
when it waits too long. Code: `pipeline.Config` (`backend/internal/pipeline/
pipeline.go`) — `TranscribeTimeout`, `CleanupTimeout`, `CleanNoteTimeout`,
`RouteAttemptTimeout`, `AskAttemptTimeout`, each defaulting to the constant
beside it when left zero — the `context.WithTimeout` around each provider
call (`transcribe.go`, `clean.go`, `clean_note.go`, `route.go`, `ask.go`),
and the classification in `status.go` (`handleProviderError`, `isDeadline`).

## The budget

The worker Lambda has 900 seconds. Both provider HTTP clients carry an
840-second timeout (`internal/provider/groq_stt.go`, `openai_cleanup.go`) so
a hung socket surfaces as an error the pipeline can record rather than as a
killed invocation; that is the outer bound. Inside it each provider call runs
under its own deadline, taken inside the breaker's `Do` so the reservation
is released on the caller's still-live context:

| Stage | Deadline | Constant | Why this number |
|---|---|---|---|
| transcribe | 5 min | `defaultTranscribeTimeout` | A 20-minute recording returns from Whisper turbo in well under a minute; five is several times the worst case `service.MaxCaptureBytes` admits and leaves nine minutes of Lambda for the stages after it. |
| route (per attempt) | 15 s, `routeAttempts` = 2 | `defaultRouteAttemptTimeout` | A routing answer not back in fifteen seconds is stuck in the provider's queue; two attempts, then the new-note fallback (`routing.md`). |
| cleanup | 2 min | `defaultCleanupTimeout` | Rewrites one dictation; output about the size of the input. |
| clean-note | 3 min | `service.CleanNoteTimeout` | Reads up to `model.MaxCleanNoteInputBytes` and writes up to `MaxCleanedBodyBytes`, an order of magnitude longer completion than cleanup. The constant lives in `service` because the request path's in-flight guard is the same duration. |
| ask | 20 s, then a retry at `askRetryShare` (0.75) of it | `defaultAskAttemptTimeout` | The client polls the row for 60 s (`ASK_POLL_TIMEOUT_MS`, `api/queries/ask.ts`) and then reports the question as not sent; a cold start, the reads and both attempts must land inside that minute. |

A capture can be transcribed twice in one invocation — a recording routed to
a note whose `language` differs from the one it was transcribed in is
transcribed again in the note's language, and "Transcribe again" on the
Recordings tab restarts a finished one (`note-screen.md`, "Transcribe again")
— and the transcribe deadline applies to each call. The worst case inside
one invocation is two transcriptions (10 min), routing (30 s), cleanup
(2 min) and the append, inside the 900 s with headroom.

## What a deadline means

A deadline exceeded is an infrastructure fault, not a verdict on the capture.
`handleProviderError` classifies `context.DeadlineExceeded` and
`context.Canceled` (the invocation itself ending) as **retryable**: nothing is
written to the row, the capture stays in the stage's status
(`transcribing`, `cleaning`), the invocation returns an error, and Lambda's
asynchronous retry (two more attempts, then the dead-letter queue) runs the
pipeline again. The pipeline resumes at the first stage whose artefact is
missing — `run` checks `RawKey`, `NoteID`, `CleanKey` — so a cleanup stall
does not transcribe (and bill) the recording again. The whole-note clean
follows the same rule: no `cleaned_error` is written, the previous view and
its stale flag stand, and the idempotent task runs whole on the retry.

Every other provider error keeps its classification: a spend cap is
`spend_capped`, a 401/403 is `ErrProviderKeyRejected`, a 429 or 5xx or decode
failure is `failed` with the counters unchanged.

## Observability

`ProviderTimedOut{Stage}` counts a stage's own deadline firing (`transcribe`,
`cleanup`, `clean_note`); routing and ask count `RouterTimedOut` and
`AskTimedOut` per attempt. The metric is not emitted when the invocation's
own context ended — that is the Lambda's timeout, already alarmed — but the
WARN line carries `stage_deadline=false` for it. A non-zero `ProviderTimedOut`
over a day says either the number in the table is too small for the
recordings this instance sees, or the provider is stalling; the log line
names the capture. The alarm is in `infrastructure/template.yaml`; the
metrics' home is `docs/ops/metrics.md`.

## Tests

`internal/pipeline/stage_deadline_test.go`: a transcription stall leaves the
capture retryable and the second run finishes it; a cleanup stall does not
re-run transcription; a clean-note stall writes no verdict and the retry
stores the view; every default is set, and within five minutes, when the
config leaves it zero.

History: `docs/backlog.md`.
