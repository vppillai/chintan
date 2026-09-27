# Checklist notes

A note can be a checklist: recordings filed into it become items, items are
ticked, crossed items sink, and all checklists are one filter away. This note
is the backend half — what is stored, what the worker writes, how the cleaned
view differs — and why the body stays the single source of truth. Code:
`model.NoteIndex.Kind` (`backend/internal/model/types.go`),
`Pipeline.extractItems` (`backend/internal/pipeline/clean.go`), `checklistItems` in
`Pipeline.append` (`backend/internal/pipeline/append.go`), the items prompt and `ParseItems`
(`backend/internal/cleanup/items.go`), `service.CheckCleanMode`
(`backend/internal/service/note_clean.go`). The frontend
half — the parser, the Items tab, the grip's drag — is
`frontend/src/features/notes/checklist.ts`, `ChecklistEditor.tsx` and
`frontend/src/hooks/useDragReorder.ts`, and "Editing the list" below.

## Data model

`NoteIndex.Kind` is `""` for a plain note or `"checklist"`. It is a promoted
DynamoDB attribute like `language`, written only when set, so a row from
before 2026-09-21 carries no attribute and needs no backfill;
`NoteItemAttributes` includes it, so `chintanctl reconcile` re-promotes it
with the rest. The wire maps `""` to `"note"` and `kind` is required on every
`Note` and `NoteDetail`, the search-corpus rows included. `GET
/v1/notes?kind=checklist` filters before the page is cut, exactly as `?tag=`
does, so a page holds up to `limit` matches and the cursor is set only when
more matches exist.

A checklist body is GitHub task-list syntax, one item per line — `- [ ] text`
open, `- [x] text` done — and nothing else. Blank lines are ignored by every
reader. The server never converts a body: switching `kind` is a PATCH that
carries the converted body from the client, and a body the client did not
send is left as it is.

A sub-item is two spaces of indent under its parent — `  - [ ] Plates` under
`- [ ] Party` — and there is one level of them (`MAX_DEPTH = 1` in
`checklist.ts`; owner decision CL-D1, 2026-09-27, Keep parity: a spoken list
is a handful of items, and nothing in a voice-first product produces depth
three). The frontend parser (`parseChecklist`) reads the indent as a depth
clamped to the parent's plus one and to `MAX_DEPTH` — a jump of two levels
reads as one, a third level written elsewhere reads as the second and
flattens to it on the first save here, a child with no parent is top level —
and writes it back as two spaces per level, so a one-level body indented in
another editor round-trips byte for byte. Until 2026-09-26 such a line
missed the item pattern altogether: it showed as an open row whose text was
the raw syntax and was rewritten to `- [ ]   - [x] Candles` on the first
save. The worker appends at the end of the body with no indent, so a filed
item is top level by construction, and `extractItems` and the `tasks` prompt
know nothing of depth; the editor gives a new item the depth of the item it
follows, nests and un-nests on request ("Sub-items" below), and a moved item
takes the depth of the slot it lands in and carries its sub-items with it
(`blockOf`, `moveItem`). The backend is indent-blind, in four places, and
stays so: `keepTick` (`pipeline/append.go`) carries a tick only from a line
that begins `- [x] `, which every worker-written line does; `lowerTick` and
`checklistItemLine` (`cleanup/prompt.go`) trim a line before reading it, so a
`tasks` answer over a nested body is checked and stored flat — adopting the
view drops the indent, never a tick; `replaceChecklistItems`
(`pipeline/append.go`, `regenerate.md`) finds a recording's earlier items by
their words whatever their indent and writes the new block at the top level,
so a regeneration or a retranscription brings an item the person had nested
back to the top and a typed sub-item that followed a removed parent nests
under the line now above it (the parser clamps an orphan, so nothing breaks);
and the row's "3 of 7 done" counts every item whatever its depth, as Keep's
count does. Export and the search text see
the raw lines, indent and all, which is Markdown.

## The append rule

When the destination note is a checklist, the recording becomes the items it
named, one open line each — `- [ ] Chickpeas`, `- [ ] Green gram` — and
nothing else. The items come from one model call in place of the transcript
cleanup (`Pipeline.extractItems`, `cleanup.ItemsPrompt`): the prompt reads
the **raw** transcript with the list's title beside it and answers
`{"items":[…]}` — short noun phrases in the speaker's words, language and
script, quantities kept, the words addressed to the app left out ("add",
"into it", "to my list", the list's own name), "X and Y" split into two. It
runs in the `cleaning` status, under the cleanup op and deadline, and stores
the items one per line at `clean_key`, so a retry does not call again.

Until 2026-09-26 the item was the cleaned transcript on one line, and which
words those were depended on the router's span removal, which knows filing
and naming instructions and nothing about items: "Add umbrella to shopping
list" became the item `list` and "create a shopping list and add chickpeas
and green gram into it" became `Add chickpeas and green gram into it.`. The
router still decides the destination for a routed recording; its spans, and
the targeted path's instruction strip, are not applied to a checklist's
content, because the extraction handles those words itself and the item is
no longer at the mercy of where a span ended.

### A new list

A recording on Home that names a list no note holds — "add milk to the
shopping list" before any Shopping list exists — used to end as a plain note
holding the cleaned sentence `Add milk to the shopping list.`: the router
created every new note plain (`CreateNote(ctx, tenantID, title, nil)`), so
the pipeline ran the prose cleanup, and when the owner converted the note the
sentence became its one item (owner feedback 2026-09-27, capture of 05:42Z).
The router's `new` decision now carries a `kind` — `checklist` when the
speaker names a list (shopping list, groceries, to-do, packing list, "add X
to the Y list") or dictates things to tick off one by one; `note` otherwise
and in doubt (`routing.SystemPrompt`, "Kind") — and `Pipeline.route` writes
it on the new row before the capture points at the note, in the same
`PutNote` as the language, so `run()` takes the `extractItems` branch for
that same recording and the body is `- [ ] Milk`. The parse is strict
(`RouteDecision.Checklist`): exactly `checklist` makes one, anything else is
a plain note, because a plain note the person can convert while a checklist
they did not ask for has already turned their prose into items.

An append never carries a kind. When the router files a list-shaped sentence
into an existing plain note — "add eggs to my kitchen note" — the note stays
what it is and the sentence is cleaned into it; converting is the person's
call, and nothing guesses it. A capture parked at `needs_target` (routing
unconfigured, or a routing fault) loses the kind too: the person names the
note and it is created plain.

Three outcomes besides items:

- **No items** (`{"items":[]}`): the recording only told the app what to do
  — "create a shopping list". The capture is `no_content`, as an
  instruction-only recording is for a plain note; the note exists and gets
  no item.
- **Not a list** (no JSON object, no `items` array, an empty completion, more
  than 100 items): the recording is appended as one item with its line
  breaks collapsed, the pre-2026-09-26 behaviour, and
  `ChecklistItemsDiscarded{Reason=unusable}` counts it. Dictation is never
  lost to a bad reply.
- **Verbatim checklist**: no model call; the raw transcript — the recording
  as spoken, on the routed path as on the targeted one, never the router's
  span-cut text — is one item with its line breaks collapsed.

The prompt is the only guard on what an item is. `ParseItems` checks the
shape (a JSON object with an `items` array, at most 100, each at most 2,000
runes) and nothing about the words: a subsequence check against the
transcript would refuse the STT-garbling fix the prompt asks for, and a rule
dropping an item equal to the title would silently lose "add batteries" to a
list titled Batteries. An item the prompt should not have produced is visible
in the list and one tap from gone; a dropped one is lost. So an invented item
is caught only by the live evaluation (`provider.TestLiveEval/items` over
`testdata/eval/fixtures.json`, run against the real model before a prompt
change ships; `docs/design/prompts.md`) —
`ChecklistItemsExtracted{Outcome=items}` counts recordings, not whether their
items were spoken.

A request that is not an add — "remove milk from the list", "tick off the
eggs", "actually make that two umbrellas" — is returned by the prompt as one
open item exactly as spoken: honest and visible, never silently dropped and
never turned into an add of the thing named. Applying such a request to the
list is future work; its shape would be `{"add":[…],"remove":[…],"done":[…]}`
applied as a body edit under the recording's marker, and it is not built.

The capture marker keeps its place on the line before the first item
(`<marker>\n- [ ] A\n- [ ] B`, after `\n\n` when the body has content). A
paragraph runs to the next marker, so `CutCaptureParagraph` finds exactly
this recording's items: deleting or moving the recording removes them and
nothing else — a typed item with no marker and an item the person has since
ticked are untouched — and a moved recording lands in the target in
chronological position, its items still ticked if they were. That holds
until the first save from the Items tab: `serialiseChecklist` writes the
items with no blank line between recordings, so `CarryCaptureMarkers` finds
no paragraph boundary to keep a marker at and carries every marker to the
end, after which delete and move cut nothing (a tick is such a save). Not a
checklist regression — a plain note's marker moves the same way once its
paragraph is edited — but a checklist is edited far more often than it is
dictated into, so in practice the per-recording delete works for a list that
has only been spoken to. Transcribing a
recording again — or regenerating the note, `regenerate.md` — replaces its
items where they stand, or, on a list whose markers have been carried,
finds them by their words and swaps them in place (`replaceChecklistItems`);
a tick follows its item's words, and line for line only when no words match
and the count holds (`keepTick`). Each item is cut at 2,000 runes. Snippet and search text
see the raw lines.

## No cleaned view

A checklist has no cleaned view. `CheckCleanMode` answers
`ErrChecklistCleanMode` — 400 `a checklist has no cleaned view` — for `PATCH
cleaned_mode` and `POST …/clean` in any mode or none, and nothing is stamped
on the row or handed to the worker; `auto_clean` kept from before a note
became a checklist asks for nothing after an append or a body write; the
worker's `CleanNote` does nothing for a checklist task queued before the
deploy; `cleanedOf` renders `cleaned` as null for one, so a view stored
earlier is never shown, and the export leaves it out. A `cleaned_mode`
preference chosen while the note was prose is kept and reported, and applies
again when it is switched back. The frontend shows Items and Recordings only;
a link or a remembered tab naming Cleaned opens the items.

Until 2026-09-27 a checklist cleaned in a `tasks` mode — the Split up tab: the
whole list re-split into one task per action, with a done-item check and a
subsequence check on the answer, adopted over the body by the first tick.
Thirty of the thirty-four whole-note calls in the week measured were that
mode regenerating a 10–780-byte list after every appended item, and since the
items prompt splits "chickpeas and green gram" at capture time the tab mostly
re-answered a solved question (round-5 prompts lens, PR-D4). Items per
recording, and editing the list by hand, are the way; a one-shot "split this
long item" later would be a button that calls the items prompt on one line,
not a mode.

## Editing the list

Body order is display order for the open items, and a reorder is one body
write. The grip at a row's left edge is the one control for the order: a
drag lifts the row and the list re-sorts under the pointer
(`useDragReorder`, the pinned group's gesture, shared), nothing is written
while it is in the air, and on release `moveItem` rewrites the body once and
the editor saves at once, as it does for a tick — a discrete act, not typing.
The arrow keys on a focused grip move the row one slot. A tap on the grip —
a lift that never moved — opens the row's menu: Move up, Move down, Move to
top, Move to bottom, Make a sub-item, Move up a level, Delete. That is the
single-pointer path WCAG 2.5.7 asks
for, and it hangs on the grip rather than on a ⋮ of its own because a phone's
width has no room for a grip, a box, a dictated sentence and a ⋮ in one row.
The hook reports the tap before it swallows the browser's click: once the
list holds the pointer capture, that click is targeted at the list, not at
the grip, so the menu could not be opened by it. A body that changes under a
lifted row — a recording landing by refetch, a conflict's answer — drops the
row, since the slots it was moving between are gone. The touch-driven
pull-to-refresh stands down for a `touchmove` whose default the lifted row
has already prevented, or a downward drag at the top of a list pulled the
page along under the row.

### Sub-items

One level, as Keep has, and Keep's keys: Tab in an item's field makes it a
sub-item of the open row shown above it, Shift+Tab brings it up a level; the
grip's menu carries the same two as "Make a sub-item" and "Move up a level"
for a finger and for anyone who does not know the keys, 44 px each and named
for a screen reader, which also hears "Made a sub-item" / "Moved up a level"
and the field's name change from "Item 2" to "Sub-item 2"; the keys are
described on the field itself (`aria-describedby`), where they act, since a
reader in the field never hears the grip's description. A sub-item is set
in by one spacing step (`data-depth`, `--space-6`) with the same drawn box
and grip after it; the indent alone says "part of the row above". The first
open item can never be a sub-item — it has nothing to nest under — and a
Tab that can change nothing (that row, a row already a sub-item, Shift+Tab
at the top level) is left to the browser, so focus moves on and the list is
never a keyboard trap.

The row above is the one a person sees, not the body's previous line
(`nestUnder`): with `Milk`, `[x] Eggs`, `Bread` in the body the open rows
are Milk and Bread, and Tab on Bread makes it Milk's sub-item, moving its
line above Eggs's — a done line's place shows nowhere, so nothing visible
moves, and ticking Milk then takes Bread with it as the eye expects. Nesting
under the previous body line instead would have made Bread the sub-item of a
row sitting in Done: indented under Milk on screen, orphaned when Milk was
ticked, and pulled under Eggs when Eggs was reopened. Under a sub-item, Tab
makes a sibling under the same parent. Up a level (`unnest`) is in place,
and the sub-items that followed under the same parent become the row's own,
as an outliner does. A parent made a sub-item takes its children along as
its siblings, the one level being the limit; the same clamp applies when a
parent is dropped on a sub-item's slot.

Ticking a parent ticks its sub-items — the parent is the whole job, and a
finished job has no open parts — and the whole block is held for the tick's
beat and moves to Done together, its depth kept, so a finished parent reads
as a block there too. Reopening a sub-item reopens its parent, because a
parent with an open part is not done. Reopening a parent leaves its
sub-items as they are (the choice CL-D2 asked to be stated): they were
finished on their own terms, nothing about the parent says otherwise, and
the person can tick the parent again once the reopened part is done — Keep's
behaviour, and the one that never un-does work by implication. The reopened
parent then stands open in the list over sub-items that show only under
Done, and ticking it again re-ticks lines already done, so nothing visible
changes but the parent's own row: known, and kept, because the alternative
reopens work nobody asked to reopen. A sub-item
ticked on its own moves to Done alone and its parent stays open; nothing
completes a parent by counting its children. Delete done takes a done
parent's sub-items with it (`removeDone`); Uncheck all reopens every line.

Enter at the end of a parent starts its first sub-item (`insertItemAfter`),
as an outliner does: the new row appears right under the parent, where the
eye is, and a top-level line there would have taken the parent's sub-items
for its own — the body `Party`, `[ ]`, `  Plates`, `  Cups` reads as an
empty parent over them. The other choice, a top-level row after the block,
puts the new row under the last sub-item, away from where Enter was pressed.
Deleting a parent — Backspace in its emptied field, the menu's Delete, the ×
under Done — brings its sub-items up a level (`removeItem`) rather than
leaving the parser to read the first of them as the parent of the rest: the
job is gone, not its first part.

Moving a parent from the grip moves its block: a drag carries the sub-items
(`moveItem`), Move down and the down arrow step past the parent's own
children to the first row outside the block, a block that ends the list
cannot move down, and a row dropped on a sub-item's slot becomes one while a
sub-item dropped on a top-level slot comes out. Neither the menu nor the
arrow keys offer a level, so a move that changes the row's level is said:
"Now a sub-item" / "Now a top-level item" on the status line. A drag's draft
shows the lifted row alone while it is in the air and the block snaps
together on release; a parent dropped one slot down stands on its own
sub-item's slot, which the draft can show but nothing can mean, and it goes
back where it was rather than past the next row as the menu's step would
(a drag is placed by eye, and a jump past what the eye placed it on is the
surprise). Split up (`extractItems`, the `tasks` prompt) appends and proposes
at depth 0 as before; export and the search text are unchanged. A third
level is `MAX_DEPTH` plus one `data-depth` rule in `checklist.css`.

Done items keep their line where it stands; the Done section is the view's
grouping, not the body's. It is a disclosure — the `<h2>` holds a button
with `aria-expanded` — remembered per note for the session in
`sessionStorage` (`chintan.checklist-done.<id>`, open by default, the
NoteTabs pattern), with Uncheck all and Delete done beside it. Uncheck all
flips every `[x]` to `[ ]` in place (`uncheckAll`). Delete done drops every
done line, and a done parent's sub-items with it (`removeDone`), and offers
Undo in the shell's toast for six seconds — no typed word and no dialog
(OF-DEL): Undo writes the previous body back and saves. Done rows have no
grip, because their order is the body's and nothing shows it; a drag among
them would move lines whose places are invisible.

The capture-marker rule is unchanged by a reorder. Every save from the Items
tab already carries the markers to the end (`serialiseChecklist` writes no
paragraph break, so `CarryCaptureMarkers` finds none to keep a marker at),
and a reorder is such a save. Per-line ownership — a marker on every item,
so a recording's items stayed deletable together after a reorder — was
rejected: a reorder can separate a recording's items, after which "delete
this recording's items" is either a surprise or a lie. Autosave and the
conflict prompt are unchanged too: a plain 409 whose only difference is an
appended item still offers Keep both (`additionTo` / `withAddition`), which
places the new item after the reordered draft; any other divergence is the
usual choice.

Rejected: items as rows, or order metadata beside the body — the state of a
task in two places, see below; hold-to-lift on the row as the pinned group
has — a row's words are a field, and a hold on them should select words, not
lift the row, so the grip is where every pointer lifts; unlimited nesting
depth (CL-D1, 2026-09-27) — one level is what a spoken list needs and what
Keep offers, deeper lists want an outline UI (collapse, guide lines per
level) and make the 500-rune snippet count and the `tasks` view harder to
reason about, and a third level is one constant plus CSS if a real list ever
asks; a drag-right gesture to nest — Tab and the menu cover keyboard and
finger, and a horizontal threshold on a vertical drag is a second gesture to
learn and to get wrong.

## Why the body stays the single source of truth

Every reader — the row's "3 of 7 done", the Items tab, the offline corpus,
Ask, the export — derives from the body, and every writer writes the body:
the worker's append, the editor's save, delete and move, the client's
conversion between kinds. Ticking an item is a body edit that flips `[ ]` to
`[x]` in place (a parent's sub-items with it), so the recording's marker
stays attached to its item and the delete/move rule keeps working after any
number of ticks.

The alternative — items as rows, or a done-set beside the body — would put the
state of a task in two places and make every existing invariant conditional:
the exactly-once append guard reads the body; the autosave protocol
(`append-vs-autosave.md`) conditions on the body's ETag; the cleaned view is
regenerated from the body and is never written by the API. A checklist that is
a body with a line format inherits all of that for the cost of a parser on
each side, and `chintanctl export` needs no change because a task list is
Markdown.
