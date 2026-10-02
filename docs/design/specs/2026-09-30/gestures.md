# R8 spec: gestures. F5 + F6 + F7 (push-to-talk on the record button), F2 (back navigation), F3 (recording while typing)

Design only. The checkout is `main` @ 3ac3b2c. Paths are relative to `chintan/frontend/` unless they start with `docs/`. Mockups: `mock-gestures-{holding,locked,typing}.svg` beside this file. I rendered and checked each one.

---

## 0. The owner decisions this reverses

| Earlier decision | New feedback | Why the new feedback wins |
|---|---|---|
| 2026-09-27 feedback, backlog row **F4** ("fb0927/pttscope"): "The PTT on the recording icon on a normal screen is not what I asked for. I want it only on the widget, not the main app." The tab-bar disc became tap-only, and `/talk` became the only PTT surface. | **F5**: "the record button acts as PTT by default (hold to talk)… WhatsApp-like". **F7**: "PTT as a home-screen feature … is not useful … Clean it up." | On 09-27 the owner assumed the "widget" (`/talk` plus the manifest shortcut) would be the push-button surface. He has now tried it and rejects it (F7), and he asks for the hold on the main record button. The 09-27 complaint was about the hold of #85: a 350 ms delayed hold with no lock, a cancel that did not work, and a "PTT" caption. It was not a complaint about holding as such. The WhatsApp model below is a different gesture with a visible lock and a visible cancel. I state the reversal here so the implementer does not "restore #85". |
| R5-BR-P3 (walkie-talkie glyph on `/talk`) | F7 | The glyph goes when `/talk` goes. Nothing replaces it. The disc keeps the mic. |
| R4-16 (a You row "PTT" as the way into `/talk`) | F5 | The row is deleted. The disc is the way in. |

**Owner decision D0, confirm the reversal.** Default: yes.

---

## 1. F6: why slide-to-cancel never works today (reproduced)

**Root cause: geometry.** `useHoldToTalk.ts:226-229` measures "away" from the button's bounding-box edge. It needs `SLIDE_AWAY_PX = 80` (`holdTiming.ts:12`) beyond that edge. The `/talk` disc is `inline-size: min(100%, 26rem, 45svh)` (`talk.css:60`). On a phone that is the full column, so there is no 80 px beyond the edge to slide into.

Headless Chromium on orb, Pixel 8 Pro profile (412×915), live `/chintan/dev/talk`, fake mic, CDP touch (`r3/live/r8d-ptt.js`). Every hold ended with Escape, so nothing was sent and there were 0 uploads.

```
geometry {"vw":412,"left":20,"right":392,"top":308,"bottom":680,"w":372}
left screen edge x=1      → away:null  "Release to send"
right screen edge x=411   → away:null  "Release to send"
corner of disc bbox       → away:null  (visually outside the circle, inside the square)
above disc 79 px          → away:null
above disc 120 px         → away:true  "Release to cancel"
below disc 120 px         → away:true
desktop 1280×800: 79 px left of edge → null; 120 px → true
```

- **Horizontally, cancel is impossible on any phone.** The disc leaves 20 px each side, and the threshold is 80 px past the edge, which is off-screen.
- **Vertically,** the finger must leave the 372 px disc by 80 px. Going up lands on the target pill; going down lands on the hint and the tab bar. Nobody discovers this. The copy says "Slide away", with no direction and no affordance (`TalkScreen.tsx:156`).
- The test is against the bounding box, so the area outside the circle at the corners counts as "on the button".
- The only feedback is the label changing after the threshold is crossed. Nothing shows progress.
- **Prior QA missed it.** `r3/live/r4-ux-talk.js:66-70` slid +140 px from centre, logged `away:null` in all three runs (`r4-ux-talk-*.log`) and still passed, because it only judged contrast.
- Desktop does work: the disc is 360 px in a 1280 px column. That is why it looked fine whenever someone tested with a mouse.

**Secondary findings, fixed by the redesign:**
- `TalkScreen.tsx:91`: window `blur` calls **cancel**, which discards. The hook's own rule for an interruption is "release, never discard" (`useHoldToTalk.ts:234-244`). The two disagree.
- Releasing during `requesting` (the first-ever press raises the permission prompt, and the finger has to lift to answer it) is discarded as "Too short" (`useHoldToTalk.ts:188`). So the first press on a fresh install always fails.

---

## 2. F5 / F6: push-to-talk on the record button

### 2.1 Options

| | What | For | Against |
|---|---|---|---|
| **A** | Fix the `/talk` threshold only (press-point-relative, slide-left) | Smallest change | Does not meet F5 (PTT stays hidden) or F7 |
| **B (recommended)** | WhatsApp model on the tab-bar disc, on every screen that shows the bar. Tap opens `/capture` as today. Hold = PTT. Slide up = lock (hands-free, in place). Slide left = cancel. Delete `/talk`. | One control and one place. Taps behave exactly as they do today. The gesture everyone knows. Desktop works with the same pointer code. | The most code. The hold must coexist with the tap (a 250 ms arm). |
| **C** | Like B, but **lock hands off to `/capture`** (route change mid-utterance) instead of a locked bar in place | Reuses the full capture UI, with no new locked bar | The screen swaps under a finger that is still down, the tab bar vanishes while you speak, and you can no longer see the note you are recording into. That is not WhatsApp. |

**Owner decision D1.** Locked recording stays in place (B, default) or goes to `/capture` (C).

### 2.2 Tap: recommendation

**A tap opens `/capture`, as today.** Opening `/capture` already starts recording (`CaptureScreen.tsx:56-84`). So a tap already gives a hands-free recording, with the full screen: Pause, Stop, review, the target chooser and Discard. Nothing the owner uses today changes.

Rejected alternatives:
- **"A tap starts a locked recording in place":** this puts two hands-free modes side by side and removes the only route to Pause and review that does not need a gesture.
- **WhatsApp's "a tap only shows a tooltip":** this kills the primary flow.

**Owner decision D2.** Default: tap opens `/capture`.

### 2.3 Constants (`features/capture/holdTiming.ts`, replaces the file body)

```ts
export const HOLD_ARM_MS = 250;        // press longer than this = hold; shorter = tap
export const TAP_SLOP_PX = 10;         // movement beyond this before arm arms at once (a fast slide)
export const MIN_TALK_MS = 600;        // unchanged: less audio than this is a slip
export const CANCEL_DX_PX = 110;       // leftward from the PRESS POINT; cancels on crossing
export const LOCK_DY_PX = 72;          // upward from the press point; locks on crossing
export const HOLD_NOTICE_MS = 1_500;   // unchanged
export const DISCARD_CONFIRM_AFTER_MS = 10_000; // a locked take this long asks once before Discard
export const DISCARD_CONFIRM_MS = 3_000;        // how long "Discard?" stays armed
export const CLICK_SUPPRESS_MS = 600;  // the click a browser sends after a hold is swallowed
```

`SLIDE_AWAY_PX` and edge-relative measuring are deleted.

The thresholds are relative to the press point. They fit the narrowest viewport:
- At 320 px the disc centre is at 160, so cancel triggers at x = 50, still 30 px inside the 20 px gutter.
- At 412 px cancel triggers at x = 96.
- Lock at −72 px puts the finger 64 px above the bar's top edge. The bar is 93 px tall and the disc's top is at y 831 of 915.

### 2.4 States and transitions

A pure reducer (`features/capture/holdGesture.ts`, new) decides the transitions. The hook only feeds it events and runs the effects. This is the file the unit tests target.

```
idle ──pointerdown/Space/R keydown──▶ armed (no mic yet)
armed ──up before 250 ms, moved < 10 px──▶ idle; the browser's click = TAP → navigate /capture[?note=]
armed ──250 ms elapsed, or moved ≥ 10 px──▶ holding: store.start(target); startFeedback (40 ms buzz + 660 Hz tone, existing)
holding ──dx ≤ −110──▶ cancelled: store.discard(); vibrate(60); "Cancelled" 1.5 s; the rest of the drag is ignored until up
holding ──dy ≤ −72──▶ locked: vibrate(20); finger may lift
holding ──up, ≥ 600 ms audio──▶ sent: store.stopAndSend(api) (stopFeedback, existing); "Sent" 1.5 s
holding ──up, < 600 ms──▶ hint: store.discard(); "Too short — hold to talk" 1.5 s
holding ──up while `requesting` AND permission was not 'granted' at press──▶ locked (see 2.8)
holding ──pointercancel / lostpointercapture / window blur / page hidden──▶ treated as up (send or too-short). Never a discard.
locked ──Send (disc)──▶ sent
locked ──Stop──▶ store.stop(); navigate /capture[?note=] (review: player, Send, Discard, Re-record, all existing)
locked ──Discard──▶ take < 10 s: discard now. Otherwise the button turns "Discard?" for 3 s and a second tap discards.
locked ──model reaches `review` on its own (call ended the track, 20-min cap)──▶ navigate /capture (review). Never auto-send, never discard.
locked ──page hidden──▶ nothing (same as /capture today; the machine handles a dead track)
```

Rules for when a press does not start a hold:
- **The last recording is still `stopping`/`uploading`:** a hold shows "Still sending the last one…" for 1.5 s (the existing `busy`). A tap still opens `/capture`.
- **Another recording is live, or unsent audio is buffered** (`isCaptureBusy || hasBufferedAudio`): the hold stands down, the release counts as a tap, and `/capture` shows that recording. This is the existing stand-down rule.
- **On an archived note:** the target is a new note. `TabBar.tsx:57-60` already handles this.

Pointer hygiene:
- Primary button only.
- Track one `pointerId` and ignore a second finger.
- `setPointerCapture` on down.
- The disc gets `touch-action: none; -webkit-touch-callout: none; user-select: none`, and `contextmenu` is prevented. Today the disc has none of these. It sits outside `.app__main`, so pull-to-refresh is not involved.
- **Tap happens in `onClick`, not in `pointerup`.** TalkBack and VoiceOver activation, and Enter, send only a click. After any hold, a `suppressClickUntil = now + 600 ms` ref swallows the click Chromium sends after a long press. That is QA observation B-1 in `docs/history/reviews/2026-09-27/qa-feedback-live.md:39`. The ref is reset on the next pointerdown.

### 2.5 What it looks like (412×915; see `mock-gestures-holding.svg` and `mock-gestures-locked.svg`)

Everything stays inside the tab bar row plus two temporary elements above the disc. **Nothing in `.app__main` reflows.**

**Holding:**
- **Disc:** `transform: translate(dx, dy) scale(1.2)`, with `dx` clamped to [−110, 0] and `dy` clamped to [−72, 0]. It follows the finger. It keeps the accent, and a `0 0 0 var(--space-3) var(--color-accent-soft)` ring is added (the same treatment as the held `/talk` disc). The caption stays "Record".
- **Home tab slot:** replaced by "‹ Slide to cancel", in `--font-size-sm` and `--color-muted`, two lines allowed. The text moves with `dx × 0.5` and its opacity goes from 1 to 0.4 as cancel progress `p = −dx/110` approaches 1. From p ≥ 0.6 it turns `--color-destructive` (`#a8380a` in Ink, ink in Nocturne) and a 20 px trash glyph appears before it.
- **You tab slot:** the live level, using `Waveform` in a 64×28 box with `--color-muted` bars.
- Both tab links get `inert` and `visibility: hidden`. They keep their boxes, so the bar does not shift.
- **Clock pill:** sits in the existing `.tab-bar__into` slot above the disc, y ≈ 799–823. It is a 24 px pill in `--color-ink` with `--color-ground` text, showing a 4 px dot and "0:04". On a note it reads "0:04 · Into this note".
- **Lock pill:** 44×100 at x 184–228, y 691–791. Background `--color-raised`, 1 px `--color-line-strong` border, `--shadow-md`, `--radius-full`. It holds an open padlock (20 px, `--color-ink`) at the top and a double chevron-up beneath. The chevron bobs 4 px every 1.2 s. As lock progress `q = −dy/72` rises, the padlock moves down `q × 40` px to meet the finger, and the pill's opacity drops to 0.4 while cancel progress is the larger of the two.
- This is the one transient overlay on content. It exists only while the finger is down, the same way the deleted #85 overlay did.

**Locked:**
- The lock pill collapses into the clock pill, which gains a closed padlock: "🔒 0:12 · Into this note".
- The disc snaps home. Its glyph becomes an arrow-up and its caption becomes "Send".
- **Home slot:** a Discard button (trash 22 px plus the caption "Discard", in the same 66×59 box and type as a tab).
- **You slot:** a Stop button (16 px rounded square plus "Stop").
- **Discard armed:** the button gets a 1.5 px `--color-ink` outline, the label becomes "Discard?" in semibold, and `aria-label` becomes "Tap again to discard".
- The shell's `RecordingIndicator` is hidden while the bar is in hold or locked (`capture.css` gets a `:has(.tab-bar[data-hold])` rule replacing the `/talk` one at `capture.css:728`). Otherwise two things would state one fact.
- The page stays fully usable: you can scroll, read the note and switch tabs. System Back works. The bar is shell-level, so the locked state survives a route change. Only `/capture` hides the bar, and it shows the same recording.

**Notices** ("Sent", "Too short — hold to talk", "Cancelled", "Still sending the last one…", "Microphone blocked") use the same clock-pill slot for 1.5 s.

**Accent rule:** only the disc ever wears `--color-accent`. The pills are ink and ground; the lock pill is raised and line.

**Contrast:**
- Ink pill: #fbf9f4 on #1a1917, about 17:1.
- Nocturne pill: #0a0c0b on #f2f5f3, about 18:1.
- Muted hint: #534f48 on #f5f1e8, about 7.4:1 (Ink); #a4aca8 on #1c211f, about 7:1 (Nocturne).
- Lock-pill border: #8a8478 on #f6f3ec, about 3.4:1; #5e6e66 on #0a0c0b, about 3.6:1. Both are at least 3:1 for non-text.

**Reduced motion** (`useReducedMotion`):
- No translate, scale or bob.
- The disc keeps its place and gains the ring only.
- The hint does not move. Progress shows as opacity and colour steps at p ≥ 0.6.
- The padlock swaps from open to closed on lock instead of travelling.

### 2.6 Screen readers

- **Disc at rest:**
  - `aria-label` "Record" or "Record into this note" (unchanged).
  - `aria-description` "Hold to talk and release to send. While holding, slide up to lock or left to cancel."
  - `aria-keyshortcuts="R"`.
- **Activation** (double-tap) is a click, so it opens `/capture`, the hands-free path. A screen-reader user never needs the gesture. VoiceOver's double-tap-and-hold passes through as pointer events and works as a hold.
- **Locked bar:** buttons named "Discard recording", "Stop and review" and "Send recording". The disc's label becomes "Send recording". Focus moves to the Send disc on lock when the lock came from the keyboard.
- **One polite `role="status"`** in `TabBar` (visually hidden) announces "Recording", "Locked. Recording hands-free.", "Cancelled", "Sent", "Too short", "Microphone blocked". The level and the clock are `aria-hidden`.

### 2.7 Desktop web

- **Mouse:**
  - Click opens `/capture` (hands-free, with Send). This is the desktop's "lock button": it needs no gesture.
  - Press and hold is PTT.
  - Dragging up 72 px locks, and dragging left 110 px cancels. It is the same pointer code; pointer capture keeps the moves coming off the disc.
- **Keyboard:**
  - **Focused disc:** Enter = tap. Space held is hold-to-talk with the same 250 ms arm: a keyup before that is a tap, after it is a release. `event.repeat` is ignored and the default is prevented.
  - **Anywhere** (focus not in an `input`, `textarea`, `select` or `[contenteditable]`, no modifier): hold **R** for PTT. A quick R press does nothing; it never starts a recording by accident.
  - **Esc:** cancels while holding. While locked it is the same as the Discard button, including the confirm for a take of 10 s or more.
  - **Window blur during a key hold:** release (send or too-short). The keyup is lost.
  - Space is not taken globally, because it pages `.app__main` today.
  - **Owner decision D3:** the global key. Default **R**; the alternative is none.
- **Discoverability:**
  1. The native `title` on the disc on a fine pointer only: "Click to record · hold to talk (or hold R)".
  2. About copy: replace "open PTT from You or the home-screen shortcut and hold to talk" (`AboutScreen.tsx:73-74`) with "Tap Record for a hands-free recording, or hold it to talk and let go to send; slide up to lock, left to cancel."
  3. **First-run coach (Owner decision D4, default on):** the clock pill shows "Hold to talk · tap to record" above the disc on Home, on each of the first 3 launches. It goes for good after the first successful hold. It is stored under `chintan.coach.ptt` in localStorage (try/catch). It is `aria-hidden`, because the description already says it.

### 2.8 Microphone permission on first press

- At `TabBar` mount, read `navigator.permissions?.query({name:'microphone'})` inside try/catch. Firefox throws; that counts as `unknown`. Keep `state` live through `onchange`.
- **`denied`:** a hold starts nothing. The pill shows "Microphone blocked" for 3 s and the status line announces it. A tap still opens `/capture`, whose failure line explains (existing).
- **`prompt` or `unknown`:** the hold starts normally and the browser raises its prompt. The finger has to lift to answer it. Releasing while the model is still `requesting` goes to **locked** instead of "Too short". The pill reads "Allow the microphone…". If the user allows, recording proceeds hands-free with Send, Stop and Discard on screen. If they deny, `failed` closes the bar and shows "Microphone blocked". **This fixes "the first press always fails".**
- **`granted`:** releasing during `requesting` (a slow `getUserMedia`) is "Too short", as today.
- The permission prompt blurs the window without hiding the page. Blur only counts as a release for a keyboard hold that has gone past `requesting`.

### 2.9 Interruptions

| Event | Holding | Locked |
|---|---|---|
| Incoming call / OS takes the mic (track ends) | The model settles with audio, so the existing `holdSendable` sends it on release or on hidden | The model reaches `review`, so navigate to `/capture` review: the person decides, nothing is lost |
| Page hidden (app switch, lock screen) | Release: send if ≥ 600 ms, else hint (R4-17 rule, kept) | Keep recording, as `/capture` does today |
| `pointercancel` / `lostpointercapture` | Release (no longer a discard) | n/a |
| Route change (Back mid-hold) | The finger is still on the disc, which persists across routes; the hold continues | Continues; the bar persists |
| 20-min cap | Send on release (existing) | `/capture` review |

### 2.10 Haptics (`feedback.ts`, Android only; iOS has no `vibrate`)

| Moment | Haptic | Tone |
|---|---|---|
| Recording starts (armed becomes holding) | existing `startFeedback`: 40 ms | existing 660 Hz |
| Lock | `vibrate(20)` | none |
| Cancel crossed | `vibrate(60)` | none |
| Send | existing `stopFeedback`: [30, 60, 30] | existing 440 Hz |
| Too short / blocked | existing `errorFeedback`: [80, 80, 80] | none |

Add `lockFeedback()` and `cancelFeedback()` next to the existing ones.

### 2.11 Where it lives

- The tab-bar disc on **every screen that renders the bar**: Home, Archive, a note, You, About and Usage. It records into the open, unarchived note, or into a new note. This is `TabBar.tsx:55-60`, unchanged.
- The note's "Into this note" caption becomes the clock pill's resting text.
- **Extract `useRecordTarget()`** from `TabBar.tsx:55-60` into `components/useRecordTarget.ts`, so F3 can share it.

### 2.12 Files (fe)

- `features/capture/holdTiming.ts`: constants from 2.3.
- `features/capture/holdGesture.ts` (new): the pure reducer. Events are `down`, `tick`, `move{dx,dy}`, `up`, `interrupt`, `modelChanged`. It returns `{phase, progress:{cancel,lock}, effects[]}`.
- `features/capture/useHoldToTalk.ts`: rewritten around the reducer.
  - Adds the `armed` and `locked` phases, plus `tap` handling and click suppression.
  - Measures from the press point.
  - Adds the permission read and keyboard press/release.
  - Deletes the edge-relative `onPointerMove` and `SLIDE_AWAY_PX`.
- `features/capture/feedback.ts`: `lockFeedback` and `cancelFeedback`.
- `components/RecordButton.tsx`: the hook's handlers, glyph and caption per phase (mic/Record or arrow-up/Send), `aria-*`, and `onClick` as the tap.
- `components/TabBar.tsx`: `data-hold={phase}` on the nav, the slot contents per phase, the status region, the global R listener, the coach.
- `components/useRecordTarget.ts` (new).
- `components/Icon.tsx`: `lock`, `lock-open`, `chevrons-up`, `trash`, `stop`, `send` (reuse any that already exist); delete `ptt`.
- `styles/shell.css` (or `capture.css`): the tab-bar hold and locked rules, tokens only, with the reduced-motion block. Replace the `/talk` `:has` rule at `capture.css:721-730`.
- `screens/AboutScreen.tsx`: copy.
- `features/settings/SettingsScreen.tsx:253-258`: delete the PTT row.
- `docs/design/capture-ux.md` §PTT: rewrite. `docs/backlog.md`: rows.

### 2.13 Tests

- `holdGesture.test.ts` (new, pure, fake clock), one case per transition in 2.4:
  - tap < 250 ms
  - slop arming
  - cancel at dx −110 but not at −109
  - lock at dy −72
  - a diagonal where the first threshold crossed wins
  - after a cancel, a later lock-direction move does nothing
  - release < 600 ms gives the hint
  - release during `requesting` with permission `prompt` locks; with `granted` it is too short
  - interrupt counts as a release
  - locked plus `review` gives the navigate effect
  - the Discard confirm at 9.9 s and at 10 s
- `RecordButton.test.tsx`: rewrite the tap cases and add:
  - the click after a hold is swallowed (B-1)
  - Enter is a tap
  - Space hold sends
  - Esc cancels
  - the stand-down when busy
  - accessible names in every phase
- `TabBar.test.tsx`:
  - slots swap without the bar's height changing
  - tabs are `inert` while holding
  - locked Discard, Stop and Send, with Stop navigating to `/capture?note=`
  - the global R ignores fields and modifiers
- `e2e/capture.spec.ts`, mouse on Chromium with the fake mic:
  - hold → send → a PUT is seen
  - hold, drag left 120 → no PUT
  - hold, drag up 80, release → still recording → Send → a PUT
  - one CDP-touch case in a `hasTouch` 412×915 context: slide left from the disc centre to x = 90 cancels. **This is the regression test for F6.**
- `e2e/talk.spec.ts`: delete; its useful cases move into the above.
- `TalkScreen.test.tsx`: delete.

### 2.14 Risks

- **iOS long press:** `-webkit-touch-callout: none` plus a prevented `contextmenu` are needed, or the text-selection loupe appears. This needs a manual iPhone check, because Playwright WebKit has no mic.
- **Android "back" gesture zones:** a slide-left that starts within about 20 px of the screen edge is the OS's. The disc is at the centre, so this is not a problem.
- **Haptics:** Chrome only fires `vibrate` after a user activation. `pointerdown` counts as one.
- **First-syllable loss** from the 250 ms arm. People speak after the start buzz and tone, as on WhatsApp, which also arms. #85 used 350 ms without complaint.

**Effort: M+** (about 500 lines of TS/CSS changed, about 700 deleted with `/talk`).

---

## 3. F7: `/talk` and the manifest shortcut

**Options:**
- **(a, recommended)** Delete `/talk` entirely. Remove the "PTT" manifest shortcut. Keep "Record a thought" (`/capture`). Redirect `/talk` to `/`.
- **(b)** Keep `/talk` as a You row for one thought after another.
- **(c)** Keep everything.

**Why (a):**
- The disc hold is ready again the moment a send finishes, which is what `/talk` was for.
- `/talk`'s target pill only adds "choose a note from Home". Opening the note and holding does the same.
- A web app cannot get a hardware button or a real widget (backlog row E: that needs a TWA/native wrapper). A launcher shortcut that opens a screen where you then hold is two steps, which is exactly the owner's complaint.
- **"Record a thought" stays.** A long-press on the icon, then Record, opens `/capture`, which records immediately. That is one step from the launcher and the only shortcut worth having. The physical-button path that does work (Devices & shortcuts, the inbox keys for watch, ring and iOS Shortcuts) is untouched.

**Files:**
- `manifest.config.ts:144-151` (delete the PTT entry) and `e2e/manifest.spec.ts` (expect one shortcut).
- `app/routes.ts:24-25`, `app/router.tsx:34-36,68`.
- `routes.ts` `LEGACY_ROUTES`: add `'/talk': '/'`. The installed WebAPK keeps the old shortcut until Chrome refreshes the manifest, which can take days, so this avoids a Not Found. `legacyRedirect` merges the query, so `/?note=…` is harmless.
- `components/AppShell.tsx:27,33,49` (`talk` screen and title).
- `components/RecordingIndicator.tsx:33-38`.
- `capture.css:721-730`.
- Delete `features/talk/` and `styles/talk.css`, and remove `index.css:15`.
- `SettingsScreen.tsx:253-258`.
- `AboutScreen.tsx:73-74,111` ("above the PTT button").
- `README.md` shortcuts text.
- `docs/design/capture-ux.md`.
- `routing.test.tsx` and `SettingsScreen.test.tsx` expectations.
- Run `knip` for the leftovers (`Icon` `ptt`, `TargetChooser` if `/capture` is now its only user; it is used there, so it stays).

**Effort: S** (it lands in the same PR as F5; the deletions are what make F5 smaller).

**Owner decision D5.** Default (a).

---

## 4. F2: back navigation

### 4.1 Today

- Tabs are `<Link>`s that **push** (`TabBar.tsx:79-85`).
- About's and Usage's "‹ You" are `<Link to=/settings>` that **push** (`AboutScreen.tsx:62`, `UsageScreen.tsx:19`).
- The Archive entry pushes (`LibraryList.tsx:250`).
- Result: You → About → Home → You → About → Back walks every screen, as the owner reports.
- **What already works:**
  - `useBackGuard.ts` seeds Home under a cold deep link.
  - The note's `BackLink` (`NoteDetailScreen.tsx:960-966`) goes back one entry when `history.state.idx > 0`.
  - `/capture` replaces itself on leave (`CaptureScreen.tsx:112`).
  - Filters replace (`useLibraryParams.ts:53`).
  - Scroll is restored per `location.key` on POP, and per path when the navigation carries `RESTORE_SCROLL` (`useScrollRestore.ts:95-141`, R7-6a and R7-6b).
- **Gap in the guard:** a cold start at `/?view=archived` is "home" to the guard (`useBackGuard.ts:31` checks the pathname only), so the Archive becomes idx 0 and Back exits from it.

### 4.2 Options

- **A:** tabs use `replace: true` everywhere. This is simple, but Back from You exits the app, because Home was replaced. That breaks "Back from a top-level screen goes Home".
- **B (recommended):** an app-style stack with Home always at idx 0.
- **C:** a custom in-memory stack with `popstate` interception. This fights the browser and breaks desktop Back and Forward.

### 4.3 Recommended model (B)

**Invariant.** Home `/` (with any `q`/`mode`, but not `view=archived`) is always history idx 0 of the app's session. Top-level screens are You `/settings` and Archive `/?view=archived`; they only ever sit at idx 1. Everything else is pushed above.

| Action | History operation |
|---|---|
| Home tab, from idx n > 0 | `navigate(-n)`: one `history.go` back to the Home entry. It is a POP, so R7-6a restores the library's scroll under Home's own key. |
| Home tab while on Home | `navigate('/', {replace:true})` (clears a filter) and scroll `.app__main` to top. This is the app convention and today's effect. |
| You/Archive tab from Home (idx 0) | push |
| You/Archive tab from idx 1 (the other top-level, or a note opened from Home) | `replace` |
| You/Archive tab from idx n ≥ 2 | `navigate(-(n-1))`, then on landing `replace` with the tab (see below) |
| Same tab while on it | no-op, scroll to top |
| Open a note (row, Ask citation, filing row, "Open <title>") | push (unchanged) |
| Open About/Usage from You | push (unchanged) |
| About/Usage "‹ You" | if idx ≥ 2, `navigate(-1)`; else `navigate('/settings',{replace:true})` (a cold deep link to `/about` then sits at [Home, You]) |
| `/capture` | push on open, replace on leave (unchanged) |
| Tab navigations | carry `state: RESTORE_SCROLL`, so each tab reopens where it was left (`path:` key, already written on every cleanup) |

**The two-step case.** The router's `navigate(delta)` is a plain `history.go`; do not rely on a promise. In `app/useTabNavigation.ts` (new):
1. Write the pending tab into a module-level ref.
2. Call `navigate(-(n-1))`.
3. A `useEffect` on `location.key` in `AppShell` sees a POP with a pending tab and does `navigate(pending, {replace:true, state: RESTORE_SCROLL})`, then clears the ref.

The intermediate entry may paint for one frame. Accept this; it only happens when switching tabs from three or more levels deep. If it shows in QA, render the outlet `inert` with `visibility:hidden` while the ref is set.

Read `idx` exactly as `BackLink` does: `(window.history.state as {idx?:number}|null)?.idx ?? 0`. Use one helper, `historyIndex()`, in `app/`, and use it in `BackLink` too.

**The guard fix.** `useBackGuard.ts:31` becomes "return only if pathname is `/` and `view` is not `archived`". A cold start at the Archive then seeds [Home, Archive].

**Back from Home** (idx 0): do nothing special.
- **Installed PWA (Android system Back, `display: standalone`):** the app closes to the launcher, which is the platform norm.
- **Browser tab:** Back goes to whatever was before the app.
- No "press again to exit" and no trap; `useBackGuard`'s stated principle is kept. **Owner decision D6**, default: exit.

**Walk-through** (the owner's case):
- You(1) → About(2) → Home tab: `go(-2)`, giving [Home]. You tab: push. About: push. Back gives You, Back gives Home, Back exits.
- Home → note A(1) → You tab: replace → [Home, You]. Back gives Home.
- Desktop browser Back and Forward use the same entries. Forward after a Home-tab pop re-offers the popped screens until the next push, which is ordinary browser behaviour.
- **Deep link** `/notes/x`: [Home, note] (existing). You tab gives [Home, You].
- **Deep link** `/about`: [Home, About]. "‹ You" replaces, giving [Home, You].

### 4.4 Files (fe)

- `app/useTabNavigation.ts` (new): `historyIndex`, `goHome`, `goTab`, and the pending-replace effect, mounted in `AppShell`.
- `components/TabBar.tsx`: `TabLink` stays an `<a href>` (middle-click and screen readers keep a real link), with an `onClick` that calls `preventDefault` and then `goHome`/`goTab`. Modified clicks (meta, ctrl, shift, middle button) pass through.
- `screens/library/LibraryList.tsx:250`: Archive uses `goTab`.
- `screens/AboutScreen.tsx:62`, `screens/UsageScreen.tsx:19`: back-links as in 4.3.
- `app/useBackGuard.ts:31`.
- `features/notes/NoteDetailScreen.tsx:960-966`: use `historyIndex()`.
- `docs/design/home.md` (or a short section in `note-screen.md`): the stack model.

### 4.5 Tests

- `routing.test.tsx`, with a memory router seeded with idx:
  - each row of the table in 4.3
  - the cold Archive deep link seeds Home
  - a modified click on a tab does not intercept
- `e2e/back-nav.spec.ts` (new, Chromium and WebKit, no mic needed):
  - the owner's walk, asserting `page.goBack()` lands on You, then Home, then `about:blank`/exit (the history length check)
  - Home tab from a scrolled library → note → Home tab restores the offset (extends `scroll-restore.spec.ts`)
  - tab switching keeps the depth at ≤ 2, checked with `history.length` deltas
- Manual check on a Pixel PWA: Android system Back from You gives Home, and from Home closes.

### 4.6 Risks

- `history.state.idx` is React Router's private field. `BackLink` already depends on it. Pin it in one helper with a test.
- A `go(-n)` across a `/capture` entry: none exist, because `/capture` replaces itself on leave.
- The sign-in redirect and the passkey return already seed via replace (`usePasskeyReturn.ts:45`).

**Effort: S–M.**

---

## 5. F3: recording while typing

### 5.1 Today

With the keyboard up, the tab bar and its 76 px disc sit under the keyboard. The layout viewport is not resized; that was a deliberate choice in R6-NAV-2 (`docs/design/note-screen.md:167-213`). So you dismiss the keyboard, then press Record.

### 5.2 Options

- **A, a floating mic above the keyboard** (`bottom: var(--keyboard-inset)`, right): it sits over the text band between the caret and the keyboard, so it **masks text** when the caret is mid-note. Rejected; the owner's own condition rules it out.
- **B, `interactive-widget=resizes-content`** so the bar rides on the keyboard: Android only, and the 93 px bar takes a fifth of what is left. Already rejected in R6-NAV-2.
- **C (recommended), a mic in the banner's right end,** shown only while a note editor has focus and the keyboard is up. The banner (`.app__banner`, 47 px tall, `min-height: var(--layout-touch-target-min)`) is its own grid row, and its right end is empty on the note screen (`shell.css:50-61`). The button sits in that row, so **it cannot cover a single character, and nothing reflows**: 44 px fits the row's existing minimum height. It is the "top-right" the owner described.
- **D, keep current behaviour.**

### 5.3 Recommendation: C, exactly

**When it shows:** all of these must hold, in CSS only, with no React state:
- `:root[data-keyboard]`: `useKeyboardInset` additionally does `root.toggleAttribute('data-keyboard', inset > 80)`. The 80 px rules out an accessory bar or a zoom rounding.
- `.app[data-screen='note']`
- `:has(:is(.note-title-input, .note-body-input, .checklist-editor input, .checklist-editor textarea):focus)`
- the note is not archived, so `useRecordTarget()` returns an id. The button is rendered only then.

It is `display: none` otherwise, so it is out of the tab order and away from screen readers. The banner's height does not change either way.

**Placement and size:**
- `margin-inline-start: auto` in the banner's flex row, so it is the last child and the right end is at the banner's inline padding (16 px).
- Hit target 44×44 (`--layout-touch-target-min`).
- Visual disc 40 px, `--radius-full`.
- At 412×915 its centre is at (374, 23.5).
- Mic glyph 20 px, `strokeWidth` 2.
- An offline pill, when present, sits between the wordmark and the mic, because the flex gap is unchanged.

**Theme** (it must not wear the accent; the record disc owns it):

| | Ink & Paper | Nocturne |
|---|---|---|
| background | `--color-raised` #f5f1e8 | `--color-raised` #1c211f |
| border 1 px | `--color-line-strong` #8a8478 (3.4:1 on ground) | #5e6e66 (3.6:1) |
| glyph | `--color-ink` #1a1917 (about 15:1) | #f2f5f3 (about 15:1) |
| focus | the app's standard focus ring | same |
| pressed | `--color-surface` background | same |

**Behaviour:**
- `onPointerDown`: `preventDefault()`, so the editor keeps focus and the button does not vanish before the click.
- `onClick`: `navigate(ROUTES.captureInto(id))`. This is a tap only. A hold or slide at the top of the screen is not a gesture anyone can make; the locked, hands-free recording is what `/capture` already gives.
- Autosave: the editor unmounts on the route change. Confirm the body autosave flushes on unmount (`features/notes/autosave.ts`), and add a test that types then taps the mic, expecting the PATCH before the capture POST. If it does not flush, call its flush before `navigate`.
- On return, `/capture` replaces back to the note with `RESTORE_SCROLL` (R7-6b), so the place is kept.
- **Screen reader:** `aria-label` "Record into this note". The editor keeps focus, so nothing to announce.
- **Reduced motion:** it appears instantly. Otherwise it fades in over `--motion-duration-fast`.
- **Desktop:** it never shows, because there is no on-screen keyboard and so no `data-keyboard`.

See `mock-gestures-typing.svg`.

**Files:**
- `hooks/useKeyboardInset.ts`: the attribute, and remove it on unmount.
- `components/AppShell.tsx`: render `<BannerRecord/>` in `.app__banner` when `screen==='note'`.
- `components/BannerRecord.tsx` (new, about 25 lines), using `useRecordTarget()`.
- `styles/shell.css`: the show rule and the size and theme rules.

**Tests:**
- `useKeyboardInset.test.tsx`: the attribute toggles at 81 and 80.
- `AppShell` or `BannerRecord` test: absent on archived notes, `display:none` without the attribute.
- `e2e/note-tabs.spec.ts` (or `layout.spec.ts`):
  - set `--keyboard-inset: 400px` and `data-keyboard` directly; CDP cannot raise a keyboard, as R6-NAV-2 notes
  - focus the body: the button is visible, within the banner's box, and the banner's height is unchanged
  - blur: hidden
  - tap: lands on `/capture?note=` and the body PATCH was sent

**Risk:** iOS sometimes pans the layout viewport up on focus (`visualViewport.offsetTop > 0`). Then the banner, and the mic with it, scrolls above the visible area. The mic is then simply not there, which is the current behaviour; it never covers anything. Add this to the manual iPhone check. If it happens often, a `position: fixed` variant at `top: offsetTop` would overlay content, so do not do that without asking.

**Effort: S. Owner decision D7:** ship C (default) or keep the current behaviour.

---

## 6. Owner decisions, with defaults

| # | Question | Default |
|---|---|---|
| D0 | Reverse the 09-27 "PTT only on the widget" decision | Yes |
| D1 | Locked recording in place, or hand off to `/capture` | In place |
| D2 | Tap = open `/capture`, or start a locked recording | Open `/capture` |
| D3 | Desktop global hold key | R |
| D4 | First-run coach "Hold to talk · tap to record" (3 launches) | On |
| D5 | Delete `/talk` and the PTT shortcut, keep Record | Yes |
| D6 | Back from Home exits (no "press again") | Exit |
| D7 | Banner mic while typing | Ship |

## 7. Streams

1. **fe, M+:** F5 + F6 + F7 in one PR.
   - Files: `holdTiming.ts`, `holdGesture.ts` (new) and its test, `useHoldToTalk.ts`, `feedback.ts`, `RecordButton.tsx` and its test, `TabBar.tsx` and its test, `useRecordTarget.ts` (new), `Icon.tsx`, `shell.css` and `capture.css`.
   - Deletions: `features/talk/*`, `talk.css`, `e2e/talk.spec.ts`.
   - Edits: `manifest.config.ts` and its spec, `routes.ts`, `router.tsx`, `AppShell.tsx`, `RecordingIndicator.tsx`, `SettingsScreen.tsx`, `AboutScreen.tsx`, `e2e/capture.spec.ts`, docs.
2. **fe, S–M:** F2.
   - Files: `useTabNavigation.ts` (new), `TabBar.tsx` (`TabLink` only), `LibraryList.tsx`, `AboutScreen.tsx`, `UsageScreen.tsx`, `useBackGuard.ts`, `NoteDetailScreen.tsx`, `routing.test.tsx`, `e2e/back-nav.spec.ts` (new), docs.
   - It conflicts with stream 1 only in `TabBar.tsx`, so land it second or rebase.
3. **fe, S:** F3.
   - Files: `useKeyboardInset.ts` and its test, `BannerRecord.tsx` (new), `AppShell.tsx`, `shell.css`, an e2e case.
   - It depends on `useRecordTarget()` from stream 1.

There is no backend work.
