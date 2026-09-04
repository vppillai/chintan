# Chintan backlog

Owner's manual-trial notes from 2026-09-04, triaged. One line per item; status is one of **fixing** (in a branch now), **next** (agreed, not started), **design** (needs a decision, proposal below), **answer** (a question, answered here), **done**.

## Bugs

| # | Item | Status | Notes |
|---|---|---|---|
| B1 | Transcript panel says "made before timestamps were captured" for every recording; Copy transcript copies nothing | **done** | Root cause found: `segments.json` uses `start_ms`/`end_ms`, the parser expected `start`/`end` in seconds, so every segment was dropped. Backend was fine (`has_segments: true`, file present). Copy also gets a touch-safe fallback and clearer scope ("Copy this transcript" = that recording). |
| B2 | Note shows old text after a recording is filed into it; needs a second open or a reload | **done** | No cache invalidation when a capture reaches `appended`. The filing poll now invalidates the note, the list and the offline corpus on that transition, and on "Open the note". |
| B3 | Filing row should disappear once "Open the note" or Dismiss is tapped, and must not come back | **done** | Dismissed ids move from an in-memory set to `localStorage`; the 10-minute auto-hide is removed (rows stay until you act). |
| B4 | Previous recording's waveform flashes when a new recording starts | **done** | The canvas read the old peak collector for a frame. |
| B5 | Search does not find words inside transcripts | **done** | Both server and local search only saw title/aliases/tags/snippet. The note index gains `search_text` (lowercased body, ≤32 KB), searched on the server and shipped to the offline corpus; one-time backfill for existing notes. |
| B6 | Version string shows `v0.5.0-14-gabcdef` | **done** | Two parts: deploys now tag `main` (`vX.Y.Z+1`) and publish a release; the footnote shows the tag, or `v0.5.0+14 (abcdef)` when between tags. |
| B7 | Passkey setup is never offered | **done** (hand-off) | Confirmed in a browser: Cognito's managed login does not prompt for passkey registration after a password sign-in; it only offers "sign in with passkey" to users who already have one. In-app registration was tried against prod (2026-09-04) and is impossible: `StartWebAuthnRegistration` returns `rp.id` = the managed-login domain, and `navigator.credentials.create` from `vppillai.github.io` fails with `SecurityError: The relying party ID is not a registrable domain suffix of, nor equal to the current domain`; the hosted-UI access token also lacks `aws.cognito.signin.user.admin`, so even listing credentials is refused. What works is the managed login's own page, `/passkeys/add?client_id=…&redirect_uri=…`, which needs a live managed-login session and returns to the app with `?result=success` or `?result=invalid_session`. Shipped: a "Sign in with a passkey" card on **You** that hands off to that page and reports the result, a one-time nudge on the library (dismissal remembered per device), and unsupported-browser copy. Listing/removing passkeys in-app would need the client's OAuth scopes to include `aws.cognito.signin.user.admin` (infra). |
| B8 | Upload feels slow even for tiny recordings | **done** | Being measured from logs: API cold start, the create call, the PUT, and the UI's 4-second poll granularity. Findings and the fix land with the log review. |
| B9 | Filing sometimes takes ~1 minute | **done** | One routing call to MiniMax took 63 s; others take 1–2 s. The log review checks whether this is provider variance or our prompt/settings and proposes a timeout + fallback. |
| B10 | "Add a recording to this note" → after Stop the filed text isn't in the note until re-entering | **done** | Same cause as B2. |
| B11 | Manual edit: how many API calls? | **answer** | One PATCH per pause in typing (autosave debounces; since yesterday saves are serialised, one in flight at a time, and the version comes from the response). The QA pass counts requests for a typed sentence; if it is more than ~1 per pause, we widen the debounce. |
| B12 | Recording continues when the phone screen locks? | **design** | Android Chrome keeps a `MediaRecorder` alive with the screen locked as long as the tab holds the mic (it shows a notification); iOS Safari suspends it. The app already holds a wake lock, which keeps the screen ON instead. Proposal: keep the wake lock (safer for eyes-off use), and add an explicit "screen may lock" mode later only if Android testing shows recording survives it. Needs a real-device test. |
| B13 | Incoming call while recording | **done** | The track `mute`/`ended` handlers exist; make `mute` auto-pause (not just flag) and show "Paused: microphone taken by another app" live, resume on `unmute`. |
| B14 | PWA shortcut / launch latency | **done** | Measured by QA (cold load of `/` and `/capture` on Fast 3G, 4× CPU). Candidates: preconnect to the API and Cognito, drop react-router's 25% of the bundle, precache the capture route, defer the notes/tags/archived fetches on the capture route. |

## UX / product

| # | Item | Status | Notes |
|---|---|---|---|
| U1 | Pull-down to refresh | **done** | Library and note screen; native-feel with `overscroll-behavior` guard. |
| U2 | "Select" button top-right looks odd; long-press to select on phones; multi-select easy on desktop; bulk actions not at the end of the list | **fixing** | Proposal: remove the header button. Phone: long-press a row enters selection (checkbox column slides in, haptic tick); desktop: hover reveals a checkbox at the row's left edge, click selects, shift-click ranges. Bulk bar becomes a **sticky bar above the tab bar** (not the list end) with count · Archive · Delete forever · Cancel. |
| U3 | Pagination / page-size setting for long lists | **fixing** | Proposal: infinite scroll ("Load more" already exists; make it automatic on scroll) rather than page-size settings — settings for page size are a desktop idiom. Day groups already chunk the list visually. Open to overriding. |
| U4 | Record button larger, never covering the last row | **done** | Bigger centre button (64→76 px) and bottom padding on the list equal to the bar height plus safe area, so the last row is always fully visible above it. |
| U5 | Home title "Notes" is plain | **fixing** | Proposal: a greeting-free header with the app wordmark on the left and the day's date in serif on the right ("Thursday 4 Sept"), search below. Alternative: keep "Notes" but as a serif display with the count ("Notes · 12"). |
| U6 | Tab label "Notes" → "Home" | **done** | |
| U7 | Word count of the note | **done** | In the meta line after recordings: "412 words". |
| U8 | About page + GitHub link | **done** | On You: "About Chintan" → what it does, how filing works, privacy (where data lives), version, link to the repository. |
| U9 | Icons and app icon polish | **fixing** (first pass) | Keep a single stroke-weight icon set (e.g. Lucide-style 1.75 px strokes, tuned) and redraw the app icon around the wordmark's serif "C" with the accent dot; no emoji anywhere. I'll propose an icon sheet as an artifact before applying. |
| U10 | Don't wait on the send screen; hand off to the background as soon as Send is tapped | **done** | Agreed: Send → back to the library immediately, the filing row shows upload → transcribe → file. The upload already survives navigation (it lives in the store); only the screen's waiting behaviour changes. |
| U11 | Playback of the just-recorded audio before Send, with scrubbing | **done** | The buffered chunks are on disk; assemble a Blob and reuse the note player + scrubber on the review screen. |
| U12 | Horizontal scroll through the full waveform while uploading | **done** | Aesthetic; pairs with U11 (same waveform component). |
| U13 | Spending-cap text in Settings without a control | **done** | Read-only since 2026-09-03; per-user usage accounting is being added on the backend (see D6). The line will show your usage this month instead of a cap once `GET /v1/usage` exists. |
| U14 | Dismiss semantics of filing rows | **done** | See B3. |

## Features needing design

| # | Item | Status | Proposal |
|---|---|---|---|
| D1 | Advanced cleanup ("smart") vs the current misheard-word cleanup, with raw and cleaned as tabs; re-run over the whole note when new audio arrives | **design** | Per-note **cleanup mode**: `faithful` (today), `polished` (already exists on the backend, not exposed), `structured` (new: rewrite the *whole note* into headings/lists, run on demand and again after each append while the mode is on). Store `body_raw` (concatenated cleaned-per-capture text, today's body) and `body_structured` separately; the editor shows tabs **Text · Structured**; edits to Structured mark it stale until re-run. Cost: one extra LLM call per append in structured mode; bounded by the spend cap. |
| D2 | Move a misfiled recording to another note; splice text by timestamp | **fixing** (backend done in #30; Move UI in progress) | Recording rows get **Move to…** (single) and multi-select on recordings. Moving = remove that capture's marker-delimited paragraph from note A (we have exact markers since yesterday), append it to note B at the position implied by the recording's time relative to B's recordings (chronological insert, not always at the end), re-point the capture row. The transcript is not re-run. Structured mode (D1) re-runs on both notes. |
| D3 | Delete a single recording and its text | **fixing** (backend done in #30; Delete UI in progress) | Same primitive as D2's removal step: delete the marker-delimited paragraph, delete the capture row and objects (audio, raw, clean, segments, peaks), typed confirmation. |
| D4 | Download all recordings of a note as one archive; multi-select recordings | **fixing** (manifest endpoint in #30; client zip in progress) | Client-side zip (`fflate`, ~8 KB) from the presigned audio URLs; name files `<note>-<yyyymmdd-hhmm>.webm`; no backend change. Selection UI shared with D2/D3. |
| D5 | AI search / ask the knowledge base | **design** | Two layers. (1) **Ask** as a mode of the search field ("Ask: …") that sends the question to a new `POST /v1/ask`; the worker retrieves candidate notes by the existing text search plus a per-note **summary** (new `summary` attribute, ~60 words, generated at append time in the same cleanup call so it costs no extra request), packs the top-N bodies within a token budget, and answers with citations to note ids; the UI shows the answer with note chips. (2) Later, embeddings in DynamoDB for recall when notes outgrow N — not now. Grounding rule in the prompt: answer only from the provided notes, say "not in your notes" otherwise. |
| D6 | Per-user usage accounting for future admin/billing | **done** | Monthly and daily `USAGE#<user>#…` counters written by the breaker (cost, calls, audio seconds, tokens) plus `GET /v1/usage`; design note covers the admin listing path. |
| D7 | Transcription language per note (English default, other language, or auto/mixed) | **done** | Groq Whisper takes an ISO-639-1 `language`, or auto-detects when omitted; there is no explicit mixed mode — auto is the closest (it handles code-switching imperfectly). Per-note `language` (`en` default, any code, or `auto`) + a `default_language` setting; UI control on the note screen to follow. |
| D8 | Offline recording; notes readable offline | **answer / next** | Recording offline already works: chunks stay in IndexedDB and are offered back on reconnect. Sending is not automatic yet — the plan is to queue the send so it goes out on reconnect without a tap. Notes are already cached for offline reading (the list and the corpus); the note body itself is cached after the first open — will extend to prefetch bodies. |
| D9 | Telemetry for data-driven improvement | **design** | Backend: already emits EMF metrics per stage and provider latency; add client-observed timings (record→sent, sent→filed, cold start) as one small `POST /v1/telemetry` batch per session, written to the log only (no new storage). Frontend: `web-vitals` is *not* bundled (the console errors you pasted come from a browser extension). Keep it log-only until a question needs more. |
| D10 | "Febl index 01" ring integration | **question** | I could not identify this device or its app's outbound integration. Which ring/app is it, and what can it send (a webhook URL? a share-to-app intent? an email)? The likely path is a small `POST /v1/inbox` endpoint with a per-device token that accepts audio or text and files it like a capture. |
| D11 | Docs cleanup: remove v1/v2 history; a single-script deploy for a fresh clone | **done** | History documents removed; the 2026-09-03 review is kept as a dated report under `docs/history/`. README rewritten as: what it is · deploy · configure · operate · security · develop. `scripts/doctor.sh` checks the prerequisites and prints the next command. Verified with a fresh checkout and the scripts' dry runs. |
| D12 | Prompt-injection safety for AI features | **next** | D1/D5 add LLM calls over user content; they reuse the fenced-transcript pattern and the router's output verifier. |

## From the QA pass (docs/qa/2026-09-04/report.md)

| # | Item | Status | Notes |
|---|---|---|---|
| Q1 | Download audio never works (CORS: the `<audio>` element's no-CORS cached response poisons the later `fetch`) | **done** | `crossOrigin="anonymous"` on the element or `cache: 'no-store'` on the fetch. |
| Q2 | Re-open a note within ~30 s of editing shows the pre-edit text; next save 409s with a false "changed elsewhere" | **done** | The PATCH result is never written to the note/list query cache. Write-through on save; same root cause as Q3. |
| Q3 | Library rows stale after editing a note | **done** | See Q2. |
| Q4 | Filed receipts accumulate; dismissal not persisted | **done** (the QA sampler had misread the new `uploading` state; two real defects behind it fixed in #31) | PR #22 was meant to fix this; QA ran on that build and still saw it. Re-test, then fix. |
| Q5 | In-app URLs drop the trailing slash → outside the PWA scope; offline reload of such a URL is a browser error page | **done** | Router `basename` normalisation. Check installed-PWA behaviour on a real device after. |
| Q6 | Bulk-action bar sits below the last row (6,400 px down on a phone) | **fixing** | Folded into U2 (sticky bar above the tab bar). |
| Q7 | Note opened mid-filing never refreshes | **done** | Same fix family as B2/Q2. |
| Q8 | One `GET /v1/search` per keystroke | **done** | Debounce 250 ms; the local corpus stays instant. |
| Q9 | Unsaved settings silently lost on navigation | **done** | Save on change (each control), no Save button. |
| Q10 | Same speech, six runs, four outcomes, including a duplicate "Roof repair" note | **done** | Router redesign in progress; add "an existing note with the same title is the destination" rule. |
| Q11 | Offline recording waits for a manual Send after reconnect | **done** | Queue the send; see D8. |
| Q12 | Passkey enrolment never offered | **done** | See B7. |
| Q13–Q21 | Theme "Unsaved" flag, no `h1` on note, 20 px checkboxes, orphan chunk row per Cancel, tag chip outlives its notes, offline "Loading…" forever, fixed 12-row textarea, skip-link shadow, `has_peaks` on a peak-less capture (fixed server-side in #23) | **done** | Batch of small fixes. |

Performance (cold, Fast 3G + 4× CPU): library interactive ~2.95 s, `/capture` ~3.05 s; one 136 KB gzip JS chunk; four API calls on the library, one only for the "Archived · n" count.

## Order of work

1. B1–B6, B10 and U6 (frontend and backend branches in progress).
2. QA pass results folded in.
3. B7 passkey registration; U10/U11/U12 (send hand-off, review playback); U2/U4 selection and record button; B13 call handling.
4. D1 cleanup modes (needs your call on the three-mode proposal), D2/D3/D4 recording actions.
5. D5 Ask, D9 telemetry, D11 docs.
