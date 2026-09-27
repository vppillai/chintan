# Live QA — owner feedback 2026-09-27 (PRs #135, #136, #137)

**Target** https://vppillai.github.io/chintan/dev/ on 2026-09-27 19:46–19:51 UTC.
**Build under test** `assets/index-C2e1oSzz.js`, `routes-BMHhggMG.js`, `SettingsScreen-BGGl9oVs.js`, `AboutScreen-Loe2EbEb.js`, `TalkScreen-CV8tHXEF.js`: every string the three PRs add is present ("kept in the Archive for 30 days", "Record into this note", "Tap Record to start your first note", "Other apps, and filing into one note", "X-Chintan-Note-Id", "PTT: hold to talk"); none of the removed copy ("hold it", "hold the", `HoldOverlay`) remains. Local checkout `bae2179` includes a34eaa9 (#136), fe2227c (#137), 678c51d (#135).
**Tooling** Playwright (chromium, headless) on orb via `~/r3/live/qaf-lib.js` over `qaw1-lib.js`/`qaw1-base.js`; Pixel 8 Pro mobile (412×915, touch via CDP) and desktop 1280×800 contexts, one at a time; every script under `timeout 240 node …`. Scripts: `~/r3/live/qaf-{setup,A1,A2,A3,A5,B,C,cleanup}.js`; logs/results `~/r3/live/qaw1-qaf*-{mobile,desktop}-ink{.log,-results.json,-reqs.json}`; screenshots `~/r3/live/qaf-*.png` (33 files, listed at the end).
**Data** 16 notes created, all titled `QA F …`; 11 archived over the API for the bulk test. No other note touched, no Select all used. End state: `DELETE /v1/notes/{id}` on all 16 (4×204, 12×404 already purged in A5) then `POST /v1/notes/purge` → `purged 4, not_found 12`; a paginated scan of active and archived lists finds no `QA F` note. No AWS writes; tokens refreshed once with `~/refresh-tokens.sh` (they had expired).

## Summary

| Area | Verdict | Notes |
|---|---|---|
| A. Delete asks first (row ⋮, swipe tray, note header ⋮, selection bar; Escape / Enter-on-Cancel; Undo; Archive "Delete forever" + >10 hold) | **PASS** (28/28 checks) | Identical plain confirm in all four places; nothing archived on Escape or Enter; Undo restores; Archive confirms unchanged; the 11-note hold rejects a tap and purges on a 1.4 s hold. |
| B. PTT scope (tab-bar disc Record/mic, no hold behaviour; tap opens recorder; "Record into this note"; /talk keeps PTT; manifest shortcut; About/You copy) | **PASS** (13/13 checks) with one observation | Holding the disc 1.5 s does nothing — no overlay, no recording, no upload, no state. **Observation B-1:** in headless Chromium the release after the long touch hold was delivered as a `click`, so the recorder opened exactly as after a tap (same on desktop with a mouse, where mousedown+mouseup is a click by definition). Nothing was uploaded. Whether Android Chrome suppresses that click after a long press cannot be settled in this harness — see details. |
| C. Devices fold (one lead, five closed folds, no address in open text, fifth fold content + link; About sentence, no Pebble) | **PASS** (7/7 checks) | Five `<details>` folds, all closed, chevrons on 44 px summaries; addresses, header sentence and README link (`target=_blank`, `rel="noopener noreferrer"`, `https://github.com/vppillai/chintan#connect-a-device`) only inside the fifth fold; About has exactly one device sentence, linking to `/settings`, and never names Pebble. |
| D. Console (Home, note, /talk, You, About) | **PASS for #135–#137**; one pre-existing app error on You | No console error attributable to these PRs on Home (mobile + desktop), note, archive, /talk, About. Two errors present on prod that predate these PRs: **D-1** `GET /v1/push/key` → 404 logs `Failed to load resource` on every You load (Notifications card, #133); **D-2** GitHub Pages' 404.html fallback on every direct deep-link load (hosting artefact, recorded in qa-r4-live). |

## A — Delete asks first (details)

Scripts `qaf-A1.js` (desktop), `qaf-A2.js`, `qaf-A3.js`, `qaf-A5.js` (mobile). Every dialog was read from the DOM: `role="dialog" aria-modal="true"`, `.dialog__title`, `.dialog__body`, the buttons and `document.activeElement`.

| Surface | Dialog seen | Escape | Enter (focus on Cancel) | Confirm | Undo |
|---|---|---|---|---|---|
| Home row ⋮ (desktop, hover-revealed "More") | `Delete “QA F row menu”?` / `It is kept in the Archive for 30 days, then gone for good.` / Cancel + destructive Delete / 0 inputs / focus Cancel | closed, `archived=false`, row stays, focus back on **More** | closed, `archived=false`, row stays, no toast | `archived=true`, row gone, toast `Deleted · kept in Archive for 30 days` + **Undo** | `archived=false`, row back |
| Swipe tray (mobile, tray Pin·Delete after a 190 px left swipe) | same wording | closed, nothing archived | — | archived + Undo toast (left archived for A5) | — |
| Note header ⋮ "Note actions" (mobile) | `Delete “QA F header”?` same body | closed, still on the note, `archived=false`, focus back on **Note actions** | closed, still on the note, `archived=false` | archived, lands on Home (`/chintan/dev/`), Undo toast | restored, row back on Home |
| Selection bar, 1 selected (long press 750 ms) | `Delete “QA F sel 1”?` (names the one note) | closed, still selecting, nothing archived | — | — | — |
| Selection bar, 2 selected | `Delete 2 notes?` / `They are kept in the Archive for 30 days, then gone for good.` | — | closed, both `archived=false`, still `2 selected` | both archived, toast `2 notes deleted · kept in Archive for 30 days` + Undo | both restored, rows back |
| Archive row ⋮ "Delete forever" | `Delete this note forever?` / “QA F swipe” … destroyed … / Cancel + destructive **Delete forever** (plain tap, no hold class) | — | — | note → 404, row gone | n/a |
| Archive selection bar, 11 selected (one checkbox at a time) | `Delete 11 notes forever?` / confirm is `Hold to delete 11 notes` with `.dialog__action--hold` | — | — | a tap leaves the dialog open and bulk 01 alive (200); touch held 1.4 s (`data-holding=true` at 0.5 s) closes it and all 11 → 404, 0 rows left | n/a |

Screenshots: `qaf-A1-02-rowmenu-dialog.png`, `qaf-A1-03-after-confirm-toast.png`, `qaf-A2-01-tray-open.png`, `qaf-A2-02-tray-dialog.png`, `qaf-A2-04-bar-dialog-one.png`, `qaf-A2-05-bar-dialog-two.png`, `qaf-A2-06-bar-confirmed-toast.png`, `qaf-A3-02-header-dialog.png`, `qaf-A3-03-after-confirm-home-toast.png`, `qaf-A5-02-forever-dialog.png`, `qaf-A5-04-hold-dialog.png`, `qaf-A5-05-mid-hold.png`, `qaf-A5-06-after-hold.png`.

## B — PTT scope (details)

Script `qaf-B.js`: mobile context with a fake microphone granted (so a recording *would* have started if anything still held), then a desktop context.

- **Home disc**: `aria-label="Record"`, SVG path is the `mic` glyph, caption text `Record` (rendered upper-case by CSS `text-transform: uppercase`, as #137's "reads RECORD" says). On a note: `aria-label="Record into this note"`, mic glyph.
- **1.5 s touch hold on the disc** (CDP `touchStart`, 1.6 s, `touchEnd`): at 0.8 s the URL is unchanged, no element matching `[class*=hold]`, `[class*=overlay]`, `.recording-indicator`, `[data-holding]` or `.capture` exists, body text has no "Recording/Elapsed". Across the whole hold and after release: **zero** non-GET requests to captures/recordings/inbox/upload. (`qaf-B-02-disc-mid-hold.png`, `qaf-B-03-disc-after-hold.png`)
- **Observation B-1 — release after the long hold acted as a tap.** In-page event log on the button: `pointerdown@130 touchstart@131 … pointerup@1744 touchend@1745 click@1752`. Chromium delivered a `click`, the button did what a tap does — `navigate('/capture')` — and the recorder opened and began recording locally (state `RECORDING`; nothing uploaded, the store only uploads on Send; the script pressed Cancel). Same with a 1.5 s mouse hold on desktop (`qaf-B2-01-desktop-mid-hold.png`; mousedown+mouseup on one element is a click). Interpretation: this is the ordinary semantics of the plain `<button>` #137 asked for — the hold itself is inert and there is no PTT — but "release after ~1.5 s changes nothing" is *not* what headless Chromium does. On Android, `GestureDetector` normally drops the single-tap after a long press (`useLongPress` in this codebase exists precisely because the row's click still followed a long press in its tests, so treat that as unsettled); the acceptance text "release: no state change" therefore needs a real Pixel to confirm. If the owner wants a long press on the disc to be inert on release, the fix would be in `frontend/src/components/RecordButton.tsx` (swallow the click that follows a press longer than `LONG_PRESS_MS`, as `hooks/useLongPress.ts` does for rows). Not a regression: before #137 the same press started a PTT recording.
- **Tap** on the disc → `/chintan/dev/capture`, `.capture` present, state `RECORDING` (`qaf-B-04-after-tap-capture.png`).
- **/talk?note=…**: `<h1>` "PTT", button `aria-label="PTT: hold to talk, release to send"`, SVG path is the `ptt` walkie-talkie glyph, hint "Hold, speak, release to send. Slide away to cancel." A 1 s touch hold: `.talk[data-phase=holding]`, `data-holding=true`, label "Release to send", timer `aria-label="Elapsed 00:01"`; Escape cancels back to `idle`. A 150 ms press: phase `hint`, status "Too short — hold to talk". No upload from either (the store discards on cancel/too-short). (`qaf-B-06-talk.png`, `qaf-B-07-talk-holding.png`, `qaf-B-08-talk-too-short.png`)
- **Manifest** `manifest.webmanifest` shortcut `{name: "PTT", description: "Hold to talk, release to send", url: "/chintan/dev/talk"}` — still present and pointing at /talk.
- **Copy**: About intro reads "Tap Record and talk … or open PTT from You or the home-screen shortcut and hold to talk"; the Record step reads "Tap Record and speak"; no sentence on About or You tells the reader to hold the tab-bar mic/disc/button (regex over `body.innerText`). You's PTT row: "PTT — Hold to talk, release to send" (that is /talk's row, as intended).

## C — Devices fold (details)

Script `qaf-C.js` (mobile). You → **Devices & shortcuts** card:

- Lead: `A key lets a watch, a phone shortcut or any other app drop a recording straight into your notes.`; under "Connect a device" exactly one paragraph outside the folds: `Everything posts to the same address with your key in one header, and is transcribed and filed exactly as a recording made here.`
- Five `details.recipe`, all `open=false`, each summary 44 px with `.recipe__chevron`: From a terminal (curl) · iPhone or Apple Watch (Shortcuts) · Android (HTTP Shortcuts app) · Pebble Index 01 ring · Other apps, and filing into one note. (`qaf-C-01-devices-card-closed.png`)
- Card `innerText` with all folds closed contains none of `execute-api`, `/v1/inbox`, `X-Chintan-Note-Id`, `Authorization`, `Bearer`, `github.com`.
- Fifth fold opened by its summary: both `…/v1/inbox/audio` and `…/v1/inbox/text` addresses, the sentence "add the header X-Chintan-Note-Id with the note’s id", and the link "README on github.com" → `https://github.com/vppillai/chintan#connect-a-device`, `target="_blank"`, `rel="noopener noreferrer"`; the other four folds stay closed. (`qaf-C-02-fifth-fold-open.png`)
- About: exactly one sentence containing "device key" — "Other devices can post recordings and text into your notes with a device key — a watch, a ring, a phone shortcut — and each is filed the same way." — with the `Devices & shortcuts` link to `/chintan/dev/settings`; "Pebble" absent; no address/header/webhook text. (The generic word "ring" appears in that sentence; the Pebble product is not named.) (`qaf-C-03-about.png`)

## D — Console (details)

Collected per screen via `page.on('console'|'pageerror')` plus any API response ≥ 400.

| Screen | Profile | console.error / pageerror / API ≥ 400 |
|---|---|---|
| Home (incl. dialogs, toast, selection) | desktop, mobile | none |
| Note `/notes/{id}` | mobile | none in-app; one `Failed to load resource: 404` from the deep-link load (D-2) |
| Archive `/?view=archived` | mobile | none |
| `/talk` | mobile | none |
| About `/about` | mobile | none in-app (D-2 on deep-link load) |
| You `/settings` | mobile | **D-1** `GET /v1/push/key` → 404 `{"title":"Not Found","detail":"notifications are not configured on this instance"}` → `console.error: Failed to load resource: the server responded with a status of 404 ()` (plus D-2 on deep-link load) |

- **D-1** is the Notifications card (`frontend/src/features/settings/NotificationsCard.tsx` → `usePushKey`, `api/queries/push.ts`, `endpoints.ts:419`) reading the designed 404 as "not set up here yet" — from #133 (R5-RC-D2), merged before #135–#137. Behaviour is intended, but the browser still logs a console error on every You load; if a quiet console matters, the key fetch could be skipped until the instance advertises push, or the 404 treated as data (not an error) — outside this round's PRs.
- **D-2**: `https://vppillai.github.io/chintan/dev/<route>` answers HTTP 404 and serves `404.html` before the SPA boots; in-app navigation never logs it. Recorded as pre-existing in `docs/reviews/2026-09-24/qa-r4-live.md`.
- Warnings only (not errors): Playwright reports `requestfailed … net::ERR_ABORTED` for each `DELETE /v1/notes/{id}` and `/permanent` that returned **204** — the request log shows the 204 and the state changed; a Chromium/Playwright quirk with body-less responses, not an app fault.

## Harness notes

Four checks were first written too strictly and re-specified before the final run (all in `qaf-B.js`/`qaf-C.js`, results above are from the final runs): caption compared with `innerText` (CSS upper-cases it), an "overlay" selector that matched the pull-to-refresh element's own `data-phase`, the held `/talk` label expected on the button instead of the timer, and "ring" as a proxy for Pebble. No app behaviour changed between runs.

## Screenshots (`ubuntu@orb:~/r3/live/`)

qaf-A1-01-home · qaf-A1-02-rowmenu-dialog · qaf-A1-03-after-confirm-toast · qaf-A1-04-after-undo · qaf-A2-01-tray-open · qaf-A2-02-tray-dialog · qaf-A2-03-tray-confirmed-toast · qaf-A2-04-bar-dialog-one · qaf-A2-05-bar-dialog-two · qaf-A2-06-bar-confirmed-toast · qaf-A2-07-bar-after-undo · qaf-A3-01-note · qaf-A3-02-header-dialog · qaf-A3-03-after-confirm-home-toast · qaf-A3-04-after-undo · qaf-A5-01-archive · qaf-A5-02-forever-dialog · qaf-A5-03-eleven-selected · qaf-A5-04-hold-dialog · qaf-A5-05-mid-hold · qaf-A5-06-after-hold · qaf-B-01-home-disc · qaf-B-02-disc-mid-hold · qaf-B-03-disc-after-hold · qaf-B-04-after-tap-capture · qaf-B-05-note-disc · qaf-B-06-talk · qaf-B-07-talk-holding · qaf-B-08-talk-too-short · qaf-B2-01-desktop-mid-hold · qaf-C-01-devices-card-closed · qaf-C-02-fifth-fold-open · qaf-C-03-about (all `.png`).
