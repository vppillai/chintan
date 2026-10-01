# The recording screen and its shortcuts

Opening `/capture` starts recording; the screen is full-screen, its controls
are round discs with a word under each, and Send stops and sends in one tap.
The same machinery is reached one shorter way: the tab-bar disc
(`components/RecordButton.tsx`, `components/TabBar.tsx`) opens `/capture`
on a tap and is push-to-talk on a hold — hold, speak, release, WhatsApp's
way (R8, F5). This note is the frontend; what happens to the recording once it is uploaded is
the pipeline's and unchanged. Code: the machine and store
(`frontend/src/features/capture/machine.ts`, `store.ts`), the screen
(`CaptureScreen.tsx`), the gesture (`holdGesture.ts`, the pure reducer,
and `useHoldToTalk.ts`, which wires it to the browser), the note's filing banner
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
reads it. With no note chosen the pill reads "Chintan decides", and so does
the first option of its list, above the recent notes (R7-14, owner
2026-09-30): no target means the router files it, possibly into a note that
exists — the hint under it says "add this to my roof note" — and the old
"Into New note" promised the opposite. A chosen note reads "Into ⟨title⟩".
There is no option that forces a new note; that is an owner follow-up in the
backlog (R7-14a). A retarget is written to the capture record so a recording
resumed after a reload goes where it was last aimed. `captureReturnPath(
noteId)` is where every exit goes: the note when there is a target, else
Home. Send used to force the Recordings tab so the upload could be watched
there, which took someone reading the text to a list of players; the note now
reopens on whichever tab it was left on, and the filing banner says the
recording is still coming. Between the meta line and the tab strip it draws
this device's own upload row ("Uploading… 40 %") while the PUT is in flight,
then the newest of the note's captures that is neither appended nor dismissed
as the same row the library draws — "Filing your recording" over the four
stage segments (Uploaded · Transcribing · Filing · Saving) — until it appends.
The body has refreshed by then, but in a long note the paragraph is far below
where the person was reading, so for six seconds the banner says "Added at the
end · Show" ("Added to the list" on a checklist); Show scrolls to the
recording's addition and marks it in the selection wash for two seconds — the
paragraph, drawn in the find mirror's box, or the rows it added — without
motion under reduced motion (R7-6b). Focus goes with Show — onto the marked
paragraph, then into the textarea with the caret at its start; on a checklist
into the first added row's field — and "Added text shown" is said politely,
so the keyboard and a screen reader land there too. The note itself comes back at the offset
it was left at (`RESTORE_SCROLL`, `useScrollRestore`), not at its title. A
failed or stuck capture keeps the row's Retry and its ×; a dismissal is per
device (`dismissed.ts`), so it does not return with the note. The banner is
the library's tray around its one row (F9), with no heading, and the "Added
at the end · Show" line sits in the same tray with the receipt's check. It
is not drawn on the Recordings tab, whose own row wears the same strip.

Where the contract said `captureReturnPath(noteId, sent)` the code has one
parameter: once every exit went to the note, `sent` decided nothing. And the
banner reuses the whole `FilingItem`, not only `FilingStages`, so a failure is
met with exactly the library's controls.

## PTT (hold to talk)

The record disc on the tab bar is the one record control, on every screen
that shows the bar: Home, Archive, a note, You, About and Usage. It records
into the open, unarchived note or into a new note (`useRecordTarget()`). A
tap opens `/capture`, which records at once, hands-free, with Pause, Stop,
review and the target chooser. A hold is push-to-talk. `/talk`, the PTT
screen it replaces, and its manifest shortcut are gone (R8, F7); `/talk`
redirects to Home, because an installed app keeps the old shortcut until
Chrome refreshes the manifest. The owner once asked for the hold on that
screen only (feedback 2026-09-27); having tried it, he asked for the hold
on the disc and for the screen to go (feedback 2026-09-30).

`holdGesture.ts` decides every transition as a pure reducer; `useHoldToTalk`
feeds it pointer, key, timer, focus and visibility events and runs the
effects. The numbers are `holdTiming.ts`'s, measured from the press point:

- **Tap or hold.** A press shorter than `HOLD_ARM_MS` (250 ms) is a tap; the
  click that follows opens `/capture`. Moving `TAP_SLOP_PX` (10) arms at
  once. Arming calls `store.start(target)` with the start buzz and tone. The
  tap is the click, not the pointerup, so TalkBack, VoiceOver and Enter
  work; after a hold the click Chromium sends is swallowed for
  `CLICK_SUPPRESS_MS` (600 ms).
- **Release sends** (`stopAndSend`) when the microphone is live, or when a
  recording settled with audio under the finger (a call ended the track, the
  cap). Less than `MIN_TALK_MS` (600 ms) is a slip: discarded, with "Too
  short — hold to talk" for `HOLD_NOTICE_MS` (1.5 s).
- **Slide left `CANCEL_DX_PX` (110 px)** from the press point cancels on
  crossing: discarded, "Cancelled", and the rest of the drag is ignored. It
  used to be 80 px past the edge of the `/talk` disc, which on a phone was
  off the screen, so it never cancelled (F6).
- **Slide up `LOCK_DY_PX` (72 px)** locks: the finger may lift and the
  recording goes on in place. The disc becomes Send, the Home slot Discard
  and the You slot Stop (review on `/capture`). A locked take of
  `DISCARD_CONFIRM_AFTER_MS` (10 s) or more asks "Discard?" for
  `DISCARD_CONFIRM_MS` (3 s) before it goes. A locked take that stops on its
  own (a call, the cap) opens review; it is never sent or discarded for you.
- **Interruptions are releases, never discards.** `pointercancel`,
  `lostpointercapture`, a hidden page and, for a key hold, the window losing
  focus all count as a release. `/talk` discarded on blur. A locked
  recording carries on through a hidden page, as `/capture` does.
- **The first press on a fresh install** raises the permission prompt, and
  the finger lifts to answer it. Released while still `requesting`, with the
  permission not already granted, the hold locks ("Allow the microphone…")
  instead of failing as too short. A refused microphone starts nothing and
  says "Microphone blocked" for 3 s.
- **Stand-down.** While the last recording is stopping or uploading, a hold
  says "Still sending the last one…". With another recording live or unsent
  audio waiting, the hold stands down and the release is a tap, so
  `/capture` shows that recording.
- **Keys.** Space on the focused disc holds with the same arm; a quick Space
  is a tap. R held from anywhere outside a field, with no modifier, is
  push-to-talk; a quick R does nothing. Escape cancels a hold and is Discard
  while locked. Space is not taken globally, because it pages the content.

What the bar draws: while held, the disc follows the finger at 1.2× with the
accent ring, the Home slot reads "‹ Slide to cancel" (fading, and
destructive with a bin from 60 % of the way), the You slot shows the live
level, and a lock pill stands above the disc. The tabs keep their boxes,
hidden and inert, so the bar keeps its height and nothing in `.app__main`
moves. The clock pill rides the bar's top edge where "Into this note" sits,
and carries the notices. Reduced motion drops the travel, the scale and the
bob. One polite `role="status"` in the bar announces "Recording", "Locked.
Recording hands-free.", "Cancelled", "Sent", "Too short" and "Microphone
blocked". The disc's `aria-description` says how to hold, and
`aria-keyshortcuts` is R. The shell's recording indicator hides while the
bar holds or is locked, and through the "Sent" beat. On Home, the first
three launches show "Hold to talk · tap to record" in the pill, until a hold
first sends (`chintan.coach.ptt`).

## Receipts on Home

The top of the library is where a recording's filing is watched and where
its landing is confirmed. `FilingRow` draws four tiers, in this order: this
device's own upload (`useLocalUpload`, `LocalUploadItem`); rows still moving
(the four stage segments); rows that need the person — failed, capped,
asking "which note?", stuck past ten minutes — never grouped, never folded
and always above the fold; then the receipts, one per note the rest landed
in, newest landing first.

The section is a notice, not a note (F9, owner 2026-09-30): an h2 "Filing"
(the region's name, `aria-labelledby`) over one inset tray — `--color-notice`
(surface) with `--shadow-notice` (the inset shadow), no border — holding
every row, divided by `--color-notice-line` hairlines. Each row is three
columns, each part placed by name: a 28 px glyph (`--layout-notice-glyph`),
the text, and a 44 px ×. The glyph says the kind (`noticeKind` in
`filing/model.ts`, `data-kind` on the row): the Bindu mark for a row still
moving, `route` (ink) for "which note?", `alert` (`--color-notice-alert`)
for failed, capped, stuck or a failed upload — muted for `no_content`, where
nothing went wrong — `check` for a receipt and the fold, `plus` for a note
the recording started. The glyphs are `aria-hidden`; the title says the same
in words. R7's receipt grid set a row but no column on its chevron and ×, so
the grid placed them first and the title dropped to a second line; the
explicit columns, and an e2e check that the title and the × share a line
(`layout.spec.ts`), keep that from recurring.

A receipt is one line, plus an excerpt line when there is one (R7-7a,
owner 2026-09-30): the title cut with an
ellipsis — "Filed into “Roof repair”", "3 filed into “Kitchen rebuild”", or
"Started “Plumber”" when the capture's `created_note`
says the note was made for it (R7-7c), so a misroute into a fresh note is
visible — then "· 2 min" (`describeAgoShort`: now, min, h, d; a screen
reader hears "ago"), then the ×. There is no chevron (F9): the whole row
opens the note, and the time, chevron and × columns were what squeezed the
title to three lines. The × is always drawn, under a finger too, which
supersedes R7-7a's hidden × (F9 asked for notices "clearly identifiable as
dismissible"); the width is paid back by the chevron's column and the gaps
between rows, both gone. It is named "Dismiss" on every row. A failed or
stuck row has Retry and the ×, which replaced its text "Dismiss" button; a
`no_content` row has the × alone; a row asking "which note?" has no ×, since
putting it away would hide a recording that is in no note yet — answering is
its way off the screen. This device's failed upload keeps "Discard" as a
word, never an ×: it deletes the audio, and an × here means "put away". The
row's swipe tray (Dismiss) and "Clear all" are the other ways to put a
receipt away. One receipt is a row. Two or more fold into one
summary row, "3 filed into 2 notes ›" — a button with `aria-expanded` that
opens the receipts in place — beside a "Clear all" that dismisses every one
of them. Rows inside the fold carry no live region of their own; a landing
that makes or grows the fold is spoken by one visually hidden polite region
`FilingRow` keeps mounted whatever it shows ("Filed into “Kitchen”. 2 filed
into 2 notes"), and only a growing count speaks, so a dismissal or the
receipts already there when Home opens are silent. Dismissing one of two
receipts from the open fold puts focus on the survivor's ×. Grouping is `groupReceipts` in `filing/model.ts`, which
also carries the group's `createdNote` and its newest capture's excerpt. The
receipt is a branch of `FilingItem` rather than a component of its own,
both branches sit in one `SwipeRow` (which keeps one tree whether or not its
tray is on), and the React key is the group's newest capture id, so the row
that was just moving keeps its DOM node and its `role="status"` live region
announces the landing (a fresh node would mount silent).

What was said is on the row (R7-7b): the capture's `excerpt` — about ninety
characters of the cleaned text, or of the transcript before the clean, null
until the capture is transcribed — is a muted second line under "Which note
should this go in?", under a failed, capped or stuck row (the note's banner
included), and under a receipt, in the fold's expanded view too.

Opening a receipt — the row, which a zero-size "Open the note" button's
`::after` stretches over, its focus ring drawn on the row — refreshes the note, dismisses every capture in the group
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
`filing/TargetPrompt.test.tsx`, `holdGesture.test.ts` (every transition, on a
fake clock), `components/RecordButton.test.tsx` (tap, hold, Space, Enter, Escape,
the swallowed click, blur, the first-press prompt), `RecordingIndicator.test.tsx`,
`TabBar.test.tsx` (the slots, lock, Discard, Stop, Send, R, the coach); end to end,
`frontend/e2e/capture.spec.ts` (Send while recording, the return and the banner,
the four discs on one row at 320 px, hold to send, drag left to cancel, drag up to
lock, and the F6 touch slide on a 412 px phone), `manifest.spec.ts`.
