# API conventions

The rules that hold on every route, so no endpoint has to restate them.
Code: the error envelope in `backend/internal/httperr/httperr.go` and the
one mapping from domain error to status in `handler/errors.go` (`fail`);
the request reader and its caps in `handler/body.go`; route registration
in `handler/routes.go` and `router.go`; the correlation middleware in
`obs/middleware.go` (`Correlate`); CORS in `middleware/cors.go` and the
`HttpApi` block of `infrastructure/template.yaml`; the client's side in
`frontend/src/api/client.ts` and `problem.ts`. The contract itself is
`docs/api/openapi.yaml`, whose `info.description` states the three rules
that are not repeated per operation: authentication, errors, pagination.

## Errors: one envelope

Every non-2xx is `application/problem+json` (RFC 9457), the `Problem`
struct: `type`, `title`, `status`, and optionally `detail`, `instance` (the
request path), `correlation_id`, `current_version` and `reason`. `type` is
`about:blank` except for the two cases a client has to tell apart from
their neighbours by machine: `…/openapi.yaml#spend-capped`
(`httperr.TypeSpendCapped`), a 429 that means the daily budget and must
not be retried, where the gateway's throttling 429 carries the same title
and should be; and `…/openapi.yaml#retryable` (`TypeRetryable`), a 503
with `Retry-After: 1` that means a rolled-back operation can be repeated,
where a plain 503 means the instance is not configured for it. `reason` has
one value, `append_in_progress` on a 409 with `Retry-After: 2`, for the
save that must be repeated unchanged once a recording's paragraph has
landed (append-vs-autosave.md). net/http's own 404 and 405 are rewritten
into the same envelope (`problemFallback`,
`TestUnknownRoutesUseTheOneErrorEnvelope`).

`fail` is the only place a domain error becomes a status, and it matches
typed sentinels with `errors.Is`, never error text: a substring check would
turn a reworded message into a status change, and a wrapped S3 "not found"
into a 404 for an unrelated fault. Anything unmatched is a 500 whose text
is logged with the correlation id and never serialised; the body says
`the request could not be completed`
(`TestInternalServerErrorDoesNotSerialiseTheError`). Another tenant's id
is a 404, not a 403, so the response cannot confirm the id exists
(`TestHTTPCrossTenantNoteIsNotReadable` and its siblings).

### Fixed sentences

Every `detail` a user can see is a literal in `fail`, a handler or a
service sentinel — `no such resource`, `the note is archived; restore it
first`, `unknown device key`, `the daily provider
spend cap has been reached` — and the same situation always produces the
same words. The client never branches on them: `ApiError` exposes
`status`, `problemType`, `reason`, `currentVersion` and `retryAfterMs`,
and its predicates (`isSpendCapped`, `isAppendInProgress`, `isRetryable`)
read those fields. `isSpendCapped` matches `type` first and, for a 429
whose `type` is not the spend-cap one, falls back to `/spend|cap|budget/i`
over `type` and `title` — a fallback for a gateway's body, not the rule. A
body that is not a
problem document (a gateway's HTML 502) is replaced by a status-derived
title, so an upstream message never reaches the screen.

## Correlation

`Correlate` gives every request an id — the caller's `X-Correlation-Id`
when it passes `SanitizeCorrelationID`, a fresh one otherwise — echoes it
in the response header, puts it in every problem body and writes one
structured access line per request. The id travels into the worker inside
the invocation payload, so one capture is one trace from the request to
the append (worker.md). The gateway's `ExposeHeaders` names the header, or
a cross-origin page could not read it (`TestEveryProblemCarriesTheCorrelationID`).

## Headers a client sends

- `Authorization: Bearer <token>` — a Cognito token, verified in process
  by `middleware.Auth` (`auth.Verifier`, auth.md) on every route except
  `/v1/health`, `/v1/health/ready` (`public()`) and the three
  `/v1/inbox/*` routes, which take a device key instead (`device()`,
  inbox.md). Those five, plus `OPTIONS /{proxy+}`, are the only routes the
  gateway admits without its JWT authorizer; the conformance test holds
  the template's route table to the document
  (`TestPublicRoutesMatchTheGatewayRouteTable`).
- `Idempotency-Key`, 8–128 characters, on every route registered with
  `idempotent()`; the client sends a fresh `crypto.randomUUID()` on every
  POST, PUT, PATCH and DELETE (`newIdempotencyKey`). The record, the
  lease and what is replayed are idempotency.md's.
- `Content-Type: application/json` on a body; `X-Correlation-Id` when the
  caller has one. The client's default timeout is `DEFAULT_TIMEOUT_MS`
  (15 s). These four names are what `middleware.AllowedRequestHeaders`
  advertises, and the CORS test below holds both lists to them.

## Bodies and bounds

Every body is read under its route's cap through `http.MaxBytesReader`
(`readBody`, `body.go`); over it `readBody` answers 413 `the request body
exceeds <n> bytes` through `httperr.PayloadTooLarge`. The
default is `MaxSmallRequestBytes` (64 KiB); `POST /v1/notes` and `PATCH
/v1/notes/{id}` take `MaxNoteRequestBytes` (a `MaxNoteBodyBytes` 1 MiB body
plus 64 KiB of fields); `POST /v1/ask` takes `MaxAskRequestBytes` (16 KiB);
`POST /v1/inbox/text` takes `MaxInboxTextRequestBytes` (128 KiB),
`/inbox/captures` the default, and `/inbox/audio` is a multipart whose
recording is bounded by `MaxInboxAudioBytes` (4 MiB, inbox.md) with its own
413 sentence. An audio upload goes to S3 by
presigned PUT and is bounded by `service.MaxCaptureBytes` (256 MiB) at the
worker, not here. The idempotency wrapper reads the body under the same
cap (`router.bodyLimit`). `decodeJSON` requires a body, decodes with
`DisallowUnknownFields` and answers 400 with a fixed sentence either way,
which is what makes a field renamed on one side a test failure on the
other. Field bounds are constants in `body.go`: `MaxTitleRunes` 200
(`routing.MaxTitleRunes`), 32 aliases of 120 runes, 32 tags of 40,
`MaxMatchQuery` 2000, `MaxSearchQuery` 500.

Every collection is cursor-paginated: `limit` defaults to
`repository.DefaultListLimit` (50) and is clamped to `MaxListLimit` (200);
the cursor is opaque base64url bound to the query that minted it — a note
list's names a position (`pk`, shelf, position, id, direction;
`repository/cursor.go`), a capture list's wraps `LastEvaluatedKey`
(`dynamo_captures.go`), search's is `srch1:` plus its window (search.md) —
and one from another tenant or query is 400 `the cursor is not one this
API issued` (`TestListNotesClampsLimit`,
`TestListNotesRejectsAnotherTenantsCursor`).

## Pending captures say when to retry

A capture still in the pipeline carries `retry_after` (RFC 3339), the
instant `POST /v1/captures/{id}/retry` will first be allowed:
`service.CaptureRetryAfter`, which is `service.CaptureStuck`'s rule stated
as a time — the last write plus `CaptureStuckAfter`, or, for an
`appending` capture under its claim, the end of the claim's lease,
whichever is later. The client waits for the server's instant instead of
keeping its own copy of the two durations; a retry before it is 409
`ErrCaptureInFlight` either way. It is null on a finished capture
(`TestCaptureRetryAfterIsTheInstantCaptureStuckFirstHolds`, the
`captureInFlight` fixture).

## Rate limits

The gateway stage throttles the whole API at 50 requests a second with a
burst of 100 (`ApiStage`, `ThrottlingRateLimit`, `ThrottlingBurstLimit`),
answering its own 429; the API function has
`ReservedConcurrentExecutions: 50` because one page load fires several
requests at once. The three inbox routes, the only ones anyone on the
internet can post a body to, are held to one request a second with a burst
of ten per route (`RouteSettings`); inbox.md owns that number and the
per-key daily limit.

## CORS

One origin, never a wildcard: `ALLOWED_ORIGIN` is the Pages site's origin
(`scripts/ci-deploy-stack.sh` derives it from the Pages host, or from
`app_host` when an instance sets a custom domain), the API refuses to start
on `*` (`cmd/api/main.go`) and `middleware.CORS` reflects exactly that
origin with `Allow-Credentials: true`. What a browser is told, though, is
the gateway's `CorsConfiguration`: `OptionsRoute` sends `OPTIONS /{proxy+}`
to the Lambda without the authorizer, the middleware answers 204, and the
gateway overwrites the CORS headers on the way out from its own list —
`Authorization`, `Content-Type`, `Idempotency-Key`, `X-Correlation-Id`,
`X-Amz-Date`, `X-Api-Key`, `X-Amz-Security-Token`; the six methods;
`ExposeHeaders: X-Correlation-Id`; `MaxAge: 86400`. A header the client
sends that is missing there fails the preflight with no clue which.
`middleware.AllowedRequestHeaders` names the four the client sends, and
`TestMiddlewareCORSHeadersAreInTheGatewaysList` (`handler/openapi_conformance_test.go`)
reads the template's list and fails when the middleware names a header the
gateway does not, or omits one the client sends (`TestCORS` covers the
middleware's own behaviour). `X-Device-Key` is deliberately absent, so a
browser page cannot present a device key.

## Versioning

`info.version` in `openapi.yaml` is the version of the contract and moves
only when the contract does; it is independent of the release tag
(`vX.Y.Z`) the deploy pipeline cuts on every production deploy. Alpha
means no compatibility shim: a wire change updates the document, the
types and the fixtures in one pull request.

## The shared responses

`components.responses` holds the envelopes operations reference instead of
redescribing: `BadRequest`, `Unauthorized`, `NotFound`, `Conflict`,
`NoteConflict` (the one with `current_version` and `append_in_progress`),
`PayloadTooLarge`, `SpendCapped`, `Retryable`, `NotConfigured` (the
feature's service or the idempotency store is absent on this instance) and
`ServerError`.

## The document is held to the code, and the code to the client

Three tests in `backend/internal/handler` keep the three descriptions of
the surface from drifting:

- `openapi_conformance_test.go` — every operation the document declares
  is routable, every route the router registers is declared, every status
  it declares is one the code can produce, and the gateway route table in
  the template agrees about which routes are public.
- `TestContractResponsesAreWhatTheFrontendTypesDeclare` drives the real
  router over `httptest` and writes one body per interesting shape into
  `frontend/src/api/__fixtures__/responses.ts` as object literals
  annotated with the types in `frontend/src/api/schema.ts`; TypeScript's
  excess-property check on a fresh literal makes an added, renamed or
  retyped field a `bun run typecheck` failure.
- `TestContractRequestsFromTheFrontendAreAccepted` replays
  `__fixtures__/requests.json` — recorded by
  `frontend/src/api/contract-requests.test.ts` driving the real client
  against a stub fetch — through the router, where `DisallowUnknownFields`
  turns a one-sided rename into a 400.

The fixtures are generated, never edited: `cd backend &&
CHINTAN_UPDATE_FIXTURES=1 go test ./internal/handler/ -run Contract`
rewrites `responses.ts`, `bun run test` in `frontend/` rewrites
`requests.json`. The contract job in `.github/workflows/ci.yaml` runs
both, typechecks, runs `src/api/contract.test.ts`, fails on any `git
diff` under `__fixtures__/`, and then renames one field in `wire.go` to
prove the check still bites.
