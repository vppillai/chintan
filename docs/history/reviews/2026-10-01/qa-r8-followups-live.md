# Live QA after the round-8 follow-ups (#203–#207)

**Target:** https://vppillai.github.io/chintan/dev/ and the prod API, on the test tenant only (`claude-test@example.com`). The checks ran 2026-10-01 05:45–06:00 UTC (30 Sept evening, Pacific), at 390×844 touch (Pixel profile) and 1280×800 mouse, each in Ink & Paper and Nocturne where the item is visual.

**Build under test.**

| PR | Merge commit | Merged (UTC) | Deploy green |
|---|---|---|---|
| #205 Less on an open footnote fold | `0bf0a1fd` | 04:47 | Frontend |
| #203 Details sheet: fixed head, body scrolls | `53bf8a23` | 05:05 | Frontend |
| #207 Web Push VAPID pair installed automatically | `c636993b` | 05:15 | Backend |
| #204 Devices & shortcuts folded on You | `bcd32e86` | 05:29 | Frontend 05:35 |
| #206 Lock a held take onto the recording screen | `190ce5d4` | 05:39 | Frontend 05:40:58 |

The served bundle at the start was `index-C5ieQI-K.js` (frontend `190ce5d4`). During the run #208 (`167e8ee5`, backend only: Tidy line-loss guard and move target stamp) merged and deployed — Backend green 05:56:19, Frontend green 05:57:14 (bundle `index-B_jak_E6.js`, same frontend source, a new build sha). The Details, You and lock flows ran before and across that deploy; the Notifications, regression and console flows ran after it. Nothing under test here was touched by #208, and no result differed across the boundary. `GET /v1/push/key` answered 200 with an 87-character key throughout.

**Tooling:** Playwright (chromium, headless) on orb, through `~/r3/live/r8q2-ux.js`, `r8q2-setup.js` and `r8q2-run.sh`, built over `r8q-lib.js` → `qar6-lib.js` → `qaw1-lib.js`. `r8q2-run.sh <flow> [profile/theme …]` runs one flow per combination. Chromium's fake microphone (`--use-fake-device-for-media-stream`, `qa0924-speech.wav`) fed the hold-to-talk flows. Raw data: `qaw1-r8q2<flow>-*-{results,reqs}.json`, `qaw1-r8q2<flow>-*.log`, screenshots `r8q2-<flow>-<profile>-<theme>-NN-<name>.png` (55).

**Data and cleanup.**
- **Start state.** 57 active notes, 0 archived, no devices, no push subscriptions.
- **Created.** Two notes, "R8Q2 ptt" and "R8Q2 details" (four tags, two aliases, 30 paragraphs so the sheet scrolls). Four captures, all from a locked hold then Send on "R8Q2 ptt"; all four settled `appended` into that note. Four device keys named "R8Q2 key", one per You run, each revoked from the card's own Remove. Two push subscriptions from the stubbed browser (below), each removed by the card's own switch (DELETE 204).
- **Every cancel accounted for.** The slide-left cancels, the dialog Discards and the short-take Cancels made no POST and no PUT.
- **Cleanup.** Captures deleted (4 × 204), notes deleted and purged (2 × 204).
- **End state.** 57 active, 0 archived, no `R8Q2` note anywhere, `GET /v1/devices` empty, `GET /v1/push/subscriptions` empty. No pre-existing note was touched.

## Summary

| # | Ask | Verdict | Checks |
|---|---|---|---|
| 1 | #203 Details sheet | **PASS** on the layout, hairline, Close, pull-to-refresh and focus; **FAIL on Escape** (F1) | ×4: the sheet is `overflow: visible`, the head a fixed 45 px row, the body the only scroller (400 px of room at 390, 360 at 1280). Hairline transparent at rest, `#e4dfd2` / `#242a27` once scrolled, clear again at the top. Close 44×44, in view and the hit target at top, mid and end. A 168 px drag down on the head, and one on the body at its top, never arm pull-to-refresh and cause no refetch. Escape never closes it (F1). Focus lands on the language select, inside the sheet (N1) |
| 2 | #204 Devices & shortcuts fold | **PASS ×4** | Folded by default, "No devices yet" under the title, a 78 px summary, body not rendered. Tap opens (chevron turned, Add a device shown), tap folds. About's link, warm (Home → You → About) and cold (a fresh `/about`), lands on `/settings#devices` with the card open and at the top of the view (53 px), history unchanged; Back from there is Home. Minting a key keeps the card open with the status "1 device" and focus on the key box; Remove → "Remove it" revokes it and the card stays open at "No devices yet" |
| 3 | #205 More → Less | **PASS ×4** | Under Recording & transcription the summary reads "More" closed, "Less" open (body shown), "More" again; swapped by the `[open]` state, no script |
| 4 | #206 Lock onto the recording screen | **PASS ×4** | Hold 2.5 s, slide up 80 px: `/capture?note=<id>` at +3.0 s with the waveform, controls Cancel · Pause · Stop · Send, and the clock at 00:02 climbing to 00:04 — not reset. The shell announces "Recording, hands-free". Send → `POST /v1/captures` 201 with `note_id` = the note, upload seen, back on the note; all four filed `appended`. At ≥ 10 s, Cancel asks "Discard 00:10 of recording?" (Keep / Discard, Keep focused); Escape keeps it with the clock still running; Discard sends nothing and returns to the note. At 2 s, Cancel discards at once with no dialog |
| 5 | #207 Notifications | **PASS** (the card and the request shape); the push-service step is the environment's, not the app's | `GET /v1/push/key` 200; the card offers the switch, off. Plain headless Chromium reports `Notification.permission` denied, so the row reads "Blocked in the browser settings. Allow notifications for this site there, then turn this on." with the switch disabled — the right state for that browser (N2). With the permission and `PushManager.subscribe` stubbed, the switch POSTs `{endpoint, expirationTime, keys: {p256dh (87), auth (22)}, label}` → 201 `{id, endpoint_host, label, created_at, last_success_at}`, the switch reads on and the foot "Notifications reach 1 device, this one included."; off again DELETEs 204. No subscription left behind |
| 6 | Round-8 regressions | **PASS ×3** | The Filing tray renders with its h2 and a glyph per row (moving, needs, failed); Back from About is You, then Home; hold then slide left 115–120 px shows "‹ Slide to cancel" at the midpoint and "Cancelled" at the end with 0 uploads |
| 7 | Console | **PASS ×2** | No console errors on Home, a note or You. Only the known Pages 404 on a direct deep load (D-2) appears in the flows that start on `/notes/<id>` or `/settings` |

One bug (Low), two notes.

## Detail

### 1. #203 Details sheet
The note "R8Q2 details" at 390×844: sheet top 245, head 254–299 (45 px), body from 299, 506 px of sheet, 400 px of body scroll. At 1280×800: head 236–281, 360 px of room. Opened from ⋮ → Details on every run.

- **Layout.** `.note-panel` is `overflow-y: visible` with `scrollTop` 0 before and after the body scroll; `.note-panel__body` scrolls. `elementFromPoint` 2 px under the head's top, at the sheet's centre, is the head at rest, at 240 px and at the end — nothing from the body shows above or through it.
- **Hairline.** `border-block-end-color` of the head: `rgba(0,0,0,0)` at rest; `rgb(228,223,210)` (Ink) / `rgb(36,42,39)` (Nocturne) at 240 px, with `data-scrolled` on the sheet; transparent again at 0. Chromium runs the scroll-driven animation and the `onScroll` fallback attribute together, as designed.
- **Close.** 44×44, inside the viewport and the element under its own centre at every scroll position, both widths.
- **Pull-to-refresh (touch).** A finger on the head (x = head.left + 60) dragged 168 px down in 12 steps: `.pull-refresh[data-phase]` sampled at 56, 112 and 168 px read `idle`, `idle`, `idle`; no `GET /v1/notes/<id>` followed; `.app__main` did not move; the sheet stayed. A 140 px drag on the body at its top: the same.
- **Escape.** With focus on the language select (where opening leaves it), on the heading (`tabindex="-1"`, focused by script) and on the page body: the sheet stays open all three times, on all four runs. See F1.
- **Focus.** Right after opening, `document.activeElement` is `select#note-language-<id>`, inside the sheet. See N1.
- Screenshots `r8q2-details-{mobile,desktop}-{ink,nocturne}-01-details-rest.png`, `-02-details-scrolled.png`, `r8q2-details-mobile-*-03-head-dragged.png`.

### 2. #204 Devices & shortcuts
- **Folded.** `section#devices > details` closed; summary "Devices & shortcuts" over "No devices yet", chevron at rest; `.you-card__body` not visible (`checkVisibility()` false).
- **Tap.** Open, chevron `rotate(90deg)`, lead, list and "Add a device" shown. A second tap folds it.
- **From About, warm.** Home → You tab → "About Chintan" → "Devices & shortcuts": the URL is `/settings#devices`, the card is open with its top at 53 px (mobile) / 52 px (desktop), `history.length` unchanged (the link is a Back with the hash carried by `usePendingTab`), and Back from there is Home.
- **From About, cold.** `goto /about` then the link: the same, by the replace path.
- **Minted key.** Add a device → "R8Q2 key" → Create key: the key box appears with focus on it, the card is open and stays open through the re-render, status "1 device". Done, then Remove → the confirm "Remove R8Q2 key?" (Cancel focused, "Remove it" destructive) → the row is gone, `GET /v1/devices` has no R8Q2, the card is still open at "No devices yet".
- Screenshots `r8q2-you-*-01-devices-folded.png`, `-02-devices-open.png`, `-04-from-about.png`, `-05-minted.png` (the key value was replaced in the DOM before the shot).

### 3. #205 More → Less
`.you-card__more-summary` under Recording & transcription: closed, the visible span is "More" and `innerText` is "More"; open, "Less" with the body shown; closed again, "More". Screenshot `r8q2-you-*-03-more-open.png`.

### 4. #206 Lock
On "R8Q2 ptt". Touch via CDP on mobile; mouse on desktop.

| Step | Mobile Ink | Mobile Nocturne | Desktop Ink | Desktop Nocturne |
|---|---|---|---|---|
| Lock after 2.5 s held: arrival, clock | +3.0 s, 00:02 → 00:04 | +3.1 s, 00:02 → 00:04 | +2.9 s, 00:02 → 00:04 | +2.9 s, 00:02 → 00:04 |
| Send: POST, `note_id`, uploads | 201, the note, 0→3 | 201, the note, 0→3 | 201, the note, 0→3 | 201, the note, 0→3 |
| Cancel at 00:10: dialog | "Discard 00:10 of recording?" | same | same | same |
| Escape: clock | 00:10 → 00:12 | 00:10 → 00:12 | 00:10 → 00:11 | 00:10 → 00:11 |
| Discard: uploads | 3→3, back on the note | 3→3 | 3→3 | 3→3 |
| Cancel at 00:02 | no dialog, 3→3 | no dialog | no dialog | no dialog |

- The tab bar is gone by the time the finger lifts (`nav.tab-bar` absent at the last move), so the hand-off is on the lock crossing, not the release.
- The `role="status"` text on arrival: "Recording | Recording, hands-free".
- The dialog body: "It has not been sent, and it is not saved anywhere else." Keep is focused on open.
- Screenshots `r8q2-lock-*-01-locked-capture.png`, `-02-discard-confirm.png`.

### 5. #207 Notifications
- **Plain headless.** `pushSupport()` is `supported`, a service worker is registered, `Notification.permission` is `denied` (headless Chromium's answer whatever the context grants). The card: switch present, `aria-checked=false`, disabled; row "Notify me when a recording files — Blocked in the browser settings. Allow notifications for this site there, then turn this on."; foot "No device is enrolled yet. Only the note's title is sent, never what was said." No request was made. Screenshot `r8q2-push-mobile-{ink,nocturne}-01-notifications-card.png`.
- **Stubbed browser** (`PUSH_STUB=1`: `Notification.permission` → granted, `PushManager.subscribe` → a fake FCM subscription with a 65-byte p256dh and 16-byte auth). The switch: `POST /v1/push/subscriptions` with body keys `endpoint, expirationTime, keys, label`, an https endpoint, p256dh 87 and auth 22 characters, label "Chrome on Windows" / "Chrome on Android" → 201 `{"id":"985cc27c3c0fa0a9","endpoint_host":"fcm.googleapis.com","label":"Chrome on Windows","created_at":…,"last_success_at":null}`. The switch read on, the row's hint "Also when one needs a note chosen, or did not finish", the foot "Notifications reach 1 device, this one included." Off again: `DELETE /v1/push/subscriptions/<id>` 204, foot back to "No device is enrolled yet." Screenshot `r8q2-push-desktop-ink-02-after-switch.png`.
- The server accepted a syntactically valid but unreal key pair; whether it validates the p256dh point is not something this pass can tell, and the worker would only learn at send time (410/4xx from FCM). Both rows were removed before the worker could try.

### 6, 7. Regressions and console
- Tray (Home, `GET /v1/captures` stubbed with needs/failed/transcribing rows): h2 "Filing", `.filing__tray`, rows `moving`/`needs`/`failed` each with a glyph, × on failed only (the needs row's missing × is R8-P4, decided).
- Back: Home → You → About; Back → `/settings`; Back → `/`.
- Slide-left: at −57 px `data-hold=holding` with "‹ Slide to cancel"; at −115 px `data-notice=cancelled`; 0 POST, 0 PUT; still on the note.
- Console: 0 errors and 0 warnings on Home, the note and You, both widths. The one `Failed to load resource: 404` in other flows is the Pages 404 on a direct deep load (D-2).

## Findings

**F1 (Low, #203): Escape does not close the Details or Share sheet.**
- **Repro.** Open a note, ⋮ → Details. Press Escape — with focus where opening left it (the language select), after clicking the "DETAILS" heading, or after clicking the page. The sheet stays. Same on 390 and 1280, both themes, Share included.
- **Cause.** There is no Escape handler for the sheet. `NoteDetailScreen.tsx`'s only window `keydown` is ⌘/Ctrl+F (Find); `NoteDrawer.tsx` has none; `git log -S Escape` on the screen shows only #41 (Find). PR #203's "Kept: … Escape" names a behaviour that was never there, and `note-tabs.spec.ts` does not test it.
- **Effect.** A keyboard user closes the sheet with Tab to × or by reopening ⋮. Not a blocker: the sheet is not modal and the note stays editable behind it.
- **Fix direction.** A `keydown` on the sheet (or the screen, when `panel !== null`) that calls `onOpenChange(null)` on Escape when no dialog or menu has it — `keyTaken()` in `useHoldToTalk.ts` already encodes that rule — and returns focus to the ⋮ as Close does. Mind the `<select>`: Escape on an open native select is the browser's.

**N1 (note, #203): focus on opening Details lands on the language select, not the heading.** `openPanel()` sends Details to `noteLanguageFieldId` and Share to `notePanelHeadingId`, deliberately (the meta line's language fact opens Details). The brief for this pass expected the heading; the code's choice is documented in `NoteDrawer.tsx` and is the better one for the meta-line path. Recorded so the two specs agree; no change asked.

**N2 (note, #207): the push-service step cannot be exercised headless.** Headless Chromium reports notifications denied regardless of `grantPermissions`, and the card says so correctly. The request shape and the server's 201/204 were checked with the browser side stubbed. A real device check remains for the owner: permission prompt, a real FCM/APNs subscription, and the first delivery.

**Not covered.** iOS (the installed-app requirement for push, the loupe); real haptics on lock and cancel; `prefers-reduced-motion`; the sheet with a real on-screen keyboard.
