# Live QA — round 6 wave 1, 2026-09-29 (PRs #147, #148, #149, #150, #151, #153, #154, #156; #146 pending)

**Target** https://vppillai.github.io/chintan/dev/ and the dev API, 2026-09-29 09:10–09:22 UTC, test tenant only (`claude-test@example.com`).
**Build under test** Frontend: the round-6 wave-1 source (#147 → #150, #153, #154, #156; nothing under `frontend/` changed after #150 and the #142 bump). Two frontend deploys ran during the pass — `index-Bn6z4oDe.js` / `routes-IiAaxJ1w.js` / `SettingsScreen-Cq14GyFC.js` (Deploy Frontend for cf46696, live 09:07:43 UTC) served flows A–H (09:11–09:19), and `index-DC9OciGK.js` / `routes-CpUO7Jm7.js` (for e296fb5, live 09:20:18) served the re-runs of I, G, A-Nocturne and N (09:20–09:22). #157 and #155 touch `backend/` and `docs/` only, so both bundles are the same frontend source; every string the wave adds is in the first (`Copy note id`, `Made a sub-item`, `Your list is now the split version.`, `Use this list`, `--keyboard-inset`, `--tab-swipe-x`, `data-nest-preview`, `Other apps, and filing into a specific note`), `OneNote` is in none. Backend: #151 (Deploy Backend 36544200971, live 08:46) plus #157 (36546231457, live 09:06:51) for the routing probe at 09:10:50; #155 (36547724748) went live at 09:19:15, after the append in D2 (09:15:07). Local checkout `1fa70bf` at the start, `e296fb5` at the end.
**Tooling** Playwright (chromium, headless) on orb via `~/r3/live/qar6-lib.js` over `qaw1-lib.js`/`qaw1-base.js`: a 390×844 touch context (Pixel 7 descriptor, touch via CDP `Input.dispatchTouchEvent`) and a 1280×800 mouse context, Ink & Paper throughout and Nocturne for the swipe (A), the nest preview, Details and Split up (N); one flow per `timeout … node` call. Scripts `~/r3/live/qar6-{setup,probe,A,B,C,C2,C3,C4,D1,D2,D3,E,F,G,H,I,N,cog,cleanup}.js`; logs and results `~/r3/live/qaw1-r6*-{mobile,desktop}-{ink,nocturne}{.log,-results.json,-reqs.json}`; screenshots `~/r3/live/qar6-*.png` (64, listed at the end). The keyboard (B) is emulated inside the page: `window.visualViewport` is replaced before the app boots by a stand-in whose `height` shrinks by 300 px and which fires `resize`, so `useKeyboardInset` itself computes and writes the inset.
**Data** 9 notes, all titled `QA-R6 …`, created over `POST /v1/notes` (one archived over the API); one device `QA-R6 probe` (`expires_in_days: 1`); four `POST /v1/inbox/text` captures with its key (two into `QA-R6 recordings` by `note_id`, one into `QA-R6 checklist` by `note_id`, one with no target for the routing probe). No other note or device touched; no owner data read; no AWS writes. Tokens refreshed once with `~/refresh-tokens.sh` before the pass. End state: `DELETE /v1/devices/{id}` → 204 and the key then answers 401 on the inbox; `DELETE /v1/notes/{id}` → 204 on all nine, `POST /v1/notes/purge` → `purged 9`; paginated scans of the active and archived lists find no `QA-R6` note and `GET /v1/devices` no `QA-R6` device.

## Summary

| # | Area | Verdict | Notes |
|---|---|---|---|
| 1 | Swipe between segments (#156) | **PASS** (21/21 mobile ×2 themes, 3/3 desktop ×2) | Left → Cleaned → Recordings, right back; the URL and the strip follow; vertical and diagonal drags scroll only; 60 px at rest snaps back; edges ignored; a mouse drag does nothing (by design), arrows and End work. |
| 2 | Caret above the keyboard (#153) | **PASS** (6/6) | The hook writes `--keyboard-inset: 300px` from the visualViewport resize; typing at the end of an 80-paragraph note leaves the caret line at 435 px against a keyboard top of 451 px; Enter in the 40th item lands the new item at 373 px; the Details sheet's bottom sits at 451 px exactly. |
| 3 | Details sheet as a nested scroller (#147) | **PASS** (5/5 at 844 px, 5/5 at 560 px, +C4x) | Finger drags scroll the sheet to its end with the note at 0 throughout and pull-to-refresh `idle`; a drag past the end moves nothing; a drag down scrolls the sheet back and arms no pull; with the note scrolled to 900 the sheet's drags leave it at 900. The pull still arms on the note itself. |
| 4 | Checklist nesting (#154, #148) | **PASS** (14/14 mobile, 9/9 desktop, 3/3 append) | Tab → `data-depth=1` / "Sub-item 2" / 24 px / saved as two spaces; Shift+Tab back; the first row's Tab indents nothing and moves on; a 34 px grip drag right shows `data-nest-preview=1` then nests, no menu; reload keeps it; drag left un-nests; ticks and depth survive an inbox append. |
| 5 | Split up as the editor (#154) | **PASS** (11/11) | Details' checklist switch → Items · Split up; Generate → 4 rows in 3 s as grips, boxes, fields, add row; the first tick adopts (one PATCH, `- [x] buy milk`, toast "Your list is now the split version." + Undo); Undo restores the old body and Use this list; a keystroke adopts too, saved at once. |
| 6 | No note multi-select (#149) | **PASS** (8/8 mobile, 2/2 desktop) | 750 ms holds on Home and Archive rows select nothing (no bar, no checkbox, no "N selected", no Select in either ⋮); a 750 ms hold on a recording row still gives the bar with "1 selected", Select all → "2 selected", Cancel. |
| 7 | Note id in Details; Devices copy (#150) | **PASS** (8/8 mobile, 8/8 desktop) | `Note id` is the last Details section; Copy note id → button "Copied", `role=status` "Copied", `navigator.clipboard.readText()` returns the id, label settles after 2.5 s; the fifth recipe reads "Other apps, and filing into a specific note", its fold says "one specific note" and names `X-Chintan-Note-Id`; no "OneNote" on You. |
| 8 | Delete from ⋮ asks first | **PASS** (8/8 mobile, 4/4 desktop) | Row ⋮ and header ⋮: `Delete “QA-R6 delete me”?` / "It is kept in the Archive for 30 days, then gone for good." / Cancel focused + destructive Delete; Escape and Enter-on-Cancel archive nothing; confirm archives with `Deleted · kept in Archive for 30 days` + Undo; Undo restores. |
| 9 | Console | **PASS** | In-app navigation (Home → note: Text, Cleaned, Recordings, Details → Home → checklist: Items, Split up → Archive) logs no console error, page error or API ≥ 400 on mobile or desktop. Direct loads of `/notes/{id}` log the Pages `404.html` fallback (D-2, pre-existing); You logs the known `GET /v1/push/key` 404 (D-1). Nothing else. |
| R | Routing probe (#151, name-first) | **PASS** | `POST /v1/inbox/text` with no target, "QA-R6 shopping list add candles and matches": `transcribed` → `routing` → `appended` in 3 s into `QA-R6 shopping list`; the body gained `- [ ] Candles` and `- [ ] Matches`; no new note. |
| C | Cognito managed-login logo (#146) | **not yet deployed** | #146 was still open at 09:19 UTC with green checks; the last three Deploy Backend runs are #151, #157 and #155. See §C. |

No blocker, no bug. Findings F1–F4 below are observations and pre-existing items.

## 1 — Swipe between segments (details)

Script `qar6-A.js`, mobile Ink and Nocturne (`THEME=nocturne`), then desktop. Drags are twelve CDP touch moves (the e2e's `drag`); `rest` pauses 150 ms before the last move so no flick is read. Note `QA-R6 long note` (80 paragraphs).

- A1: a left drag of 180 px on the panel — mid-drag `.note-views[data-swiping]` with `--tab-swipe-x: -156px` (`qar6-A-mobile-ink-02-mid-swipe-left.png`) — lands on **Cleaned**; URL `?tab=cleaned`; nothing focused a textarea; exactly one `aria-selected="true"` tab, it labels the tabpanel (`aria-labelledby`), it alone has `tabindex=0`, `data-swiping` gone.
- A2–A4: left again → **Recordings (0)** (`?tab=recordings`); left on Recordings stays; right → Cleaned → Text with the `tab` param dropped; right on Text stays.
- A5: 60 px left at rest snaps back. A6: a vertical drag (8 px across, 300 down) scrolls `.app__main` 0 → 296 and switches nothing; A6b: a diagonal drag with |dy| > |dx| scrolls (346 → 599), no switch.
- A7/A7b: a swipe on the tab strip itself steps both ways. A8/A8b: drags starting at x = 10 and x = 384 (inside the 24 px edge) leave Cleaned in place; A8c: the same drag from x = 60 → Text.
- A9–A11: a tab tap still selects after swipes; on Recordings with no rows a right drag → Cleaned; a reload keeps `?tab=cleaned`.
- Desktop: a 350 px mouse drag across the panel switches nothing (a mouse is excluded by design; the tabs are the path); ArrowRight → Cleaned with focus on the tab; End → Recordings.

Screenshots: `qar6-A-mobile-{ink,nocturne}-01-note-text`, `-02-mid-swipe-left` (the panel a third of the way across, the strip unchanged until release), `-03-after-left-cleaned`, `-04-after-vertical-scroll`, `-05-cleaned-tab`; `qar6-A-desktop-{ink,nocturne}-01-desktop-recordings`.

## 2 — Caret above the keyboard (details)

Script `qar6-B.js`, mobile with the visualViewport stand-in.

- B1: `__qaKeyboard(300)` (height 844 → 544, `resize`) → `<html style="--keyboard-inset: 300px">` within a frame; B2: `.app__main` computes `padding-block-end: 300px`, `scroll-padding-block-end: 321px`; B4: `__qaKeyboard(0)` → `0px`.
- B3: caret at the end of the 80-paragraph body, `.app__main` scrolled so the end is 700 px below the fold, then `End`, `x`, Enter, "QA-R6 the last line": the textarea's bottom went 1127 → 448 px, so the caret line (bottom − 13 px padding) is at 435 px against a keyboard top of 751 − 300 = 451 px. (`qar6-B-mobile-ink-01-typing-caret-above-keyboard.png` has a grey band drawn where the keyboard would be.)
- B5: the 40-item `QA-R6 long checklist`, Enter in item 40 then "QA-R6 new one": the new row (focus "Item 41") sits with its bottom at 373 px, above 451.
- B6: Details opened with the keyboard up: the sheet's bottom is at 451 px — lifted by the inset once, not twice.

## 3 — Details sheet as a nested scroller (details)

Script `qar6-C.js` at 390×844 and 390×560 (the e2e's height, where the sheet has 481 px to scroll); the note at its top, Details open. Sheet metrics: `scrollHeight` 816, `clientHeight` 505 / 335, `overflow-y: auto`, `overscroll-behavior: contain`.

- C1: up to ten 280 px finger drags up on the sheet until its `scrollTop` stops growing: it reaches its end (311 / 481) while `.app__main.scrollTop` is 0 at every sampled moment and `.pull-refresh[data-phase]` is `idle` on every drag. (`qar6-C{844,560}-mobile-ink-01-sheet-at-end.png`)
- C2: one more 250 px drag up past the end: the note stays at 0, the sheet at its end.
- C3: a 150 px drag down on the scrolled sheet: mid-drag phase `idle`, the sheet scrolls back (311 → 170 / 481 → 342), the note stays at 0, Close details still on screen.
- C4x (`qar6-C2.js`): with Details open and the note then scrolled to 900, a 250 px drag up and a 200 px drag down on the sheet scroll the sheet (0 → 291 → 106) and leave the note at 900.
- C5 (control): with the sheet closed and the note at its top, a 220 px pull on the note reads `armed` mid-pull — the pull itself still works.

The script's own `C4` check first reported "note 900 → 0" at both heights; C2, C3 and C4 (`qar6-C{2,3,4}.js`) traced it to the harness: the note's ⋮ trigger sits in the note header inside `.app__main`, 537 px above the viewport once the note is scrolled 600 px, and Playwright's tap scrolls it into view before opening the menu. The drags never moved the note. See F3 for what that says about the screen.

## 4 — Checklist nesting (details)

Scripts `qar6-D1.js` (mobile, `QA-R6 checklist`: Milk · Bread · Eggs), `qar6-D2.js` (the append), `qar6-D3.js` (desktop, the 40-item list).

| Check | Result |
|---|---|
| Tab in Item 2 | `li[data-depth="1"]`, field renamed `Sub-item 2` and still focused, status "Made a sub-item", grip 24 px right of Milk's (`padding-inline-start: 24px`); body saved `- [ ] Milk\n  - [ ] Bread\n- [ ] Eggs` |
| Shift+Tab | depth gone, `Item 2` focused, status "Moved up a level" |
| Tab in Item 1 | no depth; focus moved on to "Move Bread" (desktop: to "Move Item 2") — no trap; the first row's grip menu has Make a sub-item, Move up a level, Move up, Move to top disabled |
| Tab in a sub-item (desktop) | left to the browser: depth stays 1, focus moves on (one level only) |
| Grip drag right, 34 px, 12 steps at 30 ms (touch) | mid-drag `data-dragging` and `data-nest-preview="1"` on the lifted row (`qar6-D1-mobile-ink-04-grip-drag-preview.png`); on release `data-depth="1"`, preview gone, no menu, tab still Items, status "Made a sub-item", saved |
| Mouse drag right, 34 px (desktop) | the same on Item 3: preview, nest, saved, kept across reload |
| Reload | `Sub-item 2` = Bread, `data-depth="1"` (both profiles) |
| Grip drag left, 34 px | preview `-1`, then depth gone, "Moved up a level" |
| 180 px drag left on the first row's grip | no tab switch, no level change |
| → / ← on a focused grip (desktop) | nest and un-nest, focus stays on the grip |
| Click on a grip (desktop) | its menu: Move up · Move down · Move to top · Move to bottom · Make a sub-item · Move up a level · Delete |
| Tick Eggs | Done (1), two open rows, saved `- [ ] Milk\n  - [ ] Bread\n- [x] Eggs` |
| Append (`POST /v1/inbox/text` "add butter and jam" with `note_id`) | `transcribed` → `appended` in 3 s; body `- [ ] Milk\n  - [ ] Bread\n- [x] Eggs\n\n- [ ] Butter\n- [ ] Jam`: the tick and the indent intact, two new items; the UI after reload shows Bread still a sub-item, Eggs still in Done (1), Butter and Jam open; the Recordings tab shows the capture "From QA-R6 probe" |

Nocturne (`qar6-N.js`): dragging Jam's grip right previews with the 3 px accent bar (`::before`, `rgb(184, 241, 53)`) and nests (`qar6-N-mobile-nocturne-02-nest-preview-nocturne.png`, `-03-nested-nocturne.png`).

## 5 — Split up (details)

Script `qar6-E.js`, mobile, `QA-R6 split` created as a plain note ("buy milk and bread\nalso eggs and butter for the cake").

- E0: Text · Cleaned · Recordings (0); its Cleaned tab has the Structured / Polished picker and "No cleaned view yet" (for the record).
- E1: Details → "This note is a checklist" → Items · Split up; the two lines became one item (a paragraph is an item).
- E2: Split up has no mode picker: "Not split up yet / The list rewritten as one task per action, in your words. / Generate". (`qar6-E-mobile-ink-01-split-empty.png`)
- E3: Generate → after 3 s the `Split up` region holds the Items editor: 4 rows (buy milk · buy bread · buy eggs · buy butter for the cake), 4 grips, 4 boxes, the add row; above them "Generated just now · Split up", Use this list, Regenerate, and "Ticking, moving or editing here replaces your list with the split version." (`-02-split-proposal.png`)
- E4: ticking the first box → toast "Your list is now the split version." with **Undo**; one `PATCH` (200) and the body is `- [x] buy milk\n- [ ] buy bread\n- [ ] buy eggs\n- [ ] buy butter for the cake`; Use this list gone, caption "Your list · split up just now". (`-03-adopted-toast.png`)
- E5: Undo → the body is the previous single item again and Use this list is back. (`-04-after-undo.png`)
- E6/E7: typing " x" into the first field adopts too — saved at once, not on blur — and the Items tab shows `buy milk x · buy bread · buy eggs · buy butter for the cake`. (`-05-items-after-adopt.png`)

## 6 — No note multi-select (details)

Script `qar6-F.js`. Facts read at 750 ms into the hold and 400 ms after release: `.selection-bar`, `/\d+ selected/` in the body text, any checkbox / `aria-pressed` / `aria-selected` / `--selected` on a row, a "Select all" button.

- Home row (`QA-R6 delete me`), mobile: nothing at either moment; the row ⋮ is `Pin | Delete`; no Select or Select all button anywhere. Desktop: a 750 ms mouse hold and a Shift+click select nothing either.
- Archive row (`QA-R6 archived`, `/?view=archived`): nothing; its ⋮ is `Restore | Delete forever`.
- Recordings (`QA-R6 recordings?tab=recordings`, two text captures): a 750 ms hold on the first row's head → `.selection-bar` with "1 selected" and Select all · Download · Move · Delete · Cancel; Select all → "2 selected"; Cancel removes the bar. (`qar6-F-mobile-ink-04-recordings-mid-hold.png`, `-05-recordings-selected.png`)
- Observation (F4): on both Home and Archive the release after the 750 ms hold opened the note (the click that follows the press), as the tab-bar disc did in the 27 September pass (B-1).

## 7 — Note id and the Devices card (details)

Script `qar6-G.js`, mobile and desktop, clipboard permissions granted to the origin. Details sections in order: Transcription language · Tags · Also called · Word for word · Checklist · **Note id**. The `<code class="note-id">` equals the note's id; the hint reads "For X-Chintan-Note-Id: a device that sends this header files everything into this note (You → Devices & shortcuts)."; the button is "Copy note id". After a tap (a sentinel string was on the clipboard first): `data-state="copied"`, the button reads "Copied", `.copy__result[role=status]` says "Copied", `navigator.clipboard.readText()` returns the id; 2.7 s later the label is "Copy note id" again. (`qar6-G-{mobile,desktop}-ink-01-details-note-id.png`, `-02-details-copied.png`, `qar6-N-mobile-nocturne-04-details-note-id-nocturne.png`)

You → Devices & shortcuts: summaries `From a terminal (curl) | iPhone or Apple Watch (Shortcuts) | Android (HTTP Shortcuts app) | Pebble Index 01 ring | Other apps, and filing into a specific note`; the fifth fold, opened, contains "one specific note", `X-Chintan-Note-Id` and "Details"; `main`'s text has no "OneNote" (nor does any deployed bundle). The `QA-R6 probe` device is listed by name. (`-03-devices-fifth-fold.png`)

## 8 — Delete from ⋮ (details)

Script `qar6-H.js`. Row ⋮ (mobile tap; desktop hover then click): dialog `role=dialog aria-modal=true`, title `Delete “QA-R6 delete me”?`, body "It is kept in the Archive for 30 days, then gone for good.", Cancel + destructive Delete, no input, focus on Cancel. Escape (mobile) and Enter on Cancel (desktop) close it with `archived=false` and the row in place. Confirm → `archived=true`, the row gone, toast `Deleted · kept in Archive for 30 days` + Undo; Undo → `archived=false`, the row back. Header ⋮ on `QA-R6 delete me too`: the same dialog; Cancel stays on the note unarchived; confirm archives, lands on Home with the toast, Undo restores. (`qar6-H-mobile-ink-01-row-dialog`, `-02-row-deleted-toast`, `-03-row-after-undo`, `-04-header-dialog`, `-05-header-deleted-home-toast`, `qar6-H-desktop-ink-01-desktop-deleted-toast`)

## 9 — Console (details)

Script `qar6-I.js` collects `console.error`, `pageerror` and every API response ≥ 400, marked per screen, navigating in-app only. Mobile and desktop: `home`, `note:text`, `note:cleaned`, `note:recordings`, `note:details`, `home again (back)`, `checklist note`, `checklist: split up`, `archive (direct load)` — all empty; no warnings either. Across the other flows the only entries were the two known ones: the Pages `404.html` fallback on each direct `/notes/{id}` load (D-2, `qa-r4-live`) and `GET /v1/push/key` → 404 on You (D-1, #133). Playwright's `requestfailed … net::ERR_ABORTED` on the body-less 204 `DELETE /v1/notes/{id}` responses (H) is the harness quirk recorded on 27 September; the 204s and the state changes are in the request logs.

## R — Routing probe (details)

Script `qar6-probe.js`, backend with #151 and #157 live. `QA-R6 shopping list` (kind `checklist`, body `- [ ] bread\n- [ ] milk`, no captures) created at 09:10; at 09:10:50 `POST /v1/inbox/text` with the device key and `{"text": "QA-R6 shopping list add candles and matches"}` — no `note_id`, no header → 202 with the capture `transcribed`, `has_audio: false`, `source: device:dev_…`. Polled `GET /v1/captures/{id}`: `routing` at +0 s, `appended` at +3 s, `note_id` = the shopping list's id. The note afterwards: `- [ ] bread\n- [ ] milk\n\n- [ ] Candles\n- [ ] Matches`, one capture, still a checklist; no note was created. The revoked key was then refused (401) and the probe's capture went with the purged note.

## C — Cognito managed login (#146)

`gh pr view 146` at 09:19 UTC: **OPEN**, `mergedAt: null`, all eleven checks green. `gh run list --workflow 'Deploy Backend' --limit 3`: 36547724748 (#155, 09:13), 36546231457 (#157, 08:59), 36544200971 (#151, 08:40) — none carries #146, so the logo is **not yet deployed** and was not judged. For the record, the hosted page as the app reaches it today (`qar6-cog.js`: a signed-out context, Sign in → `…auth.us-west-2.amazoncognito.com/login`, title "Sign-in", managed-login v2 markup): it already shows a 192×192 `img[alt="Form logo"]` and three favicon links served from the branding CDN — the existing `ManagedLoginBranding` with the shipped app icon that #146 replaces with the Bindu C mark. `qar6-cog-mobile-01-hosted-login.png` is the before picture; the after needs a re-check once #146 merges and its Deploy Backend finishes.

## Findings

- **F1 — note (pre-existing, D-2).** A direct load of `https://vppillai.github.io/chintan/dev/notes/{id}` answers 404 and serves `404.html` before the SPA boots, which Chromium logs as `Failed to load resource: 404`. In-app navigation never logs it. Recorded in `qa-r4-live` and the 27 September pass; unchanged. Repro: open a note's URL in a fresh tab with the console open.
- **F2 — note (pre-existing, D-1).** `GET /v1/push/key` → 404 (`notifications are not configured on this instance`) on every You load, logged as a console error. From #133; the 27 September pass suggests skipping the fetch until the instance advertises push. Repro: open You with the console open.
- **F3 — polish.** On a long note the header controls — ⋮ "Note actions" and "Find in note" — scroll away with the content: at 600 px of scroll they are 537 px above the viewport (the strip is sticky; the note header above it is not). Details, Share, Find and Delete are therefore a scroll-to-the-top away from the end of a long note, and the tab-bar's back-to-top is the only shortcut. Not a regression from this wave; noted because #156's design note now describes the screen as a strip with panels, and the actions could ride in the strip. Repro: open the 80-paragraph note, scroll 600 px, look for ⋮. Measured in `qar6-C4.js` (`C4-focus`: `elTop: -537`).
- **F4 — note.** With the long press gone (#149), a 750 ms touch hold on a Home or Archive row is a slow tap in headless Chromium: the release navigates to the note. On Android the gesture detector normally drops the click after a long press, so a real Pixel may open nothing; either way nothing is selected. Same shape as the 27 September observation B-1 on the tab-bar disc. Repro: hold a row 750 ms and release.
- **F5 — note.** An inbox append to a checklist leaves a blank line between the existing items and the new ones (`- [x] Eggs\n\n- [ ] Butter`, and the probe's `- [ ] milk\n\n- [ ] Candles`); the Items editor renders the list without a gap, so this only shows in the raw body (copy, export). Observed on the backend before #155 (checklist items as a one-level tree, merge into existing parents) went live at 09:19; re-check after it. Repro: `POST /v1/inbox/text` into a checklist by `note_id`, then `GET /v1/notes/{id}`.

## Harness notes

Six checks were re-specified between runs; the app's behaviour did not change between them and the results above are from the final runs. A7b/A8/A8b measured the tab strip's box once, while it was stuck at the top of a scrolled note, and after the switch to the short Cleaned panel reset the page's scroll the later drags landed outside `.note-views` (re-measured per drag; the edge tests moved to the Cleaned tab so a missed exclusion would have shown as a switch). D3 used Playwright's default substring name match (`Item 2` matched `Item 20`–`28`; `exact: true`). G1 compared the last Details heading with its CSS-uppercased `innerText`. H's row lookup by `hasText: 'QA-R6 delete me'` matched `QA-R6 delete me too` first (anchored regex). I tapped a Home row while still on the note. C4, as described in §3, was Playwright scrolling an off-screen ⋮ into view. Round-6 nested-list state left in the tenant by D1/D2/N was purged with everything else.

## Screenshots (`ubuntu@orb:~/r3/live/`, 64 files)

qar6-A-mobile-ink-01-note-text · 02-mid-swipe-left · 03-after-left-cleaned · 04-after-vertical-scroll · 05-cleaned-tab · qar6-A-mobile-nocturne-01…05 (the same five) · qar6-A-desktop-{ink,nocturne}-01-desktop-recordings · qar6-B-mobile-ink-01-typing-caret-above-keyboard · 02-checklist-new-item-above-keyboard · 03-details-above-keyboard · qar6-C844-mobile-ink-01-sheet-at-end · 02-sheet-scrolled-back · qar6-C560-mobile-ink-01-sheet-at-end · 02-sheet-scrolled-back · qar6-C2-mobile-ink-01-details-open-touch · 02-scrolled-note-sheet-dragged · qar6-C3-mobile-ink-01-note-after-menu · qar6-D1-mobile-ink-01-items · 02-tab-indented · 03-grip-menu-first-row · 04-grip-drag-preview · 05-after-grip-drag · 06-after-reload · 07-ticked-before-append · qar6-D2-mobile-ink-01-after-append · 02-recordings-tab · qar6-D3-desktop-ink-01-mouse-grip-preview · 02-after-reload · qar6-E-mobile-ink-01-split-empty · 02-split-proposal · 03-adopted-toast · 04-after-undo · 05-items-after-adopt · qar6-F-mobile-ink-01-home-mid-hold · 02-archive-mid-hold · 03-archive · 04-recordings-mid-hold · 05-recordings-selected · qar6-F-desktop-ink-01-mouse-hold · qar6-G-{mobile,desktop}-ink-01-details-note-id · 02-details-copied · 03-devices-fifth-fold · qar6-H-mobile-ink-01-row-dialog · 02-row-deleted-toast · 03-row-after-undo · 04-header-dialog · 05-header-deleted-home-toast · qar6-H-desktop-ink-01-desktop-deleted-toast · qar6-N-mobile-nocturne-01-checklist · 02-nest-preview-nocturne · 03-nested-nocturne · 04-details-note-id-nocturne · 05-split-up-empty-nocturne · 06-split-up-adopted-nocturne · 07-home-nocturne · qar6-cog-mobile-01-hosted-login (all `.png`).
