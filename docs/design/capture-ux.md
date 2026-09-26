# The recording screen and its shortcuts

Opening `/capture` starts recording; the screen is full-screen, its controls
are round discs with a word under each, and Send stops and sends in one tap.
The same machinery is reached two shorter ways: holding the tab-bar PTT disc
records in place and sends on release, and `/talk` is one giant PTT button
for thoughts one after another. This note is the frontend; what happens to the
recording once it is uploaded is the pipeline's and unchanged. Code: the
machine and store (`frontend/src/features/capture/machine.ts`, `store.ts`),
the screen (`CaptureScreen.tsx`), the gesture (`useHoldToTalk.ts`,
`HoldOverlay.tsx`, `components/RecordButton.tsx`, `components/TabBar.tsx`),
`/talk` (`features/talk/TalkScreen.tsx`), the note's filing banner
(`FilingBanner.tsx`, `FilingRow.tsx`, `features/notes/NoteDetailScreen.tsx`),
the shell's indicator (`components/RecordingIndicator.tsx`), the manifest
(`frontend/manifest.config.ts`).

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

`useHoldToTalk` is the gesture, shared by the tab-bar PTT disc (`holdDelayMs`
350: a press has to be told from a tap) and `/talk` (0: a press is a hold at
once). PTT is the name the app gives both — on the disc under its glyph, the
`/talk` heading, the You row and the manifest shortcut (owner feedback
2026-09-26); "hold to talk" is the instruction beneath the name, and the
buttons' `aria-label`s spell it out for a screen reader. Phases: idle → armed → holding → hint | sent | busy. The rules, with the
constants that pin them:

- Armed for `HOLD_DELAY_MS` (350 ms). Moving more than 10 px while armed is a
  scroll or a drag; its release is neither a tap nor a hold.
- Holding calls `store.start(target)`. The overlay above the bar shows the
  live level, the clock and "Release to send · slide away to cancel"; it is
  plain DOM over the current screen, so history is untouched and Back still
  works. Its instruction is spoken from one always-mounted live region.
- Release sends (`stopAndSend`) when the microphone is live, or when a
  recording settled with audio while the finger was still down — the cap or a
  call ending the track is a message, not a slip.
- Less than `MIN_TALK_MS` (600 ms) of audio is a slip: discarded, and "Hold to
  talk" shows for `HOLD_NOTICE_MS` (1.5 s).
- A pointer more than `SLIDE_AWAY_PX` (80) off the button's edge — the edge,
  not the press point, so a thumb drifting inside the `/talk` disc is not a
  cancel — turns the instruction to "Release to cancel"; release discards.
- Pressed while the last recording is still stopping or uploading: "Still
  sending the last one…" and no new recording. A microphone live on another
  screen, or unsent audio waiting on the capture screen, stands the hold
  down: the tap that follows goes there. A refused microphone is the capture
  screen's failure card to explain, so the release hands off to it.
- The target is the tab bar's: the open, unarchived note (the bar reads "Into
  this note"), else a new note for the router to file. On `/talk` the pill
  above the disc is the target and the tab-bar mic only taps — two hold
  surfaces in one viewport recording to different places would be two answers
  to one question.

`/talk` is the walkie-talkie: hold, speak, release, "Sent · filing" for 1.5 s,
ready for the next; `?note=` seeds the pill. The disc is `min(100%, 26rem,
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

## A widget or a hardware button

A web app cannot put a widget on an Android home screen or bind a hardware
button; the manifest shortcuts are the limit. The path is a thin native
wrapper — an Android TWA carrying a widget, or an iOS Shortcut on the Action
button posting to the inbox (`docs/design/inbox.md`) — and the inbox is ready
for either. Queued as Decision 5 in `docs/reviews/2026-09-21/morning-queue.md`;
nothing is built until the owner says which.

Tests: `machine.test.ts`, `store.test.ts` (stop-and-send), `CaptureScreen.test.tsx`,
`FilingBanner.test.tsx`, `FilingRow.test.tsx`, `features/talk/TalkScreen.test.tsx`,
`components/RecordButton.test.tsx`, `RecordingIndicator.test.tsx`, `TabBar.test.tsx`;
end to end, `frontend/e2e/capture.spec.ts` (Send while recording, the return
and the banner, the hold on Home and on a note, slide-away and the short hold,
the four discs on one row at 320 px), `talk.spec.ts`, `manifest.spec.ts`.
