# The recording screen and its shortcuts

Opening `/capture` starts recording; the screen is full-screen, its controls
are round discs with a word under each, and Send stops and sends in one tap.
The same machinery is reached one shorter way: `/talk` is one giant PTT
button for thoughts one after another — hold, speak, release. The tab-bar
disc (`components/RecordButton.tsx`, `components/TabBar.tsx`) is a plain
Record control that opens `/capture`; it held to talk from #85 until the
owner asked for push-to-talk on the widget only (feedback 2026-09-27). This
note is the frontend; what happens to the recording once it is uploaded is
the pipeline's and unchanged. Code: the machine and store
(`frontend/src/features/capture/machine.ts`, `store.ts`), the screen
(`CaptureScreen.tsx`), the gesture (`useHoldToTalk.ts`),
`/talk` (`features/talk/TalkScreen.tsx`), the note's filing banner
(`FilingBanner.tsx`, `features/notes/NoteDetailScreen.tsx`), the library's
filing row (`FilingRow.tsx`, with its parts under `filing/`: `model.ts` for
what a row says, `FilingItem.tsx` for one capture's row, `TargetPrompt.tsx`
for "which note?", `useLocalUpload.ts` for this device's own upload),
the shell's indicator (`components/RecordingIndicator.tsx`), the manifest
(`frontend/manifest.config.ts`), the sheet (`styles/capture.css`: the filing
row, target picker and indicator, then the screen).

## The machine, in one paragraph

The recording is a pure reducer driven by a controller and held in a Zustand
store outside React, so it survives navigation. States: idle → requesting →
recording ⇄ paused → stopping → review → uploading → uploaded, with failed off
to the side. Two invariants: nothing says "Recording" before `getUserMedia`
resolves, and an interruption — a call ending the track, a headset unplugged,
the twenty-minute cap — yields a partial recording in `review`, never a
discard. Everything below is a way of driving that machine.

## Send while recording

While `recording` or `paused` the row is Cancel (×) · Pause or Resume · Stop
(■) · Send (↑). Send is the row's only filled disc; the accent stays the record
button's. Stop goes to `review` — Discard · Re-record · Send, with the
recording's own waveform to listen back — and Send there uploads. Send from
the recording row is `stopAndSend`: the store asks the recorder to stop and
remembers the api. MediaRecorder hands over its last chunk after `stop()`
returns, so the send cannot follow on the same call stack; `dispatch` watches
the transition out of `stopping` and sends only if it lands in `review`.
Nothing recorded, a discard or a recorder error with no audio drops the
request. The screen leaves at once, replacing its own history entry (a
`/capture` entry left behind would make the next Back a new recording), and
the filing row or the banner shows the upload. Failure paths are unchanged: a
failed upload sits on the filing row with Retry and Discard, the bytes still
on the device.

The discs are 4 rem (64 px) on a phone and 3.5 rem under a fine pointer, four
equal grid columns that do not wrap at 320 px; the word under each disc is
visible and is the accessible name. `requesting` shows Cancel alone. `failed`
shows Discard + Try again (resend) while audio is buffered, Close + Try again
(asks for the microphone again) when it was refused or missing, otherwise
Close.

Deliberately not changed: the manifest's "Record a thought" shortcut still
opens `/capture` and records at once. The owner's remark about it is question
C in `docs/reviews/2026-09-21/morning-queue.md`.

## Recording into a note, and coming back

`?note=` seeds the target at `start`; the chooser pill can move it until Send
reads it, and a retarget is written to the capture record so a recording
resumed after a reload goes where it was last aimed. `captureReturnPath(
noteId)` is where every exit goes: the note when there is a target, else
Home. Send used to force the Recordings tab so the upload could be watched
there, which took someone reading the text to a list of players; the note now
reopens on whichever tab it was left on, and the filing banner says the
recording is still coming. Between the meta line and the tab strip it draws
this device's own upload row ("Uploading… 40 %") while the PUT is in flight,
then the newest of the note's captures that is neither appended nor dismissed
as the same row the library draws — "Filing your recording" over the four
stage segments (Uploaded · Transcribing · Filing · Saving) — until it appends,
when the body has already refreshed and the banner has nothing left to say. A
failed or stuck capture keeps the row's Retry and Dismiss; a dismissal is per
device (`dismissed.ts`), so it does not return with the note. The banner is
not drawn on the Recordings tab, whose own row wears the same strip.

Where the contract said `captureReturnPath(noteId, sent)` the code has one
parameter: once every exit went to the note, `sent` decided nothing. And the
banner reuses the whole `FilingItem`, not only `FilingStages`, so a failure is
met with exactly the library's controls.

## PTT (hold to talk)

`useHoldToTalk` is the gesture, and `/talk` is its only surface: a press is a
hold from the first frame. PTT is the name the app gives it — the `/talk`
heading, the You row and the manifest shortcut (owner feedback 2026-09-26);
"hold to talk" is the instruction beneath the name, and the button's
`aria-label` spells it out for a screen reader. The tab-bar disc is named
"Record" ("Record into this note" on a note), wears the microphone under a
"Record" caption, and only taps: it held to talk from #85 (`HOLD_DELAY_MS`,
an armed phase to tell a hold from a tap, and a card above the bar), and the
owner asked for the hold on the widget only, not in the main app (feedback
2026-09-27), so all of that is gone and the You row and the shortcut are the
ways in.

Phases: idle → holding → hint | sent | busy. The rules, with the constants
that pin them:

- Pressing calls `store.start(target)`. The disc shows the live level, the
  clock and "Release to send"; it is plain DOM, so history is untouched and
  Back still works.
- Release sends (`stopAndSend`) when the microphone is live, or when a
  recording settled with audio while the finger was still down — the cap or a
  call ending the track is a message, not a slip. The page being hidden
  mid-hold — a call, the lock screen — ends the hold as a release for the
  same reason.
- Less than `MIN_TALK_MS` (600 ms) of audio is a slip: discarded, and "Too
  short — hold to talk" shows for `HOLD_NOTICE_MS` (1.5 s).
- A pointer more than `SLIDE_AWAY_PX` (80) off the button's edge — the edge,
  not the press point, so a thumb drifting inside the disc is not a cancel —
  turns the instruction to "Release to cancel"; release discards.
- Pressed while the last recording is still stopping or uploading: "Still
  sending the last one…" and no new recording. A microphone live on another
  screen, or unsent audio waiting on the capture screen, stands the hold
  down. A refused microphone is the screen's failure line to explain.
- The target is the pill above the disc; `?note=` seeds it, as it does the
  capture screen's.

`/talk` is the walkie-talkie, and its disc wears one: the `ptt` glyph in
`Icon.tsx` — body, antenna, grille and the side key that is push-to-talk —
while the tab-bar disc keeps the microphone, because its tap still opens the
recorder and the two discs share the viewport here (R5-BR-P3). Hold, speak,
release, "Sent · filing" for 1.5 s, ready for the next; `?note=` seeds the
pill. The disc is `min(100%, 26rem,
55svh)` wide. Space held from the page is the button; Escape mid-hold and the
window losing focus cancel, as `pointercancel` does for a finger — without
that the lock screen took the keyup and the microphone stayed open until the
cap. The upload's own row is drawn under the disc while sending or failed,
since nothing else on the screen would show it; the shell's indicator hides
on `/talk` and Home while uploading for the same reason. The manifest offers
"PTT" (`/talk`, "Hold to talk, release to send") beside "Record a
thought". Beyond the contract, the code adds the busy notice, the stand-down
rules and the blur cancel; the hint on `/talk` reads "Too short — hold to
talk".

## Receipts on Home

The top of the library is where a recording's filing is watched and where
its landing is confirmed. `FilingRow` draws four tiers, in this order: this
device's own upload (`useLocalUpload`, `LocalUploadItem`); rows still moving
(the four stage segments); rows that need the person — failed, capped,
asking "which note?", stuck past ten minutes — never grouped and never
hidden; then the receipts, one row per note the rest landed in, newest
landing first: "Filed into “Roof repair”", or "3 filed into “Kitchen
rebuild”" with how long ago the last one landed where a moving row shows the
recording's length (the note's Recordings tab has the lengths). The first
three notes are rows; the fourth onward fold behind a native `<details>`
whose summary reads "and 4 more filed into 2 notes" behind a chevron that
turns as it opens (the summary's flex layout drops the native marker, and a
touch screen shows no pointer), so nothing is
unreachable and the busiest note is never the invisible one — with one card
per capture and a three-card cap, live on prod with 4 + 2 + 1 filings, the
two Kitchen-rebuild receipts showed twice while all four Shopping-list
receipts were the hidden ones. Grouping is `groupReceipts` in
`filing/model.ts`; the receipt is a branch of `FilingItem` rather than a
component of its own, and its React key is the group's newest capture id, so
the row that was just moving keeps its DOM node and its `role="status"` live
region announces the landing (a fresh node would mount silent).

Opening a receipt — the chevron, or the row, which the chevron's `::after`
stretches over — refreshes the note, dismisses every capture in the group
and navigates to it; the × dismisses without navigating. Dismissal is per
device (`dismissed.ts`, `localStorage`, the newest two hundred ids), because
`appended` rows stay in `GET /v1/captures` by contract and there is no
server-side "seen". A receipt expires on its own after a day
(`FILED_RECEIPT_MS`, `isFilingRelevant`): long enough for the walk home,
short enough that a second device does not meet receipts from weeks ago.

Home shows every capture the person did not watch land: everything the
router placed, and everything a device sent, whoever chose the note
(`isTargeted`, `targeted.ts`). A recording this app made *into* a note is
that note's to show — it was watched arriving on the Recordings tab — but a
ring's recording aimed at a note by `X-Chintan-Note-Id` is `targeted` on the
wire with nobody watching, so it gets a receipt like any routed one.

The poll behind all of this (`usePendingCaptures`) asks every 1.5 s for a
capture's first half-minute, 4 s while it is progressing, 15 s after two
quiet minutes, once a minute once it counts as stuck, and again whenever the
app comes to the foreground. How an open app might learn of a device's
capture without any gesture at all — and why that answer is Web Push — is
`docs/design/async-updates.md`.

## A widget or a hardware button

A web app cannot put a widget on an Android home screen or bind a hardware
button; the manifest shortcuts are the limit. The path is a thin native
wrapper — an Android TWA carrying a widget, or an iOS Shortcut on the Action
button posting to the inbox (`docs/design/inbox.md`) — and the inbox is ready
for either. Queued as Decision 5 in `docs/reviews/2026-09-21/morning-queue.md`;
nothing is built until the owner says which.

Tests: `machine.test.ts`, `store.test.ts` (stop-and-send), `CaptureScreen.test.tsx`,
`FilingBanner.test.tsx`, `FilingRow.test.tsx` (the tiers, the receipts, the poll's
ladder and focus refetch), `filing/model.test.ts`, `filing/FilingItem.test.tsx`,
`filing/TargetPrompt.test.tsx`, `features/talk/TalkScreen.test.tsx`,
`components/RecordButton.test.tsx` (a tap, and only a tap), `RecordingIndicator.test.tsx`,
`TabBar.test.tsx`; end to end, `frontend/e2e/capture.spec.ts` (Send while
recording, the return and the banner, the four discs on one row at 320 px),
`talk.spec.ts` (the hold, into a new note and into `?note=`, slide-away, the
short hold, Space), `manifest.spec.ts`.
