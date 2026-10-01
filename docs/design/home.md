# Home

Home is the notes list: pinned notes first, in the order the person put them,
then every other note under the day it was last touched, newest first, with a
search field and a row of filter chips above. Rows act on themselves — swipe,
the ⋮ menu — so the screen knows nothing of archive or pin. This note is the
frontend; `pins.md` is the backend of pinning. Code:
`frontend/src/screens/NotesScreen.tsx` (the composition) with its parts under
`screens/library/` — `useLibraryParams.ts` (the filters in the URL),
`LibraryField.tsx` (the search/ask field and the chips), `LibraryList.tsx` (the
rows, captions and empty states) — `features/notes/groups.ts`
(`splitPinned`, `groupByDay`), `features/notes/PinnedGroup.tsx`,
`components/NoteRow.tsx`, `components/SwipeRow.tsx` with
`hooks/useSwipeActions.ts`, `components/Toast.tsx`,
`components/ConfirmDialog.tsx` and `components/DeleteConfirm.tsx`, `usePinNote` and `useReorderPins`
(`api/queries/notes.ts`), `offline/useNotesCache.ts`, the sheet (`styles/home.css`:
the rows, chips, field and heading, then the overrides), the drawn checkbox
(`components/CheckMark.tsx` `Check` / `CheckMark`, `styles/checklist.css`).

## Header

One row. The brand leads: `Wordmark` (`components/Wordmark.tsx`), the name in
the serif at `lg`, semibold, ink — the same lockup the shell's banner wears on
every other screen, left-aligned there too. "Notes · 12" is the row's quiet
right end, sans at `sm`, muted. No date: the day is the group label beneath
(owner feedback 2026-09-26 — the date line and `describeToday` went with it).
The h1 stays first in the DOM and the brand is moved ahead of it by `order`
(`home.css`), so a screen reader hears "Notes, 12" and the a11y sweep still
finds a heading that starts with Notes. The lockup leads with the mark: the
Bindu C (R5-BR-L1), the serif C as an open ring with the bindu at its mouth,
drawn in `Mark` (`components/Wordmark.tsx`) at 1 em in `currentColor`, so the
ring's outer edge sits at the cap height and the mark follows the theme. The
same `Mark` stands at 72 px above the name on the sign-in screen and on About.
The launcher icon is the same ring with the bindu in the accent
(`public/icon.svg`, rendered to the manifest's PNGs by `frontend/scripts/make-icons.mjs`;
`public/favicon.svg` swaps to paper ink under a dark colour scheme). The design
record, with the two directions not taken, is `docs/design/branding/`.

## Filing, above the list

Between the field and the first day sits the filing section
(`features/capture/FilingRow.tsx`), whose rules are `capture-ux.md`,
"Receipts on Home". It is a notice, not a note (F9): an h2 "Filing" in the
day groups' label style, then one tray pressed into the page
(`--color-notice`, `--shadow-notice`) holding every row, divided by
hairlines, with a glyph before each row's text and `--space-6` below it, so
it reads apart from the raised note cards under it. On a phone it is kept
short (R7-7a): rows that need the person — "which note?", a failure — stay
open, each with the recording's excerpt as a muted second line; a single
receipt is one line, plus an excerpt line when there is one, "✓ Filed into
“Roof repair” · 2 min ×" or "+ Started “Plumber”" for a note the recording
made; two or more receipts are one summary row, "3 filed into 2 notes ›",
that opens in place beside "Clear all". The × is always drawn, on every
pointer, which supersedes R7-7a's hidden × (F9); the swipe tray is a second
way to put a receipt away. The "which note?" row deliberately has no ×: a
recording that is in no note yet must not be hidden, so answering is the
only way it leaves the screen.

## Order and groups

`GET /v1/notes?state=active` pages arrive in the server's order — the pinned
tier, then `updated_at` descending — and the screen derives it again anyway.
`splitPinned` takes the rows with `pinned: true` and sorts them by `pin_rank`
ascending, `updated_at` descending on a tie (two rows at one rank is a reorder
still in the air, not a state the server stores); `groupByDay` sorts the rest
newest first and labels them Today, Yesterday, the weekday for the rest of the
week, the month, then the year, in local time, since "Today" has to be the
person's. Deriving on the client is what keeps the cached list on the same
terms offline and what lets an optimistic pin move a row before the server
answers. The client's tie-break is `updated_at` where the server's is
`pinned_at`, which is not on the wire; they differ only while a reorder is in
flight.

The row on the wire is `Note {id, title, snippet, updated_at, version,
archived, pinned, pin_rank: int | null, kind, tags, purge_after?}`; Home keys
on `pinned`, `pin_rank`, `updated_at`, `kind`, `tags` and `archived`.

## Pinned

The Pinned group sits above the days under its own heading, and a pin glyph
before the title marks the row. Pin or Unpin is on the row three ways — the
swipe tray, the ⋮ menu and the note's own header menu — and each is one `PATCH
/v1/notes/{id} {version, pinned}` through `usePinNote`, which patches every
cached list at once (the row moves into or out of the group, ranked after the
last pin) and the note detail, puts them back if the server refuses, and
refetches `['notes']` either way so what is shown is what was stored. An
archived note offers no pin.

Reorder is a drag and one request. Where the pointer is fine each row wears a
grip at its left: a mouse drags it, and the arrow keys on a focused grip move
the row one step. On a phone there is no grip — the row is the phone's width —
and a press-and-hold lifts the row instead. Where the pointer is fine the grip
is the only handle, so a finger on a touchscreen laptop drags the grip and a
held row stays a row. The list owns the gesture
(`hooks/useDragReorder.ts`, shared with the Items tab's grip) — one
hold timer, one pointer capture, a non-passive `touchmove` that blocks the
scroll only while a row is lifted — and the dragged row takes the slot whose
midpoint the pointer crosses, so the group re-sorts under the pointer. Lifting
commits the draft as `POST /v1/notes/pins {ids}` through `useReorderPins`,
which ranks the listed rows `index × 1000` in every cached list, rolls back on
refusal and refetches. Escape drops the row where it was, and the click the
browser fires after a drag is swallowed so the note under it does not open.

## The row's actions

There is no selection mode (owner, 2026-09-29: with the row's own actions and
Undo, doing things to several notes at once was not needed, and the
press-and-hold, the checkboxes, the bar and the batch purge went with it).
Every action is on the row — its ⋮, its swipe tray — or in the note's own
header menu, and a hold on a plain row does nothing: its release opens the note.

Every row has a ⋮ at its right — Pin or Unpin · Delete, and in the archive
Restore · Delete forever. Under a fine pointer it is revealed
on hover and on focus-within; under a finger it is always there at the meta
colour, 44 px. Swipe stays touch-only: with a fine pointer the tray is not
offered, since a mouse drag would fight text selection. The tray is Pin ·
Delete in the library and Restore · Delete forever in the archive, the same
words as the menu with Delete at the edge of both; one row is open at a time
app-wide, a tap elsewhere or a scroll closes it, and a pointer held still for
the long-press duration is not a swipe. Nothing is only a swipe away.

## Deleting

No typed word anywhere (owner, 2026-09-26: "when I delete, I have to type
delete. I don't like that UX"). Delete on Home — the row's ⋮, the swipe tray,
the note header's ⋮ — is the archive (`useArchiveNote`), and it asks first
(owner, 2026-09-27: "When deleting, ask are you sure. Don't directly
archive"): `components/DeleteConfirm.tsx`, one `ConfirmDialog` shared by all
three places, titled "Delete “⟨title⟩”?", whose body says where the note goes —
"It is kept in the Archive for 30 days, then gone for good." — with one
destructive Delete button, no field, and focus on
Cancel, so Enter and Escape both back out with nothing gone — and back to the
control that asked, the ⋮ included (`OverflowMenu` hands focus to its trigger
before a pick runs, so the dialog has it to restore). On the answer the archive
runs and a toast in
the shell (`components/Toast.tsx`, a row above the tab bar like the update
prompt, never over the record button) says "Deleted · kept in Archive for 30
days" with an Undo button for six seconds — the clock stops while a pointer
rests on it or focus is in it; the toast stays because it costs nothing and
covers a slip on the second button too. Undo restores, and re-pins what was pinned; the
toast is a polite live region that is always mounted and displayed (the card
inside it comes and goes), and the button is a real one, so a keyboard reaches
it. There is no separate Archive item: the
Archive is where deleted notes wait, thirty days, until the sweep purges them
or Restore brings them back. The Archived chip and view keep their names.

Delete forever, in the archive only — row and header — is a plain
`ConfirmDialog` of its own: the sentence names the note and what goes with it, one
destructive button, Cancel with focus. Undo re-pins a note that was pinned (the
server's restore comes back unpinned; `useUndoDelete`). Deleting a recording
(`Recordings.tsx`) is a plain confirm too. The `requireText` typed gate is gone
from `ConfirmDialog`, and so is the held button (`holdMs`) that gated a bulk
Delete forever: there is no bulk action left to gate.

## The chips

All · Checklists · one per tag · Archived, in the URL as `kind`, `tag` and
`view`, replaced rather than pushed so Back leaves the library instead of
stepping through the filters tried. Checklists and Archived appear only with
something behind them and carry a count; the tag chips are read from the
device's copy of the active notes, not `GET /v1/tags`. A filter combines with
the tier rather than against it: `?tag=` and `?kind=checklist` filter on the
server, so the Pinned group shows the pinned notes that match and the days the
rest. The archive never has a Pinned group, because archiving clears the pin.

The Checklists count is read from the device's copy of the active notes too,
not from a list request of its own (round 7, R7-17c), so it is only as fresh
as that copy. A checklist deleted, archived or turned into a plain note on
another device stays counted here until this device's copy refreshes: the
next search-corpus fetch (at most every five minutes, and after a recording
files) or the next list page that no longer carries it. A checklist made on
another device is missing from the count for the same while. The filter
itself asks the server, so pressing the chip always shows the true list. The
Archived count comes from the archive's own list, asked for once the launch
has gone idle rather than with the first paint.

Reorder is offered only when the whole Pinned group is on screen: under a
tag or kind filter the rows have no grip and no Move items, because a drag
over the visible subset would rank it from zero and interleave the pins the
filter hid (`PinnedGroup`'s `reorderable`; round-4 R4-2, fixed in #99). A
pin writes the server's answer back into the cache, so an Unpin before the
refetch carries the new `version` (R4-3); "Move up" and "Move down" in a
pinned row's ⋮ are the path that needs no gesture (R4-4); and Pin and
reorder wait for the network — offline, the items are disabled (`useOnline`).

## Offline

Every list page and every note read passes through IndexedDB
(`offline/notesCache.ts`) on its way to the screen; a row is stored whole, so
`pinned` and `pin_rank` ride along. `useCachedNotes(state)` reads the device's
copy under a `['notes', 'offline', …]` key with `networkMode: 'always'`:
TanStack pauses a server query offline rather than failing it, so the fallback
cannot live inside `useNotes`, and the key sits under `notes` so a mutation's
invalidation refreshes both. The screen shows the cached list only when the
server has answered nothing at all — an empty answer is authoritative, or a
note archived on another device would come back — applies `tag` and `kind` by
hand, since the cache knows neither, and tiers it through the same
`splitPinned` and `groupByDay`. The first page's bodies are fetched in idle
time so a row opened offline is a note and not "Not on this device", and
"Saved on this device" is said only once it is clear the server will not
answer. When the list failed while the device is online for a reason a
connection would not fix — a 5xx, a timeout, a 429 or another refusal, any
`ApiError` but a network failure, a cancel or a 401 — the caption says so
instead — "Chintan's server didn't answer — showing
notes saved on this device" — with a Retry that refetches, since the list
may be missing what another device added and a connection is not what is
missing (R7-12, `serverFailed` in `screens/library/LibraryList.tsx`; tested
in `LibraryList.test.tsx`).

## The drawn checkbox

`Check` is a real `<input type="checkbox">` stretched invisibly over a 44 px
label, with `CheckMark` — a 22 px rounded square in the icon set's 1.75 px
stroke — as the box a finger sees. The tick is a one-unit path drawn by
sliding its dash on: a transition on the motion tokens, so it plays on a tick
and not on every render, and is one frame under reduced motion. Checked fills
the box with ink and draws the tick in ground; the hidden control's focus ring
lands on the mark. The same label shape — `.checklist__box`, then `CheckMark`
— is the Items tab's rows, the Details switches (Checklist, Word for word;
`features/notes/NoteDrawer.tsx`) and the Cleaned tab's auto-refresh.

Tests: `features/notes/groups.test.ts`, `screens/NotesScreen.test.tsx`,
`NotesScreen.pins.test.tsx`, `NotesScreen.checklist.test.tsx`,
`components/NoteRow.test.tsx`, `NoteRow.checklist.test.tsx`, `SwipeRow.test.tsx`,
`components/Toast.test.tsx`, `components/ConfirmDialog.test.tsx`,
`offline/useNotesCache.test.tsx`,
`offline/notesCache.test.ts`, `features/notes/ChecklistEditor.test.tsx` (the
rows and the tick; the grip's drag writing one body on release, its tap menu
and arrow keys, Escape and a body change dropping a lifted row; the sideways
drag nesting and un-nesting with its preview, the first row refusing, a
wobble opening no menu, →/← on the grip; the Done disclosure remembered per
note; Uncheck all; Delete done with its Undo), `ChecklistNote.test.tsx`
(Items · Recordings; Tidy up list writing the body with Undo, refusing a list
changed meanwhile, landing from another tab; the conversion tidying),
`features/notes/checklist.test.ts`, `components/PullToRefresh.test.tsx`; end
to end, `frontend/e2e/pins.spec.ts` (the group, the grip drag, the phone's
hold and tray), `swipe.spec.ts`, `archive.spec.ts` (the per-row Delete behind
its confirm, Undo reached from the keyboard, Delete forever; a held mouse press
on a row opens it), `offline.spec.ts`,
`checklist.spec.ts` (the Items tab; the grip's mouse drag and arrow keys, the
tap menu, a CDP touch drag on a phone up and down to reorder and sideways to
nest, the Done disclosure across a reload, Delete done undone from the
keyboard, Uncheck all, Tidy up list replacing the rows with Undo),
`a11y.spec.ts` (the Items tab with a grip menu open, both themes).

## Back

Home is the bottom of a short stack, app-style, not a browser's trail (R8,
F2; `app/useTabNavigation.ts`). Before, every tab and "‹ You" pushed, so
You → About → Home → You → About and then Back walked every screen visited.

- Home `/` (any search or Ask mode, but not `?view=archived`) is always the
  app's first history entry. `useBackGuard` seeds it under any cold deep link,
  the archive included.
- You and the archive are the top-level screens and only ever sit at entry 1.
  From Home they push; from entry 1 (the other one, or a note opened from
  Home) they replace it; from deeper they go back to entry 1 and then replace
  it (`usePendingTab`, which waits for the POP because `history.go` returns
  nothing to wait on). The same tab while on it does nothing.
- The Home tab goes back to entry 0, a POP, so the list comes back at its
  place under Home's own key (R7-6a). On Home it clears a filter and scrolls
  to the top, as before.
- A note, About and Usage push. "‹ You" goes Back when You is beneath it,
  and otherwise (a cold `/about`) replaces itself with You.
- Back from Home leaves the app: in the installed PWA Android's Back closes
  it, in a tab it goes wherever the tab was before. There is no "press again
  to exit" (owner decision D6).
- The index is React Router's private `history.state.idx`, read only through
  `historyIndex()`, and only inside `useTabNavigation` and `useBackGuard`: a
  note's "‹ Notes" and the archive chip go through `goBackTo` like "‹ You".
  Tabs stay real links, so a modified click opens a new tab.
- A tab move from deep in the stack is two halves — a Back, then a replace
  once the POP lands — and the second half waits in `sessionStorage`
  (`chintan.nav.pending`), stamped with the entry it was made from and the
  time: a `history.go(-n)` that never moved (the index overstating the real
  stack) leaves a pending that the next reload's initial POP would otherwise
  act on; one taken at its own entry, or older than ten seconds, is dropped.

The Archived chip is the archive's other door and moves the same way: on
from Home it pushes the archive (`goTab`), off at entry 1 it goes back down
to Home, so Back from an archive reached either way is Home. The other chips
stay filters and replace. Deleting a note goes Home the same way (`goHome`),
and deleting one forever from the archive goes to the archive by `goTab`, so
neither stacks a second copy of the screen it lands on. A reload keeps the
tab's history and the router's index, so `useBackGuard` seeds only when the
index is 0.
