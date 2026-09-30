# Live QA after round 7, 2026-09-30 (#179, #176, #182, #183 on top of #175, #177, #178, #180, #181)

**Target:** https://vppillai.github.io/chintan/dev/ and the prod API, 2026-09-30 17:55–18:15 UTC, on the test tenant only (`claude-test@example.com`).

**Build under test:** all four PRs merged, and the last deploy of each side was green before any check ran.

| PR | Merge commit | Merged | Deploy green |
|---|---|---|---|
| #179 backend speed: short dictations tidied rather than cleaned (R7-15, R7-16a/b) | `8336f9c` | 16:57:50Z | Backend 17:05:18Z |
| #176 retention, 5xx alarm, deploy rollback (R7-3, R7-4a/b) | `9af6d45` | 17:22:00Z | Backend 17:30:46Z |
| #182 Home receipts, excerpt, `created_note`, server-error caption, "Chintan decides" (R7-7a/b/c, R7-12, R7-14) | `1308b50` | 17:37:40Z | Backend 17:43:56Z (run 36752745743), Frontend 17:44:49Z |
| #183 update check on resume, lighter polling (R7-8, R7-17) | `4dd4648` | 17:51:31Z | Frontend 17:52:38Z (run 36754404795); frontend-only, so it has no backend deploy |

The served bundle was `index-_S3Dj8Wc.js`, which carries "filed into", "server didn’t answer" and "Added at the end", with "Chintan decides" in the lazy `Waveform-*.js` chunk. No deploy ran during the checks. All four PRs are covered.

**Tooling:** everything ran on orb, one run at a time.
- Batteries: `~/r3/live/r7q-routing.py`, a copy of `r6b-routing.py` that also records `excerpt` and `created_note` and names its key "R7Q battery". `r7q-items.py` is `r6b-items.py` with the retired batch purge replaced by `DELETE /v1/notes/{id}/permanent`.
- `matched_by` was joined from the worker's `routing decided` log lines with `filter-log-events`. Logs Insights is still denied, but plain filtering works.
- Capture checks: `r7q-capture.py`.
- UX: Playwright (chromium, headless) through `r7q-lib.js` / `r7q-ux.js` / `r7q-setup.js` / `r7q-post.js`, built over `qar6-lib.js` → `qaw1-lib.js`.
- Raw data: `r7q-routing.jsonl`, `r7q-routing-extra.jsonl`, `r7q-items.jsonl`, `r7q-capture.jsonl`, `qaw1-r7q*-{results,reqs}.json`, and screenshots `r7q-*.png`.

**Data and cleanup:**
- Every note created was either one of the batteries' own set-up notes (the 11 routing notes; "Shopping list" for items) or titled `R7Q …`. That is 5 UX notes, 30 archived fillers and the notes the checks routed into being.
- Keys: "R7Q battery", "R7Q items", "R7Q capture", "R7Q silence repro" and "R7Q ux". All were revoked (204), and the revoked capture key then answered 401.
- Every capture was deleted, and every note made was deleted and purged.
- End state: 57 active notes, as at the start. No `R7Q` note is active or archived, and `GET /v1/devices` is empty. No pre-existing note was edited: every append into one was undone by capture delete, and its body was checked `unchanged`.
- The one note made that had no `R7Q` title was **"Thank you"**, created by the silence bug (F1). It went with its capture.

## Summary

| Area | Verdict |
|---|---|
| Gate for `c637473` (R7-10a checklist marker): rows 18–22, 31, 32 ×3 | **REGRESSED**: row 21 3/3 → 2/3, row 32 3/3 → 2/3 |
| No-regression sweep: rows 1–17, 23–30 ×1 | 23 of 25 pass. Row 12 fails (was 3/3); row 15 parks (was 0/3) |
| Items battery (10 legacy rows + 6 tree cases) | **15 of 16** (was 16 of 16). L6 kept "A" in "A birthday card for Anu" |
| Short text tidied, not rewritten | PASS |
| "3 eggs and milk" not "3 Eggs" | PASS |
| 3 s tone → `no_content`, no note | PASS |
| 3 s **silence** → `no_content`, no note | **FAIL**: 3 of 3 filed "Thank you." into a new note "Thank you" (F1) |
| Wire: `excerpt` and `created_note` | PASS: `created_note` true on every routed new note, false on every append |
| Find in note: one flowing block | PASS ×4 (mobile/desktop × Ink/Nocturne) |
| Back restores Home and Archive scroll | PASS ×4 (one mobile run first misread the list still flinging; see F5) |
| Record into a long note → "Added at the end · Show" | PASS on mobile. **Desktop: the marked paragraph lands under the bottom bar** (F2) |
| Tick → "Milk done · Undo", Undo works | PASS ×4 (server and screen) |
| Receipts: one line, fold, excerpt, Started, Clear all | PASS ×4 |
| Capture pill "Chintan decides" | PASS ×4 |
| 5xx on `/v1/notes`: server sentence + Retry | PASS ×4. With no local copy, Home shows the problem `detail` and "Try again" |
| Console | PASS. Only the known Pages 404 on direct loads (D-2) and the injected 503s |
| Stranded capture `c_18da066315ace8dc_99879cccfdb28f8e` | **Deleted**: `DELETE` → 204, then `GET` → 404, gone from the list, a second `DELETE` → 404 |

## 1. Routing battery

The 11 set-up notes of `r6b-routing.py` were created with the fixture's aliases and tags, over the tenant's 57 notes, so the router saw 68 candidates. Each capture was posted untargeted through `POST /v1/inbox/text`, judged, then undone before the next one, as in `prod-battery-after-r6.md`.

Window: 17:55:01Z–18:03:29Z. There were 46 battery captures plus 6 supplementary ones. Every one settled in 2.3–13.1 s (median 2.4 s) with no `failed`. One capture parked (row 15), and three ended `no_content` (rows 4, 9 and 12).

`matched_by` comes from the worker log. All 52 `routing decided` lines joined one-to-one in post order, and `action` matched the landing on every capture.

**Test-tenant duplicate caveat, still standing.** The tenant holds a plain **"Grocery list"**, which rows 21 and 32's precondition forbids. It also holds "Roof repair" twice, "Dentist" once and eleven "Gutter leak" notes, and five notes carry `house`. Every row-21 miss went into that pre-existing "Grocery list".

### Gate rows (three runs each)

| # | Utterance | Run 1 | Run 2 | Run 3 | Now | After r6 | After #172 |
|---|---|---|---|---|---|---|---|
| 18 | create a shopping list and add chickpeas and green gram into it | pass: Shopping list +Chickpeas / Green gram (`title`) | pass (`title`) | pass (`title`) | **3/3** | 3/3 | — |
| 19 | Add umbrella to shopping list | pass: +Umbrella (`model`) | pass | pass | **3/3** | 3/3 | — |
| 20 | add milk to the shopping list | pass: +Milk (`model`) | pass | pass | **3/3** | 3/3 | — |
| 21 | add milk to my groceries list | pass: new checklist "Groceries list" +Milk (`none`/new) | **FAIL**: appended "Milk." to the pre-existing plain **"Grocery list"** (append, `model`, conf 1) | pass: new "Groceries list" | **2/3** | **3/3** | — |
| 22 | packing list for the weekend passport charger sunscreen and the travel adapter | pass: new "Packing list for the weekend" ×4 items (conf 0.9) | pass: new "Weekend packing list" | pass | **3/3** | 3/3 | — |
| 31 | Add milk, eggs and protein powder to shopping list | pass: +Milk / Eggs / Protein powder (`model`) | pass | pass | **3/3** | 3/3 | — |
| 32 | Groceries list milk eggs and protein powder | pass: new "Groceries list" ×3 items | **FAIL**: appended the three items to the set-up **"Shopping list"** checklist (append, `model`, conf 1) | pass | **2/3** | **3/3** | **3/3** |

**Supplementary runs 4–6 of rows 21 and 32.** These were run after the gate and are not part of it:
- Row 21: 1 of 3. Runs 5 and 6 appended "Milk." to the plain "Grocery list" again (`model`, conf 1).
- Row 32: 3 of 3 as a new "Groceries list" checklist.

Over six runs that is row 21 **3/6** and row 32 **5/6**.

**Gate verdict for `c637473`: regressed, rows 21 and 32.** Both fell from 3/3 to 2/3. Under the agreed rule, revert that commit alone.

The two misses fail in opposite directions:
- Row 32 run 2 went to a listed *checklist* with a different name. That is the direction the new Kind sentence pushes ("an item for a list goes to a listed checklist").
- Row 21 went three times to a *plain* note with a near name, at confidence 1. That is the exact case the marker was meant to stop, and it did not.

So the marker did not fix row 21 on this tenant, and it plausibly caused row 32's miss. Before #134 row 21 was 1/3, so part of row 21 may be run-to-run spread on the duplicate. The rule is the rule all the same.

### Sweep rows (one run each)

| # | Result | Landed (`matched_by`) | After r6 |
|---|---|---|---|
| 1 | pass | Roof repair "The gutter is leaking again." (`model`) | 3/3 |
| 2 | pass | Roof repair "The gutter is leaking again." (`model`) | 2/3 |
| 3 | pass | Roof repair "The downpipe bracket has come loose." (`model`) | 3/3 |
| 4 | pass | `no_content`, new "test123" (`none`) | 3/3 |
| 5 | pass | new "test 1,2,3" "Cyclops lived in a cave herding sheep." | 3/3 |
| 6 | pass | Dentist "I need to book a cleaning before December." (`title`) | 2/3 |
| 7 | pass | new "Dentist appointment" "Remind me to book the dentist on tuesday." (tidied, see §3) | 2/3 |
| 8 | pass | new "Roof thoughts and Portugal trip budget" (conf 0.3, new) | 1/3 |
| 9 | pass | `no_content`, Pebble Ring Test (`model`) | 1/3 (3/3 after #170) |
| 10 | pass | Shopping list +Eggs | 3/3 |
| 11 | pass | Roof repair "ഗട്ടർ വീണ്ടും ചോരുന്നു." | 3/3 |
| 12 | **FAIL** | `no_content`, new note titled "ദന്തഡോക്ടർ നാളെ വിളിക്കണം": the whole sentence became the title and the body is empty (F4) | 3/3 |
| 13 | pass | new "staging smoke" "and then the actual content…" | 3/3 |
| 14 | pass | Roof repair "Okay so we need to check the flashing around the chimney." (`model`) | 1/3 (3/3 after #170) |
| 15 | FAIL (park) | `needs_target`, suggestion at conf 0.5 | 0/3 |
| 16 | pass | Reading list "The new Le Guin collection is out in October." | 3/3 |
| 17 | pass | Kitchen rebuild "Money we are forty thousand over on the kitchen." | 1/3 |
| 23 | pass | App feedback "move seems to be good…" (`prefix_title`) | 3/3 |
| 24 | pass | Business ideas "By Priyanka seated pool for dogs." | 3/3 |
| 25 | pass | new "Things to talk with Milos" "Appreciation for the team." | 3/3 |
| 26 | pass | App feedback "And the push to talk icon does not look good." | 3/3 |
| 27 | pass | new "Customer visits" "And add the fact that the about screen is long." | 3/3 |
| 28 | pass | App feedback "We have made a lot of changes this week." | 2/3 |
| 29 | pass | Daily report "The memory controller is alive at eight gigabits." | 3/3 |
| 30 | pass | new "Dog dinner" (conf 0) | 1/3 (the long title after #172) |

23 of 25 pass. Row 12 is one run and has no three-run score here. Row 12's text does not name a checklist, so the marker cannot touch it, but see F4.

## 2. Items battery

Run 18:03:32Z–18:04:45Z on one "Shopping list" checklist, with the key "R7Q items". **15 of 16.**

| Step | Input | Added | OK |
|---|---|---|---|
| L1 R | Add umbrella to shopping list | Umbrella | ✓ |
| L2 R | create a shopping list and add chickpeas and green gram into it | Chickpeas, Green gram | ✓ |
| L3 R | put milk, eggs and two loaves of bread on the shopping list | Milk, Eggs, Two loaves of bread | ✓ |
| L4 R | shopping list: batteries, dish soap | Batteries, Dish soap | ✓ |
| L5 T | I also need coriander | Coriander | ✓ |
| L6 T | Buy a birthday card for Anu and post it by Friday | **A birthday card for Anu**, Post it by Friday | ✗ (want "Birthday card for Anu") |
| L7 T | two and a half kilos of onions and 500 ml of coconut oil | both | ✓ |
| L8 T | ഒരു കിലോ അരി വാങ്ങണം | ഒരു കിലോ അരി | ✓ |
| L9 T | Call the dentist tomorrow morning. Oh and we are out of dish soap. | both | ✓ |
| L10 R | This is not for the shopping list… plumber… | new note "Plumber coming on Tuesday" | ✓ |
| A–F | tree, dedupe, tick survival, reopen | Milk/Egg/Protein powder; Walmart › Eggs, Costco › Meat; Umbrella routed in; duplicate Milk dropped; ticks survive "I also need coriander"; Milk reopened in place, still once | 6/6 ✓ |

## 3. Capture path through the API

The key "R7Q capture" was fresh and was revoked afterwards. The targeted captures went into "R7Q tidy" through `X-Chintan-Note-Id`.

| Check | Input | Result | Verdict |
|---|---|---|---|
| Short text is tidied (8 words) | `the plumber comes on thursday after lunch` | Filed exactly **"The plumber comes on thursday after lunch."**: first letter up, full stop, "thursday" left lower-case, so no model rewrite | PASS |
| Short text, "i" | `i think the boiler needs a service` | "I think the boiler needs a service." | PASS |
| Digit first | `3 eggs and milk` | **"3 eggs and milk."**, not "3 Eggs" | PASS |
| Routed new note | `Create a note with the title R7Q wire check the gate latch is broken again` | new "R7Q wire check", "The gate latch is broken again.", `created_note: true`, `excerpt: "the gate latch is broken again"` | PASS |
| Routed append | `Add this to my R7Q tidy note the side gate needs oil` | appended "The side gate needs oil.", `created_note: false` | PASS |
| 3 s tone, 440 Hz, 16 kHz mono WAV, `/v1/inbox/audio` multipart | — | `no_content`, no note | PASS |
| 3 s digital silence, same shape | — | **`appended`: new note "Thank you", body "Thank you.", `created_note: true`, `excerpt: "Thank you."`**. Reproduced twice more; the second went into the same "Thank you" note | **FAIL (F1)** |

The wire carries `excerpt` and `created_note` on every `GET /v1/captures/{id}`. Across the routing battery `created_note` was true on every routed new note (including the `no_content` new notes of rows 4 and 12) and false on every append. The receipts in §4 use both.

The tidy also shows in the battery: in row 7, 8 words, "tuesday" stays lower-case where the model used to write "Tuesday".

## 4. Live UX

Viewports were 390×844 touch (Pixel 7) and 1280×800 mouse, each in Ink & Paper and Nocturne.

- **Find in note (F1 check).** "roof" in "R7Q find note" gave 5 marks. The mirror computes `display: block`, every mark is `inline`, 3 marks share the first line, and each mark is 27 px wide against a 350 / 632 px mirror, so there is no grid row and no full-width bar. Pass ×4.
- **Scroll restore.**
  - Home: a finger fling or wheel to about 1500–6500 px, open a row, Back. The offset came back exactly on desktop (1500 → 1500) and on mobile (6495 → 6495, 5164 → 5164, 5602 → 5602).
  - Archive (30 archived `R7Q archive` fillers): 1500 → 1500 on desktop and 2999 → 2999 on mobile, which was the end of the list.
  - The first mobile-Nocturne run read 3770 → 3447 because the harness measured while the fling was still moving. A settle check then showed the list still growing, and the three re-runs pass. Pass ×4.
- **Record into a long note.** The fake-mic recording `qa0924-speech.wav` was made from 800 px down "R7Q long note" (40 paragraphs).
  - The banner read "Added at the end / Show" 2.5 s after Send.
  - Show brought `mark.note-flash` into the viewport in 470–700 ms, and focus then went to the textarea with the caret at the start of the added paragraph.
  - On mobile the mark sits fully visible above the bar.
  - **On desktop the mark ends at or under the bottom bar** (F2).
- **Tick Undo.** Ticking Milk in "R7Q checklist" showed the toast "Milk done" with Undo, and the server read `- [x] Milk`. After Undo the server read `- [ ] Milk` and the box was unchecked. Pass ×4.
- **Receipts.**
  - With one capture, Home shows one receipt, `Filed into “R7Q receipts A” · now`, as one line (the title height is one line-height, the head 21 px), with the excerpt beneath.
  - With four captures into three notes (2 → A, 1 → B, and 1 routed that started "R7Q started note"), the fold reads **"4 filed into 3 notes"** with **Clear all**. Expanded, it shows `Started “R7Q started note”` (`data-started`, plus icon), `Filed into “R7Q receipts B”` and `2 filed into “R7Q receipts A”`, each one line with an excerpt.
  - Clear all removes the fold, and a reload keeps it cleared.
  - Pass ×4.
- **Capture pill.** It reads "Chintan decides ▾" with no "Into", and the sheet's first option is "Chintan decides". Pass ×4.
- **5xx.**
  - With Home loaded once, `GET /v1/notes` was routed to a 503 problem and the page reloaded. It showed "Chintan’s server didn’t answer — showing notes saved on this device." with **Retry**. After unrouting, Retry cleared the caption.
  - In a fresh context with no local copy, Home shows the problem's `detail` ("R7Q injected outage") with "Try again".
  - Pass ×4.
- **Console.** In-app flows (Home, scroll, receipts) logged nothing. Direct loads of `/notes/{id}` and `/capture` log the one Pages `404.html` fallback (D-2, pre-existing). The err flow logs only the 503s it injected. One mobile scroll run logged Chrome's "Ignored attempt to cancel a touchstart event with cancelable=false", from the harness's CDP fling.
- **Stranded capture.** `c_18da066315ace8dc_99879cccfdb28f8e` was `uploaded`, `has_audio: true`, from `2026-09-30T06:47:04Z`, which is 11 h old. Before deletion it showed on Home as "Waiting for the device that recorded it · Upload Retry Dismiss". `DELETE /v1/captures/{id}` → **204**. After that, `GET` → 404, it is gone from `GET /v1/captures`, and a second `DELETE` → 404.

## Findings

**F1 (bug): three seconds of silence files "Thank you." as a new note.**
- Repro: `POST /v1/inbox/audio` multipart with `audio` = a 3.0 s, 16 kHz, mono, 16-bit WAV of zeros (`r7q-capture.py`'s `wav(3.0, 0)`), untargeted, with a device key.
- Result: `appended`, `language_detected: English`, a new note **"Thank you"** with body "Thank you.", and `created_note: true`. A second post appends another "Thank you." to the same note. It reproduced 3 of 3.
- The same shape with a 440 Hz tone ends `no_content`, as it should.
- This is Whisper's classic silence hallucination. The R7-10c gate (`provider.Transcription.NoSpeech`, `stt.go`) needs `no_speech_prob > 0.6` *and* a low `avg_logprob`, and the fluent "Thank you." evidently passes the second test.
- A 3 s clip is also over the 1.5 s line where R7-10b starts sending the spelling prompt.
- A ring or pocket recording of silence will make junk notes.

**F2 (bug, desktop): Show can leave the marked paragraph under the bottom navigation bar.**
- Repro: at 1280×800, open a long note, scroll 800 px, Record into this note, Stop, Send, then press Show on "Added at the end".
- Run 1 (Nocturne): the mark settled at 785–802 px against a bar top of 707 px, and the screenshot `r7q-landed-desktop-nocturne-02-show-mark.png` shows the new paragraph hidden behind Home / Record / You.
- Run 2 (Ink): the mark sat at 693–710 px, flush with the bar.
- The scroll aims at the viewport's bottom and not the area above the bar; `toBeInViewport` in the e2e cannot see the overlap. Mobile is fine: the mark sits above the bar.

**F3 (note): items L6 keeps the article.** "Buy a birthday card for Anu and post it by Friday" now gives "A birthday card for Anu". It was "Birthday card for Anu" on every earlier run. This is one run and cosmetic.

**F4 (note): sweep row 12 took the whole sentence as the title.** "പുതിയ കുറിപ്പ് പേര് ദന്തഡോക്ടർ നാളെ വിളിക്കണം" made a new note titled "ദന്തഡോക്ടർ നാളെ വിളിക്കണം", ended `no_content` and left an empty body (was 3/3). It was one run, so it needs three before it counts.

**F5 (note): the receipt excerpt is the transcript, not the filed text.** The excerpt reads "the bins go out on wednesday night" and "the plumber comes on thursday after lunch" while the note got the tidied "The … ." `model.CaptureExcerpt`'s comment says the cleaned text once there is one. That holds for the model clean ("the ladder is in the shed…" was also left lower-case by the model), but on the tidy path the excerpt seems to be cut before the tidy. This is harmless.

**F6 (note): the capture pill is disabled without mic permission.** With `--deny-permission-prompts`, `/capture` renders the pill disabled, so no destination can be chosen. That is probably intended, since there is no recording to file, and is recorded for completeness.

**Test-tenant caveat.** The tenant's plain "Grocery list" is the note rows 21 and 32 are designed never to find. Every row-21 miss landed in it. A tenant without it would read row 21 differently, but the gate was defined on this tenant, and it regressed there.
