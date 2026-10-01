# Live QA after round 8 (#190–#199)

**Target:** https://vppillai.github.io/chintan/dev/ and the prod API. The checks ran 2026-10-01 03:31–04:08 UTC (30 Sept evening, Pacific), on the test tenant only (`claude-test@example.com`). The harness was dry-run against the partial build from 03:31. Every result below comes from the final build, after 03:47Z.

**Build under test:** all ten PRs were merged and the last deploy of each side was green before the final matrix ran.

| PR | Merge commit | Merged (UTC) | Deploy green |
|---|---|---|---|
| #190 F9 filing notices as an inset tray | `6680d23` | 01:45:59 | Frontend 01:46:59 |
| #191 R8-F1a three checklist levels in parsers, merge, Split up | `cb0dd74` | 01:58:08 | Backend 02:04:34 (run 36803600953), Frontend 01:59:08 |
| #192 filing tray polish | `ea4a6f2` | 02:04:59 | Frontend 02:06:06 |
| #194 R8-F1b edit three levels | `2780958` | 02:22:01 | Frontend 02:22:52 |
| #193 R8-F5/F6/F7 hold the record disc to talk; delete /talk | `8168fa5` | 02:33:28 | Frontend 02:34:33 |
| #195 R8-F8 Tidy up list; no auto-clean for checklists | `b7b63c3` | 02:58:12 | Backend 03:04:06 (run 36808317767), Frontend 03:05:02 |
| #196 R8-F4 directional swipe enter, sliding pill | `3706d03` | 03:12:16 | Frontend 03:13:05 |
| #199 R8-F8b Regenerate poll timer | `947ae52` | 03:25:16 | Frontend 03:26:09 |
| #197 R8-F2 Home at the bottom of the history stack | `52990fb` | 03:34:23 | Frontend 03:35:20 (run 36811128552) |
| #198 R8-F3 banner mic while typing | `4d1ae6d` | 03:44:03 | Frontend 03:45:18 (run 36811868575) |

- **Bundle and deploys.** The served bundle was `index-CpYHKuFO.js`. It carries `banner-record`, "Tidy up list" and "Slide to cancel". No deploy ran during the final matrix.
- **Backend.** #191 and #195 are the only PRs that touched the backend, and both backend deploys were green.

**Tooling:** everything ran on orb, one run at a time.
- **UX harness.** Playwright (chromium, headless) through `~/r3/live/r8q-lib.js`, `r8q-ux.js`, `r8q-setup.js`, `r8q-post.js` and `r8q-run.sh`, built over `qar6-lib.js` → `qaw1-lib.js`. `r8q-run.sh <flow>` runs one flow at 390×844 touch (Pixel profile) and 1280×800 mouse, each in Ink & Paper and Nocturne.
- **Fake microphone.** Chromium's `--use-fake-device-for-media-stream` and `--use-fake-ui-for-media-stream` feed `qa0924-speech.wav`.
- **Keyboard.** qar6's stand-in `window.visualViewport` emulates the keyboard: `__qaKeyboard(400)` shrinks it and fires `resize`.
- **Raw data.** `qaw1-r8q*-{results,reqs}.json` and `qaw1-r8q*.log`, plus screenshots `r8q-*.png`.

**Data and cleanup:**
- **Start state.** 57 active notes, 0 archived, no devices.
- **Created.** 8 `R8Q …` notes (depth, tidy, prose, checklist, ptt, long note, receipts A/B), 30 archived `R8Q archive NN` fillers and the key "R8Q ux". There were 22 captures: 18 PTT sends into "R8Q ptt" and 4 inbox posts. The inbox posts went into receipts A ×2 and B, plus one routed "Create a note with the title R8Q started note…", which made **"R8Q started"**. That was the only note created by routing, and it carried the `R8Q` prefix.
- **Every capture accounted for.** Every capture created after 03:30 was in the harness's list (22 of 22, all `appended`). The cancels, Esc and `/capture` discards created **no** capture.
- **Cleanup.** Every capture was deleted (22 × 204), and every note was deleted and purged (39 × 204). The key was revoked (204), and it then answered 401.
- **End state.** 57 active, 0 archived, no `R8Q` note anywhere, and `GET /v1/devices` is empty. No pre-existing note was touched.

## Summary

| Ask | Verdict | Checks |
|---|---|---|
| **F1** three checklist levels | **PASS ×4** | Tab 0→1→2 (a third Tab stays at 2); sideways grip drag +30 px = 1 level, +55 px = 2 with `data-preview-depth`; reload keeps depth (screen and server body); ticking a parent ticks children and grandchildren; reopening a level-3 item reopens both ancestors. 24/24 |
| **F2** app-like Back | **PASS ×4** | You→About→Home→You→About is 2 entries above Home, then Back gives You, Home, out; Archived chip → Back = Home; deep link to a note → Back = Home → out; note → You tab → Back = Home; "‹ You" on About → You → Back = Home. 20/20 |
| **F3** banner mic while typing | **PASS** (mobile ×2; desktop never shows) | It appears only with the keyboard up and a field focused. It sits inside the 47 px banner row, the banner height is unchanged, and it covers no text. Tap → `/capture?note=<id>`, with the typed text PATCHed first and recording started. Without a keyboard, or with no field focused, it is hidden |
| **F4** tab swipe animation | **PASS** (mobile ×2, desktop pill ×2) | Frame samples: the panel translate tracks the finger 1:1 and the pill slides with the drag. On release the new panel enters from the side the finger left (+254 → rest on a left swipe, −214 → rest on a right swipe) and decelerates with no overshoot. The edge rubber-bands (43 px for 156 px of finger). On desktop a tab click slides the pill |
| **F5/F6** hold-to-talk on the disc | **PASS ×4** | Hold → release sends; slide left 115 px cancels with **0 uploads**; slide up locks, then Send sends; tap opens `/capture?note=`. Desktop: mouse hold, drag-left cancel, drag-up lock, **Space** on the focused disc, **hold R**, **Esc** cancels with 0 uploads, R in a field types, and a quick R does nothing |
| **F7** /talk gone | **PASS ×4** | `/talk` → Home; the manifest has only "Record a thought → /capture"; You has no PTT row |
| **F8** Split up replaced | **PASS ×4** | A checklist shows Items · Recordings only, and `?tab=cleaned` lands on Items. ⋮ → Tidy up list: "Tidying the list…", then "List tidied: 1 line → 4 items." with Undo, the body is written, and Undo restores it. Prose → checklist auto-tidies to "Made a checklist: 2 paragraphs → 5 items." (4 on one run), and Undo restores the two paragraph items |
| **F9** Filing tray | **PASS** with two polish findings (F2, F4 below) | An h2 "Filing" labels the section (`aria-labelledby`). The tray is an inset surface (Ink `#f2eee4` with an inset shadow against the rows' `#f5f1e8`; Nocturne `#141816` against `#1c211f`), with hairline dividers and a glyph per kind (moving, needs, failed, filed, started). Receipts and failed rows carry an always-visible 44×44 × aligned with the title (dy 0–1 px). **A `needs_target` row has no ×** (F2) |
| Round 7: receipts, excerpt | PASS ×4 | One receipt is one line with an excerpt and its × dismisses it. Four captures into three notes fold to "4 filed into 3 notes" with Clear all. The fold shows Started “R8Q started” (kind `started`, plus glyph), and Clear all survives a reload. The wire `excerpt` and `created_note` are right on all 4 posts |
| Round 7: scroll restore | PASS ×4 | Home 5599→5599, 5073→5073, 1500→1500 ×2; Archive 2950→2950, 2999→2999, 1500→1500 ×2 |
| Round 7: tick Undo | PASS ×4 | "Milk done · Undo"; Undo unticks on the server and on screen |
| Console | PASS, with one finding (F1 below) | Only the known Pages 404 on direct loads (D-2) and `/v1/push/key` 404 on You (instance not configured). Plus **one 409 on every Tidy / convert** (F1) |

No blockers and no bugs. Four polish items and three notes follow.

## Detail

### F1: three levels
The note "R8Q depth" was reset to six flat items before each run (Costco, Meat, Rice, Party, Plates, Paper ones).

- **Tab.** Tab on Meat made it a child of Costco. One Tab on Rice made it Meat's sibling, and a second made it Meat's child. A third Tab left it at level 3.
- **The level-3 row.** It has `aria-level="3"`, the label "Sub-item 3, level 3", and a 48 px inline-start padding.
- **Grip drags.** On mobile these were CDP touch drags; on desktop, mouse drags. Plates moved +30 px and the preview read `data-preview-depth="1"`, at about 24 px. Paper ones moved +55 px and the preview read `"2"`, at about 45 px. The status line said "Made a sub-item of “Plates”".
- **Server body after the drags.** `- [ ] Costco\n  - [ ] Meat\n    - [ ] Rice\n- [ ] Party\n  - [ ] Plates\n    - [ ] Paper ones`. A reload rendered the same depths.
- **Ticks.** Ticking Costco wrote `[x]` on all three of its levels. Ticking Rice back open reopened Meat and Costco.
- Screenshot: `r8q-depth-*-01-three-levels.png`. The indents are even, the grip and box step in together, and nothing clips at 390 px.

### F2: Back
- **The owner's walk.** It runs from `about:blank`, then Home, You, About, the Home tab, You and About. `history.length` grew by exactly 2 over Home. Back went to `/settings`, then `/`, then `about:blank`.
- **The Archived chip.** It added one entry, and Back landed on Home with the list shown (50 rows).
- **A cold deep link to a note.** Back went to Home, then out.
- **A note, then the You tab.** That left one entry over Home, and Back went to Home.
- **The other flows.** "‹ You" on About replaced back to You. All of these were the same on touch and mouse, in both themes.

### F3: banner mic
- **Geometry.** With the keyboard emulated at 400 px and the body focused, `:root[data-keyboard]` was set. The mic showed at x 330, y 1, 44×44, inside the 0–47 px banner. `.app__main` starts at 47, and the hit-test at the button's centre is the button itself.
- **Theme.** Ink: background `#f5f1e8`, glyph `#1a1917`. Nocturne: `#1c211f` and `#f2f5f3`. It does not wear the accent.
- **Tap.** Typing " R8Q typed" and then tapping saved the PATCH (200) before `/capture?note=<id>` opened. The recording started there and was then discarded, with no capture created.
- **Hidden when it should be.** With no field focused it is hidden, and focused with no keyboard it is hidden. On desktop it never shows, because nothing sets `data-keyboard` there. The harness forced the attribute on desktop and the mic then appeared, which is the CSS working as specified rather than a defect.
- Screenshot: `r8q-banner-mobile-*-01-keyboard-mic.png`.

### F4: swipe
These are CDP touch frames at 390 px, on "R8Q long note" (tabs Text · Cleaned · Recordings).

| | During the drag (per 14 px of finger) | After release (per rAF) |
|---|---|---|
| Swipe left 170 px | panel translate 0, −14 … −156; pill x 24 → 70 | Cleaned selected; panel x −136, **254**, 254, 254, 164, 125, 99, 79 … 22, 20 (rest is 20, the gutter); pill 70 → 139 |
| Swipe right 170 px | translate 0 … +156 | Text selected; panel x 176, **−214**, −100, −56, −28, −9, 4, 13, 18, 20 |
| Swipe right on the first tab | translate 0, 11, 19 … 43 (rubber band) | stays on Text |

The direction is right both ways, the pill moves with the finger, and the settle is a deceleration with no overshoot. On desktop a tab click slides the pill 328 → 537 px over about 13 frames.

### F5/F6: hold-to-talk

**The disc at rest.**
- `aria-label`: "Record into this note".
- `aria-description`: "Hold to talk and release to send. While holding, slide up to lock or left to cancel."
- `aria-keyshortcuts`: "R".
- `title` on desktop only: "Click to record · hold to talk (or hold R)".

**On touch.**
- **Holding.** `nav[data-hold=holding]`, with the clock pill "00:02 · Into this note" after about 2.3 s, "‹ Slide to cancel", the lock pill and the level.
- **Release.** Shows the notice "Sent", with the capture POST and its upload seen.
- **Cancel.** A slide left in 11.5 px steps was still holding at −80 and showed "Cancelled" at −115. No POST and no PUT were seen.
- **Lock.** A slide up to −80 locked. The disc became "Send recording", and Discard and Stop took the tab slots (`r8q-ptt-*-03-locked.png`). Send sent.
- **Tap.** A tap under 250 ms opened `/capture?note=<id>`.

**On desktop**, the same with the mouse, plus:
- Space held on the focused disc sends on keyup.
- R held with focus outside any field sends; a quick R press does nothing.
- R held with Esc pressed cancels, with 0 uploads.
- R held in the note body types an "r" and starts nothing.

**What landed.** All 18 sends settled `appended` in "R8Q ptt", whose Recordings tab showed them. The body grew by the fake mic's "This is a QA test recording…".

### F8: Tidy up list
- **The run.** On "- [ ] Buy milk eggs and bread and also call the plumber about the boiler", ⋮ → Tidy up list showed "Tidying the list…" above the rows. The result was Milk / Eggs / Bread / Call the plumber about the boiler, with the toast "List tidied: 1 line → 4 items." and Undo. It took about 4 s from tap to toast.
- **Undo** restored the original body on the server.
- **Prose to checklist.** Turning on "This note is a checklist" under Details for a two-paragraph note PATCHed `kind: checklist` with the paragraph items, then auto-tidied. The model split was 5 items on three runs and 4 on one; the second sentence was kept whole once.
- **Undo after converting** restored the two paragraph items.

## Findings

**F1 (polish): every Tidy and every prose → checklist conversion logs a 409 before it applies.**
- **Repro.** Open a checklist with a run-on item, then ⋮ → Tidy up list (or Details → "This note is a checklist" on a prose note). Watch the network.
- **Sequence** (`qaw1-r8qtidy-*-reqs.json`, the same 8 of 8 runs):
  - `POST /clean` → 202;
  - the poll `GET` sees the tidy;
  - `PATCH {version: 3, body: <tidied>}` → **409** "the resource changed since you read it";
  - `GET`;
  - `PATCH {version: 4, …}` → 200.
- **Cause.** The worker's write of `cleaned_body` bumps the note version, and the apply PATCHes with the version the editor held before the clean.
- **Effect.** The retry recovers, so the list is right and nothing is lost. But each tidy puts a `Failed to load resource: 409` in the console and costs an extra round trip.
- **Fix direction.** Apply with the version from the refetch that carried the result.

**F2 (polish, F9): a "Which note should this go in?" (`needs_target`) row has no ×.**
- **Spec.** `spec-visual.md` "Controls per row" says needs gets "TargetPrompt plus ×".
- **Code.** `FilingItem.tsx` draws `DismissButton` only for `actionable || no_content`, so a needs row offers only "Choose a note".
- **Repro.** Stub `GET /v1/captures` with one `needs_target` capture that has `targeted: false` (flow `tray`). Screenshots: `r8q-tray-*-01-tray.png`.
- **Decision for the owner.** If keeping it un-dismissable is deliberate (a parked recording has no note yet, so hiding it risks losing it), the spec should say so. Otherwise add the ×.

**F3 (polish, F6): the "‹ Slide to cancel" hint slides off the left edge of a 390 px screen.**
- **What.** The hint moves at dx × 0.5. By about −80 px of finger it is clipped at x = 0, reading "Slide to / ancel" in destructive orange (`r8q-ptt-mobile-ink-02-slide-to-cancel.png`).
- **Repro.** At 390×844 touch, hold the disc and slide left about 80 px without releasing.
- **Fix direction.** Clamp the hint's translate to the gutter.

**F4 (polish, F9): "Clear all" sits flush with the tray's right edge.**
- **What.** The fold row's "Clear all" pill ends at 952 px against the tray's 956 px edge at 1280, and at 866 against 876 at 390. Every × in the same tray is inset by the row padding.
- **Repro.** Post two or more captures into notes and open Home (`r8q-receipts2-*-02-fold-open.png`, `r8q-tray-desktop-ink-01-tray.png`).

**F5 (note): the clock pill reads "00:02", where the spec says "0:04".** This is the existing `formatElapsed`. It is cosmetic.

**F6 (note): typing R in the note body still types an "r".** That is correct: the global R is ignored in fields. It is recorded because it shows the test writing into the note ("This is a QA test recording.r"); the note was purged.

**F7 (note): not covered here.**
- iOS: the long-press loupe and `-webkit-touch-callout`.
- Real Android behaviour: haptics, system Back in the installed PWA, and a real on-screen keyboard. CDP cannot raise one, so F3 used the stand-in `visualViewport`.
- `prefers-reduced-motion` for the PTT, tray and swipe animations.
- The first-run microphone permission prompt. The fake UI grants it.

These need the manual phone check that the specs already list.
