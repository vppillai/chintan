# The note screen

A note is one screen: the way back, Find and the note's ⋮ menu across the
top, the title, one line of facts, then a strip of three segments — Text ·
Cleaned · Recordings (N) — and the one panel it selects, with a drawer for
Details or Share at the foot. The strip sticks under the banner while the
panel scrolls, so the recordings of a note with a few thousand words are one
tap — or, on a phone, one swipe — away from any paragraph. This note is the
frontend; what the panels show is described elsewhere (`checklists.md` for
the Items tab, `prompts.md` for the cleaned view, `append-vs-autosave.md`
for how an edit and a voice append share the body). Code:
`frontend/src/features/notes/NoteDetailScreen.tsx` (the composition;
`NoteViews` holds the strip, the swipe and the panel; `TextPanel` the body
and its mirror), `NoteTabs.tsx` (`useNoteTab`, `NoteTabList`),
`NoteActions.tsx` (`NoteMenu`), `NoteDrawer.tsx` (`NoteDrawer`), `FindBar.tsx`
with `find.ts`, `CleanedPanel.tsx`, `Recordings.tsx`, `ChecklistEditor.tsx`,
`hooks/useHorizontalSwipe.ts`, `hooks/usePullToRefresh.ts`,
`hooks/useKeyboardInset.ts` (called once in `components/AppShell.tsx`), the
sheet (`styles/notes.css`; the scroll region's rules in `styles/shell.css`
`.app__main`).

## Composition

Top to bottom, inside the shell's one scroll region (`.app__main`):

- **Header** (`screen__header--detail`): Back (`BackLink`, which goes back
  one entry if the app has one and to the library otherwise), the Find toggle
  (`aria-expanded`, `aria-controls` the bar; Ctrl/⌘+F opens it too), and the
  ⋮ menu (`NoteMenu`: Details · Share · Pin/Unpin · Regenerate from
  recordings… · Delete, or Restore · Delete forever on an archived note). The
  action bar that once stood between the strip and the tab bar is gone; the
  tab bar's mic records into this note while it is open (review 2026-09-21,
  T6).
- **Title**: an input whose label is the screen's hidden h1, so the page has
  a heading and the field a name from the same element (QA D14).
- **Meta line** (`NoteMeta`): "5 Sept · house · 3 rec · 0:17 · Malayalam",
  a real " · " between the facts so a screen reader hears a sentence; the
  language is a button that opens Details on its select, because a fact
  should lead to where it is set.
- **Filing banner** (`features/capture/FilingBanner.tsx`): a recording still
  on its way into this note, above the strip and not on the Recordings tab,
  where the row itself wears the same progress. Outside the swipe region.
- **The strip and one panel** (`NoteViews`): the tablist and, under it, the
  Find bar while it is open; then exactly one mounted panel. One at a time,
  because the recordings' `<audio>` and the body's autosize measurement each
  belong to the panel on screen; a hidden panel's player would keep playing
  under the text.
- **The drawer** (`NoteDrawer.tsx`): Details or Share, rendered only while one
  is open. Outside the panels, so it is there on every tab; hidden (not
  unmounted) while recordings are being selected, whose own bar takes the
  foot of the screen.

`NoteViews` is keyed by the note's id: the strip's memory and the panels'
state belong to one note, and "Open <title>" after a move walks from one note
to another without remounting the screen. The Find state is not keyed — a
query carried into the next note is one the user may want there too.

## The three tabs

Text · Cleaned · Recordings (N); on a checklist note, Items · Recordings (N)
— no Cleaned tab, since Tidy up list in the ⋮ writes the `tasks` view into
the body (R8-F8, `checklists.md` "Tidy up"), and a `?tab=cleaned` there
lands on Items. The count includes an upload still on its way. `useNoteTab`
decides which is open: the URL's `?tab=` first — a deep link is an explicit
request, and the capture screen's "Record into this" lands on
`?tab=recordings` — then the tab this session last used for the note
(`sessionStorage`, `chintan.note-tab.<id>`), then Text: the note is the
document, the other two are a reading of it and its sources. Choosing a tab
writes both the memory and the URL, replacing the history entry rather than
pushing one, so Back leaves the note instead of stepping back through tabs.
Nothing is written on arrival: the back guard is rewriting history under a
deep link at that moment. A checklist's kind is read from the draft, so
flipping the Details switch renames the tabs before the save lands.

The strip is a WAI-ARIA tablist with automatic activation: Left and Right
select as they move, Home and End jump, and only the selected tab is in the
Tab order, so a keyboard user reaches the panel in one press. Changing tabs
also rewinds Find: another tab is another text, and the count is the new
panel's to report.

A new tab starts at its top. The scroll offset is the page's, so from deep
in Text a change used to land mid-Cleaned. On every change (swipe, tap or
key) a layout effect in `NoteViews` checks whether the strip is stuck — the
region's top above `.app__main`'s — and if so moves the page so the new
panel starts just under it; with the head on screen nothing moves (owner
default; a per-tab memory is not kept). It runs before the shell's
`useScrollRestore`, a parent, reads the offset for the replaced entry, so
Back then Forward returns to the line under the strip.

## The swipe

On a phone the panels are one swipe apart (R6-NAV-1, owner feedback
2026-09-29). `useHorizontalSwipe` listens on `div.note-views`, which wraps the
strip and the panel: pointer events, touch and pen only — a mouse has the
tabs. The region wears `touch-action: pan-y pinch-zoom`, so the browser keeps
the vertical scroll and the pinch (a pan that starts arrives as
`pointercancel`) and never pans sideways; the hook takes the horizontal
drags. What yields to what:

- the first 24 px at either screen edge are the system's back gesture;
- a start on a control whose own gesture is horizontal — `.checklist__grip`,
  `[role="slider"]`, `.scrubber__track`, `.overflow-menu`, `select`,
  `input[type=range]` — is never a swipe. The grip's exclusion is the one
  mechanism that keeps a sideways grip drag (the sub-item gesture,
  `checklists.md`) from being a tab switch; the checklist adds no attribute
  of its own;
- a LEFT drag that begins on a closed `.swipe` row (a recording's head) is
  the row's tray, which opens leftwards only, and a RIGHT drag on a closed
  row is the swipe's; an open row (`.swipe[data-open]`) owns both
  directions, because a right drag on it is its own close gesture — taking
  it stole the row's pointer capture, left the tray open and flipped the tab
  (review 2026-09-29);
- the axis is decided at 12 px of travel, and |dy| ≥ |dx| is a scroll.

Once committed the region takes pointer capture and prevents the default of
every `touchmove`, so a later vertical wander cannot start a pan and
pull-to-refresh stands down (the `defaultPrevented` protocol below). Only the
horizontal travel is followed. Letting go at 30 % of the width, or a
0.4 px/ms flick in the same direction, steps; otherwise the panel snaps back.

The motion (R8 F4). The panel follows the finger 1:1 out to the full width
and fades by up to 40 % as it goes; where there is no neighbour it
rubber-bands, `R·(1 − 1/(1 + |dx|/R))` with `R` 15 % of the width, so it
never gets past `R`. On a step the old panel holds where the finger let go
until the new tab commits; then, in a layout effect before that commit
paints, the new panel is posed where a neighbour would have been, on the
side the finger pulled away from (a left drag of −156 px at 390 starts it
at +234), under `data-tab-enter` with no transition. It waits for the
commit, not the pointerup, because a tab named in `?tab=` changes through
the router in a transition a frame late, and posing at pointerup showed the
old panel at the enter pose for that frame (review of #196). Two frames later the pose is cleared and it settles to rest over
`--motion-duration-base` (220 ms, decelerate), or `--motion-duration-fast`
(140 ms) after a flick. Before R8 the new panel was drawn at the old offset
and slid back against the finger. The strip's selected face is one pill
(`.note-tabs__indicator`, placed by `--tab-index` of `--tab-count`, so any
number of tabs, clamped to the track, and remounted rather than slid when
the count changes) that tracks the drag through `--tab-progress` and slides
on to the new tab with the panel; a tap or a key slides the pill too, but swaps
the panel at once. A new finger during the two posed frames lands the panel
at rest. Under reduced motion the follow, fade and rubber band stay — they
move only with the finger — and every settle is instant through the tokens.
Snap back uses decelerate, not the spring (owner default).

Every write is `translate` and `opacity` on the panel (`[data-swipe-panel]`)
and the pill (`[data-swipe-indicator]`), found once when the axis is decided
along with the region's width; nothing in the move path reads layout or
goes through React, because the textarea or a forty-row checklist sits under
the region and an inherited property on the region restyled all of it. Only
`dragging` is state. The click
that follows a committed drag is swallowed once, so lifting over the new
panel neither focuses the textarea nor toggles a recording; Chromium fires no
click after a touch that moved, so the flag can outlive the gesture, and a
keyboard activation's click (`detail` 0) is let through — Enter on a tab
after a swipe did nothing (review 2026-09-29) — while a cancelled pointer
sets no flag. A step goes through the
same `setTab` as a tap — `?tab=`, the session memory and Find behave
identically — and moves no focus and announces nothing: `useRouteFocus` keys
on the pathname, and the tab buttons remain the keyboard and screen-reader
path. `translate` is applied only while a swipe or its settle is in
progress, and the layer is promoted only then; at rest the panel has none, so a row's menu is not trapped in a stacking context and the
layout sweep sees no sideways scroller. Known trades: the Find field, a
single-line input inside the region, loses the horizontal drag-scroll of an
overflowing value; and a pen is a finger here, so a sideways S Pen drag
across the body — Android's pen text-selection gesture — steps the tab
rather than selecting (the owner's pen-device check decides whether that
holds). Where else the gesture applies is R6-NAV-D1 — nowhere
yet: Home's rows own the horizontal axis (trays, the pinned hold-and-drag)
and its chip row scrolls sideways.

## The drawer

Details (language, tags, "also called", the verbatim and checklist switches,
and the note's id in monospace with a **Copy note id** button and the
`X-Chintan-Note-Id` hint — R6-ID-1, #150, in `NoteDrawer.tsx` since #158) or
Share (copy, download). A sheet at the foot of the scroll region: `position:
sticky; inset-block-end: 0` inside `.app__main`, not fixed to the viewport —
the tab bar owns the viewport's bottom row, and a sticky element is clipped by
the region, so the sheet can cover nothing outside the note. Capped at
`60dvh` and scrolling inside itself, with its head (the eyebrow and Close)
sticky at its top; content-height, Details took 715 of a phone's 915 px (QA
2026-09-21, finding 7). Opening it moves focus in: Details to the language
select, Share to its heading; Close hands focus back to the ⋮. Hidden while
recordings are selected, and stepped aside while a conflict banner is up.

`overscroll-behavior: contain` on the sheet holds in Chromium — measured at
412×915 and 412×700 (T2–T4): a drag past the sheet's end, a drag on the short
Share sheet, and a drag on a Details that fits all left `.app__main` at its
scroll position — so there is no body lock and no scrim. What did break was
the pull: `usePullToRefresh` armed whenever `.app__main` was at its top,
wherever the finger landed, and its first prevented `touchmove` cancelled the
sheet's own scroll and pulled the note down under it — "sometimes the note
scrolls, sometimes the details" (T1: `data-phase="armed"`, `--pull-offset:
90px`, the sheet unmoved, the note refetched). **The arming rule** (R6-NAV-3):
on `touchstart` the hook walks from the target up to the container and never
arms inside an element with computed `overflow-y: auto|scroll` and more
content than height. That covers the sheet and the transcript scrollers on
the Recordings tab at once; a scroller with nothing to scroll is passed over,
so the short Share sheet still lets the note be pulled. If an iPhone is ever
seen to chain from that short sheet, the one-rule answer is
`.app__main:has(.note-drawer:not([hidden])) { overflow: hidden }`.

## The keyboard

Chrome on Android (since 108, `interactive-widget=resizes-visual` is the
default) and iOS keep the layout viewport at full height while the keyboard
is up; only the visual viewport shrinks. The shell is `100dvh`, so
`.app__main`'s bottom edge (y≈822 of 915 on a Pixel 7) sat under a ~400 px
keyboard, and the browser's own caret reveal — which scrolls the nearest
scroller until the caret rect touches that edge and no further — left the
textarea's bottom at 835 after typing at the end of a note (K3): the caret
line flush against the keyboard's top, the next line under it, and a nested
scroller on WebKit not reliably following typing at all.

The model (R6-NAV-2): tell the scroll container where the keyboard is.
`useKeyboardInset` writes `--keyboard-inset` on `<html>` as `innerHeight −
visualViewport.height − visualViewport.offsetTop`, rounded, never negative,
and 0 while the viewport is zoomed (the same arithmetic would read a zoom as
a keyboard); it listens to the visual viewport's `resize` and `scroll` and
the window's `resize`, writes only on change, and removes the property on
unmount. `.app__main` then gets that much `padding-block-end` — the room to
scroll the end of the note above the keyboard — and `scroll-padding-block-end`
of the inset plus a space and the focus ring's clearance, which moves the
reveal target above it. Measured with an inset of 400 (E1): both rules land
the caret line at 822 − 400 + 2; `scroll-padding` alone is stuck at 798 (no
room); `padding` alone stays at 835 (target unchanged). Both are needed;
together nothing else is — no caret geometry, no per-editor code. Enter in
the last of forty checklist items focuses the new field well above (E2).

The drawer needs no inset of its own: a sticky foot rests on the scroll
container's content edge, above the padding, so the sheet rises with the
region (measured: its bottom at 422 — the region's edge, a tab bar's height
above the keyboard's top at 515, because the inset is the window's and the
bar is not on every screen, so it is not subtracted; the caret likewise
rests a bar's height above the keyboard). Its cap becomes
what fits above the keyboard — `min(60dvh, 100dvh − inset − the tab bar − 2 ×
a touch target − the safe insets)`, 335 of the 359 px available at 412×915;
a taller sheet cannot leave its containing block and was pinned to the
screen's top with 60 px under the keyboard. `interactive-widget=
resizes-content` was not taken: it is Android-only, and shrinking the layout
viewport would put the tab bar with its 92 px record disc on top of the
keyboard, a fifth of what is left. CDP cannot raise a keyboard
(`visualViewport` ignores the emulated metrics), so the e2e cases set the
property directly; a Pixel 7 Chrome and an iPhone PWA check is the manual
step — type at the end, Items Enter at the end, Details → add a tag, and the
scroll position stable while panning with the keyboard up, since the
property is rewritten on every visual-viewport scroll — and if an iPhone
still hides the caret the next step is a small `useCaretInView` on input.

## Find

A bar under the strip — query, "3 of 12", previous, next, close — that
searches whichever panel is open and owns none of the text: the panel finds
the matches in what it shows, marks them and reports the count through
`FindTarget.onTotal`; the screen's reducer (`find.ts`) moves the active
match. Matching folds case and the generic combining diacritics ("cafe"
finds "Café") but not the Indic vowel signs, which are letters. A `<mark>`
cannot be drawn inside a textarea, so while the bar has a query the Text
panel shows a read-only **mirror** of the body in the same box; closing the
bar, or tapping the mirror, brings the textarea back with the caret on the
match that was current. On a checklist the mirror shows the raw lines,
marked. The Recordings tab is not searchable; the bar stays, greyed, and says
so. Enter is next, Shift+Enter previous, Escape closes.

## What holds it

- `note-tabs.spec.ts`: the strip's count, `?tab=`, the session memory and
  Back; the arrow keys; the strip sticking under the banner; Close in reach
  while Details scrolls; and the Pixel 7 describe — the Details-sheet drag
  (R6-NAV-3), the five swipe cases (R6-NAV-1: left → Cleaned with the URL
  and no focus move, right → Text, right on Text and a short drag stay, a
  vertical drag scrolls, a left drag on a recording head opens its tray, a
  right one on the open head closes it and one on the closed head goes to
  Cleaned, a drag from the edge does nothing, a left
  drag on a grip on Items switches nothing), and the keyboard cases
  (R6-NAV-2: typing at the end of a long note, Enter in the last checklist
  item, the Details sheet's bottom at the keyboard's top).
- `PullToRefresh.test.tsx`: the arming rule beside the hook's other cases;
  `useHorizontalSwipe.test.tsx`: commit at 30 %, a flick under it, the
  damped follow with no neighbour, mouse, edge, the `.swipe` row closed and
  open, cancel with no click swallowed, the swallowed tap and the keyboard
  click let through; `useKeyboardInset.test.tsx`: the arithmetic, the
  zoom, unmount, no `visualViewport`.
- `NoteDetailScreen.test.tsx` (the composition and the tab precedence),
  `FindBar.test.tsx` and `find.test.ts` (the bar and the matching);
  `checklist.spec.ts`, `recordings.spec.ts`, `cleaned.spec.ts` and
  `swipe.spec.ts` for the panels' own gestures, which the swipe must yield
  to; `a11y.spec.ts` and `layout.spec.ts` for no transform at rest and no
  sideways scroller.
