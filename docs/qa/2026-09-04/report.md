# Chintan — exploratory QA of the live PWA, 2026-09-04

**Target:** https://vppillai.github.io/chintan/dev/ (`chintan-dev-prod`), test account `claude-test@example.com`.
**Build under test:** `v0.5.0+14 (96311d0)`, bundle `index-B-4bjH0K.js`. A deploy (PR #22, *"Fix the transcript parser, the stale note after filing, filing-row dismissal, the waveform flash, Home tab, version footnote, body search"*) landed at 20:03 UTC while the first mobile pass was running against the previous bundle (`index-CvF8Uo8Y.js`). Everything below was re-run on 96311d0 (mobile 28/28 specs, desktop 27/28 — the one failure is a test-side timeout); where the two builds differ it is said explicitly. Old-build screenshots are in `old-build/`.
**Tooling:** Playwright 1.62 / Chromium 1234 on the orb Linux box, two projects — `mobile` (Pixel 7 descriptor overridden to 412×915 @2.625 with touch, i.e. a Pixel 8 Pro) and `desktop` (1280×800). Sign-in goes through the real Cognito managed-login page once per project and the tokens are persisted (`storageState`). The microphone is Chromium's fake device fed a 12 s espeak-ng WAV. Scripts live in `frontend/qa/` (see the end). Every request, console line and observation of every run is in `frontend/qa/shots/*.log.json` on orb; the log lines quoted below come from there.

Nothing in the application was modified. Data created in the account: 25 seeded notes (`Roof repair` … `Weekend hike`, via `POST /v1/notes`) and the notes prefixed `Bulk`, `Edit me`, `Tag me`, `Record target`, `Stale check`, `Version check`, `Mid-filing`, `Rename me`/`Renamed`, `Archive me`, plus the captures the record flows produced.

## Summary

| id | sev | screen | viewport | one line |
|---|---|---|---|---|
| D1 | High | Note › Recordings | both | **Download audio never works** — the `fetch` of the presigned S3 URL fails the CORS check because Chromium reuses the `<audio>` element's no-CORS cached response; user sees "Could not download — try again." |
| D2 | High | Note | both | Re-open a note within ~30 s of editing it: the **pre-edit text is shown**, the next save is a **409**, and a false "This note changed elsewhere" conflict appears whose *Keep my edits* would overwrite the user's own edit |
| D3 | Medium | Library | both | Library rows are **stale after editing** a note (old title/snippet on return; no refetch until the 30 s staleTime passes) |
| D4 | Medium | Library › filing rows | both | **"Filed" receipts never leave**: *Open the note* does not dismiss, dismissals do not survive reload, every capture of the last 10 min stacks at the top — 19 rows observed |
| D5 | Medium | Shell / PWA | both | Every in-app navigation lands on `/chintan/dev` (no trailing slash), **outside the manifest and service-worker scope**; an offline reload of `/chintan/dev?view=archived` is the browser error page while `/chintan/dev/` is served |
| D6 | Medium | Library › Select | both (worse on phone) | The **bulk-action bar sits below the last row** (`position: static`); with 30+ notes it is 6 400 px down on the phone (8 000 px on desktop) |
| D7 | Medium | Note ← Record into this | both | A note opened while its recording is still filing **never refreshes** (10 s after the server appended: old body, 0 recordings, one GET) |
| D8 | Medium | Library › search | both | Server search fires **one `GET /v1/search` per keystroke**, no debounce (8 for "flashing"), responses land out of order |
| D9 | Medium | You | both | **Unsaved settings are lost silently** on tapping another tab (no prompt; only `beforeunload` is guarded) |
| D10 | Medium | Record → filing (backend) | n/a | The same 8 s speech, six runs, **four different outcomes**: appended to the existing *Roof repair* (2×), a **second note also titled "Roof repair"**, a new note "Gutter leak" (then appended to it), *needs a target* |
| D11 | Medium | Record, offline | both | A recording made offline is **not sent when the connection returns**; it waits as "You have an unsent recording" until the user taps Send (60 s observed). Offline Send says "SOMETHING WENT WRONG" although the offline banner is showing |
| D12 | Medium | Sign-in | both | Cognito **never offers passkey enrolment** (username → Next → password → Continue → app); README §2 and the sign-in screen copy promise it |
| D13 | Low | You | both | Theme change applies and persists on the device at once yet reads "Unsaved changes"; after reload "All changes saved" while the server still says `ink` — no PUT was sent |
| D14 | Low | Note, Library | both | a11y: note screen has **no `h1`** (axe `page-has-heading-one`, moderate); selection **checkboxes 20×20 px**; playback slider 16 px tall (WCAG 2.5.8 minimum 24) |
| D15 | Low | Record › Cancel | both | Every *Cancel* during a recording leaves an **orphan row in IndexedDB `captureChunks`** (1 → 2 → 3 → 4 after four cancels) |
| D16 | Low | Library | both | A **tag chip outlives its notes** after *Delete forever*; selecting it says "No notes are tagged…" until reload |
| D17 | Low | Note, offline | mobile | Intermittent: an uncached note opened offline shows **"Loading…" forever** (2 of 4 runs; the others showed "Not on this device") |
| D18 | Low | Note | both | Body is a fixed 12-row textarea with its own scrollbar: long notes scroll inside a box nested in the scrolling page; short notes waste ~300 px and push *Recordings* below the fold on the phone |
| D19 | Low | Shell | desktop | The skip link's box-shadow **peeks out of the top-left corner** (~10 px grey sliver) on every screen at 1280×800 |
| D20 | Low | Note › Recordings | both | The pre-existing smoke capture says `has_peaks: true` but `peaks.json` is a 404 (console error on every open); the flag is set before the object exists |
| D21 | Info | Library | mobile | Long-press on a row simply opens the note; pull-to-refresh does nothing. Neither is claimed by the design; noted because a phone user tries both |

Fixed by #22 and **confirmed fixed** on 96311d0 (all were real on the previous bundle): the transcript parser (`start_ms`/`end_ms` were dropped, so every recording read *"This recording was made before timestamps were captured…"*, the *Timestamped/Cleaned* toggle never appeared and *Copy transcript* copied an empty string while saying "Copied" — `old-build/mobile-note-04b-download-audio.png`); the previous recording's waveform flashing on re-entering the record screen (one frame, 1 244 painted pixels under "Starting the microphone…"; now 0); the note screen showing the pre-append body even after a reload (`old-build/mobile-note-12-after-reload.png`); body-only search hits. *Filing-row dismissal* is in #22's title but *Open the note* still leaves the row (D4).

---

## Defects

### D1 — Download audio never works (High, both)

**Steps.** Library → *staging smoke* → the recording row is expanded by default → *Download audio*.
**Expected.** A `.webm` download and the inline "Downloaded".
**Actual.** "Downloading…" for ~150 ms, then *"Could not download — try again."*; no download event. Every attempt, both viewports, both builds.
**Evidence.** `mobile-note-04b-download-audio.png`, `desktop-note-04b-download-audio.png`. Feedback sequence: `Downloading… → Could not download — try again. → Download audio`. Console:
```
Access to fetch at 'https://chintan-content-dev-prod-338186951935.s3.us-west-2.amazonaws.com/tenants/…/audio.webm?X-Amz-…'
from origin 'https://vppillai.github.io' has been blocked by CORS policy: No 'Access-Control-Allow-Origin' header is present on the requested resource.
Failed to load resource: net::ERR_FAILED
```
**Why.** The bucket CORS is correct (`OPTIONS` with `Origin: https://vppillai.github.io` → `Access-Control-Allow-Origin: https://vppillai.github.io`, `GET, PUT, HEAD`). But the same URL was already loaded by `<audio preload="metadata">` as a *no-cors* media request (206, no `Origin` header, so S3 answered without ACAO and without `Vary: Origin`), and Chromium's HTTP cache serves that response to the later CORS `fetch()` in `DownloadButton`. Fix options: `crossOrigin="anonymous"` on the `<audio>`, `fetch(url, { cache: 'no-store' })`, or a cache-busting parameter on the download URL.

### D2 — Stale note on re-open, then a false conflict (High, both)

**Steps.** Library → open a note → type in the body → wait for "Saved" → *‹ Notes* → tap the same note again within 30 s → *Tags* → add a tag.
**Expected.** The note shows the text just saved; the tag saves.
**Actual.** The re-opened note shows the **body from before the edit** (screen: "v1 body"; server: "v1 body more words here."). The tag PATCH carries the **old version**, gets **409**, and the screen shows *"This note changed elsewhere — A voice capture or another device saved this note while you were editing. Nothing has been overwritten — choose which version to keep."* Nothing else touched the note. *Keep my edits* would send the stale body + tag and overwrite the user's own 30-second-old edit.
**Evidence.** `desktop-follow-01-after-tag.png`, `mobile-follow-01-after-tag.png`. Request log (identical on both viewports):
```
PATCH 200 {"version":2,"title":"Version check desktop","body":"v1 body more words here.","aliases":[],"tags":[]}
PATCH 409 {"version":2,"title":"Version check desktop","body":"v1 body","aliases":[],"tags":["vtag"]}
server afterwards: v3, tags [], body "v1 body more words here."
```
This is also why the first note pass saw "tags not persisting" (`server tags [] aliases []` for *Edit me*): that note had been edited, left and re-opened before tagging. Tagging a freshly opened note works (`Tag me`: two PATCHes 200, server keeps `qa-tag` / `nickname`).
**Why.** The PATCH result is never written into the `['notes', id]` query (nor invalidated); with the provider's `staleTime: 30_000` the second mount reads the cached pre-edit note, `useNoteEditor` resets from it (its guard is `loadedId === note.id`), and the version it sends is stale.

### D3 — Library rows are stale after editing (Medium, both)

**Steps.** Open a note, rename it (or edit the body), wait for "Saved", *‹ Notes*.
**Actual.** The row keeps the old title and snippet; no `GET /v1/notes` on return (`list refetched 0 time(s)`). Corrects itself after the 30 s staleTime plus a refetch trigger, or a reload.
**Evidence.** `mobile-notedetail-06-library-after-rename.png` (row "Rename me mobile" after the title was saved as "Renamed mobile"); `desktop-notedetail-06-library-after-rename.png`; first pass: saved title "Edit me desktop (edited)" + edited body, row read `Edit me desktop / Original body.`
**Why.** Same root as D2 — the mutation does not touch the list cache.

### D4 — "Filed" receipts pile up and never leave (Medium, both)

**Steps.** Record, Send; when the row says *Filed*, *Open the note*, go back. Record again. Reload.
**Expected (proposal).** The row is a receipt; *Open* or *Dismiss* removes it.
**Actual.** After *Open the note* + back the row is still there (`filing rows now 12` on mobile, `19` on desktop by the end). A reload brings back every row previously dismissed (the dismissed set is an in-memory `Set`). `RECENTLY_SETTLED_MS` is 10 min, so every capture appended in the last ten minutes renders a full-height card with two buttons; on the phone the first note row started ~1 300 px down.
**Evidence.** `mobile-off-08-back-online.png` (resume prompt + receipts fill the screen), `desktop-lib-01-top.png`, `mobile-recdetail-second-done.png`. One send's timeline: `942 ms [cleaning] … | 1 152 ms [uploaded] new row | 5 288 ms [appended] Filed`.

### D5 — In-app URLs drop the trailing slash and fall outside the PWA/SW scope (Medium, both)

**Observed URLs** after ordinary use: `https://vppillai.github.io/chintan/dev` (after Discard, after Archive, after the Home tab), `…/chintan/dev?q=flashing` (typing in search), `…/chintan/dev?tag=house`, `…/chintan/dev?view=archived`. `App.tsx` strips the slash from `basename`. The manifest says `scope: "/chintan/dev/"`, `start_url: "/chintan/dev/"`; the service worker is registered at `https://vppillai.github.io/chintan/dev/`.
**Measured consequence.** Offline, `goto('/chintan/dev?view=archived')` → `chrome-error://chromewebdata/` (`net::ERR_INTERNET_DISCONNECTED`), while `/chintan/dev/` offline renders the cached library (33–41 rows). The URL the app itself puts in the address bar after tapping *Archived* is the one that cannot be reloaded offline. Online, GitHub Pages 301s `/chintan/dev` → `/chintan/dev/`, which hides the problem.
**Unverified on a device:** whether Android Chrome shows the out-of-scope toolbar / iOS drops standalone mode for these pushState URLs. Worth a check on the owner's phone.
**Evidence.** `mobile-off-05b-no-trailing-slash-offline.png` (blank error page); URL lines in every `*.log.json`.

### D6 — Bulk-action bar is at the bottom of the list (Medium)

**Steps.** Library with 30+ notes → *Select*.
**Actual.** *Select all / n selected / Archive / Delete forever* renders after the last row (`position: static`; box at y = 6 441 px on a 915 px viewport, y = 8 091 px on desktop). Nothing above the fold changes except the checkboxes, so the user has to scroll to the end to find what *Select* did, then back up to tick rows.
**Evidence.** `mobile-lib-02b-select-top.png` (top of list in selection mode, no bar), `mobile-lib-08-selecting.png` (a 2-row list, where the bar happens to be visible).

### D7 — A note opened mid-filing never refreshes (Medium, both)

**Steps.** Note → *Record into this* → record → Stop → Send → back on the library, tap the note's own row while the filing row is still at *Uploaded*. Stay.
**Actual.** The server appends within ~1–4 s (note `v2`), but 10 s later the screen still shows the old body, *Recordings* is empty and exactly one `GET /v1/notes/{id}` was made. Reload fixes it on 96311d0 (on the old build even a reload rendered the IndexedDB copy with no server GET). A synthetic window `focus` also refetches.
**Evidence.** `desktop-follow-02-note-mid-filing.png`, `mobile-follow-02-note-mid-filing.png`; log: `10 s later on the note, no reload: body "Only paragraph."; recordings 0; GET note calls 1`.

### D8 — One server search request per keystroke (Medium, both)

Typing `flashing` at 60 ms/key produced 8 `GET /v1/search?q=` (`f, fl, fla, flas, flash, flashi, flashin, flashing`, 90–205 ms each) with responses landing out of order; 17 search requests in one short session. Local filtering already answers instantly; the server call wants a ~300 ms debounce. Results are correct (see *Works as intended*). Seen once on desktop and not reproduced: clearing the field left the previous query in place so the next word was appended (`flashingzebra7`).
**Evidence.** `mobile-lib-search.log.json`, `desktop-lib-search.log.json`.

### D9 — Unsaved settings lost on in-app navigation (Medium, both)

Set *Keep recordings for* to 7 → "Unsaved changes" → tap the *Home* tab → back to *You*: value 0, no dialog (`dialog shown: 0`). Only `beforeunload` is guarded.
**Evidence.** `mobile-you-03-dirty.png`; log `navigated away with unsaved settings → …/chintan/dev; dialog shown: 0 / back on You: retention now 0`.

### D10 — Routing is non-deterministic for the same speech (Medium, backend)

The same 8 s clip ("The gutter on the north side is leaking again near the downpipe. Ask the roofer whether the flashing was replaced last time or just sealed…"), recorded six times with the seeded *Roof repair* note present, was filed as: appended to *Roof repair* (2×); a **second note titled "Roof repair"** (`note_…2b046264`); a new note **"Gutter leak"** (and, in the desktop re-run, appended to that one); and once *needs a target* suggesting *Add to "Roof repair"*. Creating a duplicate-title note is the case worth guarding deterministically before trusting the LLM's answer.
**Evidence.** `GET /v1/notes` lists two `Roof repair` notes (19:54 and 19:59 UTC) and `Gutter leak` (19:59); `mobile-recdetail-second-done.png` shows the *Which note should this go in?* row.

### D11 — Offline recording is not sent on reconnect (Medium, both)

**Steps.** `setOffline(true)` → Record → Stop → Send.
**Actual.** *SOMETHING WENT WRONG — The upload did not finish. Your recording is safe on this device. Discard / Try again* (the offline banner is on screen at the same time). Leaving, the library offers *"You have an unsent recording from a moment ago — Discard / Send"*. After `setOffline(false)`: nothing for 60 s (no `POST /v1/captures`); tapping *Send* uploads and files it. The text edit made offline synced by itself ("Waiting to sync 1 change" → gone; body contains "Offline edit."). "Foreground retry on reconnect" holds for edits but not for the one artefact that exists nowhere else.
**Evidence.** `mobile-off-06-send-offline.png`, `mobile-off-07-library-after-offline-send.png`, `mobile-off-08-back-online.png`.

### D12 — No passkey enrolment on the managed-login page (Medium)

Screens seen, both viewports, first sign-in and after sign-out: (1) *Sign in to your account — Username — Next*, (2) *Enter your password — Password — Show password — Forgot your password? — Continue — Back*, (3) redirect to the app. No passkey, MFA or "set up a passkey" step anywhere. README §2 ("the managed-login page offers to register one after a password sign-in") and the app's copy ("where you can use a passkey once you have set one up") describe something the user cannot reach.
**Evidence.** `setup-mobile-signin-01-cognito-username.png`, `setup-mobile-signin-02-cognito-password.png`, `setup-mobile-signin-09-app-signed-in.png`; desktop equivalents.

### D13 — Theme "unsaved" state is misleading (Low, both)

Tap *Nocturne*: applied at once (`data-theme=nocturne`, dark ground), status "Unsaved changes". Reload without saving: Nocturne is selected and the status says "All changes saved" — no `PUT /v1/settings` ever went out; the server still has `theme: ink`. Either the theme is device-local (then it should not dirty the form) or a server setting (then the reload should show it unsaved).
**Evidence.** `mobile-you-02-nocturne.png`.

### D14 — Accessibility (Low, both)

axe-core 4.12 (`wcag2a/aa`, `wcag21aa`, best-practice): library, selection mode, You, capture — **0 violations**. Note screen — **1** (`page-has-heading-one`, moderate): the title is an `<input>`, no `h1`. Target sizes: `.note-row__checkbox` 20×20 px (every row in selection mode), `.player__range` 16 px tall. Tab order on the library is sensible (skip link → Select → search → chips → rows), 3 px solid focus ring; no target under 24 px on the other screens.

### D15 — Cancel leaves orphan chunk rows (Low, both)

Cancel a recording after 300 / 1 000 / 2 000 / 3 500 ms: no unsent prompt appears (good), but IndexedDB `chintan/captureChunks` goes 1 → 2 → 3 → 4 while `captures` stays 0. Nothing references or removes them. (On the old build one run surfaced a cancelled recording as *"You have an unsent recording"* in the library — seen once in the a11y run, not reproduced on 96311d0 in eight tries.)

### D16 — Tag chip outlives its notes (Low, both)

Two notes tagged `bulkmobile` → Select all → *Delete forever* (typed "delete"). The notes are gone (API confirms) but the chip stays and selecting it shows *No notes are tagged "bulkmobile".* until reload — `['tags']` is not invalidated by archive/purge.
**Evidence.** `mobile-lib-14-after-active-delete.png`.

### D17 — Uncached note offline: "Loading…" forever (Low, intermittent, mobile)

Offline, tapping a note never opened on this device showed *Loading…* with no way out (16 s+) in two of four runs; the other two showed the intended *Not on this device* copy.
**Evidence.** `mobile-off-05-note-uncached.png`; log: `never-opened note offline: "…Waiting to sync 1 change.\n\nLoading…"`.

### D18 — Fixed-height body textarea (Low, both)

`#note-body` is `rows=12` (350 px) with `overflow-y: auto`. A 3 200-character body scrolls inside the box (scrollHeight 1 655 px) while the page also scrolls — two nested scroll regions under a thumb. A two-line note reserves the same 350 px, so on the phone *Recordings* starts below the fold and its row is cut by the sticky action bar.
**Evidence.** `mobile-note-08-long-body.png`, `mobile-note-01-open.png`.

### D19 — Skip link shadow visible top-left (Low, desktop)

`.skip-link` is `position: fixed; top: 12px` translated −70 px; its box ends at y = −12 but its shadow bleeds in as a rounded grey sliver at (0–160, 0–10) on every desktop screenshot.
**Evidence.** `desktop-skiplink-corner.png` (clip), any `desktop-*.png`.

### D20 — `has_peaks` true, `peaks.json` missing (Low)

Capture `c_18d23519976a1f60` (the account's pre-existing smoke recording): `has_peaks: true`, `has_segments: true`; `segments.json` and `clean.txt` exist, `peaks.json` is `NoSuchKey`. Every open of that row logs a 404 and falls back to the plain slider. Captures made through the app upload peaks fine; the flag should follow the object.

---

## Works as intended (covered, no defect)

- **Sign-in / sign-out.** Cognito managed login (username → Next → password → Continue) returns with `chintan.tokens.v2` (`idToken, accessToken, refreshToken, expiresAt, tokenType`) in `localStorage`; the `?code=` is stripped from the URL. *Sign out* asks "Sign out? Your notes stay on the server. Nothing is waiting to sync from this device.", clears tokens, goes through `/logout` (302) to the signed-out screen; signing back in asks for the password again. Both viewports.
- **Library.** 26 → 50 notes render in one page; the API returned every item without a cursor, so `Load more` never appeared (pagination could not be exercised). `<main>` is the scroll container; the last row sits fully above the tab bar (mobile: row bottom 799 px, bar top 831 px) — the record button overlaps nothing. Day grouping, two-line snippets, tag pills, recording counts as in the proposal.
- **Search.** "flashing" (body-only) → found and highlighted (`matched_in: body`); "zebra7" (body-only, beyond the snippet) → *Bike service*; "Bike" (title) → 1; "xyzzyq" → *Nothing matches "xyzzyq" in the notes searched.* Query lives in the URL and survives reload; clearing the field clears the query.
- **Tag chips / Archived.** `house` → 5 rows, all tagged house, `?tag=house`; tag + search combine; *Archived · n* → `?view=archived`, *Nothing is archived.* when empty; *All* clears.
- **Bulk actions.** Select → Select all → *Archive* ("Archive 2 notes?") → two `DELETE /v1/notes/{id}` 204, rows leave, chip reads *Archived · 2*, selection exits. Archive view shows *Deletes in 30 days*. *Restore* (`POST …/restore` 200). *Delete forever*: button disabled until "delete" is typed (case-insensitive) → `POST /v1/notes/purge` 200. From the active list the same gate archives + purges. API confirms 0 left.
- **Record flow.** Record → *Starting the microphone…* → *Recording* (timer, live waveform, hint, no tab bar) → Pause/Resume → Stop → *Ready to send* → Send → *SENDING… → SENT* → library in ~1 s with the filing row at *Uploaded*; stages advance (*Transcribing complete / Filing in progress*); **Send → Filed in 5.3–5.4 s** for a 4–5 s clip (poll every 4 s; the pipeline itself takes ~4 s). *Open the note* lands on the note with the new paragraph already there. Recording a second time without reload works; 0 console errors. Direct `/capture` (PWA shortcut) starts recording at once. Discard returns to the library.
- **Note screen (96311d0).** Newest recording expanded by default; artifacts fetched (`download?kind=audio|segments|peaks|clean`, then S3); transcript shows timestamped lines with the *Timestamped / Cleaned* toggle; the playing line highlights (`aria-current`); tapping a line seeks (`currentTime 3.04`) and plays; *Copy this transcript* puts the 148-char transcript on the clipboard and says "Copied". Play/Pause work (`paused:false, currentTime 2.39` after 2.5 s; label flips to "Pause recording from…").
- **Editing.** The 1.2 s debounce holds: a 58-char sentence at 70 ms/key → **1 PATCH**; two bursts with a 1.6 s gap → +2; title edit + blur → +1 (4 total, all 200). Indicator cycles *Unsaved changes → Saving… → Saved*. Server body/title/version match.
- **Tags / aliases** on a freshly opened note: Enter adds a chip, PATCH carries `tags`/`aliases`, server keeps them, meta shows the tag, library gets a `qa-tag` chip; chips are named "Remove qa-tag".
- **Share.** *Copy note* → clipboard `"<title>\n\n<body>"`; *Download note* → `<title>.md` with `# title\n\nbody\n`.
- **Record into this.** Pill reads *Into <this note>*; the chooser lists *New note* + 20 recent notes with the current one pressed; the capture lands in that note (server v3, 1 capture, meta *1 recording · 0:05*).
- **Archive / restore / purge from the note.** Dialog copy as designed; archived note shows "This note is archived. Deletes in 30 days.", *Record into this* hidden, *Restore* / *Delete forever* present; purge gate requires the title (case-insensitive); after purge → archive view; the purged URL → *Note not found*. The archived note stays editable (title/body enabled) — a choice, noted.
- **Settings.** Appearance (3, applied live), Cleanup (2), Keep recordings for (number + live explanation), Daily spending cap read-only ("$5 a day … set in the instance configuration"), Session/Sign out, footnote `App version v0.5.0+14 (96311d0)`. *Discard* confirms and resets.
- **Offline.** Banner *Offline — showing saved notes.*; reload at `/chintan/dev/` serves the shell and the cached rows with *Saved on this device. Recordings and transcripts need a connection.*; a cached note opens with *Edits are kept here and sent when you reconnect*; an offline body edit → *Saved on this device — will sync* / *Waiting to sync 1 change.* and it synced on reconnect. Service worker active at scope `/chintan/dev/`.
- **Capture screen a11y.** 0 axe violations; visually-hidden h1, live status region, labelled waveform.

## Performance

Cold context (no service worker, empty cache), signed in. Fast 3G = 562.5 ms RTT, 1.6 Mb/s down / 750 kb/s up ×0.9, CPU ×4 via CDP. "Interactive" = first `.note-row` visible (library) or `.capture__state` reading *Recording* (capture), measured from navigation start.

| load | interactive (mobile / desktop) | FCP | DCL | requests after the bundle |
|---|---|---|---|---|
| `/` Fast 3G, ×4 CPU | **2 962 / 2 946 ms** | 2 040 ms | 1 994 ms | `/v1/captures` 700 ms, `/v1/notes?state=active` 750 ms, `/v1/notes?state=archived` 659 ms, `/v1/tags` 679 ms |
| `/capture` Fast 3G, ×4 CPU | **3 058 / 3 012 ms** | 2 028 ms | 1 983 ms | `/v1/notes` 742 ms (target chooser) |
| `/` unthrottled | 355 / 364 ms | 104 ms | 77 ms | |
| `/capture` unthrottled | 247 / 137 ms | 92 ms | 70 ms | |

**Bundle on a cold load:** `index-B-4bjH0K.js` **136 KB gzipped / 433 KB raw** (1 360 ms on Fast 3G — the single largest item), `index-WlLI1pOG.css` 7 KB / 42 KB, `registerSW.js` < 1 KB; 144 KB transferred in total, 7 requests for the library, 4 for `/capture`. One JS chunk, no splitting: the capture shortcut downloads the library, note and settings code before the microphone opens. The library also spends a full `GET /v1/notes?state=archived` on every load only to print the number on the *Archived* chip. JS heap after load ~5 MB. `LCP` entries were empty in headless Chromium; FCP/DCL are reported instead.

The pipeline: upload → `appended_at` in **~4 s** for an 8 s clip; the UI adds up to one 4 s poll interval.

## Not covered / limits

- Pagination (`Load more`): the list endpoint returned all 50 notes without a cursor.
- Real-device behaviour for D5 (installed-PWA scope), native pull-to-refresh, the iOS share sheet — headless Chromium only.
- Speech was synthetic (espeak-ng); routing quality (D10) was observed, not characterised.
- No load beyond one user; no spend-cap path (`$5/day` cap not reached).

## Scripts and artefacts

- `frontend/qa/playwright.config.ts`, `frontend/qa/tests/*.spec.ts` (`10-library`, `20-record`, `21-record-detail`, `30-note`, `31-note-detail`, `32-followups`, `40-settings`, `50-offline`, `60-a11y`, `70-perf`, `90-signout`, `auth.setup`), `frontend/qa/tests/{helpers,api}.ts`, `frontend/qa/make-speech.sh`. Run from `frontend/` with `QA_USER=… QA_PASS=… npx playwright test -c qa/playwright.config.ts [--project=mobile|desktop]` under Node (the Playwright runner does not work under Bun; orb has Node in `~/temp/node`). They are outside vitest's `src/**` include and `playwright.config.ts`'s `./e2e` testDir, and pass the repo's eslint config; run artefacts (`results/`, `shots/`, `state/`, `speech.wav`) are git-ignored.
- Screenshots referenced above are in this directory (`mobile-*`, `desktop-*`, `setup-*`); `old-build/` holds the pre-#22 evidence. Full request/console logs: `~/temp/chintan-qa/frontend/qa/shots/*.log.json` on orb.
