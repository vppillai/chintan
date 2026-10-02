# r9 review — frontend lens

Reviewed: main `53bf8a23` ("Details sheet: fixed head, only the body scrolls (R8-S1) (#203)", 2026-09-30 22:05 -0700). Baseline for growth: `69d1f2b` (29 Sept 23:32), 20 frontend commits since, 114 files, +9,264/−2,779 lines under `frontend/`. Read-only; measurements on an orb mirror (`bun install --frozen-lockfile`, `bun run build`, `vitest run --reporter=json`); e2e numbers are CI's (run 36818138175 on main). Each finding says measured, traced (read) or reproduced. Nothing already open in backlog Round 7/8 (R7-14a, R8-F8b→d, the 320 px capture flake, T47's `zipRecordings` follow-up, B14) is re-reported except where a new fact changes it.

## Summary

| Severity | Count | Ids |
|---|---|---|
| Critical | 0 | — |
| High | 1 | FE-1 |
| Medium | 12 | FE-2 … FE-13 |
| Low | 9 | FE-14 … FE-22 |

Owner decisions: FE-2 (budget number), FE-6 (one Undo rule), FE-10 (layout matrix), FE-13 (wire screenshots to release), FE-19 (`/talk` sunset).

## Findings, ranked

### FE-1 · High · An edit made offline is not shown again while still offline, and a second keystroke overwrites it
`src/features/notes/useNoteEditor.ts:299-311`, `src/features/notes/NoteDetailScreen.tsx:102`, `src/offline/queuedEdits.ts:35,73`, `src/features/notes/autosave.ts:477-481`. **Traced, not reproduced.**
On an offline save the body goes only into the queue payload (`enqueueReplacing`); `recordSavedNote` runs on success only (`:258`), so neither `['note',id]` nor the IndexedDB detail is patched. Leave the note and return while offline: `note = served ?? cached.data` serves the pre-edit body, `reconcileQueued` says "Saved on this device — will sync" over text that does not contain the edit, and nothing reads the queued text back (`queuedEditPayload` has one caller, the flush in `useOfflineQueue.ts:26`; `queuedEditFor` returns flags only). Type one character: `enqueueReplacing` under the same id replaces the queued PATCH with pre-edit body + keystroke — the first offline edit is lost silently. `e2e/offline.spec.ts:359-445` covers queue and flush, never reopen-while-offline.
Why it matters: the offline promise is false on the one path the owner hits on a flight. Fix: on queue, patch `['note',id]` and `cacheNoteDetail` with the draft (version unchanged) — the shape `recordSavedNote` already has — or hydrate the editor from the queued payload on load; one e2e that edits, leaves, returns, offline. ~20 lines + test. **Effort S–M. No decision.**

### FE-2 · Medium · Main chunk +35 kB (+7 %) in one round, the shell now carries the recorder, no size guard
**Measured** (both trees built on orb, same toolchain):

| Chunk | 69d1f2b raw / gzip | HEAD raw / gzip | Δ raw |
|---|---|---|---|
| `index-*.js` (main) | 493.55 / 154.21 kB | 528.72 / 165.07 kB | **+35.2 kB (+7.1 %)**, gzip +10.9 kB |
| `index-*.css` | 103.35 / 14.54 | 109.85 / 15.78 | +6.5 kB |
| `routes-*.js` | 81.59 / 27.39 | 81.65 / 27.40 | flat |
| `CaptureScreen-*.js` | 10.49 / 3.74 | 13.35 / 4.75 | +2.9 kB |
| `SettingsScreen/About/Usage/AskPanel` | 28.18 / 8.50 / 8.23 / 3.33 | 28.10 / 8.48 / 8.11 / 3.33 | flat |
| `Waveform-*.js` (lazy) | 4.26 / 1.92 | **gone — folded into main** | |
| `TalkScreen-*.js` | 4.89 | gone (R8-F7) | |
| `YouBackLink-*.js` | — | 0.40 kB new chunk | |
| precache | 28 entries, 773 KiB | 27 entries, 808 KiB | +35 KiB |

(The backlog's "493,557 / 152,353" is the same raw figure; gzip differs by implementation.) Why: `TabBar.tsx:6-11` statically imports `useHoldToTalk` (444 L), `holdGesture` (386 L), `Waveform`, `machine`, `store` — the PTT work put the recorder's gesture layer in the shell every launch loads, including the `/capture` shortcut B14 (open, "next") is trying to speed up. `fflate` is still static (`zipRecordings.ts:1`, `Recordings.tsx:28`) — recorded under T47, unchanged. A 400-byte `YouBackLink` chunk and a 245-byte `costNote` chunk are two extra requests for shared trivia.
Three rounds have hand-recorded the bundle size (B14 534 KB, T47 548 KB, now 529 KB); CI notices nothing. Fix: `lazy()` the `Waveform` in TabBar (rendered only while holding); inline the two micro-chunks (`manualChunks`) or accept them; a 10-line build step comparing `dist/assets/index-*.js` to a committed budget, no new dependency. **Effort S. Owner decision: the budget number** (suggest 530 kB raw).

### FE-3 · Medium · The Details/Share sheet is a modal that does not trap focus or close on Escape
`src/features/notes/NoteDrawer.tsx:85` (h2 `tabIndex=-1`); `useModalFocus.ts` users are only `ConfirmDialog.tsx:78` and `MoveSheet.tsx:144`. **Traced.** After R8-S1 the sheet is a fixed-head scroller with a backdrop and reads as a dialog, but has no `role=dialog`/`aria-modal`, no Tab trap, no Escape (Escape handlers exist in `useModalFocus`, `OverflowMenu`, `FindBar`, `useDragReorder`, `useHoldToTalk`, `TargetChooser`, `SelectionBar` — not here). Fix: reuse `useModalFocus` as `MoveSheet` does. **Effort S. No decision.**

### FE-4 · Medium · Six to ten live regions mounted at once on the note screen
**Traced** (84 `role=status|alert|aria-live` sites in non-test source). Standing on an open text note: `AppShell.tsx:184` StatusRegion, `TabBar.tsx:293`, `NoteDetailScreen.tsx:516` ("Added text shown", always mounted), `:1076` save indicator, `PullToRefresh.tsx:21`, `FindBar:101` when open; conditional: offline copy `:289`, archived `:323`, `FilingBanner:63`, per in-flight capture `FilingItem.tsx:240`; Items tab adds `ChecklistEditor.tsx:564,669`; Recordings adds `Recordings.tsx:390` plus one per row (`RecordingRow.tsx:382-392`). A tick fires the editor region, the Toast (`Toast.tsx:96`, atomic) and the save indicator inside one second; VoiceOver drops overlapping polite announcements. R7-13 chose the double announcement knowingly; the save indicator and per-row counts were not part of that choice. Fix: route the editor's and save indicator's text through the shell's one `StatusRegion`; make per-row counts plain text. **Effort M. No decision.**

### FE-5 · Medium · `useHoldToTalk`'s browser wiring has no unit test, and its `lostpointercapture` rule differs from the other three gesture hooks
`useHoldToTalk.ts:314-423`; no `useHoldToTalk.test.*`; `TabBar.test.tsx` has 0 cases for `pointercancel`, `lostpointercapture`, window `blur`, `visibilitychange`, second finger (`:357`), `CLICK_SUPPRESS_MS` (`:415`). **Measured by grep.** These are the branches R8-F6 changed. The reducer (`holdGesture.ts`, 21 pure tests) is well covered; the glue is not. `onLostPointerCapture` (`:385`) treats any bubbling loss as a release, where `useSwipeActions.ts:273-285`, `useHorizontalSwipe.ts:401`, `useDragReorder.ts:265` guard `event.target === event.currentTarget` after T1. Chromium's implicit→explicit hand-over inside `pointerdown` does not fire the event (the CDP-touch e2e at `capture.spec.ts:702` passes), so this is latent, not live — but the fix is one line and the hold is now the app's main gesture. Fix: `useHoldToTalk.test.tsx` with fake timers driving the five interruptions through the real store (~80 L); add the guard. **Effort S. No decision.**

### FE-6 · Medium · Two Undo staleness rules, and a strong notice can still bury a strong one
`ChecklistEditor.tsx:222-248` (tick: refuses unless `currentBody() === next`), `:518-540` (Delete done: `currentBody() !== lastWritten.current` — a ref the person's later ticks/typing update, so Undo silently reverts their own 6-second-old edits; `:92-97` says so by design), `useTidyList.ts:187-199` (Tidy: exact body). **Traced.** R7-13's review called "takes back later edits" a bug and fixed it for ticks only. `ToastNotice.weak` protects a standing Undo from ticks, but a Tidy answer landing by poll (`useTidyList.ts:184`) or a note Delete replaces a standing Delete-done Undo with no way back. Fix: one rule (exact body: a one-line change at `:528` deletes the `lastWritten` ref `:201-209`); a shared 5-line `undoIfUnchanged` removes ~12 net lines — only with the rule change. **Effort S. Owner decision: which rule.**

### FE-7 · Medium · A queued edit the server refuses is marked dead and its text is unreachable
`src/offline/queue.ts:258-260` (`markDead` on 409; comment: "kept and surfaced so the user can reconcile it"), `autosave.ts:481` ("That edit did not save."). **Traced:** `grep .dead` → only autosave.ts; no screen shows the dead payload, no action clears it, the banner keeps counting it. Fix: show the queued body through the existing conflict banner (Keep mine / Take theirs exist) or drop the promise and add Discard. **Effort S–M. No decision.**

### FE-8 · Medium · `TextPanel` does four jobs with three focus-restoring effects ordered by comment
`NoteDetailScreen.tsx:618-824` (206 L): textarea, find mirror, flash mirror (R7-6b "Show"), checklist delegate; `lastActive`/`wasMirrored`/`caretAfterFlash` refs; effects `:667-686` and `:739-751` both call `textarea.focus()` on different triggers, sequenced only by the comment at `:664`. File 900→1,091 lines, 127 branches. **Measured.** Fix: `FindMirror` and `FlashMirror` components owning their focus effects; `TextPanel` becomes a four-way switch (~60 L). **Effort M.**

### FE-9 · Medium · The checklist level planner lives inside the component and runs every render
`ChecklistEditor.tsx:264 plan`, `:353 openBlock`, `:367 clampLevels`, `:384 moveOpen`, `:456 previewDepths` — ~190 lines of hook-free pure functions inside a 562-line component function (`:112-674`, 91 branches); `previews = previewDepths()` at `:475` re-parses `shiftLevel(...)` every render during a sideways drag. **Measured.** Behaviour is pinned by `ChecklistEditor.test.tsx:605-889`, so moving to `checklist.ts` is mechanical; `useMemo` on the preview is one line. **Effort M.**

### FE-10 · Medium · e2e: the layout matrix is 42 % of the suite, and the WebKit exclusion list is a hand copy of it
**Measured from CI run 36818138175:** 382 tests, 363 passed, 19 skipped (all env/browser-gated), 0 retries, Playwright wall 5.5 min (job 6 m 26 s). `layout.spec.ts` 229.9 s / 103 tests (2 themes × 12 viewports × routes, `:360-373`, plus the WebKit copy); next `capture.spec.ts` 71.3 s / 22. `playwright.config.ts:49-50` excludes 11 of the 12 viewport names by regex, so a 13th viewport runs on WebKit unnoticed. `note-tabs.spec.ts:277-487` has five `waitForTimeout(300)` swipe settles that should wait on `data-tab-enter` clearing. Fix: mark the one WebKit viewport in the spec and `test.skip` the rest; replace the 300 ms sleeps. **Effort S. Owner decision: full matrix in both themes on Chromium, or sample.**

### FE-11 · Medium · Unit tests that depend on real time
**Measured:** 1,405 tests / 101 files, 11.8 s wall (jsdom set-up 35 % of summed time). 18 real sleeps in 10 files, 8 of which never use fake timers: `useNotesCache.test.tsx:175,192,219,231` ("not called within 50 ms" negatives), `useNoteEditor.test.tsx:222,289`, `notes.test.tsx:81,162`, `CaptureScreen.test.tsx:635`, `ChecklistEditor.test.tsx:1046` (300 ms real sleep; production constant not imported), `routing.test.tsx:412` (real 2 s for the `requestIdleCallback` stand-in, `:417`; the only test over 1 s). Timer literals: `AskPanel.test.tsx` advances `600` ×6 and `1_000` ×4 without importing the debounce constant; `1_000` appears 12× across the suite — a constant change passes silently. Fix: fake timers + imported constants in those 10 files; `pool: 'vmThreads'` if the suite keeps growing. **Effort S.**

### FE-12 · Medium · axe covers the library and note screens, not the surfaces rounds 7–8 added
`e2e/a11y.spec.ts`: `/`, archive, search, settings (+passkey error), about, note (+recordings, +cleaned), Items with grip menu, Home with every filing tier — both themes, both browsers. **Traced.** Not covered: `/capture` idle/recording/review, the bar's hold/locked states, tick/Tidy Undo toast, `ConfirmDialog`, `MoveSheet`, Details/Share sheet (FE-3 would have been caught), `FindBar`, `BannerRecord` with `data-keyboard`, recordings `SelectionBar`, `SignedOut`/`RouteError`. Fixture pattern at `:92-110` makes each ~10 lines. **Effort S.**

### FE-13 · Medium · The install sheet shows a UI three rounds old; `shell.css` is the sheet that absorbs everything
`public/screenshots/home-{narrow,wide}.jpg` last written 2026-09-21 (`08bf4202`, #75) — before the Bindu mark (#129), brand header (#114), filing tray (#190), PTT bar (#193); regeneration is `SCREENSHOTS=1 bunx playwright test e2e/screenshots.spec.ts` (`:27`), in no workflow. **Measured by git log.** `shell.css` is 1,712 lines (+242 this round: `.tab-bar__cancel/level/lock/padlock/chevrons/dot/slot--*`, `.banner-record`), holding shell, signed-out, dialogs, primaries, offline banner, banner mic; its entry fee for R8-F3 is the app's deepest selector, `shell.css:127-141` `:root[data-keyboard] .app[data-screen='note']:has(:is(.note-title-input, .note-body-input, .checklist-editor input, .checklist-editor textarea):focus) .banner-record` — every new editable field must be added there. Fix: rerun screenshots; split the PTT bar into its own sheet (~250 L); set a `data-editing` flag from `useKeyboardInset` and collapse the chain. **Effort S (shots) / M (split). Owner decision: wire screenshots to release.**

### FE-14 · Low · Navigation: one model, two bypasses, one duplicated seed, a pending with no expiry
`useTabNavigation.ts` is one documented model isolating `history.state.idx` to one helper. But `NoteDetailScreen.tsx:994` (BackLink) and `LibraryField.tsx:214` read `historyIndex()` and do their own arithmetic instead of `goBackTo`; `Redirect.tsx:23-26` repeats `useBackGuard.ts:42-45`'s seed. `chintan.nav.pending` (`:54`) has no expiry and `usePendingTab` runs on the initial `POP`, so a pending that never landed (a `history.go(-n)` that is a silent no-op when `idx` overstates the real stack) is consumed on the next reload and replaces whatever URL was loaded. **Traced, not reproduced.** Fix: BackLink → `goBackTo`; one seed; stamp the pending with `location.key`. −25 L. **Effort S.**

### FE-15 · Low · Gesture duplication: four names for one slop, five "swallow the next click" rules — no shared primitive
`TAP_SLOP_PX` 10, `SWIPE_SLOP_PX` 12 (`useHorizontalSwipe.ts:82`), a private `SWIPE_SLOP_PX` 10 (`useSwipeActions.ts`), `AXIS_SLOP_PX` 10, `LONG_PRESS_TOLERANCE_PX` 10. Click swallowing: 600 ms window (`useHoldToTalk.ts:225,415`), ref with `detail===0` keyboard pass-through (`useHorizontalSwipe.ts:405`), refs without it (`useDragReorder.ts:267`, `useSwipeActions.ts:291` — would swallow an Enter click after a cancelled touch), `consumeClick()` (`useLongPress.ts:104`). **Traced.** A `usePointerTrack` primitive was costed: ~110 duplicated lines against an ~80-line primitive needing options for where/when capture is taken, pointer types, blur — net −30 L and a sixth state machine. **Verdict: not worth it.** Do only `hooks/swallowNextClick.ts` (~15 L) and one `GESTURE_SLOP_PX`: −40, +15. `usePullToRefresh` stays on touch events. **Effort S.**

### FE-16 · Low · Polling and query hygiene leftovers
**Traced.** R8-F8d holds: no `refetchInterval` on `['note',id]`; `usePollNote` is the only timer poller, cleared on unmount; no double pollers. Remaining: `useAsk` (`ask.ts:68`) is the one poller with `refetchIntervalInBackground: true`, against R7-17a's rule; three `queryFn`s write other queries as side effects (`captures.ts:160,276`, `notes.ts:204`) — the shape that needed R7-17d's once-only guard; `useRetranscribeCapture.ts:24-32` sets then invalidates the same key (dead patch); `['notes','offline']` spelled at `notes.ts:62` and `useNotesCache.ts:28`; five key families outside `keys.ts`; `invalidateNoteLists` also hits `['notes','offline',…]`, triggering the IDB `getAll` that `recordSavedNote` avoids; `notes.ts:206-207` re-implements `refreshAppendedNote` inline. **Effort S each.**

### FE-17 · Low · Dead CSS declarations, eleven never-read tokens, one dead icon, one dead attribute
**Scripted.** Same selector + property, second wins: `.note-meta` margin-block `notes.css:249` vs `:386`; `.ask__error` color `ask.css:78` vs `:100`; `.library-heading` inline-size `home.css:44` vs `:393`. Tokens read nowhere: `--z-toast`, `--layout-strip-height`, `--space-0/10/16/20`, `--radius-2xl`, `--font-scale-ratio`, `--font-weight-bold`, `--motion-duration-instant`, `--motion-ease-accelerate` (`check-tokens.mjs` checks only the other direction). `Icon.tsx:88` `download` has no `<Icon name="download">` (`DownloadButton.tsx:96` is a class; `Record<IconName,string>` hides it from knip). `data-signed-out` (`AppShell.tsx:122`) has no reader. Fix: delete; add a "defined, never read" pass to `check-tokens.mjs`. **Effort S.**

### FE-18 · Low · Comment rot after rounds 5–8
`index.html:44-47` (a `public/404.html` that moved in `cbbaf1c7`), `holdTiming.ts:7-9` ("the old `/talk` disc"), `SelectionBar.tsx:7`, `NoteDetailScreen.tsx:78`, `Recordings.tsx:160`, `notes.css:265` (the round-5 action bar), `useTidyList.ts:18-21` (Split up tab), `queue.ts:258` (FE-7). **Traced.** Comments are this codebase's design record, so rot costs more than usual. **Effort S.**

### FE-19 · Low · `/talk` alias and three copies of the ground colour
`routes.ts:51` keeps `/talk → /` with no sunset. `index.html` `theme-color` `#fbf9f4`/`#0a0c0b` and `manifest.config.ts` `GROUND` hand-sync `--color-ground`. **Traced. Effort S. Owner decision: a sunset for `/talk`.**

### FE-20 · Low · Two more components that grew past their name
`TabBar.tsx` 86→331 lines: tabs + PTT pill + cancel hint + lock + locked buttons + coach mark (`COACH_KEY` localStorage `:77-100`, a third persistence idiom in one file) + live region — a `HoldChrome` component gives the bar back ~90 lines. `FilingRow.tsx` 110→427: tiering, dismissal, fold, focus hand-off, two live regions, a minute tick; `filing/model.ts` already owns `groupReceipts`, and `moving/needsYou/shown/folded` belong beside it. **Measured. Effort S each.**

### FE-21 · Low · Gesture tests by input mode; implementation-detail probes
**Measured by grep.** `useDragReorder` has no `pointercancel` case (unit or e2e) and no pen; pen is exercised once in the suite (`SwipeRow.test.tsx:149`); `useHorizontalSwipe` has one mouse case. 132 `querySelector` + 22 `toHaveClass` against 1,393 `*ByRole` (10 %), concentrated in `ChecklistEditor.test.tsx` (35), `FilingRow.test.tsx` (22), `SwipeRow.test.tsx` (20); gesture tests legitimately read transforms, `ChecklistEditor` and `SettingsScreen` (17) could move to roles. **Effort S–M.**

### FE-22 · Low · Small a11y names and lint
`BannerRecord.tsx:38` `aria-label="Record into this note (while typing)"` — the parenthetical is state. `NoteDrawer.tsx:182,316` style field captions as `h2`. `SelectionBar.tsx:47` yields Escape via `document.querySelector('[role="dialog"]')`. ESLint has no `jsx-a11y`; axe in e2e is the only a11y check and FE-12 lists its gaps. **Traced. Effort S.**

## Checked and clean
knip 0, eslint 0, check-tokens 0; 497 class selectors all rendered (re-checked with a stricter quoted-literal pass); 51 e2e class locators all exist; `!important` only in the reduced-motion block (`tokens.css:430-433`); no CSS data-attribute selector without a setter; `/talk` screen, PTT setting, `talk.css`, `ptt` glyph, `HoldButton`, `holdToDelete`, Split up tab fully gone; `SelectionBar` and `CheckMark` live; no second import-order fight beyond R7-5; no `test.only`/`fixme`, no unconditional e2e skip, 0 CI retries over the last 12 main runs; manifest and icon assets all referenced; `signOut.ts:60-66` sweeps every `chintan.*` sessionStorage key; `useModalFocus` traps and restores where used; optimistic pin/reorder have rollback; queue replay order and flush lock are right.

## What is good — keep it
1. `holdGesture.ts` is a pure reducer with effects as data and 21 tests; the hook is only glue. That is why R8-F6's fix was a constants change.
2. `keys.ts` + `recordSavedNote` with `refetchType:'none'`, `usePollNote` as a plain timer, `supersedes()` with explicit tested rules, the `weak` toast flag — small, correct models the next change can reason about.
3. Lint is real: strict TS (`exactOptionalPropertyTypes`, `noUncheckedIndexedAccess`), knip, `check-tokens`, and a frontend/backend contract job that proves it can fail. All at zero.
4. `useTabNavigation` documents one history model and isolates the router's private `history.state.idx` to one helper and one test.
5. The test estate is fast and honest: 1,405 unit tests in 12 s, 382 e2e in 5.5 min with zero retries, WebKit in the matrix, CDP touch where it matters.