# Pipeline deadlines

Status: implemented 2026-09-05 (external review, H2). `pipeline.Config`
fields `TranscribeTimeout`, `CleanupTimeout`, `CleanNoteTimeout`; the
existing `RouteAttemptTimeout` and `AskAttemptTimeout`.

## The budget

The worker Lambda has 900 seconds. Both provider HTTP clients carry an
840-second timeout (`internal/provider/groq_stt.go`, `openai_cleanup.go`) so a
hung socket surfaces as an error the pipeline can record rather than as a
killed invocation. Until this change that was the **only** bound on
transcription, cleanup and the whole-note clean: a provider stall held the
invocation for fourteen minutes, and when the client finally gave up the
capture was marked `failed` — permanently, for a transient fault.

Each provider call now runs under its own `context.WithTimeout`, inside the
breaker's `Do` so the reservation is released on the caller's still-live
context (the same shape as `routeOnce`):

| Stage | Deadline | Why this number |
|---|---|---|
| transcribe | 5 min | A 20-minute recording returns from Whisper turbo in well under a minute; five is several times the worst case `service.MaxCaptureBytes` admits and leaves nine minutes of Lambda for the stages after it. |
| route (per attempt) | 15 s | A routing answer not back in fifteen seconds is stuck in the provider's queue; two attempts, then the new-note fallback. |
| cleanup | 2 min | Rewrites one dictation; output about the size of the input. |
| clean-note | 3 min | Reads up to 150 KB and writes up to 200 KB — an order of magnitude longer completion than cleanup. |
| ask (per attempt) | 25 s | The client is waiting on the answer. |

The HTTP client timeouts stay as the outer bound.

## What a deadline means

A deadline exceeded is an infrastructure fault, not a verdict on the capture.
`handleProviderError` classifies `context.DeadlineExceeded` (and
`context.Canceled`, the invocation itself ending) as **retryable**: nothing is
written to the row, the capture stays in the stage's status
(`transcribing`, `cleaning`), the invocation returns an error, and Lambda's
asynchronous retry (two more attempts, then the dead-letter queue) runs the
pipeline again. The pipeline resumes at the first stage whose artefact is
missing — `run` checks `RawKey`, `NoteID`, `CleanKey` — so a cleanup stall
does not transcribe (and bill) the recording again. The whole-note clean
follows the same rule: no `cleaned_error` is written, the previous view and
its stale flag stand, and the idempotent task runs whole on the retry.

Every other provider error keeps its existing classification: a spend cap is
`spend_capped`, a 401/403 is `ErrProviderKeyRejected`, a 429 or 5xx or decode
failure is `failed` with the counters unchanged.

## Observability

`ProviderTimedOut{Stage}` counts a stage's own deadline firing (`transcribe`,
`cleanup`, `clean_note`); routing and ask keep `RouterTimedOut` and
`AskTimedOut`. The metric is not emitted when the invocation's own context
ended — that is the Lambda's timeout, already alarmed — but the WARN line
carries `stage_deadline=false` for it. A non-zero `ProviderTimedOut` over a
day says either the number in the table is too small for the recordings this
instance sees, or the provider is stalling; the log line names the capture.

## Tests

`internal/pipeline/stage_deadline_test.go`: a transcription stall leaves the
capture retryable and the second run finishes it; a cleanup stall does not
re-run transcription; a clean-note stall writes no verdict and the retry
stores the view; the defaults are set when the config leaves them zero.
