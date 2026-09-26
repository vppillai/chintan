# Home

Home is the notes list: pinned notes first, in the order the person put them,
then every other note under the day it was last touched, newest first, with a
search field and a row of filter chips above. Rows act on themselves — swipe,
the ⋮ menu, press-and-hold to select — so the screen knows nothing of archive
or pin. This note is the frontend; `pins.md` is the backend of pinning. Code:
`frontend/src/screens/NotesScreen.tsx`, `features/notes/groups.ts`
(`splitPinned`, `groupByDay`), `features/notes/PinnedGroup.tsx`,
`components/NoteRow.tsx`, `components/SwipeRow.tsx` with
`hooks/useSwipeActions.ts`, `hooks/useLongPress.ts`,
`components/SelectionBar.tsx`, `components/Toast.tsx`,
`components/ConfirmDialog.tsx`, `usePinNote` and `useReorderPins`
(`api/queries.ts`), `offline/useNotesCache.ts`, the drawn checkbox
(`features/notes/ChecklistEditor.tsx` `Check` / `CheckMark`,
`styles/checklist.css`).

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

Reorder is a drag and one request. Where the pointer is fine and nothing is
being selected, each row wears a grip at its left: a mouse drags it, and the
arrow keys on a focused grip move the row one step. On a phone there is no
grip — the row is the phone's width — and the press-and-hold that selects a
row everywhere else lifts a pinned row instead; Select is still in its ⋮. The
two holds never arm together: the row's selects exactly where the pointer is
fine, the list's lifts exactly where it is coarse, so a finger on a
touchscreen laptop selects and the grip drags. The list owns the gesture — one
hold timer, one pointer capture, a non-passive `touchmove` that blocks the
scroll only while a row is lifted — and the dragged row takes the slot whose
midpoint the pointer crosses, so the group re-sorts under the pointer. Lifting
commits the draft as `POST /v1/notes/pins {ids}` through `useReorderPins`,
which ranks the listed rows `index × 1000` in every cached list, rolls back on
refusal and refetches. Escape drops the row where it was, and the click the
browser fires after a drag is swallowed so the note under it does not open.

## Selection and the row's actions

The checkbox that slid in at the row's left edge on hover is gone. Selection
starts from a row: press and hold it — `useLongPress` is pointer events,
primary button only, so a mouse works exactly as a finger; travel past 10 px
cancels it, which is also how a scroll or a swipe that begins on a row never
selects it — or pick Select from the row's ⋮. Once selecting, every row is a
`<label>` round a real checkbox: click toggles, Shift-click selects the range
from the last toggled row in on-screen order, Escape or Cancel leaves, and the
sticky bar above the tab bar carries Delete on Home, and Restore and Delete
forever in the archive. The click that follows the long press is consumed, so
the row it just selected is neither deselected nor opened.

Every row has a ⋮ at its right — Pin or Unpin · Delete · Select, and in the
archive Restore · Delete forever · Select. Under a fine pointer it is revealed
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
the note header's ⋮ and the selection bar — is the archive (`useArchiveNote`,
`useBulkArchiveNotes`): it happens on the tap with no dialog, and a toast in
the shell (`components/Toast.tsx`, a row above the tab bar like the update
prompt, never over the record button) says "Deleted · kept in Archive for 30
days" with an Undo button for six seconds — the clock stops while a pointer
rests on it or focus is in it. Undo restores, and re-pins what was pinned; the
toast is a polite live region that is always mounted and displayed (the card
inside it comes and goes), and the button is a real one, so a keyboard reaches
it. There is no separate Archive item: the
Archive is where deleted notes wait, thirty days, until the sweep purges them
or Restore brings them back. The Archived chip and view keep their names.

Delete forever, in the archive only — row, header and bar — is a plain
`ConfirmDialog`: the sentence names the note and what goes with it, one
destructive button, Cancel with focus. For a bulk Delete forever of more than
ten notes (`HOLD_TO_DELETE_ABOVE`) the button reads "Hold to delete N notes"
and has to be held for a second (`holdMs`) under a finger or a mouse button —
the pointer is where the slip is; the fill across it shows the second passing
and appears whole under reduced motion. Enter or Space, and whatever a screen
reader or a switch activates a button with, confirm at once: a held keystroke
is a timing WCAG 2.1.1 forbids, and focus lands on Cancel, so reaching the
control took a deliberate Tab. Undo re-pins a note that was pinned (the
server's restore comes back unpinned; `useUndoDelete`). Deleting a recording (`Recordings.tsx`) is
a plain confirm too. The `requireText` typed gate is gone from `ConfirmDialog`.

## The chips

All · Checklists · one per tag · Archived, in the URL as `kind`, `tag` and
`view`, replaced rather than pushed so Back leaves the library instead of
stepping through the filters tried. Checklists and Archived appear only with
something behind them and carry a count; the tag chips are read from the
device's copy of the active notes, not `GET /v1/tags`. A filter combines with
the tier rather than against it: `?tag=` and `?kind=checklist` filter on the
server, so the Pinned group shows the pinned notes that match and the days the
rest. The archive never has a Pinned group, because archiving clears the pin.

That is also the known gap. A drag inside a filtered Pinned group sends only
the visible ids; the server ranks that subset from zero and leaves the others
where they were, so the whole group can interleave (round-4 R4-2). With it: a
pin stamps `updated_at`, so an unpinned note re-files under Today (R4-1,
backend); an unpin inside the refetch window carries a stale `version` and is
refused (R4-3); and on the phone nothing says a pinned row can be moved
(R4-4). Neither mutation checks the connection. All four are in
`docs/reviews/2026-09-24/round-4.md`, with fixes in flight.

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
answer.

## The drawn checkbox

`Check` is a real `<input type="checkbox">` stretched invisibly over a 44 px
label, with `CheckMark` — a 22 px rounded square in the icon set's 1.75 px
stroke — as the box a finger sees. The tick is a one-unit path drawn by
sliding its dash on: a transition on the motion tokens, so it plays on a tick
and not on every render, and is one frame under reduced motion. Checked fills
the box with ink and draws the tick in ground; the hidden control's focus ring
lands on the mark. The same label shape — `.checklist__box`, then `CheckMark`
— is the Items tab's rows, the Split up preview, the Details switches
(Checklist, Word for word) and the Cleaned tab's auto-refresh. Not, on main,
the bulk-select box on a note row, which is still the native control at 24 px
with `accent-color` ink; the contract asked for the drawn box there too, and
that is in flight with the round-4 fixes.

Tests: `features/notes/groups.test.ts`, `screens/NotesScreen.test.tsx`,
`NotesScreen.pins.test.tsx`, `NotesScreen.checklist.test.tsx`,
`components/NoteRow.test.tsx`, `NoteRow.checklist.test.tsx`, `SwipeRow.test.tsx`,
`components/Toast.test.tsx`, `components/ConfirmDialog.test.tsx`,
`hooks/useLongPress.test.tsx`, `offline/useNotesCache.test.tsx`,
`offline/notesCache.test.ts`, `features/notes/ChecklistEditor.test.tsx`; end to
end, `frontend/e2e/pins.spec.ts` (the group, the grip drag, the phone's hold
and tray), `swipe.spec.ts`, `archive.spec.ts`, `offline.spec.ts`,
`checklist.spec.ts`.
