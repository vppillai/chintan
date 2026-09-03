# Chintan backend API layer — code review

Scope: `backend/cmd/api`, `internal/{handler,middleware,auth,service,upload,httperr,keys,routing,match,model}`, `docs/api/openapi.yaml`. Repo at commit `9aa4435` (as cloned on `orb`). All line numbers are against the working tree in `/Users/vpillai/Downloads/chintan/chintan/backend`.

## Summary

The API layer is in good shape for what it is. Identity is derived only from a verified Cognito JWT (`alg` pinned to RS256, `iss`, `exp`, `iat`, `token_use`-gated `aud`/`client_id`, 30 s leeway, `kid`-miss refresh rate-limited), every DynamoDB read and write is keyed on `USER#<sub>` from that identity, S3 keys are validated by `keys.check`, cursors are checked against partition and query, error bodies never carry infrastructure text, and there is an HTTP-level cross-tenant test suite. `go vet`, `staticcheck`, `golangci-lint` and `go test -race ./...` are all clean; `govulncheck` reports no reachable vulnerabilities. I found **no critical (auth-bypass / cross-tenant) defect**.

What I did find:

- **Two High correctness bugs.** (1) A 5xx during an `Idempotency-Key` request leaves the key claimed-but-unfinished, so every retry with the same key answers `409 an identical request is still in flight` for 24 hours — the opposite of what the code comment promises. (2) A failed *second-device* biometric enrolment calls `DeleteAllWebAuthnCredentials`, wiping the first device's credential too.
- **Several Medium items**: JWKS refresh failure after the 1 h TTL turns into a total 401 outage instead of serving the stale key; WebAuthn sign-counter regression (`CloneWarning`) is ignored; a store-level version conflict reports `current_version: 0`; biometric login hands the refresh token back to the browser, which then persists it (making the sealed vault mostly decorative); `GET /v1/notes/{id}` silently caps captures at 50; the `?tag=` filter is case-sensitive against lowercased stored tags; `/health/ready` is an unauthenticated, unthrottled DynamoDB+S3 round trip.
- **Over-engineering** is moderate rather than egregious: roughly 450–600 non-test lines in scope exist only for configurations that cannot occur (a store without `ListCaptures`, a router without Search/Tags/Export, a Lambda whose CORS the gateway does not already handle, a presigner that cannot tag) plus ~400 lines of tests for them. Another ~600 lines (readiness probe, per-container rate limiter, export "job" ceremony) are defensible but arguably unnecessary for a single-user instance behind API Gateway throttling.
- **Tests are behaviour tests**, not mock tests: the handler suite drives the real service layer over an in-memory store; WebAuthn ceremonies are exercised with a virtual authenticator. Coverage is 79–92 %. The gaps line up exactly with the bugs above (no 5xx-then-retry idempotency test, no multi-credential enrolment test, no clone-counter test, JWKS test *pins* the outage behaviour).
- **Dependencies** are lean and current; `golang.org/x/crypto v0.52.0` (indirect via go-webauthn) has four advisories that govulncheck says are unreachable — bump anyway.

## Critical

None found.

## High

### H1. A 5xx pins the Idempotency-Key to 409 for 24 hours
- **Where:** `internal/handler/idempotency.go:104-139`; `internal/repository/dynamo.go:1181-1243`; `internal/repository/memory/memory.go:461-491`.
- **What:** `BeginIdempotent` claims the key with `idem_done=false` and a 24 h TTL (`dynamo.go:1166-1187`). The wrapper then runs the handler and only calls `CompleteIdempotent` when `status < 500` (`idempotency.go:133`). On a 5xx (or a panic, which unwinds past this code) the record stays claimed and undone. The next request with the same key and body hits the conditional-put failure, matches the fingerprint, sees `Done == false` and a different attempt token, and returns `ErrIdempotencyInFlight` (`dynamo.go:1235-1242`) → `409 an identical request is still in flight` (`errors.go:34-35`). The comment at `idempotency.go:130-132` says "a retry of a transient failure must be allowed to succeed" — it cannot, unless the client mints a new key, which defeats the header's purpose on exactly the path (transient DynamoDB/S3 error) where a retry matters most.
- **Why it matters:** `POST /v1/captures` and `POST /v1/notes` always carry the header from the frontend. One transient 500 during a capture start locks that recording out for a day with a misleading "in flight" message.
- **Fix:** add `Store.AbandonIdempotent(ctx, tenant, key)` (DeleteItem conditioned on `idem_attempt = :token`, which means `BeginIdempotent` must return the token or a handle) and call it in a `defer` that also recovers panics; or write the claim with a short `idem_expires_at` (say 60 s) and extend it on completion. Add a test: handler returns 500 once, then 201 on replay.

### H2. Failed enrolment of a second device deletes every device's credential
- **Where:** `internal/service/webauthn.go:183-202`.
- **What:** `FinishRegistration` stores the new credential (`:183`), then if Cognito rejects the refresh token, the verified `sub` mismatches, or `Seal` fails, it calls `s.store.DeleteAllWebAuthnCredentials(ctx, userID)` (`:190, :195, :200`). That deletes *all* credentials in `USER#<sub>` — including ones enrolled from other devices that are working fine — while leaving the existing refresh vault in place. The test `TestFinishRegistrationUnwindsWhenCognitoRejectsTheRefreshToken` (`webauthn_ceremony_test.go:296`) only ever has one credential, so it cannot see this.
- **Why it matters:** enrolling a laptop with an expired refresh token silently un-enrols the phone; the phone's next unlock gets a generic 401 ("biometric verification failed") with no hint why.
- **Fix:** delete only the credential just written (`credential.ID` is in hand; add `Store.DeleteWebAuthnCredential(ctx, tenant, credID)` that removes both the `USER#` row and the `pkWebAuthnCredList` row). Better still, do the Cognito refresh and `sub` check *before* `storeCredential` so there is nothing to unwind. Add a two-credential test.

## Medium

### M1. JWKS refresh failure after TTL is a full authentication outage
- **Where:** `internal/auth/jwks.go:71-99`, pinned by `internal/auth/jwks_test.go:118-146`.
- **What:** `cached()` returns `false` once `fetchedAt` is older than the 1 h TTL regardless of whether the `kid` is present (`:94-96`). `Key()` then calls `refresh()`; if Cognito's JWKS endpoint is unreachable or returns non-200, the error propagates and `Verify` fails → `401`. The still-valid key is in memory but deliberately not used. The test asserts this behaviour ("expected the upstream 500 to surface").
- **Why it matters:** every warm Lambda container crosses the hour boundary; a 5 s DNS/TLS hiccup at that moment turns into 401s, which the frontend treats as session expiry and forces a re-login. Cognito key rotation is rare and the `kid` miss path already handles it.
- **Fix:** on refresh failure, if `kid` exists in the stale set, return it (optionally log a warning); only fail when the `kid` is genuinely unknown. Change the test accordingly.

### M2. Sign-counter regression (`CloneWarning`) is ignored
- **Where:** `internal/service/webauthn.go:254-266`.
- **What:** go-webauthn v0.17.4 does not fail `ValidateDiscoverableLogin` on a counter regression; it sets `credential.Authenticator.CloneWarning = true` (`webauthn/authenticator.go:61` in the module) and leaves the decision to the RP. The code never reads it, stores the regressed counter via `storeCredential`, and issues Cognito tokens. `TestStoreCredentialAndUserRoundTripTheAuthenticatorRecord` asserts the counter is *stored* but nothing asserts it is *enforced*.
- **Why it matters:** the counter is the only clone-detection signal WebAuthn offers. Platform authenticators often report 0 (in which case the library never flags), so the practical exposure is small, but when a counter is present the current code accepts a cloned authenticator silently.
- **Fix:** `if credential.Authenticator.CloneWarning { log; return nil, ErrWebAuthnVerification }` before `storeCredential`. Add a test with the virtual authenticator replaying a lower counter.

### M3. Store-level version conflict reports `current_version: 0`
- **Where:** `internal/service/notes.go:345-347`; `internal/handler/notes.go:182-188`.
- **What:** `UpdateNote` returns the *current* note on the cheap pre-check conflict (`:283`) and on the ETag conflict (`:318`), but on the conditional `PutNote` conflict it returns `model.NoteIndex{}, err` (`:345-347`). The handler then calls `failConflictAt(w, r, err, updated.Version)` with `updated.Version == 0`, and `httperr.Conflict` serialises `current_version: 0` (a non-nil pointer to zero). `TestUpdateRequiresVersionAndReportsTheCurrentOne` (`handler_test.go:274`) only exercises the pre-check path.
- **Why it matters:** the client's reconcile loop is told the note is at version 0 and will conflict again.
- **Fix:** on `ErrVersionConflict` from `PutNote`, re-read and return the current index (or return `note` with the version the store reported); or have the handler omit `current_version` when it is 0.

### M4. Biometric login returns the refresh token to the browser, and the browser keeps it
- **Where:** `internal/service/webauthn.go:289-305`; `internal/model/types.go:240-246`; `frontend/src/api/tokens.ts:55-57`, `frontend/src/features/auth/useAuth.ts:243-247`.
- **What:** `FinishLogin` returns the full `CognitoTokenSet` including `RefreshToken` (Cognito returns the same refresh token on a `REFRESH_TOKEN_AUTH` grant, and `CognitoRefresher.Refresh` explicitly carries the input token forward at `cognito_refresh.go:36-39`). The frontend persists it. From that point the device can refresh indefinitely without biometrics; the sealed vault protects nothing the browser does not also hold in `localStorage`/IndexedDB.
- **Why it matters:** this is a design gap rather than a bug — the vault still lets a user unlock after their local session is gone — but the README and `vault_box.go` header describe the vault as *the* holder of the token. Either the intent is "biometric gates session start, not refresh" (then document it) or `refresh_token` should be stripped from the biometric login response so an idle device must re-assert.
- **Fix:** decide, then either blank `tokens.RefreshToken` before returning at `:305` or update the docs. The OpenAPI `TokenSet` already makes `refresh_token` optional (`openapi.yaml:1152-1160`).

### M5. `GET /v1/notes/{id}` silently truncates captures to 50 and re-reads the note
- **Where:** `internal/handler/notes.go:142-150`; `internal/service/capture.go:393-398`; `internal/repository/store.go:36`.
- **What:** `getNote` calls `ListCapturesForNote(..., repository.ListOptions{})` which uses `DefaultListLimit = 50` and discards the returned cursor; `NoteDetail` has no cursor field. A note with 51+ captures shows 50 with no indication. `ListCapturesForNote` also does its own `GetNote` (`capture.go:394`) immediately after `GetNoteDetail` already did one — three DynamoDB reads plus one S3 GET per note open.
- **Fix:** either `DrainPages` with a bound (captures per note is small) or expose `captures_cursor`; drop the second `GetNote` (the handler already has the note).

### M6. `?tag=` filter is case-sensitive against lowercased stored tags
- **Where:** `internal/handler/notes.go:68-82`; `internal/service/notes.go:76-101`.
- **What:** `normalizeTags` lowercases and collapses whitespace on write; the list filter compares the raw query value with `t == tag`. `GET /v1/notes?tag=Roof` returns nothing for a note tagged `roof`. `TestNotesCanBeFilteredByTag` (`search_test.go:129`) uses a lowercase tag and cannot catch it. The tag list endpoint returns lowercase names so the UI probably never sends mixed case, but the API contract does not say the parameter is case-sensitive.
- **Fix:** run the query value through the same normalisation (export a `NormalizeTag`).

### M7. `/health/ready` is an unauthenticated, unthrottled DynamoDB + S3 round trip
- **Where:** `internal/handler/routes.go:19`; `internal/handler/health.go:27-53`; `internal/service/readiness.go:60-81`.
- **What:** the route is `public()` with no `perIP` limiter, and every call does a `GetItem` and an S3 `GetObject`. Nothing in the stack consumes it (no orchestrator; the smoke test in CI hits it once). It is the cheapest anonymous lever on the bill after the login routes, which *are* rate-limited (`routes.go:62-63`).
- **Fix:** either put it behind auth, add the same `perIP` limiter, or delete it (see O5).

### M8. `golang.org/x/crypto v0.52.0` carries four advisories
- **Where:** `backend/go.mod:57`.
- **What:** `govulncheck -show verbose` lists GO-2026-6355, GO-2026-6354, GO-2026-6303 (fixed in v0.55/v0.56) and GO-2026-5932 (no fix) in `golang.org/x/crypto@v0.52.0`, reached via `go-webauthn`. Symbol analysis says none are called from this code. The `go.mod` comment block is meticulous about the Go toolchain patch level for exactly this class of finding, so the indirect dep should get the same treatment.
- **Fix:** `go get golang.org/x/crypto@v0.56.0 && go mod tidy`.

## Low

### L1. Replayed idempotent 4xx is served as `application/json`
`internal/handler/idempotency.go:113-118` hard-codes `Content-Type: application/json` on replay. A stored 400/409 body is `problem+json`; the replay violates the "every non-2xx is problem+json" rule (`httperr.go:3-4`) and the conformance harness (`problemOf`, `testsupport_test.go:170-183`) would fail on it — it just never replays an error. Store the original Content-Type in `IdemRecord` or set it from `buffered.header`.

### L2. WebAuthn challenge is not single-use under concurrency
`internal/service/webauthn.go:230-234` loads the challenge, then deletes it in a `defer`. Two concurrent `FinishLogin` calls with the same captured assertion both succeed within the 5 min window. Requires the attacker to hold a valid assertion, so impact is "two token sets to the same holder". A conditional delete before validation (`DeleteWebAuthnChallenge` returning `ErrNotFound` if already gone) closes it.

### L3. `FinishLogin` userHandle fallback is dead and passes an unvalidated string as a partition key
`internal/service/webauthn.go:244-249`: if the credential-id lookup fails, `s.user(ctx, string(userHandle))` queries `USER#<attacker bytes>`. It cannot succeed — the same credential id is looked up again at `:259-263` and fails identically — so it is dead code that also bypasses the "no unvalidated tenant id reaches a key" rule from the spec (§4.1). Delete the branch.

### L4. Repository does not validate tenant ids
Spec §4.1 says `internal/repository` validates tenant identifiers against the `keys` charset so a `#` in a subject cannot collide. `userPK` (`dynamo.go:78-80`) and friends do no such check. Cognito subs are UUIDs, so no practical exposure today, but the invariant the spec relies on is enforced only for S3 keys (`keys.go:8-15`).

### L5. `POST /v1/notes` with a body does eight round trips and returns version 2
`internal/handler/notes.go:103-123`: `CreateNoteWithTags` (2 S3 PUTs + 1 conditional Dynamo put) then `UpdateNote` (GetNote, GetWithETag, PutIfMatch, meta PUT, PutNote). A freshly created note has `version: 2`. Pass the body into `CreateNoteWithTags`.

### L6. Enqueue failure after a state reset leaves the capture stranded-but-pending
`internal/service/capture.go:290-303, 355-367`: `PutCapture` resets status to `uploaded`/`transcribed`/`cleaned` and clears `Error`, *then* enqueues. If `EnqueueCapture` fails the row shows as pending with no worker coming. A second retry fixes it, but the UI shows "processing" until then. Enqueue first or record the failure back on the row.

### L7. `beginCapture` returns 404 for an unknown `note_id` in the body
`internal/service/capture.go:183-187` wraps `ErrNotFound`; `fail` maps it to 404 on a `POST /v1/captures`. The resource being created is the capture, not the note — 400/422 with "note_id does not name one of your notes" is the conventional answer. OpenAPI (`:394-405`) does not declare 404 for this operation either; the conformance test checks declared statuses are *producible*, not that produced statuses are *declared*.

### L8. Test hooks compiled into the production binary
`internal/service/cognito_refresh.go:49-73` (`FakeRefresher`), `internal/auth/verifier.go:52` (`http://127.0.0.1` issuer accepted), `internal/middleware/auth.go:16-18` (`WithUserID`, test-only per `deadcode`). Move to `_test.go` or an `internal/testing` package.

### L9. Spend day boundary is UTC
`internal/service/spend.go:85` formats the day in UTC; the worker's breaker presumably does the same, so the numbers agree, but a user in UTC-8 sees the cap reset at 4 pm. Cosmetic for one user; document or make the day zone a setting.

### L10. Cold start does a network call to SSM in `init()`
`cmd/api/main.go:122, 217-247`: `GetParameter` runs on every cold start before the handler is registered; five AWS clients (DynamoDB, S3, SQS, SSM, Cognito IdP) are constructed. This is fine — it is one call and clients are cheap — but the vault key could be lazily loaded on the first biometric request so a cold start on `GET /v1/notes` does not pay for it. JWKS is already lazy.

### L11. Rate limiter is per container
`internal/handler/ratelimit.go:29-35` says so explicitly. With Lambda concurrency N an attacker gets N × 10/min; API Gateway throttling is the real control. Not a bug, but see O6 for whether the code earns its keep.

## Over-engineering / deletable

Non-test lines in scope: **7,430** (`find ... | xargs cat | wc -l` on `orb`). Test lines in scope: ~10,700 (handler 3,750; service 4,700; auth 465; middleware 250; upload 237).

| # | What | Where | Why it is dead or unnecessary | Lines (non-test / test) |
|---|------|-------|-------------------------------|-------------------------|
| O1 | `TenantCaptureLister` interface + `listCapturesByWalk` + walk cursor | `service/capture_list.go:61-78, 135-253` | `repository.Store` already declares `ListCaptures` (`store.go:131`), so the type assertion at `:100` can never fail. The fallback pays a Query per note and mints its own cursor format. | ~145 / ~200 (`capture_list_test.go`) |
| O2 | Lambda-side CORS middleware | `middleware/cors.go`, `middleware/cors_test.go`, `handler/router.go:91` | `infrastructure/template.yaml:1635-1665` configures CORS on the HTTP API with `MaxAge: 86400`; for HTTP APIs the gateway answers preflight itself and overrides integration CORS headers. The template comment acknowledges the Lambda list is not what browsers see. Two sources of truth, one of them inert. | 40 / 70 |
| O3 | `ObjectsPresigner` fallback | `upload/objects.go`, `service/capture.go:145, 158-163` | Exists so `NewCaptureService` works without `WithUploads`. Production always wires `S3Presigner`; tests could pass a fake. The fallback's own doc says it must never reach production. Make the presigner a constructor argument. | 46 / 0 |
| O4 | Nil-dependency 503 branches | `handler/search.go:30, 74`, `export.go:22, 41`, `notes.go:143`, `router.go:29-33` | `Search`, `Tags`, `Export`, `Readiness`, `Captures` are always constructed in `main.go:138-152`; only `WebAuthn`, `Spend` and `Store` have a real nil meaning. | ~25 / 0 |
| O5 | Readiness probe | `handler/health.go:21-53`, `service/readiness.go`, `service/readiness_test.go`, `handler_test.go:28-69` | No orchestrator consumes it; CloudWatch alarms watch Lambda errors and 5xx. It is also M7. | ~130 / ~200 |
| O6 | Per-IP rate limiter | `handler/ratelimit.go`, `ratelimit_test.go` | Per-container brake on two routes already behind API Gateway stage throttling. Defensible, but 370 lines for a control the author describes as "not a distributed quota". Optional. | 183 / 188 |
| O7 | Export "job" indirection | `service/export.go:37-44, 153-206` | Work is synchronous; the job record (`job.json` in S3, `pending/running/failed` states never produced) exists so the endpoint "can become asynchronous later". YAGNI for a feature used "a handful of times a year". | ~60 / ~80 |
| O8 | `keys` package repetition | `keys/keys.go` | Eight near-identical 9-line functions; `CaptureMeta` is unreachable (`deadcode`). One `capture(userID, id, name)` helper plus two note helpers is ~35 lines. | ~80 saved / 0 |
| O9 | Single-implementation interfaces and aliases | `service.NoteCreator`, `service.Enqueuer`, `service.SpendCounter`, `handler.SpendGate`, `service/capture_status.go:9-15`, `NotesService.DeleteNote` (`notes.go:354-357`), `httperr.MethodNotAllowed` (`deadcode`), `webauthn.go:244-249` (L3) | Each has exactly one production implementation and exists to avoid an import edge or was left from a rename. `MethodNotAllowed` and the `DeleteNote` wrapper have zero callers. | ~50 / 0 |
| O10 | `Deps.DefaultSpendCapMicros` duplicates `SpendGate.defaultCapMicros` | `handler/router.go:56`, `main.go:108, 151` | Same env var read twice into two places. Expose it from the gate. | ~5 |

Confident deletions (O1–O4, O8–O10): **~450 non-test + ~270 test lines**. Arguable (O5–O7): a further **~370 non-test + ~470 test lines**. Not deletable but worth noting: `internal/repository/memory/memory.go` (814 lines) and `internal/provider/fake/fake.go` (192 lines) are test doubles compiled in the non-test tree because they are shared across packages; `deadcode` flags every method. `testdoubles_guard_test.go` exists to keep them out of the binaries, but it `t.Skip`s when `go` is not on `PATH` (`:19`).

Things that look like over-engineering but are earning their keep: the idempotency layer (real double-tap problem on mobile), optimistic concurrency + ETag on note bodies (real race with the worker's append), the `problem+json` envelope, cursor scoping, the `fallbackWriter` for 404/405, and the retention-tag presigner middleware.

## Test quality

**Good:**
- Handler tests run the real router + real services over `memory.Store`/`memory.Objects` (`handler/testsupport_test.go:63-97`). They assert on HTTP status, body shape and *subsequent state* (e.g. `TestIdempotentReplayReturnsTheOriginalResponse` counts notes; `TestHTTPCrossTenantNoteIsNotEditable` re-reads as the owner). These are behaviour tests.
- `isolation_test.go` covers read/list/delete/edit/capture/download/retry across tenants and unauthenticated access to five data routes.
- `openapi_conformance_test.go` cross-checks OpenAPI paths/methods/statuses against `routes.go` and the API Gateway route table; `contract_test.go` checks responses against the frontend's TypeScript fixtures. This is what keeps the three descriptions of the surface honest.
- WebAuthn: `webauthn_ceremony_test.go` uses a virtual authenticator to run real registration and login ceremonies, including expired challenge, replayed response, wrong authenticator, sub mismatch, unreadable vault. Handler-level WebAuthn tests use `fakeWebAuthn` and only test error→status mapping, which is appropriate given the service tests.
- JWKS tests cover TTL, unknown `kid`, rate limiting, concurrent misses, empty document.
- Coverage: handler 79.1 %, auth 90.2 %, middleware 92.1 %, service 83.0 %, upload 81.6 %.

**Gaps (each maps to a finding above):**
- No test for a 5xx during an idempotent request followed by a retry (H1). The only in-flight test (`repository/dynamo_test.go:585`) tests the concurrent case.
- No test enrols two credentials and fails the second (H2).
- No test for a sign-counter regression (M2); `webauthn_binding_test.go:374-399` asserts the counter is stored, not enforced.
- `TestKeySetSurfacesFetchFailureWithoutPoisoningCache` (`jwks_test.go:118`) *asserts* the outage behaviour in M1 — it will need to change with the fix.
- Store-level `PutNote` conflict path (M3) untested at the handler; `handler_test.go:274-320` only reaches the pre-check.
- Tag filter test uses lowercase only (M6).
- No test for a note with more than `DefaultListLimit` captures in `GET /v1/notes/{id}` (M5).
- `testdoubles_guard_test.go:19` skips silently without `go` on `PATH` — a guard that can pass by not running.
- Handler tests never inject a `Verifier`; they always set identity via `middleware.WithUserID`. That is fine for route logic, but it means the `Auth` middleware's interaction with the router (e.g. that `idempotent()` runs *inside* auth) is only proven by `TestHTTPDataRoutesRequireAuthentication`.

**Tests that cannot fail:** none found. No assertion-free tests; the one `t.Skip` is noted above.

## Tool output

Run on `ubuntu@orb`, `~/temp/chintan/backend` at `9aa4435`, Go from `/usr/local/go` with `GOTOOLCHAIN=auto` (govulncheck switched to go1.26.8).

```
== go vet ==
(clean)

== go test -cover ==
ok  github.com/vppillai/chintan/backend/internal/handler     0.184s  coverage: 79.1% of statements
ok  github.com/vppillai/chintan/backend/internal/auth        0.566s  coverage: 90.2% of statements
ok  github.com/vppillai/chintan/backend/internal/middleware  0.001s  coverage: 92.1% of statements
ok  github.com/vppillai/chintan/backend/internal/service     0.023s  coverage: 83.0% of statements
ok  github.com/vppillai/chintan/backend/internal/upload      0.002s  coverage: 81.6% of statements

== go test -race ./... ==
(all packages ok; no output after filtering "ok" and "no test files")

== staticcheck ./... ==
(no findings, exit 0)

== golangci-lint run ./... ==
0 issues.

== deadcode ./... (in-scope entries) ==
internal/httperr/httperr.go:161:6: unreachable func: MethodNotAllowed
internal/keys/keys.go:107:6: unreachable func: CaptureMeta
internal/middleware/auth.go:16:6: unreachable func: WithUserID
internal/obs/metrics.go:41:6: unreachable func: SetMetricOutput
internal/repository/memory/memory.go: every method (test double compiled in non-test tree)
internal/provider/fake/fake.go: every method (same)
(plus chintanctl/ports.go StringAttr/NumberAttr, breaker.WithClock, meter.MultiSink.Record,
 pipeline.NewDynamoUsageSink/DynamoUsageSink.Record, repository.NoteIndexKeys — outside scope)

== govulncheck ./... ==
No vulnerabilities found.
Your code is affected by 0 vulnerabilities.
This scan also found 0 vulnerabilities in packages you import and 4
vulnerabilities in modules you require, but your code doesn't appear to call
these vulnerabilities.

== govulncheck -show verbose (module-level) ==
GO-2026-6355  golang.org/x/crypto@v0.52.0  fixed in v0.56.0
GO-2026-6354  golang.org/x/crypto@v0.52.0  fixed in v0.56.0
GO-2026-6303  golang.org/x/crypto@v0.52.0  fixed in v0.55.0
GO-2026-5932  golang.org/x/crypto@v0.52.0  fixed in N/A
```

Dependency review (`backend/go.mod`): direct deps are aws-lambda-go, aws-sdk-go-v2 (config, credentials, attributevalue, cloudformation, cognitoidentityprovider, dynamodb, s3, sqs, ssm, sts), smithy-go, aws-lambda-go-api-proxy, go-webauthn v0.17.4, golang-jwt/jwt v5.3.1, google/uuid. All maintained and current. `cloudformation` and `sts` are for `chintanctl`, not the API binary. `aws-lambda-go-api-proxy` is the one slightly sleepy dependency (last tagged release 2023) but it is thin and used only for `httpadapter.NewV2`; it could be replaced with ~60 lines if it ever bit-rots. Nothing heavy or unnecessary. Bump `golang.org/x/crypto` (M8).
