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
(`components/CheckMark.tsx` `Check` / `CheckMark`, `styles/checklist.css`),
and the typed note (`screens/library/NewNote.tsx`).

## Header

One row. The brand leads: `Wordmark` (`components/Wordmark.tsx`), the name in
the serif at `lg`, semibold, ink — the same lockup the shell's banner wears on
every other screen, left-aligned there too. "Notes · 12" is the row's quiet
right end, sans at `sm`, muted, with the **+** for a typed note after it.
No date: the day is the group label beneath.
The h1 stays first in the DOM and the brand is moved ahead of it by `order`
(`home.css`), so a screen reader hears "Notes, 12" and the a11y sweep still
finds a heading that starts with Notes. The lockup leads with the mark: the
Bindu C, the serif C as an open ring with the bindu at its mouth, drawn in
`Mark` (`components/Wordmark.tsx`) at 1 em in `currentColor`, so the ring's
outer edge sits at the cap height and the mark follows the theme. The same
`Mark` stands at 72 px above the name on the sign-in screen and on About. The
launcher icon is the same ring with the bindu in the accent
(`public/icon.svg`, rendered to the manifest's PNGs by
`frontend/scripts/make-icons.mjs`; `public/favicon.svg` swaps to paper ink
under a dark colour scheme). The design record, with the two directions not
taken, is `docs/design/branding/`.

## New note

A note without a recording. The + at the header's end (`NewNote.tsx`, an
`OverflowMenu` through its own 44 px trigger, named "New note") opens a
two-item menu, **Note** or **Checklist**; the pick is one `POST /v1/notes
{title, kind}` with the placeholder title "New note" or "New checklist"
(`NEW_NOTE_TITLE`) — the server refuses an empty title — and the
`Idempotency-Key` the client puts on every mutating request. On the 201 the
app navigates to the note with `focusTitle` in the route state, and the note
screen focuses the title field with the placeholder selected, once, so the
first keystroke replaces it (`note-screen.md`, "Title"). The + is a disc at
the meta colour like a row's ⋮, not a second record control: the disc on the
tab bar is still how a note usually begins. Offline the + says "New note
needs a connection." under the header rather than queueing: the edit queue
carries edits to notes that exist, and a note with no server id could not be
opened. A refused create says "The note could not be created. Try again."
and keeps the +. A placeholder left exactly as it was made — the title
untyped, no body, no recording — is discarded when its screen is left
(`features/notes/newNote.ts` `isUntouchedPlaceholder`; the note screen
archives and purges it, no confirm, no toast), so Back from an idle + leaves
no "New note" row behind. One left to record into is kept (`note-screen.md`).

## The field

One field, two modes (`LibraryField.tsx`): a search that filters on every
keystroke, and Ask, a question sent on Enter (`ask.md`). The switch is
inside the field's end, a sparkle and the word "Ask"; in Ask mode the same
place is a × named "Back to search". A glyph alone, with "Back to search"
only as a hover title, left Ask unfound and gave a phone no visible way
out. The search's rules are `search.md`.

## Filing, above the list

Between the field and the first day sits the filing section
(`features/capture/FilingRow.tsx`) — this device's upload, the captures still
moving, the ones that need the person and the receipts for the rest — whose
rules are `capture-ux.md`, "Receipts on Home".

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
refetches `['notes']` either way so what is shown is what was stored. A pin
writes the server's answer back into the cache, so an Unpin before the refetch
carries the new `version`. An archived note offers no pin.

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
"Move up" and "Move down" in a pinned row's ⋮ are the path that needs no
gesture.

Reorder is offered only when the whole Pinned group is on screen: under a
tag or kind filter the rows have no grip and no Move items, because a drag
over the visible subset would rank it from zero and interleave the pins the
filter hid (`PinnedGroup`'s `reorderable`). Pin and reorder wait for the
network (`useOnline`): offline, the swipe tray omits Pin — a tray has no
disabled state — and the ⋮ lists Pin, Move up and Move down disabled.

## The row's actions

There is no selection mode: every action is on the row — its ⋮, its swipe
tray — or in the note's own header menu, and a hold on a plain row does
nothing: its release opens the note. With Undo on each row, doing things to
several notes at once is not needed.

Every row has a ⋮ at its right — Pin or Unpin · Colour… · Delete, plus Move up · Move
down on a pinned row, and in the archive Restore · Delete forever. Under a
fine pointer it is revealed on hover and on focus-within; under a finger it
is always there at the meta colour, 44 px. Swipe stays touch-only: with a
fine pointer the tray is not offered, since a mouse drag would fight text
selection. The tray is Pin · Delete in the library and Restore · Delete
forever in the archive, the same words as the menu with Delete at the edge of
both; one row is open at a time app-wide, a tap elsewhere or a scroll closes
it, and a pointer held still for the long-press duration is not a swipe.
Nothing is only a swipe away.

## Colour

A note can wear one of eight pastel tints — rose, peach, sand, lime, mint,
sky, lavender, slate — or none (`NoteColor` in `openapi.yaml`; the stored
name is `color`, data-model.md). Colour… on a row's ⋮ opens a small dialog
(`components/ColorPicker.tsx`, `ColorDialog`) holding the picker: nine
44 px swatches (a dot and a name), "No colour" first, native radios named by the colour, the
current one ringed in ink. A choice applies at once and the status region
says "Colour: Rose"; Done or Escape closes. It is one `PATCH {version,
color}` (`useColorNote`), shown before the server answers and put back if
refused; the requests of one note share a mutation scope and each reads
the version the last one left, so arrowing through the swatches is a run of
saves, not of conflicts. Like Pin it is online only from the row and is
not an edit: `updated_at` stays, so the note keeps its day. The note's own
Details has the same picker and carries a colour through the offline queue
(note-screen.md).

The tint fills the whole card (`.note-row[data-color]`, `home.css`), not a
band at its edge. Both were tried at 360 px in both themes: a pastel band
beside a raised card barely registers in Ink & Paper and vanishes in
Nocturne, while a filled card is found at a glance and still reads as one
list because only the ground changes — title, snippet, time, tags and meta
keep their roles, and a tag chip's hairline steps up to `faint` so it
survives the tint. Each name is a pair of tokens (`--color-tint-<name>`,
`tokens.css`), the same eight hues spread round the OKLCH wheel: a pastel
at L 0.90, C 0.06 in Ink & Paper and a muted mid-dark at L 0.35, C 0.065 in
Nocturne (slate at 60% of that chroma, so it reads as blue-grey beside sky).

A tint at the lightness of the plain card measures 1.00–1.06:1 from it,
and in Nocturne such tiles read as black and cannot be told apart. So the
tints differ from the card by lightness and chroma both, and since a
ground that visible does not keep the theme's `faint` (and in Nocturne
`muted`) at AA, a tinted card and a tinted note screen swap in their own:
`--color-on-tint-muted` and `--color-on-tint-faint` (Ink & Paper: `muted`
itself and `#625b4d`; Nocturne: `#c4ccc8` and `#aab4ae`).
"vs card" is the tint against `raised`; in Ink & Paper it stays under
1.25:1 by design — a light pastel cannot be much darker and stay a pastel —
and the hue carries the difference. Contrast, WCAG 2.2, computed:

| Tint | Ink & Paper | vs card | ink | muted | faint | accent-strong | Nocturne | vs card | ink | muted | faint | destructive |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| rose | `#ffced3` | 1.24 | 12.57 | 5.82 | 4.81 | 4.64 | `#572b31` | 1.40 | 10.66 | 7.14 | 5.49 | 4.90 |
| peach | `#fed4b9` | 1.22 | 12.83 | 5.94 | 4.91 | 4.74 | `#543016` | 1.41 | 10.56 | 7.08 | 5.44 | 4.85 |
| sand | `#edddb1` | 1.20 | 13.03 | 6.04 | 4.99 | 4.82 | `#483909` | 1.45 | 10.26 | 6.87 | 5.28 | 4.71 |
| lime | `#d1e7bd` | 1.17 | 13.28 | 6.15 | 5.08 | 4.91 | `#2f411b` | 1.47 | 10.11 | 6.77 | 5.20 | 4.64 |
| mint | `#b8ebd6` | 1.17 | 13.30 | 6.16 | 5.09 | 4.91 | `#0b4534` | 1.49 | 9.99 | 6.69 | 5.14 | 4.59 |
| sky | `#b3e8fb` | 1.18 | 13.26 | 6.14 | 5.08 | 4.90 | `#004253` | 1.48 | 10.05 | 6.73 | 5.17 | 4.62 |
| lavender | `#e7d5ff` | 1.21 | 12.85 | 5.95 | 4.92 | 4.75 | `#423156` | 1.40 | 10.60 | 7.10 | 5.46 | 4.87 |
| slate | `#d1dff7` | 1.19 | 13.06 | 6.05 | 5.00 | 4.83 | `#2f3b4f` | 1.45 | 10.29 | 6.89 | 5.30 | 4.73 |

muted and faint are the `on-tint` values. The picker does not show the
wash: each swatch is a solid dot of the colour at full strength
(`--color-dot-<name>`, OKLCH L 0.60, C 0.12, the same in both themes and
at least 3.2:1 on every ground of either theme) beside its name, in a grid of 44 px
targets; the checked one is ringed in ink and its name set in bold. The
focus ring is ink, outside the card. `e2e/colors.spec.ts` sets a colour
from the row and from Details, reloads, and runs axe's contrast rule over
every tint and the open picker in both themes.

## Deleting

No typed word anywhere. Delete on Home — the row's ⋮, the swipe tray, the
note header's ⋮ — is the archive (`useArchiveNote`), and it asks first:
`components/DeleteConfirm.tsx`, one `ConfirmDialog` shared by all three
places, titled "Delete “⟨title⟩”?", whose body says where the note goes — "It
is kept in the Archive for 30 days, then gone for good." — with one
destructive Delete button, no field, and focus on Cancel, so Enter and Escape
both back out with nothing gone — and back to the control that asked, the ⋮
included (`OverflowMenu` hands focus to its trigger before a pick runs, so the
dialog has it to restore). On the answer the archive runs and a toast in the
shell (`components/Toast.tsx`, a row above the tab bar like the update prompt,
never over the record button) says "Deleted · kept in Archive for 30 days"
with an Undo button for `TOAST_MS` (6 s) — the clock stops while a pointer
rests on it or focus is in it; the toast stays because it costs nothing and
covers a slip on the second button too. Undo restores, and re-pins what was
pinned (the server's restore comes back unpinned; `useUndoDelete`); the toast
is a polite live region that is always mounted and displayed (the card inside
it comes and goes), and the button is a real one, so a keyboard reaches it.
There is no separate Archive item: the Archive is where deleted notes wait,
thirty days, until the sweep purges them (`retention.md`) or Restore brings
them back. The Archived chip and view keep their names.

Delete forever, in the archive only — row and header — is a plain
`ConfirmDialog` of its own: the sentence names the note and what goes with
it, one destructive button, Cancel with focus. Deleting a recording
(`Recordings.tsx`) is a plain confirm too. `ConfirmDialog` has no typed gate
and no held button: there is no bulk action to gate.

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
not from a list request of its own, so it is only as fresh as that copy. A
checklist deleted, archived or turned into a plain note on another device
stays counted here until this device's copy refreshes: the next search-corpus
fetch (at most every five minutes, and after a recording files) or the next
list page that does not carry it. A checklist made on another device is
missing from the count for the same while. The filter itself asks the server,
so pressing the chip always shows the true list. The Archived count comes
from the archive's own list, asked for once the launch has gone idle rather
than with the first paint.

## Offline

The device's copy of the notes — the IndexedDB cache, `useCachedNotes`, and
the rule for when the screen shows it instead of the server's answer — is
`offline.md`. What is Home's: the cached list is filtered by `tag` and `kind`
by hand, since the cache knows neither, and tiered through the same
`splitPinned` and `groupByDay`; the first page's bodies are fetched in idle
time so a row opened offline is a note and not "Not on this device"; the
caption says "Saved on this device" once it is clear the server will not
answer, or — when the list failed while online for a reason a connection
would not fix (a 5xx, a timeout, a 429, any `ApiError` but a network
failure, a cancel or a 401) — "Chintan's server didn't answer — showing
notes saved on this device" with a Retry (`serverFailed` in
`screens/library/LibraryList.tsx`); Pin and reorder are disabled (above),
and the + says it needs a connection.

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

## Back

Home is the bottom of a short, app-style history stack: the Home tab goes
back to it, You and the archive sit one above it, and Back from Home leaves
the app. The rules are `navigation.md`.

Tests: `features/notes/groups.test.ts`, `screens/NotesScreen.test.tsx` (the
+ included),
`NotesScreen.pins.test.tsx`, `NotesScreen.checklist.test.tsx`,
`screens/library/LibraryList.test.tsx`, `components/NoteRow.test.tsx`,
`NoteRow.checklist.test.tsx`, `SwipeRow.test.tsx`, `components/Toast.test.tsx`,
`components/ConfirmDialog.test.tsx`, `offline/useNotesCache.test.tsx`,
`offline/notesCache.test.ts`, `components/PullToRefresh.test.tsx`; end to
end, `frontend/e2e/pins.spec.ts` (the group, the grip drag, the phone's hold
and tray), `swipe.spec.ts`, `archive.spec.ts` (the per-row Delete behind its
confirm, Undo reached from the keyboard, Delete forever; a held mouse press
on a row opens it), `offline.spec.ts`, `new-note.spec.ts`, `a11y.spec.ts`
(both themes).
