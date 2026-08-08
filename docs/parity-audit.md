# Chintan v2 — parity audit

**Date** 2026-08-08 · **Branch** `main` · **Scope** what a user can *do*, v1 vs v2

## Why this document exists

v1 was audited for defects (`docs/review-2026-08-08.md`, `docs/audit/2026-08-07-production-readiness-audit.md`).
v2 was specified for the features to add (`docs/superpowers/specs/2026-08-07-chintan-v2-design.md`).
Nobody asked whether v2 still did everything v1 did. It shipped with no login screen
and no sign-out control, and the owner found that by trying to use the app.

This is that missing question, asked properly. **17 capabilities are gone or were
never built** — 15 outright missing, 2 materially degraded — beyond the two auth
gaps already known.

## Method

v1's inventory came from `frontend/index.html` and `frontend/js/*.js` at tag
`v0.2.0-prerewrite` — a single HTML file with every screen as a hidden div, so the
enumeration is complete rather than sampled.

v2's inventory came from **running it**. The bundle was built with the e2e
environment, served from `vite preview`, and driven with Playwright across every
route, with the API stubbed to match `docs/api/openapi.yaml`. Controls were read
out of the live DOM, not out of JSX. Where a capability depends on state the UI
must reach (a `needs_target` capture, an offline reload, a note opened with no
network) that state was produced and the result observed.

Grep was used only to locate. Every verdict below was confirmed by reading the
component and, for anything user-visible, by rendering it. Two things that grep
would have called present are called present here for the right reason, and one
thing grep would have called present — retention — is called missing below.

---

## The table

Ordered by user impact, not by area.

| # | Capability | v1 | v2 | Verdict | Evidence |
|---|---|---|---|---|---|
| 1 | Sign in | Yes | Yes | **fixed** | v1 `index.html:35`. v2 `features/auth/SignedOutScreen.tsx:31`, landed in `0fb3162`. Confirmed present; not re-reported. |
| 2 | Sign out | Yes | Yes | **fixed** | v1 `index.html:49`. v2 `features/auth/SignOutSetting.tsx:54`, landed in `0fb3162` — rendered and observed as "Sign out / On this device" on `/settings`. |
| 3 | Delete a note (archive) | Yes | **No** | **MISSING** | v1 `index.html:136`, `js_notes.js:217`. v2: no control on any screen. `endpoints.ts:98 archiveNote` is reachable only from `offline/useOfflineQueue.ts:35`, which replays a queue nothing writes to (row 9). Rendered `/notes/roof-repair`: only Back, title, player, transcript, body, tags, aliases. |
| 4 | See archived notes | Yes | **No** | **MISSING** | v1 `index.html:121` Archive tab, `js_notes.js:113`. v2 `screens/NotesScreen.tsx:18` hardcodes `useNotes({ state: 'active' })`. No archive route in `app/routes.ts:2-10`. Rendered `/notes?status=archived` → param ignored, app still requested `GET /v1/notes?state=active`. `/archive` → "Nothing here". |
| 5 | Restore an archived note | Yes | **No** | **MISSING** | v1 `index.html:134`, `js_notes.js:236`. v2 `endpoints.ts:104 restoreNote` — only call site is the dead offline replayer. |
| 6 | Delete forever (purge) | Yes | **No** | **MISSING** | v1 `index.html:135`, `js_notes.js:248`. v2 `endpoints.ts:111 deleteNoteForever` — **zero callers anywhere**. Its own doc comment claims it is "gated by the confirm dialog"; no dialog calls it. |
| 7 | Read a note offline | — | **No** | **MISSING** (promised) | Spec §5.5: "IndexedDB holds … the note corpus for offline reading". Rendered offline, `/notes/roof-repair` → **"Note not found — No note with that identifier. It may have been archived or purged."** `NoteDetailScreen.tsx:39` treats any error as 404. IndexedDB holds only `captureChunks`, `captures`, `mutations` (`offline/db.ts:91-101`) — there is no notes store. |
| 8 | Offline edits queue and sync | — | **No** | **MISSING** (promised) | Spec §5.5: "a queue of pending mutations … Background Sync … flushes the queue". `offline/queue.ts:38 enqueue` is called from **no non-test file**. `OfflineBanner` nonetheless renders "No connection. Your work is saved on this device and will sync." — observed. Nothing is saved and nothing syncs. |
| 9 | Biometric unlock at sign-in | Yes | **No** | **MISSING** | v1 `index.html:32`, `js_app.js:93`, `js_webauthn.js`. v2 has the *enable* toggle (`BiometricSetting.tsx:105`) and vaults a refresh token (`:39-57`), but there is **no assertion path at all**: no `webauthn/login` wrapper in `endpoints.ts`, no assertion helper in `features/settings/webauthn.ts`, and `useAuth.ts:136` always redirects to Cognito. Users enrol into something no code can unlock. |
| 10 | Search notes offline | — | **Broken** | **MISSING** (promised) | Spec §5.5: "the note corpus for offline reading and **instant search**". Observed offline with the note list cached and visibly showing "Roof repair", searching `roof` returned *"Nothing matches 'roof'"*. `SearchScreen` reads a separate `notesCorpus` query key (`queries.ts:43`) that is only fetched when the search screen is opened online. |
| 11 | Create a note manually | Yes | **No** | **MISSING** | v1 `index.html:117` "+ New Note", `js_notes.js:315`. v2 `endpoints.ts:81 createNote` is uncalled. The only way to mint a note is to record audio and route it (`ProgressCard.tsx:249-277`). You cannot create an empty note, and you cannot create any note without speaking. |
| 12 | Find a note by describing it | Yes | **No** | **MISSING** | v1 `index.html:66-78`, `js_capture.js:52` — type a description, get ranked candidates, append or create. v2 `endpoints.ts:118 matchNotes` has **zero callers**. This is goal 24 of the v1 spec ("high confidence → auto-select; ambiguous → top matches + New note") and it is still served at `POST /v1/notes/match`. |
| 13 | Record straight into a chosen note | Yes | **No** | **MISSING** | v1 chose the note first, then recorded (`js_capture.js:134`). v2's `CaptureScreen` accepts `?note=<id>` (`CaptureScreen.tsx:44`) but **nothing in the app ever constructs that URL** — `RecordButton.tsx:26` always navigates to bare `/capture`, including the copy sitting inside a note. |
| 14 | Download a recording or transcript | Yes | **No** | **MISSING** | v1 `js_notes.js:302-304` — Audio / Raw STT / Clean buttons per capture. v2 uses `downloadUrl` only to feed `<audio src>` and two internal `fetch`es (`features/notes/artifacts.ts:112-142`). No `<a download>`, no `window.open`, no export button. Your audio is not retrievable through the UI. |
| 15 | Export everything | — | **No** | **MISSING** (never built) | `POST /v1/export` and `GET /v1/export/{id}` are in `openapi.yaml:467,484` and served at `routes.go:52-53`. `endpoints.ts:226,230` wrap them. **Zero callers.** Export exists only as `chintanctl export` (README:194), which an owner on a phone cannot run. |
| 16 | Browse or filter by tag | — | **No** | **MISSING** (never built) | `GET /v1/tags` served (`routes.go:38`), wrapped (`endpoints.ts:125`), hooked (`queries.ts:101 useTags`) — and `useTags` has no caller. Tags can be *edited* on a note and shown on a row, but never used to find anything. |
| 17 | Per-user audio retention actually expires audio | No (known dead) | **No** | **MISSING** | See "Settings the backend ignores" below. The v2 UI renders "Keep recordings for — Days to keep source audio", and nothing reads it. |
| 18 | The app tells you where it thinks a recording goes | Yes | Degraded | **MISSING (degraded)** | v1 showed `Add to "<suggested note>"` as the primary button plus a preview of what you said (`js_capture.js:399-406`, `:436`). v2 shows an **unranked list of every note** with no suggestion and no transcript preview (rendered: "Roof repair", "Reading list", "Or start a new note"). The backend computes the suggestion — `model/types.go:140-141 SuggestedNoteID/SuggestedTitle`, set at `pipeline/pipeline.go:495,517` — and `handler/wire.go:97-108` **drops both fields before they leave the API**. An LLM routing call is paid for and thrown away. |
| 19 | See a note's recordings and their state | Yes | Degraded | **MISSING (degraded)** | v1 listed every capture with a status label, timestamp and error (`js_notes.js:295-308`). v2 renders a picker labelled "Recording 1..n" and **only when `captures.length > 1`** (`NoteDetailScreen.tsx:75`) — no date, no duration, no status, no error, and with one capture no list at all. |
| — | Edit note title / body | Yes | Yes | present | `NoteDetailScreen.tsx:57,96`. Autosave replaces v1's Save button — deliberate. |
| — | Edit aliases | Yes | Yes | present | `NoteDetailScreen.tsx:120` "Also called". Chips replace v1's comma field — deliberate, and confirmed rendering. |
| — | Cleanup mode setting | Yes | Yes | present | `SettingsScreen.tsx:152-175`, persisted via `PUT /v1/settings`. **Not** the transcript raw/cleaned toggle (`TranscriptPanel.tsx:72`), which is local `useState` and persists nothing. |
| — | "Where should this go?" prompt | Yes | Yes | present | Rendered: `needs_target` → "Which note should this go in?" → "Choose a note" → picker. See row 18 for what it lost. |
| — | Retry a failed capture | No (dead in v1 too) | Yes | **added** | v1's `api.retryCapture` (`js_api.js:185`) had no UI caller either. v2 wires it at `ProgressCard.tsx:178-187`. Spec §5.3 promised this and delivered. |
| — | Biometric enable/disable setting | Yes | Yes | present | `BiometricSetting.tsx:105`. Enabling it is now pointless — row 9. |
| — | Theme, spend cap | — | Yes | **added** | `SettingsScreen.tsx:129,211`. Spend cap is genuinely enforced (`service/spend.go:77`). |
| — | Recent notes on home | Yes | No | **deliberately dropped** | Spec §5.2: home is the record surface "with nothing competing for attention". Notes are one tap away. |
| — | Toast notifications | Yes | No | **deliberately dropped** | Replaced by inline status plus one polite live region (`StatusRegion.tsx`, spec §5.7). The live region is `visually-hidden`; sighted feedback is inline instead. Acceptable, but note that *every* confirmation v1 spoke ("Note saved", "Settings saved") is now silent unless a screen renders it. |
| — | Service-worker update prompt | Yes | Yes | present | `pwa/UpdatePrompt.tsx:67`. |
| — | `setupInstallPrompt` dead code | Yes (dead) | Deleted | **resolved** | Spec §5.8 said wire it or delete it. Deleted — no `beforeinstallprompt` anywhere. |

---

## The three worst

**1 — A note can never be removed. Rows 3, 4, 5, 6.**
Four v1 capabilities went together: delete, view archive, restore, purge. v2 is
append-only. Every mis-dictated note, every duplicate the router created, every
private thing said by accident is permanent and always on the notes list. The
backend serves all four operations; the client has wrappers for all four; not one
is wired to a control. `NoteDetailScreen.tsx:47` even tells the user a note "may
have been archived or purged" — describing states the UI cannot produce or show.

**2 — Offline is a facade that claims the opposite. Rows 7, 8, 10.**
Spec §5.5 promises a cached note corpus, instant offline search, and a queue of
pending mutations flushed on reconnect. None of the three exists. The banner
states *"No connection. Your work is saved on this device and will sync."* — it is
saved nowhere and syncs never. Opening any note offline reports it *"may have been
archived or purged"*. A user who trusts that banner and keeps typing loses the
work, and the app told them it was safe.

**3 — Biometric unlock enrols users into nothing. Row 9.**
v1 had "Unlock with biometrics" on the login screen. v2 kept the settings toggle,
kept the enrolment round trip, kept the sealed refresh-token vault in the backend
and a KMS-adjacent design around it — and shipped no assertion path. There is no
`webauthn/login` call anywhere in the client. Turning the setting on performs a
real WebAuthn registration and stores a real credential that no code can ever use.
It is a security-shaped feature that does nothing, and it looks like it works.

---

## Endpoints with no UI caller

28 operations are specified in `openapi.yaml`; the Go router registers exactly 28,
1:1, enforced by `handler/openapi_conformance_test.go`. **13 of them no UI ever
calls.**

| Endpoint | Client wrapper | Why nothing calls it |
|---|---|---|
| `POST /v1/notes` | `endpoints.ts:81` | No create-note control (row 11) |
| `DELETE /v1/notes/{id}` | `:98` | No delete control; only site is the dead offline replayer (row 3) |
| `POST /v1/notes/{id}/restore` | `:104` | Same (row 5) |
| `DELETE /v1/notes/{id}/permanent` | `:111` | Zero callers (row 6) |
| `POST /v1/notes/match` | `:118` | Zero callers (row 12) |
| `GET /v1/tags` | `:125` | `useTags` has no caller (row 16) |
| `GET /v1/captures/{id}` | `:162` | `useCapture` has no caller; progress polls the list endpoint instead |
| `POST /v1/export` | `:226` | Zero callers (row 15) |
| `GET /v1/export/{id}` | `:230` | Zero callers (row 15) |
| `POST /v1/auth/webauthn/login/options` | **none** | No wrapper exists (row 9) |
| `POST /v1/auth/webauthn/login` | **none** | No wrapper exists (row 9) |
| `GET /v1/health` | `:47` | Never called. v1 probed it on startup (`js_app.js:207`) |
| `GET /v1/health/ready` | `:51` | Never called (infrastructure probe; fine) |

Dead hooks alongside them: `useUpdateNote` (`queries.ts:79`), `useSearch` (`:91`),
`useTags` (`:101`), `useCapture` (`:174`). Dead state machine:
`app/sheet.ts:61 sheetReducer` and `:90 isSheetLocked` are referenced only inside
their own file.

**Also undocumented, not merely uncalled:** `suggested_note_id`, `suggested_title`
and `route_confidence` exist on the stored capture (`model/types.go:140-142`) and
appear nowhere in `openapi.yaml`. The spec cannot be the reason the client dropped
them, because the spec never mentioned them either.

## UI with no backend

**None.** Every path `endpoints.ts` constructs maps to a registered route. The only
off-API calls are the Cognito hosted UI and the S3 presigned PUT, both expected.
This direction is clean.

## Settings the backend ignores

Four settings fields exist; the wire shapes match exactly in both directions
(`schema.ts:58-63` ↔ `handler/wire.go:179-191`), and `theme`, `cleanup_mode` and
`daily_spend_cap_micros` are all genuinely acted on.

**`retention_days` is not.** This is the `RetentionDays` problem from v1, still
present, and the code says otherwise. `internal/upload/upload.go:12` states: *"In
v1 RetentionDays was stored, returned, rendered in the UI, and read by nothing.
This is the piece that stops that being true."* It does not stop it being true for
the **user's** setting:

- `upload.go:182` tags every uploaded object with a **constant** `chintan-artifact: capture-audio`. The user's retention value is not encoded in the tag.
- The S3 lifecycle rule (`infrastructure/template.yaml:463-476`) takes its `ExpirationInDays` from the CloudFormation stack parameter `RetentionDays`, a per-instance deploy-time value (README:94).
- Nothing in the request path reads `settings.RetentionDays`. Its only readers are the type, the wire struct, the validator, and the store default.

So the number a user types into "Days to keep source audio" is validated, stored,
returned — and cannot change when anything expires. Worse, the helper text asserts
*"Recordings are kept indefinitely"* whenever the field reads 0, which is false on
any instance whose stack parameter is set. The v1 defect was fixed at the instance
level and re-shipped at the user level.

## Smaller inconsistencies

- `TagEditor` truncates aliases at 40 characters (`TagEditor.tsx:27,37`) while its own comment documents the alias cap as 120 (`:15`). The call site does not override it.
- `NoteDetailScreen.tsx:47` names archive and purge states the UI can neither reach nor display.
- v2 lost v1's startup API health probe, so a dead API now surfaces as a per-screen error rather than one clear message.

---

## What I could not check, and why

- **Anything requiring AWS.** Credentials are expired. The retention finding above is from source and `infrastructure/template.yaml` only — I did not confirm the deployed lifecycle rule, the `RetentionDays` parameter any live stack actually carries, or whether a deployed stack has drifted from the template. If a deployed rule differs, row 17's *mechanism* changes but its conclusion — that the user-facing setting is not an input to it — does not, because that is settled in the Go source.
- **The real backend.** The Go API was not run. Every v2 observation used a stubbed API matching `openapi.yaml`. Behaviour that depends on real responses — pagination cursors, genuine 409 conflicts, real pipeline transitions, spend-cap enforcement — was simulated or read, not exercised end to end.
- **v1 at runtime.** v1's frontend is deleted from `main`. I read every line of it at `v0.2.0-prerewrite` but did not run it; it needs a generated `config.js` and a live Cognito. The v1 column is a source reading, not an observation. It is nonetheless complete: v1 is one HTML file and eight scripts.
- **Real devices and platform authenticators.** Headless Chromium has no platform authenticator, so biometric *enrolment* was verified by reading code, not by performing it. PWA install, wake lock, haptics and iOS safe-area behaviour were not exercised. Row 9 does not depend on any of this — the absence of an assertion code path is a source fact.
- **The auth work.** `frontend/src/features/auth/` was being written by another agent while this ran; it landed mid-audit as `0fb3162`. Rows 1 and 2 were observed both before and after that commit and are sound, but I did not audit the sign-in implementation itself — only that a way in and a way out now exist. Note that row 9 (biometric unlock) is **not** covered by that commit: it wires OAuth only, and `useAuth.ts:136` still always redirects to Cognito.
- **`chintanctl`.** Audited only for whether it substitutes for missing UI (row 15). Its own correctness was out of scope.

## What would restore each gap

| Rows | Work |
|---|---|
| 3, 4, 5, 6 | A destructive-action control on the note screen wired to `archiveNote`, an archive list (`useNotes({ state: 'archived' })` — the type already allows it, `schema.ts:111`) reachable from the notes screen, and restore/purge controls on an archived note. `ConfirmDialog` already exists and spec §5.7 already reserves it for "the two destructive actions". Backend and client wrappers are complete; this is UI plus routing only. |
| 7, 8, 10 | A `notes` object store in `offline/db.ts`, written on every successful list/get; `useNote` falling back to it and distinguishing 404 from offline; and at least one `enqueue()` caller on the note-edit path. Until then, change the OfflineBanner copy — it should not promise persistence that does not exist. |
| 9 | Add `webauthn/login/options` and `webauthn/login` wrappers, an assertion helper beside `performRegistration`, and an unlock button on `SignedOutScreen`. Backend endpoints and the token vault are already built and served. |
| 11 | A create-note control calling the existing `createNote`. |
| 12, 18 | Add `suggested_note_id`/`suggested_title` to the `Capture` wire struct and to `openapi.yaml`, read them in `TargetPicker` to lead with the suggestion, and give the picker a description box calling the existing `matchNotes`. |
| 13 | Pass the current note id into `ROUTES.capture` from the note screen; `CaptureScreen` already honours `?note=`. |
| 14, 15 | A download control per artifact using the existing `downloadUrl`, and an export screen over the existing `startExport`/`getExport`. |
| 16 | Call `useTags`, and let a tag filter the notes list. |
| 17 | Either make the per-user value an input to expiry — encode it in the object tag and give the lifecycle rule matching filters — or delete the field from the UI and the API and let the instance parameter be the honest answer. Do not leave it rendered. |
| 19 | Render the capture list unconditionally with date, duration and status, as v1 did. |

## The process gap that produced all of this

Every one of these is the same failure that hid the login screen. The v2 spec is a
list of things to **add**; §5 opens with *"Complete rewrite. Nothing from
`frontend/js/` is carried forward."* — and nothing checked what that sentence was
discarding. The two prior audits looked at v1's defects and v2's rendering. No
document in this repo, before this one, contains the sentence "v1 could do X; can
v2?"

A rewrite needs that list written down **before** it starts, and checked before it
ships. The cheapest version is a table like the one above, built from the old UI on
day one and used as an exit criterion.
