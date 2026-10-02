# Smoke test — checklist notes and the capture-screen controls (prod, 2026-09-21)

> Committed on 2026-10-01 (PR9-21) from the 21 September checkout, unchanged; the queue's QA section carries its outcome, and the code has moved since.

Target: https://vppillai.github.io/chintan/dev/ serving frontend `v0.5.19-1-g6e090ff` (PR #64, Deploy Frontend run 172) against backend PR #65 `fd3dab2` (Deploy Backend run 88). Throwaway user `claude-test@example.com`, headless Chromium on orb, Pixel 8 Pro (412×915, touch, DPR 2.625) and desktop 1280×800, Ink & Paper and Nocturne. Contracts: `contract-checklist.md`, `contract-capture-ux.md`. No code changed. Every note created here was archived and deleted forever at the end (both return 404; no captures or archived rows remain). Screenshots and logs: orb `~/r3/smoke/` (`<profile>-<step>-NN-<name>.png`, `*.log`).

## Findings

1. **Medium — the Checklists chip says "N+" on Home, including "Checklists · 0+" when there are none.** `mobile-after-purge-01-home.png`, `desktop-after-purge-01-home.png` (0+), `mobile-checklist-ink-full-01-home.png` (2+ with exactly two). Root cause: `GET /v1/notes?kind=checklist` returns a continuation cursor whenever the raw scan page was full, regardless of how many rows matched — with 50+ notes and one checklist the response is `{items:[1], cursor:"…"}`, and following the cursor yields `{items:[], cursor:null}`. `tag=` behaves the same (`?tag=house` → 3 items + cursor), which is why the contract's "same mechanism as tag" carried the quirk over. `NotesScreen` renders `+` from `checklists.hasNextPage` and never pages that query, so on the unfiltered Home the `+` is permanent. With the filter active the (short) list auto-pages, the cursor exhausts and the chip settles to "Checklists · 2" (`mobile-checklist-nocturne-visual-02-home-checklists.png`), so the two states disagree. Side effect: the chip's accessible name omits the `+` ("Checklists · 2" vs visible "Checklists · 2+"). Cheapest fix is server-side and helps `tag=` too: when a filter is set, keep scanning until `limit` matches or the partition ends, and return `cursor:null` at the end.

2. **Medium — the Items tab clips long items.** Each open item is a single-line `<input>`; there is no wrap and no ellipsis. The item a recording produced — "The gutter on the north side is leaking again, and the roofer should check the flashing before Saturday." — shows as "The gutter on the north side is leaking again," on the Pixel 8 Pro (`mobile-recording-delete-ink-02-items-with-recording.png`; `mobile-checklist-ink-full-03-items.png` shows the seeded long item cut at "book th"). Since one recording becomes one item (up to 2,000 runes), whole dictated sentences are the normal case for this feature, and the Items tab is where they are meant to be read and ticked. The Split up preview and the Done rows use a `<span>` and wrap correctly. Same in both themes and on desktop for longer items (554 px available). Suggest an auto-growing `textarea` (or wrapping `contenteditable`) for open items.

3. **Low — "Use this list" leaves the Split up tab reporting itself stale.** Adopting the list PATCHes the body to exactly `cleaned.body`, and the server then marks `cleaned.stale=true` (GET right after: `body === cleaned.body`, `stale: true`), so the tab shows "The note changed since this was generated — Regenerate now" for a list identical to the note (banner state in `mobile-checklist-nocturne-visual-04-split-up.png`). Harmless, but it invites a pointless regeneration. Suggest: a save whose body equals the current `cleaned.body` keeps `stale=false`.

4. **Info — automation-only console noise, not user-visible.** (a) `GET /chintan/dev/capture` → 404 on any deep link: GitHub Pages serves `404.html`, the SPA boots from it; expected on Pages. (b) Playwright reports `requestfailed net::ERR_ABORTED` for the S3 `PUT audio.webm`/`PUT peaks.json` and for `DELETE /v1/captures/{id}`: the responses have no body the app reads (upload returns on `ok`; 204), so Chromium cancels the body stream after success — the capture was filed and the delete landed. (c) `wakeLock.request` rejects `NotAllowedError` in a headless document; the recorder proceeds as designed.

A false alarm worth recording so nobody chases it: an early harness measured ~30 s between tapping Cancel/Discard and leaving `/capture`. That was the harness — `locator('.capture__state').textContent()` auto-waits Playwright's 30 s default once the element has gone. Re-measured with URL polling and an instrumented init script (`discard-trace.js`): Discard from review leaves in 52 ms, Cancel while recording in 60 ms; IndexedDB prune and `history.replaceState` all fire within 10 ms of the tap.

## Worked as intended

**API (Phase 1, `api-checklist.log`)**
- `POST /v1/notes {title, kind:"checklist", body}` → 201 with `kind:"checklist"`.
- `GET /v1/notes?kind=checklist` lists it (and only checklists); the unfiltered list carries `kind` on every row (`["checklist","note"]`); `include=search_text` rows carry `kind`.
- `PATCH` `kind:"note"` + paragraph body → GET returns `kind:"note"` and the paragraphs; `PATCH` back to `kind:"checklist"` + item body → GET byte-identical to the original body. `PATCH` with `kind` alone leaves the body untouched.
- Guards: `PATCH cleaned_mode:"polished"` and `POST …/clean {mode:"polished"}` on a checklist → 400 `cleaned_mode must be tasks for a checklist`; `kind:"bogus"` → 400 `kind must be note or checklist`.
- `POST …/clean {}` → 202 `{status:"queued", mode:"tasks"}`; `cleaned` arrived after 6 s with `mode:"tasks"`, `stale:false`; every non-blank line matches `^- \[( |x)\] \S`; the long item was split ("call the bank about the mortgage" / "book the car service"), "milk" kept verbatim, order kept, 3 items from 2.

**UI (Phase 2, `mobile-checklist-ink-full.log`, `desktop-checklist-ink-desk.log`, `*-visual.log`)**
- Home: Checklists chip right after All, 128×44, `?kind=checklist` in the URL on tap, pressed state; row shows the checklist glyph, open items as "milk · call the bank …", and "0 of 2 done" (`mobile-checklist-ink-full-02-home-checklists.png`).
- Note screen: tabs Items · Split up · Recordings (0); meta "Updated today 00:27 · 0 of 2 done"; checkbox labels 44×44; rows 45 px.
- Split up: read-only preview with disabled boxes, no mode picker, "Use this list" (filled) and "Regenerate", auto-refresh switch present; "Use this list" replaced the body with `cleaned.body` (GET confirmed) and the meta became "0 of 3 done" (`mobile-checklist-ink-full-04-split-up.png`, `-05-split-up-after-use.png`).
- Tick "milk": moved to "Done (1)", greyed `rgb(113,106,91)` with line-through, × is 44×44, meta "1 of 3 done", body saved as `- [x] milk` within a second; reload keeps it (`-06-items-ticked.png`, `-07-items-after-reload.png`). Live region announces "Marked done".
- "Add an item" + Enter appends `- [ ] eggs` and keeps focus in the add row; × on the done row removes `milk` and its Done section (`-08-items-added.png`, `-09-items-deleted.png`).
- Details → Checklist switch off: `kind:"note"`, body `call the bank about the mortgage\n\nbook the car service\n\neggs`, tabs relabel Text · Cleaned, textarea shown, meta "11 words"; switch on: `kind:"checklist"`, body identical to before the round-trip (`-10-details.png`, `-11-details-plain.png`, `-12-details-checklist-again.png`).
- Desktop keyboard: Enter in an item inserts an empty item right under it and focuses it; Backspace on the emptied item removes it and steps back to the item above; reopening from Done returns the item to the open list in body order (`desktop-checklist-ink-desk-04…07.png`).
- Nocturne: chip, rows, Items, Split up (incl. the stale banner), Details and Recordings all render correctly on both viewports (`mobile-checklist-nocturne-visual-0*.png`, `desktop-checklist-nocturne-visual-0*.png`). No console or page errors in any run.

**Recording into a checklist (Phase 3, `capture-into.log`, `mobile-recording-delete-ink.log`)**
- `POST /v1/captures {content_type, note_id, duration_ms, size_bytes}` → `uploaded`; PUT of `~/e2e.webm` → 200; `appended` after 4 s.
- Exactly one `- [ ]` line was added (the marker is stripped from the wire body, leaving the paragraph break): "- [ ] The gutter on the north side is leaking again, and the roofer should check the flashing before Saturday." Items tab shows it; meta "1 recording · 0:08 · 0 of 4 done".
- Recordings (1) tab shows the row with transcript; More → Delete recording → type "delete" → Delete it: row gone, `captures: 0`, the item and its blank line removed, nothing else touched (`mobile-recording-delete-ink-01-recordings-tab.png`, `-04-confirm.png`, `-06-recordings-after-delete.png`, `-07-items-after-delete.png`).

**Capture screen (Phase 4, `*-capture-*.log`)**
- Chromium's `--use-fake-device-for-media-stream` works against the live site, so the whole tour ran: `requesting` shows "Starting the microphone…" with Cancel only; `recording` shows Cancel · Pause · Stop · Send, four round discs, 64×64 on the phone and 56×56 under a fine pointer, one row, visible 11.1 px labels, Send the only filled disc (`mobile-capture-ink-send-03-recording.png`, `desktop-capture-ink-nosend-03-recording.png`, `mobile-capture-nocturne-nosend-03-recording.png`).
- Pause → "Paused" with Resume; Resume → Recording; Stop → "Ready to send" with Discard · Re-record · Send and the review player; Re-record → Recording again in 56 ms (`-04-paused.png`, `-05-review.png`, `-06-re-recording.png`).
- Send while recording: left `/capture` at once to `/notes/{id}?tab=recordings`; filing row "Uploading… 40%" plus the "Sending a recording — tap to return" indicator; the capture was filed into the target note as one item (the fake tone transcribed as "you"), the recording row appeared with peaks (`-07-after-send.png`, `-08-filing-0.png`, `-10-recordings-tab.png`).
- 320 px: four 64 px discs in a 280 px row, same baseline, `scrollWidth` 320 — no wrap (`narrow-capture-ink-send-01-320-recording.png`).
- Microphone refused: "SOMETHING WENT WRONG · No microphone was found." with a single Close control, same graphic style (`mobile-capture-ink-deny-02-final.png`, `desktop-capture-ink-deny-03-final.png`). Cancel/Discard return to the target note, replacing the history entry.

## Not exercised

- The "permission denied" wording: `--deny-permission-prompts` makes Chromium raise `NotFoundError`, so only "No microphone was found." was reachable.
- Real speech through the capture screen (the fake device is a tone); real speech went through the API path in Phase 3.
- WebKit/Safari, offline, the PWA shortcut, and the `failed`-with-retry state (Discard + Try again), which needs an upload failure.

## Exact commands (orb, `ubuntu@orb`; scripts in `~/r3/smoke/`)

```
# tokens: ~/review-tokens.json's idToken expired 12 min into the run; refreshed the way the app does
# (grant_type=refresh_token, no rotation in the pool) into ~/r3/smoke/tokens.json; lib.js points at it.
~/r3/smoke/refresh.sh

# Phase 1 — API
~/r3/smoke/api-checklist.sh | tee ~/r3/smoke/api-checklist.log        # writes NOTE_ID to ids.env

# Phase 2 — UI (node 22 + Playwright from the frontend node_modules)
cd ~/r3/smoke
~/temp/node/bin/node ui-checklist.js mobile  ink      full
~/temp/node/bin/node ui-checklist.js desktop ink      desk
~/temp/node/bin/node ui-checklist.js mobile  nocturne visual
~/temp/node/bin/node ui-checklist.js desktop nocturne visual
~/temp/node/bin/node ui-checklist.js desktop ink      visual

# Phase 3 — recording into the checklist note, then delete it from the UI
./capture-into.sh | tee capture-into.log                              # e2e.webm with note_id; writes CAP_ID
~/temp/node/bin/node ui-recording-delete.js mobile ink

# Phase 4 — capture screen (own target note CAP_NOTE_ID in ids.env)
~/temp/node/bin/node capture-ui.js mobile  ink      send              # tour + Send-while-recording (real upload)
~/temp/node/bin/node capture-ui.js desktop ink      nosend
~/temp/node/bin/node capture-ui.js mobile  nocturne nosend
~/temp/node/bin/node capture-ui.js desktop nocturne nosend
~/temp/node/bin/node capture-ui.js mobile  ink      deny
~/temp/node/bin/node capture-ui.js desktop ink      deny
~/temp/node/bin/node cancel-timing.js mobile                          # the false alarm
~/temp/node/bin/node discard-trace.js | tee discard-trace.log         # the correction: 52 ms / 60 ms

# cursor probe behind finding 1
source ~/review-env.sh; TOK=$(jq -r .idToken ~/r3/smoke/tokens.json)
curl -sS "$API/v1/notes?kind=checklist" -H "Authorization: Bearer $TOK" | jq '{n:(.items|length), cursor}'
curl -sS "$API/v1/notes?kind=checklist&cursor=<cursor>" -H "Authorization: Bearer $TOK" | jq '{n:(.items|length), cursor}'   # → 0, null

# purge (both notes): archive, delete forever, confirm 404
for ID in $NOTE_ID $CAP_NOTE_ID; do
  curl -sS -X DELETE "$API/v1/notes/$ID" -H "Authorization: Bearer $TOK"
  curl -sS -X DELETE "$API/v1/notes/$ID/permanent" -H "Authorization: Bearer $TOK"
  curl -sS -o /dev/null -w '%{http_code}\n' "$API/v1/notes/$ID" -H "Authorization: Bearer $TOK"   # 404
done
```
