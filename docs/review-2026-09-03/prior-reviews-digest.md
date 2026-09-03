# Prior-docs digest — chintan @ 9aa4435 (2026-09-03)

Method: read each doc in full; for every finding, grepped the current tree and `git log`
for the corresponding change. "fixed" = code now does what the finding asked; "open" = the
defect is still present in the tree; "unclear" = cannot be settled from the repo alone.
Note: the tree has had no commits since 2026-08-17; almost everything below landed 08-07..08-14.

## 1. Findings vs. current code

### 1a. docs/review-2026-08-08.md (blind six-lens review of v2 `main`)

| ID | Sev | Finding (one line) | Status | Evidence |
|---|---|---|---|---|
| C1 | Crit | SQS queue policy SourceArn lacked `${Environment}`; every deploy failed | fixed | `a69c454`; template.yaml:857 now includes `${Environment}` |
| C2 | Crit | One recording per page load; capture model never reset | fixed | `fc4ca56`; `CaptureScreen.tsx:68,82` call `reset()` |
| C3 | Crit | Search and Notes shared query key -> crash / dead offline search | fixed | `41c9aa2`; `queryKeys.notesCorpus = ['notes','corpus',q]`; route `errorElement` present |
| H1 | High | bootstrap.sh shipped API zip as worker; SQS deleted every capture | fixed | `d27c208`; `WorkerCodeKey MinLength: 1`; bootstrap.sh passes `--worker-output` |
| H2 | High | Bucket rename orphans existing prod content (owner decision) | **unclear** | No `ContentBucketName` *parameter*, no legacy-name path, no `s3 sync` doc anywhere. Template hard-codes the new name (template.yaml:877). If prod was redeployed after `a69c454`, the old `chintan-content-dev-<acct>` bucket holds orphaned content unless migrated by hand. Not recorded. |
| H3 | High | chintanctl derived bucket name without env; erase/backup silently no-op | fixed | `3475441`; `awsports.go:288 contentBucketOutput = "ContentBucketName"`, `resolveContentBucket` |
| H4 | High | Presigned PUT unbounded/replayable; size never enforced | fixed | `3475441`; `worker.go:77 RejectOversizedCapture`, `upload.go signContentType`; `pipeline/oversize_test.go` |
| H5 | High | Cancel during mic acquisition left mic live | fixed | `2a3a13b`; `recorder.ts:133` sets `stopping=false` before the await, checks after |
| H6 | High | Offline at unvisited URL -> bare "Offline" | fixed | `80d237b`; `sw.ts:53 caches.match(SHELL_URL,{ignoreSearch:true})`, SHELL_URL from scope |
| H7 | High | System Back inside autosave debounce dropped edits | fixed | `731d404`; `useNoteEditor.ts:204 flush()` on unmount |
| M1 | Med | WebAuthn challenges never expired | fixed | `webauthn.go:89 Enforce: true`, `:390` checks `ExpiresAt` |
| M2 | Med | Deploy role: mutating `kms:*` on `Resource: '*'` | fixed | `b37ff91`; bootstrap.yaml: mutating KMS conditioned on `aws:ResourceTag/Project: chintan`; CMK itself removed (`f546b2a`) |
| M3 | Med | Deploy role: mutate any Cognito pool | mostly fixed | `b37ff91`; Delete/Update/SetMfa conditioned on Project tag. `UpdateUserPoolDomain` still unconditioned on `'*'` (`d63681a`, deliberate: action cannot carry the condition per `f658869`) |
| M4 | Med | Rate limit keyed on left-most XFF | fixed | `ratelimit.go:140` RemoteAddr first, right-most XFF fallback |
| M5 | Med | `verbatim`/`created_at` destroyed on every note read | fixed | `36b52ea`; `dynamo.go:326,332,374,384` |
| M6 | Med | Append lease (900s) < SQS visibility (960s) -> duplicate append | fixed | `store.go:98 AppendClaimLease = 20m`; `TestAppendClaimLeaseOutlastsTheQueueVisibilityTimeout` |
| M7 | Med | Editor save unconditional S3 Put wipes concurrent voice append | fixed | `2461e17`; `notes.go:313 PutIfMatch` -> `ErrPreconditionFailed` |
| M8 | Med | Search truncates hits it will never return | fixed | `2461e17`; windowed `searchCursor{Start,End,Skip}` in search.go |
| M9 | Med | `excerpt()` panics on case-folding runes | fixed | `search.go:324-335` rune-offset |
| M10 | Med | Tenant spend cap never reached the breaker | fixed | `breaker.go WithCapResolver`; `worker/main.go:129`; `pipeline/tenant_cap_test.go` |
| M11 | Med | Offline notes list showed "Nothing here yet" | fixed | `6aef3fb`; `NotesScreen.tsx:88-192` branches on `fetchStatus==='paused'`/online |
| M12 | Med | `needs_target` card had no action | fixed | `cf7f8e4`; `ProgressCard.tsx:249 useSetCaptureTarget()` |
| M13 | Med | Back out of capture: mic keeps recording, no indicator | fixed | `RecordingIndicator.tsx` uses `isCaptureBusy`; `CaptureScreen.tsx:67` |
| M14 | Med | "Pull down to try again" — no such gesture | fixed | `NotesScreen.tsx:181 Try again -> refetch()` |
| M15 | Med | "Cleaned" transcript tab permanently empty | fixed | `artifacts.ts:120` fetches cleaned text |
| M16 | Med | Orphan report blind to KMS/Cognito | fixed | `teardown.sh:217-260` scans `kms list-keys` by Project tag |
| L1 | Low | Note IDs are raw wall clock | fixed | `notes.go:155 crypto/rand` + `note_%016x_%s` |
| L2 | Low | Transient GetNote error -> silent second note | fixed | `pipeline.go:488-510` `ErrNotFound` vs `default: return err` |
| L3 | Low | Filtered capture list returns empty page + cursor | fixed | `capture_list.go maxCaptureFilterRounds` loop |
| L4 | Low | `Settings.RetentionDays` read by nothing | fixed | `46647c4`; `capture.go:254 CaptureAudioTags(settings.RetentionDays)`; 4 tiered lifecycle rules |
| L5 | Low | Failed server search says "Nothing matches" | fixed | `SearchScreen.tsx:83 serverUnavailable = !online || server.isError` |
| L6 | Low | Offline banner claims cached notes that don't exist | fixed | `cf41933`; `OfflineBanner.tsx:31-32` state-driven; `db.ts` has `notes` store |
| L7 | Low | `ROLLBACK_COMPLETE` makes every deploy fail opaquely | fixed | `dcaf70e`,`79dea02`; `deploy.sh:305,348` branch on status |
| L8 | Low | Stack-status filter hides failures; SIBLINGS guard fails open | fixed | `common.sh:465` excludes only `DELETE_COMPLETE`; `cleanup-aws.sh:131` dies on empty enumeration |
| L9 | Low | `DailySpendCapMicros` unreachable from instance config | fixed | `list-instances.sh:141`; dev.yaml `5000000`, dev-staging `2000000` |
| L10 | Low | Noncurrent versions never expired by default | fixed | template `ExpireNoncurrentVersions` (unconditional, 7d) |
| L11 | Low | `AdvancedSecurityMode: ENFORCED` forces Plus tier | fixed | `13affbe`; `CognitoTier` default `ESSENTIALS`, add-on only if PLUS |
| L12 | Low | `/v1/health/ready` documented public, gateway said 401 | fixed | `b0189ec`; template.yaml:1721 `AuthorizationType: NONE` |
| L13 | Low | README CI table omitted 3 jobs + lint detail | fixed | README.md:258-264 |
| L14 | Low | `pwa:` config block read by nothing | **open** (documented, not fixed) | README.md:98 now *says* "nothing currently reads" it; `vite.config.ts` still hardcodes manifest; both configs still carry the block. Neither of the two proposed fixes (wire it or delete it) was taken. |
| L15 | Low | `ci-deploy-stack.sh` has no `--help` | fixed | `ci-deploy-stack.sh:34`; README:18 amended |
| L16 | Low | `POST /notes/{id}/archive` undocumented | fixed (removed) | `routes.go:36` only `DELETE`; bulk purge/restore documented in openapi |

Review items the reviewers explicitly flagged as owner decisions: #2 (H2, bucket story) — **not recorded**;
#9 (`max_bytes` advisory or removed) — still returned, comment now says advisory; #23 (TokenVaultKey Retain) — moot, key removed;
#26 ENFORCED vs AUDIT — resolved via `CognitoTier`; #27 retention — resolved (made real).

### 1b. docs/audit/2026-08-07-production-readiness-audit.md (v1 @ 32802db)

| ID | Sev | Finding | Status | Evidence |
|---|---|---|---|---|
| B1 | Blocker | `X-User-ID` header trusted over JWT; no signature check | fixed | `internal/auth/{verifier,jwks}.go`; `middleware.Auth(v)`; `isolation_test.go` at both layers |
| B2 | Blocker | Lambda 120s vs API GW 30s cap | fixed | `cmd/worker`, SQS `CaptureQueue`, `390604f` |
| B3 | Blocker | Duplicate note content on retry | fixed | `92dfe61`; `pipeline/append_once_test.go`, `append_redelivery_test.go` |
| B4 | Blocker | Lost updates; no conditional writes | fixed | 14 `ConditionExpression` in dynamo.go; `PutIfMatch` on S3 (`999f574`) |
| B5 | Blocker | Unpaginated queries | fixed | `repository/cursor.go`; `LastEvaluatedKey` x10 |
| B6 | Blocker | Destructive ops scripts target wrong resources | fixed | `06dce05`, `9614f13`; `agent-common.sh` deleted, `execute_cmd`/eval gone |
| B7 | Blocker | `bootstrap.sh` cannot succeed (wrong output name) | fixed | `bootstrap.sh:108 LambdaDeploymentBucketName` |
| B8 | Blocker | Audio lost on upload failure; no IndexedDB buffer | fixed | `offline/db.ts captureChunks`; `f8e1934`, `6d54f6a` |
| §2 | — | No GSI | fixed | `gsi1`, `gsi2` (`38af0f4`) |
| §2 | — | Opaque JSON blob, no projections | fixed | promoted attrs in `noteItemAttrs` |
| §2 | — | `purgeExpired` on read path; string `PurgeAfter` | fixed | `internal/purge`, DynamoDB TTL + `ExpiryLambdaFunction` (`2bebcd2`) |
| §2 | — | No request size limits | fixed | `handler/body.go MaxBytesReader`; worker oversize reject |
| §2 | — | Errors classified by substring | fixed | `handler/errors.go` typed sentinels |
| §2 | — | Four error envelopes | fixed | `httperr` RFC 9457 problem+json; `openapi_conformance_test.go` |
| §2 | — | `InternalServerError` discards error | fixed | `httperr.go:199-207` logs with correlation id |
| §2 | — | Test doubles in production binary (`PlainBox`) | fixed | `repository/memory`, `provider/fake` packages; `testdoubles_guard_test.go` |
| §2 | — | `BeginLogin` reads every credential | fixed | `webauthn.go:125,213 Limit: 1` |
| §2 | — | No observability | fixed | `internal/obs` slog+EMF, `TracingConfig`, access logs, 10 alarms, readiness probe |
| §2 | — | Byte-slice snippet; RFC3339Nano sort | fixed | `notes.go:395`; `pipeline.go:548` fixed-width layout |
| §3 | — | No build/router/state/a11y/offline; no wake lock/waveform/pause | fixed | Vite+React, `wakeLock.ts`, `Waveform.tsx`, `machine.ts paused`, `e2e/a11y.spec.ts` (axe) |
| §3 | — | Tokens in `localStorage`; no CSP | **open** | `api/tokens.ts` still localStorage-backed (behind an interface); no `Content-Security-Policy` meta in `frontend/index.html` |
| §3 | — | iOS manifest/icons/safe-area/orientation | fixed | `index.html:7,24-25`; tokens `--safe-*` |
| §4 | — | No alarms/budgets/DLQ/access logs/X-Ray | fixed | all present in template |
| §4 | — | No WAF | **open** (arguably unnecessary for HTTP API + JWT + per-IP limiter) | grep `WebACL|WAF` = 0 |
| §4 | — | Deletion policies inconsistent | fixed | Retain on table, bucket, user pool (template:146,627,865) |
| §4 | — | Third party cannot clone-and-deploy (OIDC ARN default, Terraform refs, setup.sh) | fixed on paper | `ExistingOIDCProviderArn` default `''`; README rewritten. **Never verified by an actual third-party run** (v2 DoD #7). |
| §4 | — | guardrails-check not in CI; no CODEOWNERS | fixed | `.github/CODEOWNERS`; ci.yaml `guardrails`, `check-boundary-drift` jobs |
| §4 | — | boundary.json near 6144 limit, hand-maintained | fixed | `policy-source.json` generator; boundary 6068 non-ws chars (76 headroom — still tight) |
| §4 | — | No staging, no rollback, no `fail-fast: false`, CI gates missing | fixed | staging-first deploy, Lambda alias, `-race`, golangci-lint, govulncheck, cfn-lint, shellcheck, contract, e2e |
| §4 | — | Cognito minimal (8-char, no MFA, default validity) | fixed | 12-char+symbols, `MfaConfiguration: OPTIONAL`, `DeletionProtection`, explicit token validity |
| §4 | — | `InitiateAuth` on `*`; `dynamodb:Scan` | fixed | conditioned on `cognito-idp:userPoolArn`; Scan removed |
| §4 | — | `invite-user.sh` prints password / `--permanent` | fixed | writes mode-600 file; `--print-password` opt-in |
| §5 | — | Spend breaker / metering dropped | fixed | `internal/breaker`, `internal/meter` |
| §5 | — | Idempotency keys | fixed | `handler/idempotency.go` |
| §5 | — | Backup/export/erase tooling | fixed | `cmd/chintanctl` (backup copies S3 bodies) |
| §5 | — | Cross-tenant isolation test | fixed | `repository/isolation_test.go`, `handler/isolation_test.go` |
| §5 | — | Append-only audit log ("required before a second user") | **open** | no `internal/audit`; only `meter` usage rows |
| §5 | — | I3 audio never through Lambda | fixed | presigned GET to Groq, io.Pipe |
| §5 | — | Cleanup as validated patch / verbatim flag | partially fixed | per-note `verbatim` flag exists; cleanup output is still whole-text LLM rewrite, "do not add information" still prompt-only |
| §5 | — | Recover `0001-no-android-auto.md` as documentation | **open** | no `docs/decisions/` in tree |

## 2. parity-audit.md and ui-audit.md

**parity-audit.md (2026-08-08)** asked "v1 could do X; can v2?" and found 17 gaps. Status now:

- Fixed: sign in/out (`0fb3162`); archive/restore/purge UI (`1132548`, `ArchiveScreen.tsx`, bulk purge/restore); offline note reading, offline search, offline mutation queue (`cf41933`, `db.ts notes` store, `enqueue` has a caller); biometric assertion path (`7827b49`, `unlock.spec.ts`); download audio/transcript (`5970e8b`, `f02d048`, `DownloadButton.tsx`); per-user retention (`46647c4`); suggested note surfaced (`83a73dd`/`cf7f8e4`, `wire.go:143`, openapi:993).
- **Still open** (zero UI callers, verified by grep):
  - Row 11 — create a note manually: `createNote` 0 callers. Only way to mint a note is to speak.
  - Row 12 — find a note by describing it: `matchNotes` 0 callers; `POST /v1/notes/match` still served.
  - Row 13 — record straight into a chosen note: `RecordButton.tsx:24` always navigates to bare `/capture`; `?note=` never constructed.
  - Row 15 — export from the UI: `startExport`/`getExport` 0 callers; export exists only as `chintanctl export`.
  - Row 16 — browse/filter by tag: `useTags` 0 callers.
  - Row 19 (degraded) — per-capture list with date/duration/status: `NoteDetailScreen.tsx:112` still `captures.length > 1` gate, still "Recording N" with no date/status/error.
  - Startup API health probe: `api.health()`/`ready()` have no caller (v1 had one).
  - `useCapture` hook still dead.
- Small: TagEditor 40-rune cap now documented as the contract for *tags* (aliases 120 via prop) — resolved.

**ui-audit.md (2026-08-08, commit 8913ec9)** was a layout/overlap audit (11 viewports x 2 themes x 13 states). All ten defects D1–D10 were fixed in the same pass (`483d38d`) with `e2e/layout.spec.ts` as regression. Three items **deliberately left**, still as described: single-line `.note-title-input` truncates long dictated titles (needs header redesign); 20-tag rows clamped to one line with no "+N more"; `space-evenly` on `.bottom-bar__group` at ultrawide.

## 3. cost-analysis.md — key numbers and suspect claims

Key numbers (us-west-2, single user):
- Fixed/idle: **~$0.09/mo** (CloudTrail S3 PutObject only) after removing the $1/mo KMS CMK (`f546b2a`). Measured whole-month bills: June $0.0137, July $0.0010 (v1 stack).
- Scenarios: Light (30 x 2 min) AWS $0.13 + providers $0.69; Heavy (300 x 5 min) AWS $0.25 + providers $16.80. AWS is ~6% of heavy spend.
- Provider assumptions (`meter.DefaultPrices`): groq 2 micro$/audio-sec (~$0.43/audio-hr), openai 1/4 micro$ per in/out token ($1/$4 per Mtok). Doc itself says these are **3–10x high** vs Groq's published $0.04–0.11/hr and MiniMax pricing — the cap therefore binds at ~1/3–1/10 of intended dollars. Never corrected.
- Custom metrics: 20 names, 317-identity ceiling, 10 free metric-months; safe only because of hourly proration (~300 active hrs/mo threshold).
- Alarms: 10 declared (9 unconditional + `SpendCapRejectionsAlarm` when cap != 0) against a free allowance of exactly 10 account-wide; staging has `enable_alarms: false`.

Claims that look wrong / stale (internal inconsistencies in the doc):
1. §1 headline says idle ~$0.09 / light $0.13 / heavy $0.25, but the §4 line-item table still totals **$0.00 / $0.04 / $0.16** — §4 was never updated for the CloudTrail correction. README.md:328 also still says "$0.00 idle".
2. §1 says "No Metrics Insights alarm remains" (SpendCapRejections now uses `CountWithRollup`), yet §5.3 and the Decisions table still present replacing the Metrics Insights alarm as an open $0.30/mo trade-off. Template confirms a plain `MetricName: SpendCapRejections` alarm — §5.3 is stale.
3. §5.5 "Do not cut `AdvancedSecurityMode: ENFORCED`" is superseded: `13affbe` made tier a parameter defaulting to `ESSENTIALS` with no add-on. The Decisions table still says "Not recommended: turning off ENFORCED".
4. §2.1 counts "9 standard alarms" and says SpendCap alarm "not deployed" because cap defaults to 0 — but `config/instances/dev.yaml` now sets `daily_spend_cap_micros: 5000000`, so prod deploys all 10 (README:93 agrees: "exactly ten"). Headroom is zero, not one.
5. `LLM_MODEL: MiniMax-M3` at `https://api.minimax.io/v1` is hardcoded in template.yaml (3 places) despite the audit noting `LLM_BASE_URL/LLM_MODEL` should be instance config; list-instances.sh dropped `llm_model` as "no code consumed". Cannot verify the model name exists.
6. "S3 versioning ... lifecycle rules already stop noncurrent versions accumulating" was written before L10 was fixed; true now, was not then.

## 4. Plans — completed vs abandoned (judged from code)

| Plan | Verdict | Evidence |
|---|---|---|
| `2026-08-06-chintan-v1.md` (17 tasks) | **completed** as v1, then superseded | every task's files exist in `v0.2.0-prerewrite`; v1 frontend deleted in `cdeb20a`; Go core kept |
| `2026-08-07-biometric-unlock.md` | **completed** (v1, `19a14c2`); v2 lost the assertion path then re-added it (`7827b49`) | `service/webauthn.go`, `handler/webauthn.go`, `unlock.spec.ts`; KMS vault replaced by SSM/AES-GCM (`f546b2a`, `vault_box.go`) |
| `2026-08-07-note-archive.md` | **completed** (v1, `9e19dfa`); v2 UI dropped then re-added (`1132548`) | `notes_archive_test.go`, `ArchiveScreen.tsx`; lazy purge replaced by TTL + expiry Lambda |
| `2026-08-07-chintan-v2-phase1-auth.md` | **completed** | `internal/auth/*`, `middleware/auth.go`, isolation tests |
| `2026-08-07-chintan-v2-master.md` (phases 1–9) | **substantially completed in code; plan artefact abandoned** | Progress log frozen at "Phase 2/3/4/6/7/8/9 in flight"; phase 2–9 plan files were "written just-in-time" and never committed (only phase1 exists). Code for every phase is present: cursor.go/gsi (2), idempotency.go (3), cmd/worker+SQS (4), meter/breaker (5), problem+json/openapi/obs/search/tags/export (6), Vite/React (7), alarms/budget/DLQ/Cognito (8), chintanctl/CODEOWNERS/guardrails/staging/alias (9). |

v2 master "Definition of done" (8 items): 1 isolation test ✓; 2 20-min capture append-once ✓ (`append_once_test`, `append_redelivery_test`); 3 network-loss Playwright ✓ (`e2e/offline.spec.ts`); 4 `chintanctl export` ✓; 5 spend cap distinct status ✓ (`CaptureSpendCapped`); 6 **Lighthouse PWA/a11y > 90 — not in CI** (only axe "no critical violations"); 7 **third-party clone-and-deploy — never demonstrated**; 8 rollback tag ✓.

## 5. Recommended by previous reviewers, never done

1. **Prod bucket migration story (H2, review fix-list #2, "owner decision").** No legacy-name parameter, no `s3 sync` runbook, no pre-deploy guard against replacing a populated `ContentBucket`. Whether prod content was orphaned on the first post-`a69c454` deploy is unknowable from the repo.
2. **Create a note without speaking / find by description / record into a chosen note / tag browsing / in-app export (parity rows 11, 12, 13, 15, 16).** Backend routes and client wrappers exist for all five; zero UI callers. The parity audit gave a one-line fix for each.
3. **Per-capture list with date, duration, status, error (parity row 19).** Still "Recording N", still hidden when there is one capture.
4. **`pwa:` instance-config block (L14).** Neither wired to `VITE_*` nor deleted; README now merely admits it is inert. Contradicts `list-instances.sh`'s own "unknown field fails the deploy" promise.
5. **Append-only audit log (audit §5, "required before a second user").** Not present.
6. **Correct `meter.DefaultPrices` against a real invoice** (cost-analysis §4: 3–10x high, so the spend cap binds far below the dollar figure the owner set). Still the shipped table.
7. **Reconcile cost-analysis.md with itself and with the template** (§4 table vs §1; §5.3 and §5.5 stale after `98ea626`/`13affbe`; alarm headroom now zero with dev.yaml's cap). README:328 "$0.00 idle" vs the doc's own $0.09.
8. **CSP header/meta and moving tokens out of `localStorage`** (audit §3). Still absent / still localStorage.
9. **Lighthouse PWA + a11y > 90 gate (v2 DoD #6)** — only axe critical-violation check exists. **Third-party clone-and-deploy (DoD #7, audit §4)** — rewritten README, never exercised.
10. **`docs/decisions/0001-no-android-auto.md`** — audit asked for it to be recovered as documentation; not in tree.
11. **Cleanup as a validated patch rather than whole-text rewrite (Invariant I4)** — `verbatim` flag exists, but "do not add information" is still prompt-only.
12. **Startup API health probe in the frontend** (parity "smaller inconsistencies") — `api.health()`/`ready()` still uncalled; a dead API surfaces per-screen.
13. **`max_bytes` in `POST /v1/captures` response (review #9, owner decision)** — still returned; now labelled advisory rather than removed.
14. **`UpdateUserPoolDomain` on `Resource: '*'`** remains unconditioned in the deploy role (M3 residue; commits `d63681a`/`f658869` say the action cannot carry the condition — accepted, but unrecorded as a residual risk).
