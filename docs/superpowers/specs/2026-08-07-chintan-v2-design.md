# Chintan v2 — Production Design

**Date:** 2026-08-07
**Status:** Approved for planning
**Supersedes:** `2026-08-06-chintan-design.md` (clean-slate v1), which remains accurate as a statement of product intent
**Audit this responds to:** `docs/audit/2026-08-07-production-readiness-audit.md`
**Rollback point:** tag `v0.2.0-prerewrite`, branch `pre-rewrite/main-2026-08-07`

---

## 1. Purpose

v1 proved the product: speak a thought, have it transcribed, cleaned, and filed into the right note. The core loop works and the LLM-safety work inside it is sound.

v2 makes it releasable. Three things are true of the current build and none of them are cosmetic:

1. It has a live cross-tenant impersonation defect and no real JWT verification.
2. Its synchronous pipeline cannot outlast API Gateway's 30-second ceiling, which caps recording length and causes duplicate note content on the retry that follows.
3. Its frontend is a prototype — no build, no router, no state model, no accessibility, no offline capture — sold for a use case (driving, walking) it was never designed for.

v2 fixes all three, adds the features that make it a complete notes product rather than a capture demo, and builds the seams for a second user without paying for multi-tenancy today.

## 2. Scope decisions

| Decision | Choice |
|---|---|
| Product scope | Personal-first, **built so multi-user is a config change, not a rewrite**. Build the seams (real auth, tenant-scoped identity, isolation tests, idempotency, per-user quotas). Skip the bloat (audit log, per-tenant KMS indirection, consent ledger, billing). |
| Backend | Re-architect the edges. Keep `routing`, `cleanup`, `provider`, `keys`, `match` and their tests. |
| Frontend | Full rewrite: Vite + React + TypeScript. |
| Capture UX | Persistent progress card that survives navigation and reload. |
| Features added | Full-text search, transcript-synced inline playback, tags, export/backup. |
| Visual direction | Two themes: **Ink & Paper** (default), **Nocturne**, plus **Follow system**. |
| Navigation | Record-first home with a pull-up library sheet; collapsed sheet is a persistent strip. |

### 2.1 Non-goals

Unchanged from v1: no knowledge backlinks, no splitting one recording across notes, no billing or self-service signup, no native apps, no CarPlay/Android Auto, no real-time streaming transcription.

Newly and explicitly excluded, having been considered and rejected: OpenSearch or any managed search service (cost exceeds the rest of the stack at this scale); per-tenant KMS key indirection and consent event logging (they guard things that do not yet exist); append-only audit logging (required before a second real user, not before v2); and every piece of process apparatus from `archive/phase0-wip` — the 21-check CI matrix, the containerised toolchain, the three parallel documentation registers, the §11A metrics programme.

---

## 3. Architecture

```
GitHub Pages (PWA)  ──── Vite/React/TS static bundle, per-instance path
        │ HTTPS, Cognito JWT (verified twice: gateway + service)
        ▼
API Gateway HTTP API ──► API Lambda (Go, arm64)   fast paths only, <5s
        │                      │
        │                      ├─► DynamoDB   index, settings, usage, idempotency
        │                      └─► S3         presigned PUT/GET only
        ▼
   S3 ObjectCreated ──► SQS ──► Worker Lambda (Go, arm64)   long-running pipeline
                          │        transcribe → route → cleanup → append
                          │        writes segments.json, peaks.json, clean.txt
                          └─► DLQ + alarm
```

**The separation is the point.** The API Lambda never performs an operation that can exceed a few seconds. Everything slow is the worker's, which is invoked out-of-band and is not bound by the gateway's fixed 30-second integration timeout.

Instance isolation (`chintan-<instance>-<env>`), the console-navigable S3 layout, and the passbook deploy model are retained from v1 unchanged. They were never the problem.

---

## 4. Backend

Module path and Go version unchanged. Packages retained as-is: `internal/routing`, `internal/cleanup`, `internal/provider`, `internal/keys`, `internal/match`, and all of their tests.

### 4.1 Identity and authentication

A new `internal/auth` package performs real verification:

- JWKS fetched from the instance's Cognito issuer and cached in memory with a TTL and a `kid`-miss refresh.
- Verified claims: signature, `iss`, `aud` (client id), `exp`, `nbf`, and `token_use`.
- Verification runs in the service regardless of the gateway authorizer. Defence in depth is the requirement; the gateway is not the only ingress and never sanitised headers.

`X-User-ID` is deleted from `middleware/auth.go` and from the CORS `Access-Control-Allow-Headers` list. There is no test-only header path; tests construct an `Identity` directly.

```go
type Identity struct {
    UserID   string // Cognito sub
    TenantID string // == UserID in v2; the multi-user seam
}
```

Every repository and key-derivation call takes `TenantID`, not a bare string. Making Chintan multi-user later means populating `TenantID` from a claim instead of the subject — no storage or API change.

`service/webauthn.go`'s `subFromIDToken` is replaced by the same verifier. It currently guards the KMS-sealed refresh token with an unverified base64 parse.

`internal/repository` validates tenant identifiers against the same charset `keys` already enforces, so a `#` in a subject can never collide across the key space.

### 4.2 API surface

`net/http.ServeMux` method-and-wildcard patterns replace hand-parsed paths and `strings.Contains` error classification.

| Area | Endpoints |
|---|---|
| Health | `GET /v1/health` (liveness), `GET /v1/health/ready` (dependency probe) |
| Settings | `GET /v1/settings`, `PUT /v1/settings` |
| Notes | `GET /v1/notes`, `POST /v1/notes`, `GET|PATCH|DELETE /v1/notes/{id}` |
| Archive | `POST /v1/notes/{id}/archive`, `POST /v1/notes/{id}/restore`, `DELETE /v1/notes/{id}/permanent` |
| Match | `POST /v1/notes/match` |
| Search | `GET /v1/search?q=&cursor=&limit=` |
| Tags | `GET /v1/tags`, tag mutations via `PATCH /v1/notes/{id}` |
| Capture | `POST /v1/captures`, `GET /v1/captures/{id}`, `GET /v1/captures?status=pending`, `POST /v1/captures/{id}/target`, `POST /v1/captures/{id}/retry` |
| Media | `GET /v1/captures/{id}/download?kind=audio\|raw\|clean\|segments\|peaks` |
| Export | `POST /v1/export`, `GET /v1/export/{id}` |

Normative rules for every endpoint:

- **One error envelope**: RFC 9457 `application/problem+json` with `type`, `title`, `status`, `detail`, `instance`, and a `correlation_id`. Infrastructure error strings are logged, never serialised to the client.
- **Typed sentinel errors** in the service layer map to status codes in one place. No substring matching.
- `405` responses set `Allow`.
- `http.MaxBytesReader` on every body. Caps: note body 1 MiB, title 200 runes, 32 aliases of 120 runes each, 32 tags of 40 runes each.
- **Cursor pagination** on every list endpoint (`cursor`, `limit`, default 50, max 200), backed by `LastEvaluatedKey`. No unbounded query survives.
- `Idempotency-Key` accepted on every `POST` (see §4.5).
- No `Access-Control-Allow-Origin: *` code path exists; the origin is validated at startup and a wildcard is a fatal configuration error.

An OpenAPI 3.1 document is generated from the handler definitions and checked into `docs/api/openapi.yaml`, verified in CI against the router.

### 4.3 Asynchronous capture pipeline

**API Lambda (`POST /v1/captures`)** — validates, writes the capture row, returns a presigned PUT with a content-length range and a content-type constraint. Nothing else.

**Worker Lambda (`cmd/worker`)** — triggered by S3 `ObjectCreated` via SQS. Higher memory, long timeout, `maximumReceiveCount` into a DLQ with an alarm. It:

1. Transcribes by handing Groq a **presigned GET URL**, not audio bytes pulled through Lambda memory. Recording length stops being bounded by the heap.
2. Requests `verbose_json` with segment and word timestamp granularity; writes `raw.txt` and `segments.json`.
3. Computes and writes `peaks.json` — a downsampled amplitude envelope for waveform rendering.
4. Routes (existing `provider/openai_router.go` and its verifier, unchanged).
5. Cleans (existing `internal/cleanup`, unchanged).
6. Appends to the note under the idempotency guard in §4.5.

Status transitions (`uploaded → transcribing → routing → cleaning → appending → appended`, or `needs_target`, or `failed`) are written to DynamoDB after each stage and are what the frontend's progress card reads. Failure at any stage preserves every artifact produced so far and is resumable from the last good stage.

The correlation ID is generated at the API and carried through the SQS message attributes into every worker log line, so one capture is one greppable trace.

### 4.4 Data model

S3 layout is unchanged except for two new per-capture objects:

```
tenants/<tenantId>/notes/<noteId>/note.md
tenants/<tenantId>/notes/<noteId>/meta.json
tenants/<tenantId>/captures/<captureId>/audio.<ext>
tenants/<tenantId>/captures/<captureId>/raw.txt
tenants/<tenantId>/captures/<captureId>/segments.json   ← new
tenants/<tenantId>/captures/<captureId>/peaks.json      ← new
tenants/<tenantId>/captures/<captureId>/clean.txt
tenants/<tenantId>/captures/<captureId>/meta.json
```

DynamoDB stays single-table with `pk=TENANT#<id>`. New and changed items:

| `sk` | Purpose |
|---|---|
| `NOTE#<id>` | Gains `version` (optimistic concurrency), `tags []string`, `purge_after_epoch` (number, DynamoDB TTL attribute) |
| `CAPTURE#<id>` | Gains `version`, `append_token`, `appended_at`, `duration_ms`, `segments_key`, `peaks_key` |
| `USAGE#<yyyy-mm-dd>#<id>` | One record per billable provider call (§4.6) |
| `SPEND#<yyyy-mm-dd>` | Atomic counter, updated with `ADD` (§4.6) |
| `IDEM#<key>` | Idempotency record with TTL (§4.5) |
| `TAG#<name>` | Tag index for `GET /v1/tags` |

**GSI1** is added with `gsi1pk = TENANT#<id>#NOTE#<noteId>`, `gsi1sk = CAPTURE#<createdAt>`, so note→captures is a direct query instead of a full-partition scan with client-side filtering.

Item attributes that are queried or projected (`title`, `updated_at`, `tags`, `status`, `version`) become top-level attributes rather than fields inside an opaque `data` blob, so lists can use `ProjectionExpression` and stop transferring every snippet to render titles.

`purge_after` becomes an epoch integer so DynamoDB TTL performs expiry. The synchronous `purgeExpired` sweep is deleted from the read path entirely; a DynamoDB Streams handler performs the S3 cascade when TTL removes a note.

Two correctness fixes carried in the model work: snippets truncate by rune, not byte; and all timestamps are stored as `RFC3339` with fixed-width fractional seconds so lexicographic ordering is chronological ordering.

### 4.5 Idempotency and concurrency

**Idempotency.** Every `POST` accepts an `Idempotency-Key`. The service writes an `IDEM#<key>` item with a conditional put, storing an attempt token so an SDK-level retry of a committed write is distinguishable from a genuine duplicate and cannot lock the original caller out for the TTL. A repeated key returns the original response.

**Optimistic concurrency.** Notes and captures carry `version`; every write uses `ConditionExpression: version = :expected` and increments. A conflict returns `409` with the current state, and the client reconciles.

**S3 append safety.** `appendToNote` uses a conditional put with the read ETag. On mismatch it re-reads and retries with bounded backoff.

**Append idempotency.** The append is guarded by `append_token`: the worker writes the token and `appended_at` in the same conditional update that flips status to `appended`. A retry that finds a matching token skips the append entirely. This is the defect that today silently duplicates note content after a gateway timeout.

**Dual-write reconciliation.** S3 objects are written before their index record and are always reachable by prefix, so a failed index write orphans data rather than losing it. A reconciliation command in the CLI (§7) lists orphans and repairs or removes them. `hardDeleteNote` no longer swallows cascade failures — it fails loudly and leaves the note marked for retry, because silently orphaning audio the UI claims was purged is a correctness problem, not a leak.

### 4.6 Spend metering and circuit breaker

`internal/meter` writes a `USAGE#` record for every billable provider call: `{tenant, provider, op, model, units, unit_kind, cost_micros, correlation_id}`. This data cannot be reconstructed retroactively, which is why it lands in v2 rather than later.

`internal/breaker` owns the provider call:

```go
func (b *Breaker) Do(ctx context.Context, est Estimate, fn func(context.Context) (Result, error)) (Result, error)
```

The provider call is unreachable without passing the check, and the breaker writes the metering record itself — closing the window where a cost is in neither the pending nor the spent column. It enforces a configurable per-tenant daily cap via an atomic `ADD` on `SPEND#<date>`. Exceeding it fails the capture with a distinct status the UI can explain, rather than a generic error.

A per-user request quota and a per-IP limit on the unauthenticated WebAuthn login routes sit alongside the existing stage-level throttle.

`service/webauthn.go`'s `BeginLogin` stops reading every credential of every user to test emptiness; it issues a `Query` with `Limit: 1`.

### 4.7 Search

`GET /v1/search?q=` performs a paginated query over the tenant's own partition with a filter expression across title, aliases, tags, snippet, and cleaned transcript text, returning ranked results with match context. Bounded by the caller's own data and cursor-paginated, this is correct at personal scale and costs nothing beyond the table.

The client additionally filters its IndexedDB corpus instantly as you type, so results appear before the network responds and search works offline. The server call refines and extends them.

If the corpus ever outgrows this, the replacement is a per-tenant inverted index in the same table. No managed search service.

### 4.8 Observability

`log/slog` with JSON output replaces all 21 unstructured `log.Printf` calls. Every log line carries `correlation_id`, `tenant_id`, and stage. The existing and correct discipline of never logging transcript or audio content is preserved and enforced by a CI check.

`httperr.InternalServerError` no longer discards its error argument — every 500 logs with its correlation ID, which today produces no log line at all.

EMF metrics: capture success and failure by stage, provider latency and cost, router content-discard rate, breaker trips, queue depth, DLQ depth. X-Ray tracing enabled on both functions. API Gateway access logging enabled on the stage.

### 4.9 Retention

`RetentionDays` becomes real. An S3 lifecycle configuration on the `captures/` prefix expires audio at the configured age; `0` means indefinite and installs no rule. Cleaned text and note bodies are never expired by retention — only source artifacts. The setting is validated (non-negative, bounded) and the API echoes what was stored, not what was sent.

### 4.10 Test doubles

`repository/memory.go`, `provider/fake.go`, `PlainBox`, and `FakeRefresher` move behind a `testing` build tag or into `_test.go` files. `PlainBox` — a `SealBox` whose `Seal` is the identity function — currently ships in the production binary alongside the real KMS implementation, where one wiring mistake stores every Cognito refresh token in plaintext.

---

## 5. Frontend

Complete rewrite. Nothing from `frontend/js/` is carried forward.

### 5.1 Stack

Vite + React + TypeScript, built to a static bundle and deployed to the same GitHub Pages per-instance path. TanStack Query for server state, Zustand for capture and UI state, React Router for real routing, `idb` for IndexedDB, Workbox for the service worker, Vitest and Testing Library for unit tests, Playwright for end-to-end.

Configuration moves from a generated `config.js` to build-time environment variables baked into the hashed bundle, which removes the class of failure where an installed client is permanently pinned to a dead API endpoint by a cache-first service worker.

### 5.2 Navigation

Home is the record surface: a large centred record target with nothing competing for attention. The library is a pull-up sheet.

- **Collapsed** — the sheet is a persistent strip showing **Notes · Search · You**, so those are one tap rather than a discovered gesture.
- **Expanded** — full library, with the **record button centred in the bottom bar** so it never overlays content.
- **Recording** — the sheet locks shut and the surface becomes the capture screen.

Every state is a real URL. Browser and Android Back collapse the sheet or pop a screen; they never exit the app. Deep links restore state on load.

### 5.3 Capture

The live waveform is driven by `AudioContext` + `AnalyserNode` against real microphone input, rendered to canvas. Alongside it: a large tabular-numeral elapsed timer, and pause, cancel, and stop as distinct controls. Nothing in the UI claims recording has started before `getUserMedia` resolves.

Audio is requested as 16 kHz mono with a bitrate cap — appropriate for speech-to-text, and a fraction of the bytes of today's 44.1 kHz stereo request over cellular.

Robustness requirements, none of which exist today:

- A screen wake lock is held for the duration of the recording.
- `mediaRecorder.onerror`, track `mute`, and track `ended` are handled, so an incoming call produces a saved partial recording and a clear message rather than silent truncation.
- Recordings are chunked into IndexedDB as they are produced, not accumulated in a JS array. The buffer is pruned only after the server confirms the upload.
- Duration and size caps with a warning before the limit.
- Upload is resumable with bounded retry and visible progress.

Non-visual feedback — a start and stop tone, and haptic confirmation where supported — because the stated use case is eyes-off.

The **progress card** appears on stop, showing transcribe → clean → file. It is backed by capture state in IndexedDB and the `GET /v1/captures?status=pending` endpoint, so it survives navigation, reload, and app restart. Tapping it opens the note when complete. A failed capture shows a real retry control wired to `POST /v1/captures/{id}/retry` — today that API method exists in the client and is called from nowhere.

### 5.4 Note detail and playback

Playback is inline. There is no navigation to a presigned S3 URL.

The player renders `peaks.json` as a scrubbable waveform with a playhead and tabular elapsed/total time. Below it, the transcript panel renders `segments.json`: tapping any line seeks the audio, and the active segment highlights as it plays.

**A deliberate constraint, stated plainly:** timestamps belong to the raw transcript. Cleanup rewrites the text, so cleaned prose carries no reliable time mapping. The note body therefore shows cleaned text, and the tap-to-seek transcript panel shows timestamped raw segments, with a toggle to view the cleaned version without sync. Aligning cleaned text back onto raw timings would seek to plausible-looking wrong places.

Captures recorded before v2 have no `segments.json` or `peaks.json`; they render a plain audio player. There is no backfill.

### 5.5 Offline

IndexedDB holds recorded audio until confirmed, the note corpus for offline reading and instant search, and a queue of pending mutations. A Background Sync registration flushes the queue where supported, with a foreground retry on reconnect everywhere else. The service worker's stubbed `syncCapturesWhenOnline` is replaced with a working implementation.

The UI states plainly when it is showing cached data. Today the service worker sets an `X-Offline` header that nothing reads.

A 401 attempts a token refresh before surfacing anything to the user, and never reloads the page — which currently discards unsaved edits and in-flight recordings. Token field names are unified in one typed module, removing the `access_token`/`id_token` conflation that silently logs biometric users out.

### 5.6 Design system and themes

Design tokens as CSS custom properties: colour, a typographic scale with defined ratios, spacing, radius, shadow, and motion. No literal colour or font size outside the token definitions, enforced by a lint rule.

Two complete themes plus a system-following mode:

| Setting | Behaviour |
|---|---|
| **Ink & Paper** (default) | Warm off-white ground, near-black ink, one burnt-orange accent reserved for the live/record state. Serif for note titles and numerals, grotesk for UI. |
| **Nocturne** | Near-black ground, soft off-white text, one electric accent reserved for the live signal. The waveform is the visual centre. |
| **Follow system** | Ink & Paper under `prefers-color-scheme: light`, Nocturne under dark. |

Icons are SVG. No emoji as iconography. `prefers-reduced-motion` is honoured for every animation.

### 5.7 Accessibility

Currently the frontend contains zero `aria-*`, `role`, `tabindex`, or `:focus-visible`. v2 requires:

- Semantic elements throughout; note rows and match candidates are buttons, not clickable divs, so the library is keyboard-operable.
- `aria-live` on toasts and on capture status, so pipeline progress is announced.
- Focus moved and route changes announced on navigation; focus restored on dismissal.
- The confirmation dialog is a real dialog: `role="dialog"`, `aria-modal`, focus trap, Escape, inert background, focus restore. It gates the two destructive actions in the app.
- Visible `:focus-visible` styling on every interactive element.
- WCAG AA contrast in both themes, verified by an automated check in CI.
- Landmarks and a skip link.

### 5.8 PWA

Workbox with network-first for the app shell and precaching of content-hashed assets, so a deploy cannot strand installed clients. One update strategy — a prompt on `updatefound`, no `skipWaiting` racing it. PNG icons at 192 and 512 with a correct maskable safe zone, `apple-touch-icon`, iOS meta tags, `viewport-fit=cover`, and `env(safe-area-inset-*)` so the header does not run under the notch. `dvh` units for full-height layouts. Manifest `shortcuts` with a direct record action, and orientation unlocked so a car mount works in landscape.

`setupInstallPrompt` is either wired up or deleted; it is currently defined and never called. Console logging of the user's email is removed.

---

## 6. Infrastructure

Additions to `infrastructure/template.yaml`:

- SQS queue, worker Lambda, DLQ, redrive policy.
- CloudWatch alarms: Lambda errors and throttles, DLQ depth, API 5xx rate, breaker trips.
- An AWS Budget defined in IaC rather than a README copy-paste, with the IAM permission to create it.
- API Gateway `AccessLogSettings`; X-Ray `TracingConfig` on both functions.
- `DeletionPolicy: Retain` and `UpdateReplacePolicy: Retain` on the content bucket, the token-vault KMS key, and the user pool. Deleting the stack today schedules the CMK for deletion and silently bricks every enrolled biometric credential.
- S3 lifecycle rules implementing §4.9, plus expiry of current objects in the artifact bucket, which currently grows without bound.
- Log group retention set explicitly, and `DependsOn` ordering to avoid the auto-created-group conflict.
- Cognito hardened: advanced security mode, explicit `TokenValidityUnits` and refresh token lifetime, account recovery, deletion protection, MFA available, stronger password policy.
- IAM tightened: `cognito-idp:InitiateAuth` conditioned to the instance's user pool rather than `Resource: "*"`; `dynamodb:Scan` removed.
- Lambda `Timeout` on the API function reduced below the gateway's fixed 30-second integration ceiling; provider HTTP client timeouts set below the enclosing Lambda timeout at both tiers.
- `ExistingOIDCProviderArn` parameterised in `bootstrap.yaml` and passed by `setup.sh`. It currently defaults to the author's AWS account, so a third-party clone federates its deploy role to someone else's OIDC provider.

`boundary.json` and `deny.json` are generated from a single source with a drift check, rather than maintained as two byte-identical 300-line files with ~115 characters of headroom before the IAM size limit breaks the deploy.

---

## 7. Operations and CI

**Scripts are replaced, not patched.** One shell library, not two mutually incompatible ones. No `eval`-constructed commands. Stack naming unified to `chintan-<instance>-<env>` across `bootstrap.sh`, `cleanup-aws.sh`, `teardown.sh`, and the workflows. Teardown becomes stack-scoped rather than account-prefix-scoped, so it can no longer reach the CloudTrail audit bucket. Empty-bucket and pagination handling fixed. `--region` accepted everywhere. Dry-run stays the default, which v1 got right.

Scripts that cannot currently work — `bootstrap.sh` querying a CloudFormation output name that does not exist — are fixed or deleted. A broken destructive script that appears to work is worse than no script.

**A `chintanctl` CLI** provides `export`, `backup`, `restore`, `reconcile`, and `usage`. Export enumerates by S3 prefix and DynamoDB partition rather than by entity type, so a future schema addition cannot silently fall out of the export. This also closes the audit finding that nothing backs up S3 content at all.

**Guardrails.** `.github/CODEOWNERS` is added, `guardrails-check.sh` is corrected to reference paths that exist, and it is wired into a PR job and demonstrated failing before being trusted. It currently cannot pass and no workflow invokes it.

**Pipeline.** A staging instance alongside prod. Change-set deploys executed with a scoped CloudFormation service role. A Lambda alias with a documented rollback command. `fail-fast: false` on the deploy matrix. Real environment protection on production. Smoke tests run against staging before prod, not after prod.

**CI gains:** `go test -race`, `golangci-lint`, `govulncheck`, `cfn-lint`, `shellcheck` and `shfmt`, frontend typecheck, unit tests and build, Playwright end-to-end against a preview deploy, a contrast check, and a check that no provider adapter logs response bodies.

---

## 8. Migration and rollout

Work proceeds on `feat/v2`. `main` stays deployable from the tag throughout.

**Data migration is additive.** New attributes (`version`, `tags`, `purge_after_epoch`, `append_token`, `duration_ms`, `segments_key`, `peaks_key`) default cleanly on read for existing items; a one-shot backfill job populates them and promotes the projected attributes out of the `data` blob. GSI1 is added to the existing table. No table replacement, no export/reimport.

**Behavioural breaks, both intentional:**

1. `X-User-ID` stops working. Nothing legitimate uses it.
2. Captures created before v2 have no timestamps or peaks and render a plain player.

**Order of work.** Security and data-integrity first, because those are live defects; then the ceiling; then spend safety; then the frontend, which is the largest single piece and depends on the API contract being fixed.

1. Auth rewrite, tenant-scoped identity, isolation tests, `X-User-ID` removal.
2. Repository rewrite: pagination, GSI1, conditional writes, projected attributes, TTL.
3. Idempotency and the append guard.
4. Async pipeline: SQS, worker, timestamps, peaks, correlation IDs.
5. Metering and circuit breaker.
6. Handler rewrite, error envelope, OpenAPI, observability.
7. Infrastructure and ops.
8. Frontend rewrite.
9. Search, tags, export.

Phases 1–3 are independently shippable and worth deploying before the rest.

---

## 9. Testing

- **Unit** — retained provider and prompt tests; new coverage for auth verification, pagination cursors, conditional-write conflicts, idempotency replay, breaker limits, and segment/peak generation.
- **Isolation** — a test below the HTTP layer asserting tenant A cannot reach tenant B's keys. This is the test that would have caught the impersonation defect.
- **Integration** — DynamoDB Local and a fake S3 for the repository layer, which is currently untestable by construction because it holds a concrete `*dynamodb.Client`. That dependency becomes an interface.
- **Concurrency** — parallel completion of two captures against one note; concurrent edit and voice append. Run under `-race`.
- **Frontend** — unit tests for state machines and the offline queue; Playwright end-to-end for login, record, disambiguate, search, edit, archive, and offline capture with reconnect.
- **Contract** — generated OpenAPI verified against the router in CI.

---

## 10. Success criteria

1. A second Cognito user cannot read, write, or delete the first user's data by any means, proven by an automated test.
2. A twenty-minute recording completes end-to-end without a gateway timeout and appends exactly once, verified under an induced retry.
3. Killing the network mid-recording, then reconnecting, files the note with no data loss.
4. Every note, transcript, and audio file is recoverable through `chintanctl export` without console access.
5. A daily spend cap stops provider calls and reports it clearly in the UI.
6. Lighthouse PWA and accessibility scores above 90, with zero critical axe violations in both themes.
7. A third party clones the repository, deploys an instance, sees tagged spend, and tears it down without touching unrelated AWS resources — following only the README.
8. Rolling back to `v0.2.0-prerewrite` restores a working deployment.

---

## 11. Deferred

Obsidian-style backlinks; splitting one recording across notes; multi-tenant product surface, billing, self-service signup; native apps and in-car integrations; streaming transcription; append-only audit logging; per-tenant KMS key indirection; consent ledger; correction-learning from user edits.

Two items deferred with a note, because doing them later is materially harder:

- **Cleanup as a validated patch rather than a whole-text rewrite.** v2 keeps whole-text cleanup, but adds a per-note **verbatim** flag that bypasses cleanup entirely, so dictated content that must not be reworded — a spec, a quote, a prompt — is safe. The stronger structural guarantee waits.
- **Audit logging.** Required before a second real user. The correlation ID and structured logging introduced in v2 are its foundation.
