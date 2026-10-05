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
`spend_capped`, a 401/403 is `ErrProviderKeyRejected`, a 429 or 5xx that
outlasts the client's retries (below) or a decode failure is `failed` with
the counters unchanged.

## Retries inside a deadline

The live model answers HTTP 529 ("overloaded") in bursts of about ten
consecutive calls, each refused in well under a second, and a reset
connection looks the same from here. Both provider clients send such a
call again rather than hand the stage its first refusal:
`provider.retrying` (`backend/internal/provider/retry.go`) wraps the HTTP
call in `GroqSTT.Transcribe` and in `OpenAICleanup.complete`, so every
call — transcription, routing, cleanup, items, the whole-note clean, ask —
is covered by one loop with the bounds in `provider/bounds.go`:

- **What is sent again**: a 429, any 5xx, and a transport fault — a
  connection refused or reset, one the provider closed before answering,
  a body cut short (`retryStatus`; the class is deliberately narrow). A
  4xx other than 429 is this request or this key and is returned at once;
  so is a reply that would not decode, a name that does not resolve or a
  certificate that does not verify, which a moment later cannot change. A
  transcription is sent again only from a presigned URL, which each
  attempt opens afresh; a `Body` has been read once and gets one attempt.
- **How many times**: `ProviderRetryAttempts` (3) calls in all. Ask gets
  `ProviderRetryAttemptsAsk` (2): it is interactive, the app gives up at
  sixty seconds, and the ask stage already asks a second time under a
  fresh deadline, so a third client try inside the first attempt would
  only move the answer — or the fixed "could not be produced" — later.
- **How long between**: the provider's `Retry-After` when it sent one
  (seconds or an HTTP date, `StatusError.RetryAfter`) and it is within
  `ProviderRetryMaxRetryAfter` (12 s) — a longer one is "come back later",
  and the refusal is returned at once rather than waited on or truncated
  — else full jitter: uniform between zero and
  `min(ProviderRetryMaxWait, ProviderRetryBaseWait·2^attempt)`, under two
  seconds then under four, six at most in all.
- **The budget is the context's.** The waits and the retried calls run
  under the same context the stage handed in, so a retry can never push a
  stage past its deadline: the wait and a request as long as the refused
  one (measured, not assumed) have to end before the deadline, or the last
  refusal is returned as it stands — a retried call the deadline cut would
  reach the stage as a timeout, which is retryable, where the refusal is a
  verdict. A context cancelled during a wait ends it with an error that
  names both the refusal and the cancellation. There is no retry after the
  stage's own deadline has fired, because the call it would retry has
  already ended with `context.DeadlineExceeded`, which is not a refusal.

Each retry counts `ProviderRetried{Provider,Status}` with a rollup
(`docs/ops/metrics.md`; `Status` is `429`, `5xx` or `transport`) and
writes a WARN line with the exact status, the attempt and the wait; what
the stage finally sees is the last refusal itself, wrapped with the
attempt count, so `ProviderRateLimited`, `ProviderTimedOut`, the routing
fallback and the capture's verdict mean what they meant. What a retry can
cost: the meter reserves and releases per call, so a refused attempt costs
this side nothing, but a 5xx sent after the provider generated the
completion may be billed by the provider once per attempt — bounded at
three, and at two for ask.

### How the retries compose

The stage retries — routing's `routeAttempts` (2) of
`RouteAttemptTimeout` each, ask's attempt and its retry at `askRetryShare`
— each give the client a fresh context, and the client's retries live
inside it. They compose by wall time, not by multiplying attempts: a dead
provider is asked at most six times during routing (two attempts of up to
three calls, refused fast), and the stage still takes at most two
attempts' worth of time. The worst case per stage is therefore the
deadline table, unchanged by the client:

| Stage | Stage attempts | Client calls per attempt | Worst-case wall time |
|---|---|---|---|
| transcribe | 1 | 3 | 5 min |
| route | 2 × 15 s | 3 | 30 s |
| cleanup | 1 | 3 | 2 min |
| clean-note | 1 | 3 | 3 min |
| ask | 20 s + 15 s | 2 | 35 s |

The client's six seconds of jittered waits fit inside the shortest window
a call runs under — a routing attempt, ask's retry — with more than half
of it left for the calls; a `Retry-After` may run to twelve, under the
same deadline. `TestStageWorstCaseWallTimeIsTheDeadlineTable` holds this
table and that fit; `TestProviderBoundsAreRegistered` holds the bounds.

A refusal that outlasts the retries is a provider verdict, as before: the
capture ends `failed` with its Retry button, nil goes back to Lambda, and
the transcript stays at `RawKey`, so the Retry resumes at cleanup and the
recording is transcribed and billed once (worker.md, "Retries, exactly-once
and the dead-letter queue").

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
config leaves it zero; the worst-case table above holds; a 529 burst at
cleanup ends the capture failed with the transcript kept, and the Retry
transcribes nothing again and charges the day what a burst-free run does.
`internal/provider/retry_test.go`, against an `httptest` server: 529, 529,
200 succeeds in three requests with two waits under the cap and two
`ProviderRetried`; a burst longer than the attempts fails with the last
status, and the count, after exactly three requests; `Retry-After` in
seconds and as a date is waited as sent, and one past the cap is not
waited on; a deadline shorter than the wait, or than the wait plus a
request as long as the refused one, means one request and the refusal
itself, at once; a 4xx other than 429 is one request, and so are an
unresolvable name and an unverifiable certificate; a cancellation during
the wait returns promptly with both errors; a reset connection is sent
again; Ask sends twice; a URL source is sent again and a `Body` is not.

History: `docs/backlog.md`.
