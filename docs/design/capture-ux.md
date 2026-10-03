# The recording screen and its shortcuts

Opening `/capture` starts recording; the screen is full-screen, its controls
are round discs with a word under each, and Send stops and sends in one tap.
The tab bar's record disc (`components/RecordButton.tsx`, `components/TabBar.tsx`)
reaches the same machinery one shorter way: a tap opens `/capture`, a hold
is push-to-talk. This note is the frontend; what happens to the recording
once it is uploaded is the pipeline's. Code: the machine and store
(`frontend/src/features/capture/machine.ts`, `store.ts`), the recorder and
its buffer (`recorder.ts`, `buffer.ts`, `wakeLock.ts`), the screen
(`CaptureScreen.tsx`, `TargetChooser.tsx`), the gesture (`holdGesture.ts`, a
pure reducer; `holdTiming.ts`, its numbers; `useHoldToTalk.ts`, which wires it
to the browser; `components/HoldChrome.tsx`, what the bar draws), the note's
filing banner (`FilingBanner.tsx`, mounted by `features/notes/NoteDetailScreen.tsx`),
the library's filing row (`FilingRow.tsx`, with its parts under `filing/`:
`model.ts` for what a row says, `FilingItem.tsx` for one capture's row,
`TargetPrompt.tsx` for "which note?", `useLocalUpload.ts` for this device's
own upload), the poll (`api/queries/captures.ts`), the unsent-recording
prompt (`ResumePrompt.tsx`) and the reconnect resend (`useResendOnReconnect.ts`),
the shell's indicator (`components/RecordingIndicator.tsx`), the manifest
(`frontend/manifest.config.ts`) and the sheets (`styles/capture.css`;
`styles/ptt.css` for the held bar).

## The machine

The recording is a pure reducer driven by a controller and held in a Zustand
store outside React, so it survives navigation. States: idle → requesting →
recording ⇄ paused → stopping → review → uploading → uploaded, with failed off
to the side. Two invariants: nothing says "Recording" before `getUserMedia`
resolves, and an interruption — a call ending the track, a headset unplugged,
the cap (`MAX_DURATION_MS`, twenty minutes; `MAX_BYTES`, 32 MiB) — yields a
partial recording in `review`, never a discard. Everything below is a way of
driving that machine.

## Send while recording

While `recording` or `paused` the row is Cancel (×) · Pause or Resume · Stop
(■) · Send (↑). Send is the row's only filled disc, so at arm's length the
eye lands on it; the accent stays the record button's. Stop goes to `review` —
Discard · Re-record · Send, with the recording's own waveform to listen back —
and Send there uploads. Send from the recording row is `stopAndSend`: the
store asks the recorder to stop and remembers the api. MediaRecorder hands
over its last chunk after `stop()` returns, so the send cannot follow on the
same call stack; `dispatch` watches the transition out of `stopping` and sends
only if it lands in `review`. Nothing recorded, a discard or a recorder error
with no audio drops the request. The screen leaves at once, replacing its own
history entry (a `/capture` entry left behind would make the next Back a new
recording), and the filing row or the banner shows the upload. A failed upload
sits on the filing row with Retry and Discard, the bytes still on the device.

The discs are `--capture-control-size`: 4 rem (64 px) on a phone and 3.5 rem
under a fine pointer, four equal grid columns that do not wrap at 320 px; the
word under each disc is visible and is the accessible name. `requesting` shows
Cancel alone. `failed` shows Discard + Try again (resend) while audio is
buffered, Close + Try again (asks for the microphone again) when it was
refused or missing, otherwise Close.

Cancel or Discard on a take of `DISCARD_CONFIRM_AFTER_MS` (10 s, `machine.ts`)
or more opens the app's `ConfirmDialog`, "Discard 0:42 of recording?", with
Keep and Discard; Escape, like Keep, keeps it. A shorter take is discarded at
once. This holds whether the take began from a tap or a locked hold. Close on
a failure with no audio to lose does not ask.

The manifest's "Record a thought" shortcut opens `/capture` and records at once.

## Recording into a note, and coming back

`?note=` seeds the target at `start`; the chooser pill (`TargetChooser.tsx`)
can move it until Send reads it. With no note chosen the pill reads "Chintan
decides", and so does the first option of its list, above the recent notes: no
target means the router files it, possibly into a note that exists — the hint
under it says "add this to my roof note". A chosen note reads "Into ⟨title⟩".
There is no option that forces a new note. A retarget is written to the
capture record so a recording resumed after a reload goes where it was last
aimed.

`captureReturnPath(noteId)` is where every exit goes: the note when there is
a target, else Home. The note reopens on whichever tab it was left on, at the
offset it was left at (`RESTORE_SCROLL`, `useScrollRestore`), and the filing
banner says the recording is still coming. Between the meta line and the tab
strip the banner draws this device's own upload row ("Uploading… 40 %") while
the PUT is in flight, then the newest of the note's captures that is neither
appended nor dismissed as the same row the library draws — "Filing your
recording" over the four stage segments (Uploaded · Transcribing · Filing ·
Saving) — until it appends. The body has refreshed by then, but in a long
note the paragraph is far below where the person was reading, so for
`LANDED_MS` (6 s) the banner says "Added at the end · Show" ("Added to the
list" on a checklist). Show scrolls to the recording's addition and marks it
in the selection wash for `FLASH_MS` (2 s, `TextPanel.tsx`) — the paragraph,
drawn in the find mirror's box, or the rows it added — without motion under
reduced motion. Focus goes with Show: onto the marked paragraph, then into
the textarea with the caret at its start; on a checklist into the first added
row's field. "Added text shown" is said politely, so a screen reader lands
there too. A failed or stuck capture keeps the row's Retry and its ×; a
dismissal is per device (`dismissed.ts`), so it does not return with the
note. The banner is the library's tray around its one row, with no heading;
it reuses the whole `FilingItem`, so a failure is met with exactly the
library's controls. It is not drawn on the Recordings tab, whose own row
wears the same strip.

## Hold to talk

The record disc on the tab bar is the one record control, on every screen
that shows the bar: Home, Archive, a note, You, About and Usage. It records
into the open, unarchived note or into a new note (`useRecordTarget()`; an
archived note is refused by the server, so it is never a target). A tap opens
`/capture`, which records at once, hands-free, with Pause, Stop, review and
the target chooser. A hold is push-to-talk. `/talk` redirects to Home
(`app/routes.ts`), because an installed app keeps a manifest shortcut until
the browser refreshes the manifest.

`holdGesture.ts` decides every transition as a pure reducer over five phases
— `idle`, `armed` (pressed, may still be a tap), `holding` (recording under
the finger), `cancelled` (slid past the line; waits for the lift) and
`standdown` (a press that will not record) — and returns effects;
`useHoldToTalk` feeds it pointer, key, timer, focus and visibility events and
runs the effects. Distances and times are `holdTiming.ts`'s, measured from
the press point and the press time, so they fit the narrowest phone:

| Constant | Value | Rule |
|---|---|---|
| `HOLD_ARM_MS` | 250 ms | A shorter press is a tap; the click that follows opens `/capture` — the tap is the click, not the pointerup, so TalkBack, VoiceOver and Enter work. A longer one arms: `store.start(target)`, the start buzz and tone. |
| `GESTURE_SLOP_PX` (`hooks/gesture.ts`) | 10 px | Moving this far while armed arms at once: a fast slide is a hold from the first frame. |
| `MIN_TALK_MS` | 600 ms | Less audio than this on release is a slip: discarded, "Too short — hold to talk". |
| `CANCEL_DX_PX` | 110 px | Slide left this far and the take is discarded on crossing, "Cancelled"; the rest of the drag is ignored. |
| `LOCK_DY_PX` | 72 px | Slide up this far and the take locks: the finger may lift. Lock is checked before cancel when one move crosses both, because keeping the audio is the mistake that costs nothing. |
| `CLICK_SUPPRESS_MS` | 600 ms | The click a browser sends after a long press is swallowed for this long. |
| `HOLD_NOTICE_MS` / `BLOCKED_NOTICE_MS` | 1.5 s / 3 s | How long "Sent", "Too short", "Cancelled" stand; "Microphone blocked" stays longer because it is the one notice that needs acting on. |

The rules the reducer applies:

- **Release sends** (`stopAndSend`) when the microphone is live, or when a
  recording settled with audio under the finger (a call ended the track, the
  cap stopped it): that audio is the message.
- **A lock opens `/capture`** on the same take: the screen takes over a
  recording that is already running (`isCaptureBusy`), so there is no gap, no
  second recorder, the target note is kept and the clock carries on from the
  press. `/capture` is pushed, so Back returns to where the hold began. The
  shell announces "Recording, hands-free".
- **Interruptions are releases, never discards.** `pointercancel`,
  `lostpointercapture`, a hidden page and, for a key hold, the window losing
  focus all count as a release: what was said is sent, or a slip gets the
  hint. A pointer hold ignores blur, because the permission prompt takes focus
  without the finger leaving the disc. A locked recording is the capture
  screen's and carries on through a hidden page.
- **The first press on a fresh install** raises the permission prompt, and
  the finger lifts to answer it. Released while still `requesting`, with the
  permission not already granted, the hold locks, so the capture screen waits
  on the prompt and records once allowed, instead of failing as too short;
  an R or Space hold released here locks the same way. With the permission
  granted, a release during a slow start is the slip it
  looks like. A refused microphone starts nothing and says "Microphone
  blocked".
- **Stand-down** is decided at the press and applied at the arm: while the
  last recording is stopping or uploading, the hold says "Still sending the
  last one…"; with another recording live or unsent audio waiting, the release
  is a tap, so `/capture` shows that recording; with the microphone denied,
  "Microphone blocked".
- **Keys.** Space on the focused disc holds with the same arm; a quick Space
  is a tap. R held from anywhere outside a field, with no modifier, is
  push-to-talk; a quick R does nothing, so a stray key never starts or opens
  anything. Escape cancels a hold (and, inside the arm, makes the release a
  no-op) unless a field, an open dialog or menu, or another handler has it.
  Space is not taken globally, because it pages the content.

What the bar draws (`HoldChrome.tsx`, `ptt.css`): while held, the disc
follows the finger at 1.2× with the accent ring, the Home slot reads
"‹ Slide to cancel" (drifting at half the finger's travel, never closer than
16 px to the screen's edge, fading, and
destructive with a bin from 60 % of the way), the You slot shows the live
level, and a lock pill stands above the disc. The tabs keep their boxes,
hidden and inert, so the bar keeps its height and nothing in `.app__main`
moves. The clock pill rides the bar's top edge where "Into this note" sits,
and carries the notices. Reduced motion drops the travel, the scale and the
bob. One polite `role="status"` in the bar announces "Recording",
"Cancelled", "Sent", "Too short" and "Microphone blocked". The disc's hidden
description (`aria-describedby`) says how to hold, and `aria-keyshortcuts` is
R. The shell's recording indicator hides while the bar holds, and through the
"Sent" beat. On Home, the first `COACH_LAUNCHES` (3) launches show "Hold to
talk · tap to record" in the pill, until a hold first sends
(`localStorage` `chintan.coach.ptt`).

## A recording survives

Three parts keep a recording from being lost between the microphone and the
server.

- **The buffer is on disk.** `recorder.ts` writes each `ondataavailable`
  chunk to IndexedDB (`buffer.ts`, keys zero-padded so chunk 10 sorts after
  chunk 2) and prunes it only after the server confirms the upload. A tab the
  OS reclaims mid-recording — routine on a phone during a long take — leaves
  the bytes where `ResumePrompt` finds them.
- **The screen stays awake.** The recorder holds a screen wake lock
  (`wakeLock.ts`, `acquireWakeLock`) for the recording: a sleeping phone
  suspends timers and may reclaim the tab. The browser releases the lock on
  every visibility change, so the handle re-requests it when the page comes
  back; a denied request (a hidden document, a battery policy) is ignored,
  because the lock is a robustness measure, not a precondition.
- **An unsent recording is offered back.** `ResumePrompt`, on Home,
  reads the unconfirmed captures once on boot (`unconfirmedCaptures`) and
  offers the oldest — "You have an unsent recording from 3 minutes ago", its
  length, "2 more waiting" — with Send and Discard. Send is `uploadCapture`
  with the stored `serverCaptureId`, so the server answers a resumed create
  with the capture it already minted. Discard is confirmed ("It has not been
  sent, and it is not saved anywhere else."), because these bytes exist in
  exactly one place. The recording the store is sending, has sent or has
  failed to send is excluded by its id, or the prompt would offer to resend
  the upload the filing row is showing one row below; a recording left at
  `review` is not excluded — the prompt is the way back to it.
- **A reconnect resends once.** `useResendOnReconnect` (in `AppShell`)
  watches `useOnline`; on an offline → online transition it calls the store's
  `send` once for a recording that `awaitsConnection`: `failed` with a
  recoverable `upload-failed` failure, which is a recording the person already
  asked to send and only the network stopped. One attempt per reconnect; if
  it fails the row's Retry stays. A take at `review` waits for the person,
  and a spend cap is not retried, because a connection does not fix it.

## What happens when the screen locks

The recorder is built on three browser signals and holds one lock. What each
platform does with them is the browser's; what the app does with each is
`recorder.ts` and the machine. The two platform columns are from the
platforms' documentation and bug trackers — Chrome's page lifecycle rules
exempt a tab that is capturing audio from freezing and discarding; WebKit
bug 173268 is iOS muting a capture track when the page goes to the
background and unmuting it on return; iOS ending the track about thirty
seconds into the background is reported by developers, not documented —
and are unverified on a device until the owner's check below is run.
Nothing stops on the hide itself: Android keeps the recorder running behind
a locked screen, and a stop there would truncate a recording that was going
to survive.

| Signal | Android Chrome (documentation, unverified on a device) | iOS Safari and the installed app (documentation, unverified on a device) | What the app does |
|---|---|---|---|
| The page goes hidden (`visibilitychange`) | Fires when the screen locks or another app comes in front. Chrome keeps the renderer alive while the tab holds the microphone and shows its recording notification; `MediaRecorder` keeps delivering timeslices; `setInterval` is throttled, so the clock's ticks lag and catch up. | Fires; WebKit mutes the capture track and freezes the page within moments; the installed app may be jettisoned while hidden. | The recorder asks `MediaRecorder` for the chunk it is holding (`requestData`, up to `CHUNK_INTERVAL_MS` of speech) so it is on disk before anything else happens; the machine marks `hidden`; the capture record is rewritten with the recording's real length on the hide and on every chunk after it (`store.ts`), so a killed page leaves a record of the right length. The recording goes on. |
| The track mutes (`mute`) | A call, Siri's equivalent, another app taking the microphone — not the lock. | The lock, and any backgrounding; `unmute` on return. | The recorder pauses and the clock stops. The state reads "Paused — the screen locked or the app went to the background" when the page was hidden, "Paused — the microphone was taken by another app" otherwise; Cancel · Resume · Stop · Send. `unmute` resumes by itself with "Resumed — the microphone is back.", unless the person had paused first. |
| The track ends (`ended`) | A call, a headset unplugged. | The same, the OS reclaiming the device, and — as developers report — about thirty seconds into the background. | Stop; review with the audio so far — "The recording stopped while the screen was locked or the app was in the background. What was captured before that is here." when the page was hidden, "The recording was interrupted. …" otherwise — with Discard · Re-record · Send. |
| The recorder stops on its own (`onstop` with no Stop) | No report found. | A thawed page whose recorder the OS ended. | A final chunk nobody asked for is settled as an interruption: review with the audio and the sentence above, or "Recording was interrupted." when nothing was recorded. The screen never says "Recording" over a dead recorder. |
| The page is killed | Chrome's discarding exempts a tab capturing audio; memory pressure can still take it. | Freely, once hidden. | Every chunk handed over is in IndexedDB and the record names it with its length as of the last chunk; Home's `ResumePrompt` offers it with Send and Discard. |
| The wake lock | Held for the recording; the browser releases it when the page hides; `wakeLock.ts` re-requests it when the page is visible again. | The same from the versions that have it; none before. | Keeps the screen from sleeping by itself. The power button locks it anyway, and the rows above are what happens then. |

Chromium cannot lock a screen, so `e2e/capture.spec.ts` "the screen locks
while recording" raises the same signals on the real stream and document —
hidden, then `mute` or `ended` — and asserts the sentences, the controls,
and the record on disk. What only a phone can confirm is the **owner's
check**, one installed app on each:

1. Open the "Record a thought" shortcut and speak a sentence. At 0:10 press
   the power button. Wait a minute, speak a second sentence, unlock.
2. Android, expected: still "Recording", the clock past 1:10, both sentences
   in the note after Send. If the screen reads "Paused — the screen locked
   or the app went to the background", Android muted the track: Resume, and
   report it.
3. iPhone, expected one of: "Paused — the screen locked or the app went to
   the background" then "Resumed — the microphone is back." within a second,
   with the second sentence in the note after Send; or Home with "You have
   an unsent recording from a moment ago" at 0:10, which Send files. If the
   clock counted through the lock and the note has only the first sentence,
   the track did not mute while the recorder ran on silence: report it.

## Receipts on Home

The top of the library is where a recording's filing is watched and where
its landing is confirmed. `FilingRow` draws four tiers, in this order: this
device's own upload (`useLocalUpload`, `LocalUploadItem`); rows still moving
(the four stage segments); rows that need the person — failed, capped,
asking "which note?", stuck — never grouped, never folded and always above
the fold; then the receipts, one per note the rest landed in, newest landing
first.

The section is a notice, not a note: an h2 "Filing" (the region's name,
`aria-labelledby`) over one inset tray — `--color-notice` with
`--shadow-notice`, no border — holding every row, divided by
`--color-notice-line` hairlines. Each row is three columns, each part placed
by name: a 28 px glyph (`--layout-notice-glyph`), the text, and a 44 px ×. The
glyph says the kind (`noticeKind` in `filing/model.ts`, `data-kind` on the
row): the Bindu mark for a row still moving, `route` for "which note?",
`alert` (`--color-notice-alert`) for failed, capped, stuck or a failed upload
— muted for `no_content`, where nothing went wrong — `check` for a receipt
and the fold, `plus` for a note the recording started. A `no_content` row's
title is "Nothing heard" when a transcription gate ended it (the wire's
`gate`: the microphone never rose above the recorder's floor, the provider
was unsure of every word, or it read its prompt back; routing.md, "Before
routing") and "Nothing to save from that recording" when the recording was
heard and was only an instruction to the app; a recording is never dropped
without a row. A "Nothing heard" row has Transcribe anyway beside its × — a
44 px control on the same `/retranscribe` the Recordings tab's Transcribe
again uses, which lifts the gate so the person's word wins; the row says
"Transcribing again." or "Could not transcribe that again." through the live
region — and the same two labels are the Recordings tab's
(`recordings/labels.ts` `filedLabel`). The glyphs are
`aria-hidden`; the title says the same in words. An e2e check holds the title
and the × to one line (`layout.spec.ts`).

A receipt is one line, plus an excerpt line when there is one: the title cut
with an ellipsis — "Filed into “Roof repair”", "3 filed into “Kitchen
rebuild”", or "Started “Plumber”" when the capture's `created_note` says the
note was made for it, so a misroute into a fresh note is visible — then
"· 2 min" (`describeAgoShort`: now, min, h, d; a screen reader hears "ago"),
then the ×. There is no chevron: the whole row opens the note, through a
zero-size "Open the note" button whose `::after` stretches over it with the
focus ring drawn on the row. The × is always drawn, under a finger too, and
is named "Dismiss" on every row. A failed or stuck row has Retry and the ×
(on a stuck row the × is a delete once the server allows one, below); a
`no_content` row has the × alone; a row asking "which note?" has no ×, since
putting it away would hide a recording that is in no note yet — answering is
its way off the screen. This device's failed upload keeps "Discard" as a
word, never an ×: it deletes the audio, and an × here means "put away". The
row's swipe tray (Dismiss) and "Clear all" are the other ways to put a
receipt away.

One receipt is a row. Two or more fold into one summary row, "3 filed into 2
notes ›" — a button with `aria-expanded` that opens the receipts in place —
beside a "Clear all" that dismisses every one of them. Rows inside the fold
carry no live region of their own; a landing that makes or grows the fold is
spoken by one visually hidden polite region `FilingRow` keeps mounted
whatever it shows ("Filed into “Kitchen”. 2 filed into 2 notes"), and only a
growing count speaks, so a dismissal or the receipts already there when Home
opens are silent. Dismissing one of two receipts from the open fold puts
focus on the survivor's ×. Grouping is `groupReceipts` in `filing/model.ts`,
which also carries the group's `createdNote` and its newest capture's
excerpt. The receipt is a branch of `FilingItem` rather than a component of
its own, both branches sit in one `SwipeRow`, and the React key is the
group's newest capture id, so the row that was just moving keeps its DOM node
and its `role="status"` live region announces the landing (a fresh node would
mount silent).

What was said is on the row: the capture's `excerpt` — about ninety
characters of the cleaned text, or of the transcript before the clean, null
until the capture is transcribed (`model.Excerpt`, `model/types.go`) — is a
muted second line under "Which note should this go in?", under a failed,
capped or stuck row (the note's banner included), and under a receipt, in the
fold's expanded view too.

Opening a receipt refreshes the note, dismisses every capture in the group
and navigates to it; the × dismisses without navigating. Dismissal is per
device (`dismissed.ts`: `localStorage` `chintan.filing.dismissed`, the newest
`DISMISSED_LIMIT` = 200 ids), because `appended` rows stay in
`GET /v1/captures` by contract and there is no server-side "seen". Which rows
are the row's to show is `isFilingRelevant` (`captures.ts`): anything still
moving; `failed`, `spend_capped` and `needs_target` always, because each has
an action the person must take; `appended` for `FILED_RECEIPT_MS` (a day) —
long enough for the walk home, short enough that a second device does not
meet receipts from weeks ago; `no_content` for `RECENTLY_SETTLED_MS` (ten
minutes), since there is nothing to open and nothing to do.

A moving row shows its age once that is a minute: "Filing your recording"
with "· 4 min" beside it (`describe` in `filing/model.ts`, `describeAgoShort` of
`last_progress_at ?? created_at` — the poll's clock — on the row's minute
tick, `hooks/useMinuteNow.ts`). It counts as stuck after `STUCK_AFTER_MS`
(ten minutes, `schema.ts`; `isStuck`): the strip goes, since no stage is in
progress, and the row says "Still not done. You can dismiss it." until the
server will take a Retry, then "Still not done. Retry, or dismiss it." —
fixed sentences by state, so a change is a change of state. The age ("· 12
min") is drawn beside the sentence, `aria-hidden`, never inside its
`role="status"`: it moves every minute, and a stuck row read out again each
minute for ever is worse than one that says nothing (`describe` returns
`{sentence, age}`). Retry appears when the
server will accept it (`retryAccepted`): at `CaptureWire.retry_after`, the
server's own `CaptureRetryAfter` answer, which is on every non-terminal
capture; the row keeps no copy of the rule, and a tap before that instant is
answered by the 409's own sentence under the row. The × on a stuck row is not only a dismissal: past the same
bound it deletes the capture (`StuckDismissButton`, `useDeleteStuckCapture`
in `queries/captures.ts`), which drops it from the pending list at once —
so the poll stops counting it — and from its note; a refusal (an upload
whose object may yet land) hides the row on this device and says the
server's sentence through the status region; before the bound the × hides
the row as on any other. The note's Recordings tab applies the same rule to
its row (`note-screen.md`).

Home shows every capture the person did not watch land: everything the
router placed, and everything a device sent, whoever chose the note
(`CaptureWire.targeted`; `targeted.ts` is the device-side fallback,
`localStorage` `chintan.filing.targeted`). A recording this app made *into* a
note is that note's to show — it was watched arriving on the Recordings tab —
but a ring's recording aimed at a note by `X-Chintan-Note-Id` is `targeted`
on the wire with nobody watching, so it gets a receipt like any routed one.

### The poll

A recording is appended by the worker, not by the client that is watching,
so the app asks. `usePendingCaptures` makes one request,
`GET /v1/captures?status=all&limit=20` (`CAPTURE_LIST_LIMIT`), filtered on
the client by `isFilingRelevant` rather than four server-side filters in
parallel. Its cadence is `capturePollInterval`, a ladder keyed on how long
ago anything moving last made progress (`last_progress_at`, else
`created_at`); the youngest progress decides, so one capture still moving
keeps the poll brisk however long another has been stuck beside it:

| Since the last progress | Constant | Interval | Why |
|---|---|---|---|
| a capture is under `CAPTURE_POLL_FAST_WINDOW_MS` (30 s) old | `CAPTURE_POLL_FAST_MS` | 1.5 s | the first half-minute is when a capture is most likely to flip; a 4 s poll adds a median 2 s of pure waiting to a pipeline that usually finishes in two |
| under `CAPTURE_POLL_QUIET_MS` (2 min) | `CAPTURE_POLL_INTERVAL_MS` | 4 s | it is waiting on a provider |
| under `STUCK_AFTER_MS` (10 min) | `CAPTURE_POLL_SLOW_MS` | 15 s | something is slow, not stuck |
| `STUCK_AFTER_MS` and beyond | `CAPTURE_POLL_STUCK_MS` | 60 s | the row offers Retry at `retry_after`, and the minute tick shows it on time; a capture stuck for hours must not keep an open Home at nine hundred requests an hour, and the × deletes one the server has given up on |
| nothing moving | — | off | — |

It never stops while anything is non-terminal. The same query refetches
whenever the app comes to the foreground (`refetchOnWindowFocus: 'always'`,
so a `staleTime` set elsewhere cannot turn it off): focus is the one moment a
capture a device made while the app was in the background is owed a look.
TanStack pauses the interval while the document is hidden, so the background
costs nothing. The poll is not conditional — `writeJSON` sets no `ETag` or
`Cache-Control`, so every answer is the full page rather than a 304 —
because the gateway and the Lambda run either way and the ladder keeps the
poll to tens of requests a day; a conditional GET is the change to make when
a tenant's bytes matter. Web Push (`push.md`) is how a *closed* app learns
the same thing.

The note screen is the other reader. `useInFlightCaptures` polls
`GET /v1/captures/{id}` for each of the open note's non-terminal captures on
the same ladder, writes each moving stage into the cached note so the
banner's segments move, and reads the note once when one settles, so the
terminal status and the body it changed arrive together; a 404 stops that
capture's poll and rereads the note. The cadence is read from the note's copy
rather than the capture query's own data, because a regeneration sends an
`appended` capture back to `transcribed`, and a capture query left holding
`appended` would otherwise never ask again. Pull-to-refresh on Home refetches
everything at once, each list for its first page only.

## A widget or a hardware button

A web app cannot put a widget on an Android home screen or bind a hardware
button; the manifest shortcuts are the limit. A native wrapper — an Android
TWA carrying a widget, or an iOS Shortcut on the Action button — posts to the
inbox (`docs/design/inbox.md`), which needs nothing more to receive it.

Tests: `machine.test.ts` (the lock's pause, stop and spontaneous stop),
`store.test.ts` (stop-and-send, the record behind a hidden page),
`recorder.test.ts` (the hide's flush and the listener's lifetime),
`buffer.test.ts`, `uploader.test.ts`, `CaptureScreen.test.tsx`,
`FilingBanner.test.tsx`, `FilingRow.test.tsx` (the tiers, the receipts, the
poll's ladder and focus refetch), `ResumePrompt.test.tsx`,
`useResendOnReconnect.test.tsx`, `filing/model.test.ts`,
`filing/FilingItem.test.tsx`, `filing/TargetPrompt.test.tsx`,
`holdGesture.test.ts` (every transition, on a fake clock),
`useHoldToTalk.test.tsx`, `components/RecordButton.test.tsx` (tap, hold,
Space, Enter, Escape, the swallowed click, blur, the first-press prompt),
`RecordingIndicator.test.tsx`, `TabBar.test.tsx` (the slots, the lock's
hand-off to `/capture`, R, the coach); end to end, `frontend/e2e/capture.spec.ts`
(Send while recording, the return and the banner, the four discs on one row
at 320 px, hold to send, drag left to cancel, the hint's 16 px edge, drag up
to lock onto the recording screen and then Send or Cancel there, the
ten-second confirm, the touch slide on a phone, the screen locking),
`layout.spec.ts`, `manifest.spec.ts`, `launch-latency.spec.ts` (the
shortcut's numbers, by hand).

History: `docs/backlog.md` (R5-RC-3/4 for the poll ladder and the focus
refetch, R8 F5–F7 for the hold, R7-7 and F9 for the receipts, D1 for the
lock).
