# Idempotent replay

Why a double-tap on a flaky link makes one note, not two. Code: the route
wrapper (`backend/internal/handler/idempotency.go`, `router.idempotent`),
the routes that opt in (`handler/routes.go`, `idempotent()`), the record
(`repository.BeginIdempotent`, `CompleteIdempotent`, `AbandonIdempotent` in
`repository/dynamo_misc.go`; `IdemTTL`, `IdemClaimLease` in `store.go`) and
the client that sends the key (`frontend/src/api/client.ts`).

## The contract

`Idempotency-Key` is optional on the wire, 8 to 128 characters when sent;
the app sends one on every mutating request (the uploader keys a capture on
the recording's `localId`, so every attempt at the same recording shares a
key). Without the header the request passes straight through — refusing an
unkeyed POST would break every caller that has no idea what it is for.
Every POST and PATCH that creates, moves or re-runs something is wrapped:
notes, pins, settings, clean, regenerate, Ask, captures and their target,
retry, retranscribe and move, the three inbox routes, export (the list is
`routes.go`). A ring's webhook cannot set the header and files twice on a
retry (`inbox.md`).

## The mechanics

1. The body is buffered under the route's own cap and the key is bound to a
   fingerprint, `sha256(method path \n body)`. A replay with a different body
   is 409 `this Idempotency-Key was used for a different request` — handing
   back the stored answer to a request the caller did not make is worse than
   failing. A re-encoded multipart form has a new boundary, so it is this 409.
2. `BeginIdempotent` claims `USER#<tenant>` / `IDEM#<key>` with a conditional
   put. A key already claimed and not yet answered is 409 `an identical
   request is still in flight`; a key with an answer returns the record, and
   the wrapper replays its status, body and `Content-Type` with
   `Idempotency-Replayed: true`, doing no work. The attempt token on the put
   tells the SDK's own retry of a committed write from a genuine duplicate.
3. After the handler runs, only a **settled** response is recorded
   (`settled`: below 500, not 409, not 429): a 5xx, a conflict or the spend
   cap are answers the same request would not get tomorrow, and recording a
   429 once pinned "Resend tomorrow" to the recorded 429 for a day (review
   2026-09-21, T13). Anything else, and a handler panic, releases the claim
   (`AbandonIdempotent`, in a `defer`).
4. A recorded answer is honoured for `IdemTTL` (24 h); a bare claim only for
   `IdemClaimLease` (60 s), the backstop for a Lambda killed mid-request
   that no `defer` can reach — the API function's timeout is 29 s. The
   record shares the table's one `ttl` attribute.

An instance without a store answers 503 `idempotent replay is not configured
on this instance` rather than doing the work and pretending.

## What it does not do

It is per tenant and per key; it does not dedupe two keys for the same
words. Appends are made safe a second way, by the capture's claim and the
stamp (`append-vs-autosave.md`), so a replayed `POST /v1/captures/{id}/retry`
and a Lambda retry of the same task meet the same guard.
