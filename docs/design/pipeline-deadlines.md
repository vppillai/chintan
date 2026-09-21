# Pipeline deadlines

Status: implemented 2026-09-05 (external review, H2). `pipeline.Config`
fields `TranscribeTimeout`, `CleanupTimeout`, `CleanNoteTimeout`; the
existing `RouteAttemptTimeout` and `AskAttemptTimeout` (the Ask retry gets
three quarters of it).

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
| ask | 20 s, then a 15 s retry | The client polls the row for 60 s (`ASK_POLL_TIMEOUT_MS`) and then reports the question as not sent; a cold start, the reads and both attempts must land inside that minute. |

The HTTP client timeouts stay as the outer bound.

## A second transcription, and what it costs the budget

Since 2026-09-21 (round 3, T2 and T7) a capture can be transcribed twice
in one invocation, and the transcribe deadline applies to each call:

- **After routing.** A recording made from Home is transcribed in the tenant
  default before the router reads the transcript to pick the note. When the
  note it lands on (by the router, or by a person for a `needs_target`
  capture) has a `language` that is a code other than the one sent, `run`
  clears `RawKey`, `SegmentsKey` and `RoutedKey` and calls `transcribe`
  again with the note set, then runs the instruction strip with the
  destination pinned and continues to cleanup. `CaptureIndex.Language` is
  written with `RawKey`, so a retry that finds the second transcript does
  not make a third; a note that inherits the default or asks for `auto`
  never asks for a second call. A note the router creates starts in the
  language its recording was transcribed in, unless that was `auto`.
- **On request.** `POST /v1/captures/{id}/retranscribe {language?}` resets a
  finished capture to `transcribing` with `RequestedLanguage` set (it
  outranks the note's language and the default, and stays on the row so the
  rule above never undoes a person's choice), clears the transcript keys
  (the objects stay until the worker writes over them, so the previous
  transcript downloads meanwhile) and the append claim; the worker
  transcribes, strips, cleans and then **replaces** the capture's paragraph
  in the note where it stands, by its marker (`replaceCaptureParagraph`: cut
  along the boundary delete and move use, inserted back in id order; a
  checklist item as one line, a tick kept). Because the earlier paragraph
  keeps the marker until then, a retry of the append inside its claim lease
  asks whether *this attempt's text* is under the marker
  (`paragraphInNote`), not whether the marker is there; a marker over the old
  paragraph is an unwritten append, and is failed and retried like one.

Worst case inside one invocation is therefore two transcriptions (10 min)
plus routing (30 s), cleanup (2 min) and the append, which still fits the
900 s Lambda with headroom; in practice a twenty-minute recording returns
from Whisper turbo in under a minute. The second call is priced through the
breaker like the first (`op: transcribe`), about four cents per audio hour,
and only in the mismatch case. Both calls log `language_sent` and
`language_detected` and count `TranscribedLanguage{Outcome}` (T10);
`CaptureRetranscribedForNote` counts the post-routing case and
`AppendReplacedParagraph` the in-place replace.

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
