# The note screen

A note is one screen: the way back, Find and the note's ⋮ menu across the
top, the title, one line of facts, then a strip of three segments — Text ·
Cleaned · Recordings (N) — and the one panel it selects, with a drawer for
Details or Share at the foot. The strip sticks under the banner while the
panel scrolls, so the recordings of a note with a few thousand words are one
tap — or, on a phone, one swipe — away from any paragraph. This note is the
frontend; what the panels show is described elsewhere (`checklists.md` for
the Items tab, `prompts.md` for the cleaned view, `append-vs-autosave.md`
for how an edit and a voice append share the body, `regenerate.md` for the
⋮'s Regenerate). Code: `frontend/src/features/notes/NoteDetailScreen.tsx`
(the composition; `NoteViews` holds the strip, the swipe and the panel),
`TextPanel.tsx` (the body, its find mirror and Show's flash), `NoteTabs.tsx`
(`useNoteTab`, `NoteTabList`), `NoteActions.tsx` (`NoteMenu`),
`NoteDrawer.tsx`, `components/TagEditor.tsx`, `FindBar.tsx` with `find.ts`,
`CleanedPanel.tsx`, `Recordings.tsx` with `recordings/`, `ChecklistEditor.tsx`,
`hooks/useHorizontalSwipe.ts`, `hooks/usePullToRefresh.ts`,
`hooks/useKeyboardInset.ts` (called once in `components/AppShell.tsx`),
`components/BannerRecord.tsx`, the sheet (`styles/notes.css`; the scroll
region's rules in `styles/shell.css` `.app__main`).

## Composition

Top to bottom, inside the shell's one scroll region (`.app__main`):

- **Header** (`screen__header--detail`): "‹ Notes" (`BackLink`, which goes
  back one history entry when the app has one beneath and otherwise replaces
  itself with the library through `goBackTo`; the stack model is
  `navigation.md`), the Find toggle (`aria-expanded`, `aria-controls` the
  bar; Ctrl/⌘+F opens it too), and the ⋮ menu (`NoteMenu`: Details · Share ·
  Pin/Unpin · Tidy up list on a checklist · Regenerate from recordings… ·
  Delete; on an archived note Details · Share · Restore · Delete forever).
  There is no action bar between the strip and the tab bar: the tab bar's
  disc records into the open note (`capture-ux.md`).
- **Title**: an input whose label is the screen's visually hidden h1, so the
  page has a heading and the field a name from the same element. A note
  arriving from Home's + carries `focusTitle` in its route state; once the
  editor holds the title, the field takes focus with the placeholder
  selected, one time per arrival, so typing replaces it (`home.md`, "New
  note"). A title cleared to nothing is never sent (`autosave.ts`
  `patchFor`): the server refuses a blank title, and the placeholder stands
  on the server until there is a new one. Left untouched — placeholder
  title, empty body, no recording — such a note is discarded as the screen
  unmounts: `useArchiveNote` then `useDeleteNoteForever`, whose own
  `onSuccess` take it out of the lists and the device's copy.
- **Meta line** (`NoteMeta`): "5 Sept · house · 3 rec · 0:17 · Malayalam",
  a real " · " between the facts so a screen reader hears a sentence; the
  language is a button that opens Details with focus on its select, because
  a fact leads to where it is set.
- **Filing banner** (`features/capture/FilingBanner.tsx`): a recording still
  on its way into this note, above the strip and not on the Recordings tab,
  where the row itself wears the same progress. Outside the swipe region.
  Its rules are `capture-ux.md`, "Recording into a note, and coming back".
- **The strip and one panel** (`NoteViews`): the tablist and, under it, the
  Find bar while it is open; then exactly one mounted panel. One at a time,
  because the recordings' `<audio>` and the body's autosize measurement each
  belong to the panel on screen; a hidden panel's player would keep playing
  under the text.
- **The drawer** (`NoteDrawer.tsx`): Details or Share, rendered only while one
  is open. Outside the panels, so it is there on every tab.

`NoteViews` is keyed by the note's id: the strip's memory and the panels'
state belong to one note, and "Open <title>" after a move walks from one note
to another without remounting the screen. The Find state is not keyed — a
query carried into the next note is one the user may want there too.

## The three tabs

Text · Cleaned · Recordings (N); on a checklist note, Items · Recordings (N)
— no Cleaned tab, because Tidy up list in the ⋮ writes the `tasks` view into
the body (`checklists.md` "Tidy up"), and a `?tab=cleaned` there lands on
Items. The count includes an upload still on its way. `useNoteTab` decides
which is open: the URL's `?tab=` first — a deep link is an explicit request;
the capture screen sets none, because `captureReturnPath` brings a recording
back to the note on whichever tab it was left — then the tab this session
last used for the note (`sessionStorage`, `chintan.note-tab.<id>`), then
Text: the note is the document, the other two are a reading of it and its
sources. Choosing a tab writes both the memory and the URL, replacing the
history entry rather than pushing one, so Back leaves the note instead of
stepping back through tabs. Nothing is written on arrival: the back guard
is rewriting history under a deep link at that moment. A checklist's kind
is read from the draft, so flipping the Details switch renames the tabs
before the save lands.

The strip is a WAI-ARIA tablist with automatic activation: Left and Right
select as they move, Home and End jump, and only the selected tab is in the
Tab order, so a keyboard user reaches the panel in one press. Changing tabs
also rewinds Find: another tab is another text, and the count is the new
panel's to report.

A new tab starts at its top. The scroll offset is the page's, so on every
change (swipe, tap or key) a layout effect in `NoteViews` checks whether the
strip is stuck — the region's top above `.app__main`'s — and if so moves the
page so the new panel starts just under it; with the head on screen nothing
moves. No per-tab offset is kept. It runs before the shell's
`useScrollRestore`, a parent, reads the offset for the replaced entry, so
Back then Forward returns to the line under the strip.

## The swipe

On a phone the panels are one swipe apart. `useHorizontalSwipe` listens on
`div.note-views`, which wraps the strip and the panel: pointer events, touch
and pen only — a mouse has the tabs. The region wears `touch-action: pan-y
pinch-zoom`, so the browser keeps the vertical scroll and the pinch (a pan
that starts arrives as `pointercancel`) and never pans sideways; the hook
takes the horizontal drags. What yields to what (the constants are the
hook's and `hooks/gesture.ts`'s):

- the first `SWIPE_EDGE_PX` (24) at either screen edge are the system's back
  gesture;
- a start on a control whose own gesture is horizontal — `.checklist__grip`,
  `[role="slider"]`, `.scrubber__track`, `.overflow-menu`, `select`,
  `input[type=range]` — is never a swipe. The grip's exclusion is the one
  mechanism that keeps a sideways grip drag (the sub-item gesture,
  `checklists.md`) from being a tab switch; the checklist adds no attribute
  of its own;
- a LEFT drag that begins on a closed `.swipe` row (a recording's head) is
  the row's tray, which opens leftwards only, and a RIGHT drag on a closed
  row is the swipe's; an open row (`.swipe[data-open]`) owns both
  directions, because a right drag on it is its own close gesture, and
  taking it would steal the row's pointer capture and leave the tray open;
- the axis is decided at `GESTURE_SLOP_PX` (10) of travel, and |dy| ≥ |dx|
  is a scroll.

Once committed the region takes pointer capture and prevents the default of
every `touchmove`, so a later vertical wander cannot start a pan and
pull-to-refresh stands down (the `defaultPrevented` protocol below). Only the
horizontal travel is followed. Letting go at `SWIPE_COMMIT_FRACTION` (30 %)
of the width, or a `SWIPE_FLICK_PX_PER_MS` (0.4) flick in the same direction
within the last 100 ms, steps; otherwise the panel snaps back.

The motion. The panel follows the finger 1:1 out to the full width and fades
by up to `SWIPE_FADE` (40 %) as it goes; where there is no neighbour it
rubber-bands, `R·(1 − 1/(1 + |dx|/R))` with `R` `SWIPE_RUBBER_FRACTION`
(15 %) of the width, so it never gets past `R`. On a step the old panel
holds where the finger let go until the new tab commits; then, in a layout
effect before that commit paints, the new panel is posed a full width out on
the side the finger pulled away from, under `data-tab-enter` with no
transition. It waits for the commit, not the pointerup, because a tab named
in `?tab=` changes through the router in a transition a frame late, and
posing at pointerup shows the old panel at the enter pose for that frame.
Two frames later the pose is cleared and it settles to rest over
`--motion-duration-base` (220 ms, decelerate), or `--motion-duration-fast`
(140 ms) after a flick. The strip's selected face is one pill
(`.note-tabs__indicator`, placed by `--tab-index` of `--tab-count`, so any
number of tabs, clamped to the track, and remounted rather than slid when
the count changes) that tracks the drag through `--tab-progress` and slides
on to the new tab with the panel; a tap or a key slides the pill too, but
swaps the panel at once. A new finger during the two posed frames lands the
panel at rest. Under reduced motion the follow, fade and rubber band stay —
they move only with the finger — and every settle is instant through the
tokens. Snap back uses decelerate, not the spring.

Every write is `translate` and `opacity` on the panel (`[data-swipe-panel]`)
and the pill (`[data-swipe-indicator]`), found once when the axis is decided
along with the region's width; nothing in the move path reads layout or
goes through React, because the textarea or a forty-row checklist sits under
the region and an inherited property on the region would restyle all of it.
Only `dragging` is state. The click that follows a committed drag is
swallowed once, so lifting over the new panel neither focuses the textarea
nor toggles a recording; Chromium fires no click after a touch that moved,
so the flag can outlive the gesture, and a keyboard activation's click
(`detail` 0) is let through, while a cancelled pointer sets no flag. A step
goes through the same `setTab` as a tap — `?tab=`, the session memory and
Find behave identically — and moves no focus and announces nothing:
`useRouteFocus` keys on the pathname, and the tab buttons remain the
keyboard and screen-reader path. `translate` is applied only while a swipe
or its settle is in progress, and the layer is promoted only then; at rest
the panel has none, so a row's menu is not trapped in a stacking context and
the layout sweep sees no sideways scroller. Known trades: the Find field, a
single-line input inside the region, loses the horizontal drag-scroll of an
overflowing value; and a pen is a finger here, so a sideways S Pen drag
across the body — Android's pen text-selection gesture — steps the tab
rather than selecting. The gesture applies nowhere else: Home's rows own
the horizontal axis (trays, the pinned hold-and-drag) and its chip row
scrolls sideways.

## The drawer

Details — the transcription language, Tags, "Also called", the Word for
word and Checklist switches, and the note's id in monospace with a **Copy
note id** button and the `X-Chintan-Note-Id` hint — or Share. The rules:

- **Share has one primary action.** Where the browser has a share sheet
  (`navigator.share`; `components/ShareButton.tsx` `canShare`) a full-width
  **Share…** leads, handing the title and the text to the system sheet; a
  dismissed sheet (`AbortError`) says nothing, any other refusal copies
  instead and says so. Where there is none, **Copy note** is the primary
  and takes the first line. Then the quieter row — Copy note, Download note
  (Markdown) — a hint saying what the text is, and, when the worker has
  written a cleaned view, a **Cleaned view** group of its own with Copy and
  Download under a caption, so no two controls are both called Copy
  (`NoteDrawer.tsx` `ShareBody`, `shell.css` `.note-share`). The text is the
  title, a blank line, then the body. A checklist copies as `☐` / `☑`
  lines, two spaces per level, with an HTML list written beside them
  (`features/notes/checklistClipboard.ts`; `CopyButton` writes both through
  one `ClipboardItem` where the browser can, the text alone where it
  cannot), never the stored `- [ ]` markup; its download is the Markdown as
  stored. The design record is `specs/2026-10-02/share.md`.

- **A sheet at the foot of the scroll region**, `position: sticky;
  inset-block-end: 0` inside `.app__main` (`.note-screen > .note-drawer`,
  `notes.css`), not fixed to the viewport: the tab bar owns the viewport's
  bottom row, and a sticky element is clipped by the region, so the sheet
  can cover nothing outside the note.
- **At most three fifths of the screen.** `.note-panel` is capped at
  `min(60dvh, 100dvh − --keyboard-inset − the bottom bar − 2 × a touch
  target − the safe insets)`: a content-height Details sheet would hide the
  title, meta and tabs on a phone, and with a keyboard up a sheet taller
  than the room above it is pinned to the screen's top with its foot under
  the keyboard, because a sticky box cannot leave its containing block.
- **Two rows; only the body scrolls.** The sheet is a flex column of the head
  (the heading and Close), which never moves, so Close is always in reach,
  and `.note-panel__body`, the only scroller — so nothing can slide up under
  the head or show in the sheet's top padding. A hairline under the head
  appears once the body has left its top: a scroll-driven animation on the
  body's named timeline (`--note-panel-body`) where `animation-timeline:
  scroll()` is supported, else a `data-scrolled` attribute the body's scroll
  listener sets on the sheet. A body with nothing to scroll (Share) has no
  hairline.
- **Three ways to close, all ending at the ⋮.** The × in the head, Escape,
  and a drag down on the head with a finger or pen (`NoteDrawer.tsx`, the
  head's pointer handlers; a mouse has the ×). The press becomes a drag at
  `GESTURE_SLOP_PX` once it is more down than across; the sheet then follows
  the finger (`translate` on `.note-panel`, `data-dragging` turning its
  transition off) and the head wears `touch-action: none` so nothing scrolls
  under it. Letting go at `SWIPE_COMMIT_FRACTION` of the sheet's height, or
  in a downward flick at `SWIPE_FLICK_PX_PER_MS` still fresh at the lift
  (`SWIPE_FLICK_MAX_AGE_MS`) — the tab swipe's numbers, all in
  `hooks/gesture.ts` — slides it off and closes it when the slide ends
  (`transitionend`, a timer behind it); short of that it settles back. Both
  settles run on the motion tokens, so reduced motion makes them one frame.
  The click a lift fires is swallowed once (`useSwallowNextClick`), so a
  drag that ends over the × is not also a tap on it.
- **A dialog while on screen** (`useModalFocus`, as the Move sheet):
  `role="dialog"`, `aria-modal`, Tab stays inside, Escape closes it, and
  focus returns to the ⋮ that opened it. Opening moves focus in — Details
  to the language select (`noteLanguageFieldId`), Share to its heading
  (`notePanelHeadingId`). While recordings are being selected the drawer is
  hidden, not unmounted, and is not a dialog: `SelectionBar` takes the foot
  of the screen, Escape cancels the selection and R still records. It also
  steps aside while a conflict banner is up.
- **No body lock, no scrim.** `overscroll-behavior: contain` on the sheet's
  body keeps a drag past its end from reaching the page.
- **A drag on the sheet never pulls to refresh.** `usePullToRefresh` arms
  only when `.app__main` is at its top *and* the finger lands outside any
  nested scroller: on `touchstart` it walks from the target up to the
  container and stands down inside an element with computed `overflow-y:
  auto|scroll` and more content than height (`insideNestedScroller`), which
  covers the sheet's body and the transcript scrollers on the Recordings
  tab; a scroller with nothing to scroll is passed over. The sheet
  (`.note-drawer`) is excluded whole, head included and whether it scrolls
  or not, because the head is a fixed row outside the body's scroller and
  the walk alone would let a drag on it arm. Without the rule the hook's
  first prevented `touchmove` cancels the sheet's native scroll and pulls
  the note down under it. A `touchmove` whose default a nearer handler has
  already prevented — a lifted row, a committed swipe — is that handler's,
  and the pull stands down for it.

The numbers are held by `PullToRefresh.test.tsx` ("a scroller of its own
between the finger and the page", "never arms for a drag anywhere on the
note's sheet") and `e2e/note-tabs.spec.ts` ("the Details sheet keeps Close in
reach while its content scrolls", "the Details head stays put while only the
body scrolls", and under "on a phone" the two drag-down cases).

### Tags and aliases

Two `TagEditor`s in Details — "Tags" (`placeholder` "Add a tag") and "Also
called" ("Add another name") — edit `tags` and `aliases` on the note through
`useNoteEditor`, each change a `PATCH /v1/notes/{id}` on commit. The editor
is a list of removable buttons ("Remove ⟨value⟩" as the accessible name)
and one input: Enter or a comma adds, blur adds, Backspace in an empty input
removes the last value, a duplicate is ignored, and the input is disabled at
the cap. The caps are the wire's (`docs/api/openapi.yaml`, `NoteCreate` and
`NoteUpdate`): `maxItems: 32` for both lists, `maxLength` 40 for a tag and
120 for an alias; the handler refuses more with a 400 (`handler/body.go`
`MaxTags`, `MaxTagRunes`, `MaxAliases`, `MaxAliasRunes`; `checkStrings`,
"tags has 33 entries; the limit is 32"), and the aliases are capped because
every alias is rendered into the routing prompt for every future capture
(`routing.md`). Tags are normalised on write (`service/notes.go`
`normalizeTags`: trimmed, lowercased, whitespace collapsed, deduplicated,
cut at 40 runes), so "Roof", "roof " and "roof" are one tag; aliases are
stored as typed. Aliases are the note's other names for routing and search
("add this to my roof note"); tags are what Home's chips filter on.
`GET /v1/tags` (`service/tags.go` `TagsService.List`) answers every tag on
an active note with its count, most used first, derived from the note index
on each call (at most `maxTagScanNotes`, 2000, notes) rather than kept as a
counter that every archive, restore and edit would have to correct; the
app's chips are built from the device's copy of the notes instead
(`home.md`, "The chips"), and nothing in the app calls it.

## The keyboard

Chrome on Android (`interactive-widget=resizes-visual`, the default) and
iOS keep the layout viewport at full height while the keyboard is up; only
the visual viewport shrinks. The shell is `100dvh`, so `.app__main`'s bottom
edge sits under the keyboard, and the browser's own caret reveal — which
scrolls the nearest scroller until the caret rect touches that edge and no
further — leaves the caret line flush against the keyboard's top or under
it. The rules:

- **The scroll container is told where the keyboard is.** `useKeyboardInset`
  writes `--keyboard-inset` on `<html>` as `innerHeight −
  visualViewport.height − visualViewport.offsetTop`, rounded, never
  negative, and 0 while the viewport is zoomed (the same arithmetic would
  read a zoom as a keyboard); it listens to the visual viewport's `resize`
  and `scroll` and the window's `resize`, writes only on change, and removes
  the property on unmount.
- **Two CSS rules, both needed** (`shell.css` `.app__main`):
  `padding-block-end` of the inset — the room to scroll the end of the note
  above the keyboard — and `scroll-padding-block-end` of the inset plus a
  space and the focus ring's clearance, which moves the reveal target above
  it. `scroll-padding` alone has no room to scroll into; `padding` alone
  leaves the target where it was. Together nothing else is needed: no caret
  geometry, no per-editor code.
- **The inset is the window's.** Where the tab bar is rendered `.app__main`
  ends a bar's height above the window's bottom, so the caret rests a bar's
  height above the keyboard rather than flush against it; the bar is not on
  every screen, so it is not subtracted.
- **The drawer needs no inset of its own.** A sticky foot rests on the scroll
  container's content edge, above its padding, so the sheet rises with the
  region; its cap shrinks to what fits above the keyboard (the `min()`
  above).
- **`interactive-widget=resizes-content` is not used.** It is Android-only,
  and shrinking the layout viewport would put the tab bar with its record
  disc on top of the keyboard, a fifth of what is left of the screen.
- **Record while typing.** `useKeyboardInset` also sets `data-keyboard` on
  `<html>` while the inset is over `KEYBOARD_MIN_PX` (80 — an accessory bar
  or a zoom rounding is not a keyboard) and `data-editing` while the title,
  the body or a checklist field has focus (`NOTE_FIELDS`; Find's box and the
  drawer's fields are not the note, read from focus events once rather than
  by a `:has()` chain). With both set on the note screen, `BannerRecord`
  (`components/BannerRecord.tsx`, rendered by `AppShell` at the banner's
  right end) shows: a 2.5 rem disc in a 44 px target, raised and lined, never
  in the accent (`shell.css` `.banner-record`). The banner is its own grid
  row and the target takes its height out of the row's padding, so the mic
  cannot cover text and nothing reflows. Its `pointerdown` is prevented so
  the field keeps focus until the click; the click opens
  `/capture?note=<id>`, the editor's debounced save flushes as the screen
  unmounts (the PATCH goes before the capture's POST), and `/capture`
  returns to the note at its place. Not on an archived note; a desktop
  raises no keyboard, so it never shows there. iOS sometimes pans the layout
  viewport up on focus, which carries the banner, and the mic with it, off
  the top; the mic is then absent.

CDP cannot raise a keyboard (`visualViewport` ignores the emulated metrics),
so the e2e cases set `--keyboard-inset` directly. The numbers are held by
`useKeyboardInset.test.tsx` (the arithmetic, the zoom, unmount, the 80 px
mark, `data-editing` for the note's fields only, and nothing written where
there is no `visualViewport` to read) and `e2e/note-tabs.spec.ts`
"with the keyboard up" (typing at the end of a long note, Enter in the last
item of a long checklist, the Details sheet rising above the keyboard) and
"the banner mic records into the note being typed in". A phone with a real
keyboard — type at the end, Items Enter at the end, Details → add a tag, the
scroll position while panning with the keyboard up — is the manual check.

## Find

A bar under the strip — query, "3 of 12", previous, next, close — that
searches whichever panel is open and owns none of the text: the panel finds
the matches in what it shows, marks them and reports the count through
`FindTarget.onTotal`; the screen's reducer (`find.ts`) moves the active
match. Matching folds case and the generic combining diacritics ("cafe"
finds "Café") but not the Indic vowel signs, which are letters. A `<mark>`
cannot be drawn inside a textarea, so while the bar has a query the Text
panel shows a read-only **mirror** of the body in the same box
(`FindMirror`, `TextPanel.tsx`); closing the bar, or tapping the mirror,
brings the textarea back with the caret on the match that was current. On a
checklist the mirror is the rows (`ChecklistFindMirror`): the box as a
glyph, the words marked, a sub-item set in by its level, a done row struck
through, and none of the task-list syntax. The Recordings tab is not
searchable; the bar stays, greyed, and says so. Enter is next, Shift+Enter
previous, Escape closes.

## The Recordings tab

The sources a note was written from, as dated rows (`features/notes/
Recordings.tsx`, one row `recordings/RecordingRow.tsx`): newest first, the
newest *finished* recording open on arrival, one row open at a time — a
closed row has no `<audio>`, which is what makes "one plays at a time" hold
without a registry. A recording still uploading or filing is the first row
and wears the library's upload bar or stage strip (`recordings/labels.ts`
names the stages and the sources: "From Watch", `heardAs`). A row that
arrived as text has no player. An open row holds the player (`usePlayer.ts`)
with the waveform drawn from `peaks.json` as a real slider
(`WaveformScrubber.tsx`, `role="slider"`, arrow keys seek) and the transcript
(`TranscriptPanel.tsx`): Raw is timestamped and seeks on tap, Cleaned is the
text that became the note and never seeks, because cleanup reorders clauses
and a proportional alignment would land on a plausible wrong place.

The row's More menu: Move to…, Delete recording, Download audio (not for a
text row), Copy this transcript and Copy this cleaned text while the row is
open and has them, Transcribe again (below) on a settled row with audio, and
Select. A long press (`useLongPress`) or Select enters a selection mode: the
drawer hides (`NoteDetailScreen`, `selectingRecordings`) and a `SelectionBar`
(`components/SelectionBar.tsx`, "Recording actions", Escape cancels) offers
move, delete and download for several rows. On a phone the row swipes aside
for Move and Delete, the same dialog and sheet. Move opens `MoveSheet.tsx`:
the active notes most recently touched first, a filter, "New note…" with a
title (`new_note_title`), never the archive or the note itself; the dictated
paragraph goes with the recording, as a delete takes it. Several downloads
are one zip built on the device (`zipRecordings.ts`: the manifest from `GET
/v1/notes/{id}/recordings/urls`, fetched one at a time with progress, stored
not deflated via `fflate`). What an action did is said on the notice line
under the rows.

### Transcribe again

"Transcribe again in ⟨language⟩" (`retranscribeLabel`; "Transcribe again
(auto-detect)" under Auto) is for a recording that came back in the wrong
script or with sentences missing. `useRetranscribeCapture` sends `POST
/v1/captures/{id}/retranscribe {language?}`; the 202 carries the capture
back at `transcribing`, which is written onto the cached note so the row
wears the stage strip at once, and the screen's capture poll
(`useInFlightCaptures`, `capture-ux.md`) follows the run and reads the note
when it lands. The note is not refetched: the body has not changed yet.

On the server (`service.RetranscribeCapture`, `service/capture.go`), the
language asked for is stored as sent on `RequestedLanguage` — `""` when it
was omitted — and the row's `Language` and `LanguageDetected` are cleared. A
non-empty `RequestedLanguage` outranks the note's language and the tenant
default (`pipeline.transcriptionLanguage`) and stays on the row, so a later
change of the note's language never undoes a person's choice
(`wantsNoteLanguage` is false for it); an omitted one leaves the recording
on the note's effective language, and `wantsNoteLanguage` transcribes it
once more if the note's language changes afterwards. The transcript keys (`RawKey`, `SegmentsKey`,
`RoutedKey`, `CleanKey`), the detected language, the excerpt and the append
claim are cleared and the capture goes back to `transcribing`; the objects
themselves are keyed by the capture id, so the worker writes over them and
the previous transcript downloads until it does. Refused with a 409 (`detail`
says which): a capture still in flight and not yet stuck
(`ErrCaptureInFlight`, the `/retry` rule), a recording whose audio the
retention rule has deleted or that arrived as text (`ErrCaptureAudioExpired`),
or a note that is archived (`ErrNoteArchived`). The worker transcribes,
strips, cleans and then **replaces** what the recording put in the note
(`pipeline/append.go`). A plain note's paragraph is cut along the boundary
delete and move use and inserted back by its marker in id order
(`replaceCaptureParagraph`). A checklist's items are found by their words
and swapped where they stand (`replaceChecklistItems`, `checklists.md`
"Merging"); with no previous items to go by they are merged into the list,
a tick carried to the line with the same words (`keepTick`); the paragraph
cut is a checklist's path only when the marker or the previous items are
missing. Because the earlier paragraph keeps the marker until then, a
retry of the append inside its claim lease asks whether *this attempt's
text* is under the marker (`paragraphInNote`), not whether the marker is
there. `AppendReplacedParagraph` counts the in-place replace
(`docs/ops/metrics.md`). The same replace serves the pipeline's own second
transcription: a recording routed to a note whose `language` differs from
the one it was transcribed in is transcribed again in the note's language
before cleanup (`CaptureRetranscribedForNote`), and `regenerate.md`'s
by-words replacement. Each transcription runs under the transcribe stage's
own deadline (`pipeline-deadlines.md`).

## Tests

`NoteDetailScreen.test.tsx` (the composition and the tab precedence),
`NoteTabs.test.tsx`, `FindBar.test.tsx` and `find.test.ts`,
`useHorizontalSwipe.test.tsx` (commit at 30 %, the flick, the damped follow
with no neighbour, mouse, edge, the `.swipe` row closed and open, the enter
pose and the two frames, the swallowed tap and the keyboard click let
through), `PullToRefresh.test.tsx`, `useKeyboardInset.test.tsx`,
`Recordings.test.tsx`, `recordings/RecordingRow.test.tsx`,
`recordings/labels.test.ts`, `components/ShareButton.test.tsx`; end to
end, `e2e/drawer.spec.ts` (the drag
to close, with a real touch), `e2e/note-tabs.spec.ts` (the
strip's count, `?tab=`, the session memory and Back; the arrow keys; the
strip sticking under the banner; the Details sheet; the swipe cases; the
keyboard cases; the banner mic), `recordings.spec.ts`, `playback.spec.ts`,
`checklist.spec.ts`, `cleaned.spec.ts` and `swipe.spec.ts` for the panels'
own gestures, which the swipe must yield to; `a11y.spec.ts` and
`layout.spec.ts` for no transform at rest and no sideways scroller. Backend:
`service/capture_test.go` and `pipeline/retranscribe_test.go` for Transcribe again
and the in-place replace.

History: `docs/backlog.md`.
