# Chintan — Production Readiness Audit

**Date:** 2026-08-07
**Commit audited:** `32802db` (main)
**Rollback point:** tag `v0.2.0-prerewrite`, branch `pre-rewrite/main-2026-08-07` (both pushed to origin)

---

## 0. Verdict

Chintan is a working vertical slice that does what the clean-slate spec asked for: record → transcribe → route → clean → append, with a PWA, WebAuthn unlock, and note archiving. The capture pipeline works and the LLM-safety work in it is genuinely expert.

It is not releasable. The three layers fail in different ways and need different remedies:

| Layer | State | Remedy |
|---|---|---|
| Backend domain logic (`routing`, `cleanup`, `provider`, `keys`, `match`) | Good. Real threat modelling, real adversarial tests. | **Keep.** |
| Backend edges (`middleware`, `handler`, `repository`, `service/capture`) | Critical auth hole; no pagination, no transactions, no idempotency, no observability. | **Re-architect in place.** |
| Frontend | Prototype. No build, no router, no state model, no a11y, no offline. Designed for a desk, sold for a car. | **Rewrite.** |
| Infrastructure templates | Competent core, incomplete operationally. | **Extend.** |
| Ops scripts | Written aspirationally, never executed. Several are actively destructive. | **Rewrite or delete.** |

The pattern across the whole repo: the *interesting* problem (prompt injection, router verification) received expert attention; the *boring* problems (auth, pagination, transactions, logging, deploy scripts) received the first plausible answer and no second look.

---

## 1. Ship blockers

These must be fixed before a second user exists, and several before the current single user is safe.

### B1 — Cross-tenant impersonation via `X-User-ID`

`backend/internal/middleware/auth.go:37-40` reads an unverified `X-User-ID` request header and prefers it over the JWT `sub`. `backend/internal/middleware/cors.go:19` advertises that header in `Access-Control-Allow-Headers`, making it browser-reachable. Every downstream key derivation trusts the resulting string (`repository/dynamo.go:34-36`, `keys/keys.go:17-89`).

Any holder of a valid Cognito token can read, edit, archive, and permanently delete any other user's notes, audio, and transcripts by adding one header. No test covers the branch — `handler_test.go:55` injects identity through `middleware.WithUserID`, which short-circuits at `auth.go:32` and never reaches line 37.

Compounding it, `userIDFromBearer` (`auth.go:50-75`) base64-decodes the JWT payload and reads `sub` with no signature, `iss`, `aud`, or `exp` check. That is defensible only if the API Gateway authorizer is the sole ingress *and* headers are sanitised. Neither holds. `golang-jwt/jwt/v5` is already in `go.mod` and unused.

The same unverified parse guards the most sensitive object in the system at `service/webauthn.go:346-368`, where it binds a KMS-sealed Cognito refresh token to an identity.

### B2 — Lambda timeout exceeds the API Gateway ceiling

`infrastructure/template.yaml:341` is `ProtocolType: HTTP` (API Gateway v2, hard 30s integration cap, not adjustable). `template.yaml:299` sets the Lambda `Timeout: 120`, and the provider HTTP clients are also 120s (`provider/groq_stt.go:42`, `provider/openai_cleanup.go:44`).

Any capture whose STT + LLM pipeline exceeds 30s returns 504 to the user while the Lambda keeps running and billing for up to 90 more seconds. For a driving-length recording this path is not merely reachable, it is the common case.

### B3 — Duplicate note content on retry

`service/capture.go:165-189` appends clean text to the note, then updates the note index, then sets capture status to `appended`. There is no dedupe and no guard between the append and the status write. If either later write fails, the capture stays in `cleaned`; the terminal-state guard at `:120-123` only skips `appended|failed|no_content`, so the next retry re-enters at `:155`, finds `CleanKey` already set, skips cleanup, and appends the same text again.

`handler/captures.go:92` comments that retry is "idempotent". It is not. B2 guarantees this fires: gateway 504 → frontend polls and re-completes → duplicated content.

### B4 — Lost updates on every write path

`service/capture.go:427-444` (`appendToNote`) is a bare S3 read-modify-write. `service/notes.go:181-239` (`UpdateNote`) reads then writes markdown, meta, and index with no ETag or version. `repository/dynamo.go:207-238` (`PutNote`) is an unconditional `PutItem` with no `ConditionExpression`. `TransactWriteItems` appears nowhere in the codebase.

A voice append landing while the note is open in the editor silently discards one of the two.

### B5 — Unpaginated queries silently truncate data

`repository/dynamo.go:144-172`, `:335-366`, `:526-550`, `:556-580` all issue a single `Query` and ignore `LastEvaluatedKey`. Past ~1MB of items the user simply stops seeing notes, and the purge/cascade-delete paths stop finding captures to delete — so "delete forever" leaves orphans. There is no pagination in the HTTP API either (`handler/notes.go:89-106`).

### B6 — Destructive ops scripts target the wrong resources

CI deploys the stack as `chintan-<instance>-prod` (`.github/workflows/deploy-backend.yaml:107`). The scripts assume `chintan-<instance>`:

- `scripts/teardown.sh:80` derives `instance_name` by stripping the `chintan-` prefix from `chintan-dev-prod`, yielding `dev-prod`, then deletes SSM parameters at `/chintan/dev-prod/*` — a path that never existed. **The live keys at `/chintan/dev/*` survive teardown.**
- `scripts/cleanup-aws.sh:194` builds `chintan-dev`, finds no stack, warns and returns — then deletes `/chintan/dev/groq_api_key` and `/chintan/dev/llm_api_key` unconditionally at `:203`. Net effect: production is broken with 500s on every capture, and nothing was cleaned up.
- `scripts/teardown.sh:91` exports `SKIP_CONFIRMATION=true`, which `cleanup-aws.sh` never reads — its prompt at `:184` blocks on a TTY.
- `scripts/lib/common.sh:156` pipes an empty `list-object-versions` result into `delete-objects`, which the CLI rejects; under `set -euo pipefail` this kills teardown mid-run. It also never paginates past 1000 keys.
- `scripts/teardown.sh:144-195` enumerates by account-wide prefix rather than stack membership, including the CloudTrail audit bucket created by `bootstrap-agent.sh:120`. **Teardown attempts to destroy its own audit trail.**

### B7 — `scripts/bootstrap.sh` cannot succeed

`bootstrap.sh:143` queries CloudFormation output `LambdaArtifactBucketName`; `infrastructure/bootstrap.yaml:334-338` exports `LambdaDeploymentBucketName`. The query returns empty and the guard at `:146` exits. Even corrected, `:166` deploys `chintan-dev` while CI owns `chintan-dev-prod`, and both templates create identically-named physical resources — the second stack fails with `AlreadyExists` after partially creating others.

### B8 — Audio is lost on any upload failure

`frontend/js/capture.js:321` and `:500` both comment "reset UI but keep the recording". Nothing keeps it. `api.uploadAudio` (`api.js:231-250`) throws on any failure with zero retries, and `api.retryCapture` (`api.js:185`) is dead code called from nowhere. There is no IndexedDB buffer anywhere in the frontend.

The archived spec named this the one failure the product cannot absorb. It is currently unhandled.

---

## 2. Backend — structural problems

Beyond the blockers, the following need re-architecture rather than patching.

**No GSI at all.** `template.yaml:115-124` defines only `pk`/`sk`. `ListCapturesByNote` (`dynamo.go:337-360`) therefore queries the user's entire capture partition and filters in Go, so read cost grows with total captures rather than captures-for-this-note.

**Everything is an opaque JSON blob** in a single `data` attribute (`dynamo.go:65-71`). No projections, no partial updates, no filter expressions. Listing notes to render titles transfers every snippet. Any future GSI requires migrating every item.

**Expensive work on the read path.** `service/notes.go:142`, `:315`, `:341`, `:366` — every note list, restore, and archived-list first runs `purgeExpired`, which does a full list and then a synchronous cascade of S3 and DynamoDB deletes inside the request. `PurgeAfter` (`model/types.go:26`) is an RFC3339 string rather than an epoch integer, so it cannot be a DynamoDB TTL attribute — which is why this hand-rolled sweeper exists.

**No request size limits anywhere.** `MaxBytesReader` appears nowhere. `PATCH /v1/notes/{id}` accepts an unbounded body into a 512MB Lambda. Aliases are uncapped in count and length (`handler/notes.go:153`) and every alias is rendered into the routing prompt for every future capture — a stored token-cost amplifier. `PresignPut` (`repository/s3.go:90-109`) sets no content-length range, so the URL accepts an unbounded upload.

**Errors are classified by substring matching.** `handler/captures.go:113,115,117,142,208,233,248,252` use `strings.Contains(err.Error(), ...)`. Renaming an error message silently changes HTTP status codes. The typed-sentinel discipline used in `service/notes.go:26-29` was simply not applied to the capture service.

**Four different error envelopes** on the same API: `{"error":...}` (`httperr.go:11`), `text/plain` (`handler/notes.go:41`), `"404 page not found"` (`:59`), and empty-body bare status (`handler/captures.go:65`). `405`s never set `Allow`. There is no OpenAPI document.

**`httperr.InternalServerError` discards the error parameter entirely** (`httperr.go:49-55`) — every 500 raised through it produces no log line at all. Its sibling `WriteJSON` has the opposite flaw, serialising wrapped DynamoDB and S3 messages (including table names) to the client.

**Test doubles ship in the production binary.** `repository/memory.go` (435 lines) and `provider/fake.go` are not `_test.go` files. Most dangerous: `service/cognito_refresh.go:78-86` ships `PlainBox`, a `SealBox` whose `Seal` is the identity function, in the same package as the real `KMSBox`. One wiring mistake in `cmd/api/main.go:90` stores every Cognito refresh token in plaintext, with nothing at build time to catch it.

**`BeginLogin` reads every credential of every user** (`service/webauthn.go:183-188`) on the global `WACREDLIST` partition, solely to test `len(creds) == 0`. This is an unauthenticated endpoint, so it is a free cross-tenant hot-partition amplifier. A `Query` with `Limit: 1` answers the same question.

**Observability is absent.** No structured logging (21 unstructured `log.Printf`; `log/slog` unused in a Go 1.25 module), no request IDs, no correlation across STT → route → cleanup → append, no metrics, no tracing (`TracingConfig` absent from the template), no API Gateway access logs, no alarms. The health check is a static `{"status":"ok"}` with no dependency probe.

Credit where due: the code deliberately refuses to log user content (`groq_stt.go:92`, `openai_cleanup.go:96`, `openai_router.go:69-71`). The instinct is right; it never got paired with anything structured.

**Two correctness bugs worth naming:**

- `service/notes.go:279-285` truncates snippets by **byte** slice (`body[:500]`), cutting multi-byte runes in half and writing invalid UTF-8 into DynamoDB and the routing prompt. Every other truncation in the codebase correctly uses `[]rune`.
- `service/capture.go:355` sorts notes by lexicographic string comparison of `time.RFC3339Nano`. Go trims trailing fractional zeros, so `...:00Z` sorts *above* `...:00.1Z` because `'Z' > '.'`. Note recency ordering — which decides which 50 notes the router even sees — is wrong. Captures use plain `RFC3339`, so the codebase uses two time formats for two adjacent entities.

---

## 3. Frontend — why it needs a rewrite, not a restyle

**No build step.** Nine `<script>` tags in manual dependency order (`index.html:195-203`), each ending by attaching a singleton to `window`. No modules, no bundler, no minification, no cache-busting hashes.

**No router.** `ui.showScreen`/`showContentScreen` (`ui.js:19-49`) toggle a `.hidden` class. No URL, no history, no deep links, no restore-on-reload. The `popstate` handler (`app.js:111-113`) re-runs the auth check, so Android Back from a note detail does not go back one screen — it resets to home or exits the app.

**No state model.** Mutable fields scattered across `notes.js:4-8` and `capture.js:3-13`, with modules reaching directly into each other (`capture.js:358` calls `notes.showNoteDetail`; `notes.js:302` embeds `onclick="notes.downloadCapture(...)"` in generated HTML).

**It is not designed for its stated use case.** The README says "while driving or walking." The app delivers every piece of feedback — recording started, note saved, every error — through a 5-second toast in the top-right corner. There is no wake lock (screen sleeps mid-recording), no audio or haptic cue, no `mediaRecorder.onerror`, no track `mute`/`ended` handling (so an incoming call silently truncates the recording), no pause, no cancel, no playback-before-upload, no duration or size cap, and no offline queue — `sw.js:212-229` registers a `sync` listener whose handler is an empty stub with comments describing what it "would" do, and nothing ever registers the tag.

The primary record button sits at the *top* of the screen inside a card below a header — the hardest thumb region — and `styles.css:739-742` caps it at `max-width: 200px` on the smallest screens, so it gets *smaller* on phones.

**The recording indicator is the worst single decision.** `capture.js:288-289` sets the determinate progress bar to `width: 100%` and pulses its opacity. While you speak, the UI shows a full, fading progress bar — which reads as "stuck," not "listening." No waveform, no level meter, no elapsed timer.

**Baseline modern voice UX is entirely missing.** What a user expects in 2026 from any voice interface — Gemini, Claude voice, any modern recorder — and what Chintan has:

| Expected | Current state |
|---|---|
| Live waveform / level visualization while speaking | None. No `AudioContext`, no `AnalyserNode` anywhere in the frontend. The only motion is a fake 100%-wide progress bar pulsing its opacity. |
| Elapsed recording time | None. No timer of any kind during capture. |
| Inline playback of captured audio | None. `notes.js:302` renders a download link that opens a presigned S3 URL in a new tab/window — the browser either downloads the file or navigates away from the app entirely. There is no `<audio>` element in the codebase. |
| Scrubbable waveform on saved audio, synced to transcript | None. |
| Pause / resume / cancel / re-record | None. Stop is a one-way commit. |
| Review before commit | None. Audio uploads immediately on stop. |

These are not polish items — they are the difference between an app that feels like a product and one that feels like a form submission.

**Design system is half-built.** Spacing, radius, and shadow are tokenised (`styles.css:27-40`); typography is not — ten ad-hoc font sizes with no scale and no `--font-size-*` tokens. Stock Flat-UI hexes (`#3498DB`, `#9B59B6`, `#C0392B`, `#229954`) fight the deliberate forest-and-paper palette; the capture status chips render as saturated uppercase Jira pills. The record button is `--color-success` green — the same green as toast success and progress fill, so the primary action and the success state are indistinguishable, and green for *record* inverts the universal convention. Icons are emoji (`🎤`, `⚙️`). The `768px` media query appears twice as separate blocks (`:694`, `:806`) because the stylesheet was appended to rather than edited.

**Six emitted CSS classes have no definition at all** — `.error`, `.btn-warning`, `.capture-time`, `.empty-state`, `.field-error`, `.settings-label`. Consequently every error message in the app renders as unstyled body text, visually identical to normal content, and the "unsaved settings" indicator is invisible.

**Accessibility is zero.** Verified by grep: no `aria-*`, no `role=`, no `tabindex`, no `:focus-visible`, no `<main>`, no `<nav>` anywhere in the frontend. Note rows and match candidates are clickable `<div>`s, so the entire note list is unreachable by keyboard and invisible to assistive tech. The toast container has no `aria-live`, so every notification is silent to screen readers. `styles.css:233` sets `outline: none` on all inputs. `--color-text-light` on the paper background is ≈3.6:1 (fails AA) and covers most secondary text in the app; white on `#F39C12` is ≈2.1:1.

**Frontend correctness bugs:**

- `notes.js:181-191` reveals the note-detail screen and reassigns `currentNoteId` *before* the fetch resolves. On fetch failure the textareas still hold Note A's body while `currentNoteId` is Note B — the 3s autosave debounce then writes A's content into B.
- `auth.js:193` reads `tokens.access_token` while `isAuthenticated()` (`auth.js:47`) and every API call (`api.js:12`) use `id_token`. If the biometric login response lacks `access_token`, the parse throws and the user is silently logged out on the next foreground.
- `api.js:38-51` responds to a 401 by toasting and reloading the page after 2s — it never attempts a refresh despite holding a valid refresh token, and the reload discards unsaved edits and any recording in progress.
- Autosave failures are swallowed entirely (`notes.js:388-390`) with no `beforeunload` handler.
- `notes.js:306` injects server-supplied `${c.error}` unescaped; `notes.js:302` interpolates `${c.id}` into an inline `onclick` string. `escapeHtml` (`ui.js:175`) uses `div.innerHTML`, which does not escape quotes, so it is unsafe for exactly the attribute context `notes.js:302` uses it in. Tokens live in `localStorage`. There is no CSP, despite `index.html:17` commenting "CSP-friendly font loading".
- `setupInstallPrompt()` (`app.js:235-255`) and `isPWA()` (`:258-261`) are defined and never called. `app.js:222` logs the user's email to the console.
- Service worker is cache-first for everything (`sw.js:118-147`) including `js/config.js` (`:19`), which CI regenerates per deploy. If the stack is recreated and the API endpoint changes, installed users are permanently pinned to a dead config with no self-heal. `sw.js:42` calls `skipWaiting()` and `:68` calls `clients.claim()` while `app.js:53` simultaneously shows a "refresh to update" toast — two contradictory update strategies guaranteeing mixed-version assets.
- Manifest has one SVG icon and no PNGs, so iOS ignores it entirely. No `apple-touch-icon`, no iOS meta tags, no `viewport-fit=cover`, and `env(safe-area-inset-*)` appears zero times — on a notched iPhone the header runs under the status bar. `orientation: portrait-primary` locks out landscape, the common orientation in a car mount.

---

## 4. Infrastructure and deployment

**Not present anywhere in `infrastructure/`:** alarms, budgets, WAF, dead-letter queues, API Gateway access logs, X-Ray tracing, S3 backup. Grepping for `Alarm|Budget|WAF|WebACL|DeadLetter|SQS|AccessLogSettings` returns zero matches in both templates. The README documents budgets as a manual copy-paste rather than IaC, and `permissions.json:209` grants only `budgets:ViewBudget`, so the agent cannot create the budget the README describes.

**Deletion policies are inconsistent.** DynamoDB correctly gets `Retain` + PITR (`template.yaml:110-126`). `ContentBucket` (`:139`), `TokenVaultKey` (`:176`), `UserPool` (`:48`), and `ApiLogGroup` (`:322`) get none — so deleting the stack schedules the token-vault CMK for deletion, silently bricking every enrolled biometric credential, and destroys the user pool.

**A third party cannot clone and deploy this.** `README.md:24` says run `./scripts/setup.sh`, which requires an undocumented `--region` and fails with usage. The bootstrap stack references `policy/chintan-agent-boundary` (`bootstrap.yaml:62`) created only by `scripts/bootstrap-agent.sh`, which the README never mentions. `bootstrap.yaml:20` defaults `ExistingOIDCProviderArn` to the author's account (`arn:aws:iam::338186951935:...`) and `setup.sh:87-97` never overrides it — a clone federates its deploy role to someone else's OIDC provider. `README.md:29` and `:74` reference Terraform outputs in a CloudFormation repo. The documented instance-config schema (`README.md:56-63`) does not match `config/instances/dev.yaml`, and nothing reads either — the deploy workflow uses only the filename, and `LLM_BASE_URL`/`LLM_MODEL` are hardcoded at `template.yaml:306-307`.

**Guardrails that cannot pass and are not wired in.** `scripts/guardrails-check.sh:115` hard-fails on `.github/CODEOWNERS`, which does not exist (`.github/` contains only `workflows/`), and `:121` greps it for `/docs/security/`, which also does not exist. It requires `yq` with no declared dependency and hardcodes `repos/vppillai/chintan` at `:258`. No workflow invokes it. By the script's own opening argument (`:5-8`), this is precisely the "silently removed guardrail that is still trusted" failure it exists to prevent.

**`infrastructure/agent-policies/boundary.json` is 6029 of 6144 permitted characters** — ~115 characters of headroom before the next guardrail addition breaks the deploy. It and `deny.json` are byte-identical apart from one statement, maintained by hand with no generation step and no drift check.

**Pipeline.** No staging (`deploy-backend.yaml:113` hardcodes `Environment=prod`). No rollback (no change set, no `--role-arn`, no stack policy, no Lambda alias; the smoke test runs *after* deploy and leaves the bad version live on failure). No `fail-fast: false` on the deploy matrix, so one instance failing cancels siblings mid-flight. Environment gating is nominal — `setup.sh:135` creates the `production` environment with `wait_timer=0` and no reviewers. `deploy-frontend.yaml:34` targets `production` where `actions/deploy-pages@v4` expects `github-pages`. CI runs `go test` + `gofmt` + `go vet` only — no `-race`, no coverage gate, no `staticcheck`/`golangci-lint`, no `govulncheck`, no `cfn-lint`, no `shellcheck`, and no frontend tests at all against ~1,900 lines of JS.

**Cognito is minimally configured** (`template.yaml:59-65`): 8-character minimum, no symbols required, no MFA, no advanced security / compromised-credential detection, no account recovery setting, no deletion protection, and no explicit token validity — so refresh tokens live the 30-day default, which matters because the app vaults them in KMS for biometric unlock.

**IAM.** `cognito-idp:InitiateAuth` on `Resource: "*"` with no condition (`template.yaml:269-276`) lets the Lambda initiate auth against every user pool in the account. `dynamodb:Scan` is granted at `:232` with no evident need. `bootstrap.yaml:301-307` grants the deploy role `ssm:GetParameter` on `/chintan/*`, which `boundary.json:245-262` then denies for any principal not matching `chintan-lambda-*` — a dead grant.

**Two incompatible shell libraries coexist.** `scripts/lib/common.sh` (`log_info`, `DRY_RUN`, `execute_cmd`+`eval`) and `scripts/lib/agent-common.sh` (`info`, `APPLY`, `confirm_apply`, `--json`), where `agent-common.sh:5-9` explicitly argues against exactly the duplication `common.sh` represents. `execute_cmd` (`common.sh:40-48`) builds command strings and `eval`s them, making every interpolated bucket name and ARN an injection surface.

**`scripts/invite-user.sh` contradicts its own docstring:** `:2-3` claims it does not print the temporary password; `:54` prints it unconditionally. `:29-34` and `:44-49` pass `--permanent`, so the forced first-login password change the README promises never happens.

Genuinely good, and worth preserving: dry-run is the default across `setup.sh:167`, `bootstrap.sh:222`, `cleanup-aws.sh:174`, `teardown.sh:199`. `scripts/bootstrap-agent.sh` is the most professional file in the repo — idempotent policy versioning, boundary attached at role creation, IAM-propagation retry loops, Access Analyzer pre-flight, secret written mode-600 and never echoed. The OIDC trust is correctly pinned to `environment:production` including the immutable-subject form. `backend/lambda-function.zip` is **not** committed, and `.gitignore` correctly excludes `.env`, `frontend/js/config.js`, and build outputs — none appear in git history.

---

## 5. Requirements dropped in the clean-slate rewrite

The archived branch (`archive/phase0-wip`, tag `archive/pre-clean-slate`) carried a 2,979-line spec against which the current 171-line spec is a deliberate simplification. Most of that simplification was correct. These items were not.

| Dropped | Why it matters | Archive reference |
|---|---|---|
| **Provider spend circuit breaker** | Nothing today bounds Groq/OpenAI spend. A retry loop or a leaked login bills without limit; `ReservedConcurrentExecutions: 5` bounds AWS, not third-party APIs. | `backend/internal/breaker/breaker.go` |
| **Usage metering** | Prerequisite for the breaker and unreconstructable retroactively. ~100 lines: `{tenant, unit, quantity, provider, cost_micros, op}` per STT/LLM call. | `backend/internal/meter/meter.go` |
| **Async worker for the pipeline** | Directly causes B2. The archived design put the pipeline in an S3-event-triggered worker precisely because a synchronous API Lambda cannot hold STT + LLM. | `backend/cmd/worker/main.go`, ADR 0002 |
| **Audio buffered locally until upload confirmed** | Causes B8. The archived spec names this the one failure the product cannot absorb. | Invariant I2 |
| **Retention actually enforced** | `model.Settings.RetentionDays` is stored, returned to the client, exposed in the settings UI, and **read by nothing**. No S3 lifecycle rule exists. Either wire it or remove it from the UI. | — |
| **Idempotency keys on mutating endpoints** | Double-tap on a flaky mobile link creates duplicate notes and captures. | `backend/internal/idem/idem.go` |
| **Backup / export / erase tooling** | PITR covers DynamoDB; **nothing covers S3**. No versioning, no lifecycle, no export path. | `backend/cmd/chintanctl/{backup,restore,export,erase}.go` |
| **Cross-tenant isolation test bypassing the API** | Would have caught B1. | `backend/internal/repository/isolation_test.go` |
| **Append-only audit log** | Lower urgency for one user; required before a second. | `backend/internal/audit/audit.go` |
| **Audio bytes never through Lambda** (I3) | Upload is presigned, but `transcribeCapture` pulls the whole audio object into a 512MB Lambda and re-POSTs it to Groq — so Lambda memory is the real cap on recording length. | Invariant I3 |
| **Cleanup as a validated patch, not a rewrite** | Cleanup takes whole-text LLM output and writes it; "do not add information" is prompt-only. At minimum, a per-note *verbatim / no-cleanup* flag, so a dictated spec is not silently reworded by `polished` mode. | Invariant I4 |

**Correctly dropped, and should stay dropped:** the 21-check CI matrix and its 17 shell scripts written against product that did not exist; the "every check demonstrated red" ceremony; the containerised toolchain justified by an ONNX link never reached; three parallel documentation registers with ~70 pre-seeded gotchas about traps not yet encountered; the §11A metrics programme (30+ metrics, monthly review board) for one user; the 22-script inventory for pipeline stages never built; `kmsref` per-tenant key indirection and `consent` event logging, both guarding things that do not yet exist.

One item worth recovering from the archive as documentation only: `docs/decisions/0001-no-android-auto.md`, whose conclusion — voice-launch is the only genuinely hands-free path for a PWA — is a load-bearing product fact that the current spec asserts as a non-goal without recording why.

---

## 6. What to keep

Not everything needs replacing. These are good and should survive any rewrite:

- `backend/internal/routing/prompt.go` and `backend/internal/cleanup/prompt.go` — fenced untrusted transcript with marker defanging, candidate-field sanitisation preventing prompt row forgery.
- `backend/internal/provider/openai_router.go:79-94` — the "router may only delete words" verifier, with a documented rationale and genuinely adversarial tests at `openai_router_test.go:137,223`.
- `backend/internal/keys/keys.go` — every S3 path centralised and validated, traversal-proof.
- The deliberate refusal to log user content across all three provider adapters.
- The staged, resumable capture state machine (`model/types.go`, `service/capture.go`) — the design is right, the implementation needs the idempotency guard.
- The `needs_target` disambiguation flow — the best-designed interaction in the frontend.
- The note archive feature — fully implemented against its spec, with real tests.
- `scripts/bootstrap-agent.sh` and the agent IAM boundary concept.
- The S3 key layout and the passbook per-instance isolation model. The model is sound; only its packaging is broken.

---

## 7. Recommended sequence

1. **Security and data-integrity blockers.** B1, B3, B4, B5 — real JWKS verification, idempotent append, conditional writes, pagination. Add the cross-tenant isolation test that would have caught B1.
2. **The 30s ceiling.** B2 — move the pipeline to an async worker; this also unblocks I3 and lifts the recording-length cap.
3. **Spend safety.** Recover metering and the circuit breaker before anything increases usage.
4. **Ops scripts.** B6, B7 — unify stack naming, or delete the broken scripts outright rather than leave destructive ones that appear to work.
5. **Frontend rewrite.** Build step, router, state model, offline capture queue, voice-first interaction, accessibility, and a real design system.
6. **Observability and operability.** Structured logging with correlation IDs, metrics, alarms, access logs, budgets, S3 backup, staging environment, rollback path.
7. **Retention.** Enforce it or remove it from the UI. Shipping a setting that does nothing is worse than not offering it.
