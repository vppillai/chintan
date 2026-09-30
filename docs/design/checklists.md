# Checklist notes

A note can be a checklist: recordings filed into it become items, items are
ticked, crossed items sink, and all checklists are one filter away. This note
is the backend half — what is stored, what the worker writes, how the cleaned
view differs — and why the body stays the single source of truth. Code:
`model.NoteIndex.Kind` (`backend/internal/model/types.go`),
`Pipeline.extractItems` (`backend/internal/pipeline/clean.go`), `checklistItems` and
`mergeChecklistItems` in `Pipeline.append` (`backend/internal/pipeline/append.go`), the
shared item rules, the items prompt, `cleanup.Item` and `ParseItems`
(`backend/internal/cleanup/items.go`), `service.CheckCleanMode` /
`EffectiveCleanMode` (`backend/internal/service/note_clean.go`), the `tasks`
prompt, `TasksPrompt` and `SplitOutput` (`backend/internal/cleanup/prompt.go`). The frontend
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
save. The worker used to append at the end of the body with no indent, so
a filed item was top level by construction; since 2026-09-29 it writes a
group the person spoke — "eggs from Walmart" — as a parent line with its
things two spaces in ("The append rule" below), and Split up proposes the
list in the same shape. The editor gives a new item the depth of the item
it follows, nests and un-nests on request ("Sub-items" below), and a moved
item takes the depth of the slot it lands in and carries its sub-items with
it (`blockOf`, `moveItem`). The backend reads the indent where it writes
lines and folds it away where it matches words: `checklistItems`
(`pipeline/append.go`) keeps a leading two-space indent ahead of the box;
`keepTick` carries a tick to the line with the same words at whatever depth
and keeps that line's indent; `SplitOutput` (`cleanup/prompt.go`) reads a
nested body as items and writes the answer's tree with the indent, so
adopting the view keeps sub-items and never drops a tick;
`replaceChecklistItems` (`pipeline/append.go`, `regenerate.md`) and the
merge find a recording's earlier items by their words whatever their indent,
and a regenerated block is written with the recording's own indent — a
sub-item the person made by hand comes back at the depth the recording gives
it, and a typed sub-item that followed a removed parent nests under the line
now above it (the parser clamps an orphan, so nothing breaks); and the row's
"3 of 7 done" counts every item whatever its depth, as Keep's count does.
Export and the search text see the raw lines, indent and all, which is
Markdown.

## The append rule

When the destination note is a checklist, the recording becomes the items it
named, one open line each — `- [ ] Chickpeas`, `- [ ] Green gram` — grouped
as the person grouped them, and nothing else. The items come from one model
call in place of the transcript cleanup (`Pipeline.extractItems`,
`cleanup.ItemsPrompt`): the prompt reads the **raw** transcript with the
list's title beside it and answers a one-level tree,
`{"items":[{"text":"Walmart","children":[{"text":"Eggs"}]},{"text":"Milk"}]}`.
What an item is lives in one rule block, `cleanup.checklistItemRules`,
shared with Split up's prompt (`docs/design/prompts.md`): one thing the
person wants, in their own words, language and script, quantity kept; every
word about the list rather than on it left out — "add", "to my list", the
list's own name, also when the recording opens with that name to file it
("Shopping list eggs from Walmart" → Walmart › Eggs) — so "Add milk, eggs
and protein powder to the shopping list" is Milk, Eggs, Protein powder and
never "Add milk" (owner, 2026-09-29); "X and Y" split; **group as the person
grouped** — a place, a person, an occasion or a category the things are
named under is the parent, the things its children, one level, never
invented and never the list's own name; a remove/tick/change request
returned as spoken; garbling fixed and fillers dropped, nothing else
changed, nothing lost. A model that answers the old shape, bare strings,
still parses as flat items; a grandchild is clamped to a child of the
top-level item (CL-D1). It runs in the `cleaning` status, under the cleanup
op and deadline, and stores the tree one line per item at `clean_key`, a
child's line two spaces in (`cleanup.RenderItems`), so a retry does not call
again.

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
it, with the language, in the note's one create
(`NotesService.CreateNoteOnce`, under an id derived from the capture's, so a
retry after a crash finds that note rather than making another; R7-21), so
`run()` takes the `extractItems` branch for that same recording and the body
is `- [ ] Milk`. The parse is strict
(`RouteDecision.Checklist`): `checklist` in any case makes one, anything
else is a plain note, because a plain note the person can convert while a
checklist they did not ask for has already turned their prose into items.
`RouterNewNoteKind{Kind}` counts what the model answers in production.

An append never carries a kind. When the router files a list-shaped sentence
into an existing plain note — "add eggs to my kitchen note" — the note stays
what it is and the sentence is cleaned into it; converting is the person's
call, and nothing guesses it. A routing fault loses the kind too: the
worker keeps the dictation as a plain note titled from its first words
(`route.go`, the `decideTarget` error branch). So does a capture parked at
`needs_target` — the router's unsure append, or a worker with no note
creator, which production never runs — where the person picks the note or
names one, created plain.

Three outcomes besides items:

- **No items** (`{"items":[]}`): the recording only told the app what to do
  — "create a shopping list". The capture is `no_content`, as an
  instruction-only recording is for a plain note; the note exists and gets
  no item.
- **Not a list** (no JSON object, no `items` array, an empty completion, more
  than 100 items counting sub-items): the recording is appended as one item
  with its line breaks collapsed, the pre-2026-09-26 behaviour, and
  `ChecklistItemsDiscarded{Reason=unusable}` counts it. Dictation is never
  lost to a bad reply.
- **Verbatim checklist**: no model call; the raw transcript — the recording
  as spoken, on the routed path as on the targeted one, never the router's
  span-cut text — is one item with its line breaks collapsed.

The prompt is the only guard on what an item is. `ParseItems` checks the
shape (a JSON object with an `items` array of objects or strings, at most
100 counting sub-items, each text at most 2,000 runes, an item with no text
dropped and its children lifted) and nothing about the words: a subsequence check against the
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

### Merging into what the list has

A recording's items are not simply appended: what already has a line in the
list joins it first (`mergeChecklistItems`, `pipeline/append.go`, ported
from the round-6 checklist lens's merge oracle; the twelve cases that pin it
are `TestMergeChecklistItems` in `pipeline/checklist_append_test.go`), and
only the rest goes under the recording's
marker, in the recording's order. Three rules:

- an item **with children** whose words match a **top-level** line, open or
  done, joins that block: each child not already under it is added after
  the block's last line, a child already there and done is reopened, and a
  done parent that gained or reopened a child is reopened (a parent with an
  open part is not done — the editor's own rule). "chicken from Costco" over
  a list that has Costco › Meat gives Costco › Meat, Chicken, not a second
  Costco;
- an item **without children** whose words match **any** line is not added
  again: open, it is a duplicate and dropped; done, it is reopened, and a
  reopened sub-item reopens its parent, because "add milk" over a ticked
  Milk means milk is wanted again;
- matching folds case, punctuation and whitespace (`llm.FoldWords`, the one
  fold every reader uses; a combining mark such as an Indic vowel sign or
  virama is part of its word, so പാൽ and പുൽ, or दाल and दिल, are two
  items) and never reads indent; a marker line or a blank
  line is left where it stands and an insertion never crosses a marker; a
  merge never ticks a line the list has, only ever flips `[x]` → `[ ]` (an
  item that arrives done is a tick a regeneration carried, written as it
  came). `ChecklistItemsMerged{Outcome=joined|deduped|reopened}` counts
  what happened, once per body that landed.

A recording whose every item joined leaves a bare marker as a trailer, the
way a carried marker stands. The honest limit: a merged child lives under
its parent, inside whatever paragraph that parent's line is in, not under
the recording that spoke it. So deleting or moving that recording takes
what is under its own marker only, and deleting the recording that owns the
parent's paragraph takes the merged child with it — the same paragraph
rule a plain note has, and the same limit a list already edited has for
every recording. Regenerating either recording is safe: the earlier items
are found by their words wherever they stand (`replaceChecklistItems`
always goes by words for a checklist, since 2026-09-29, for exactly this
reason), a parent with another child still under it is left standing and
joined again through the same merge, and the rest goes where the first
removed line stood — so a child extracted again is not doubled, keeps its
tick and its place, and the other recording's child is not lost, whether
the recording comes back with the same words, other words or none
(`regenerate.md`). A first append that was written and never marked — the
worker died between the body write and the capture's completion — is
retried the same way: its own items stand in for the artefact copy it does
not have yet and are found by their words, so the retry inside the lease
finishes the attempt without a write and the takeover after it keeps the
child a later recording merged under its parent (`ownItems`; review
2026-09-29, DB6-1). Only a recording that never had an artefact — appended
while the list was verbatim — and now brings words no line under its
marker has, or whose marker stands bare with none of its words in the
list, is replaced by the paragraph cut; a bare marker alone proves
nothing, since the Items tab carries every marker to the end on each save.
The match is exact folded words: "Costco" and "Costco wholesale" are two
parents (a `ponytail:` ceiling in `append.go`; parent-name synonyms are
the upgrade if a real list asks).

The capture marker keeps its place on the line before the first item
(`<marker>\n- [ ] A\n- [ ] B`, after `\n\n` when the body has content). A
paragraph runs to the next marker, so `CutCaptureParagraph` finds exactly
this recording's items (and, after a merge, a child another recording put
under one of them): deleting or moving the recording removes them and
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
recording again — or regenerating the note, `regenerate.md` — finds its
earlier items by their words, under its marker or anywhere a save has
carried them, and swaps the new ones in where the first of them stood
(`replaceChecklistItems`); a tick follows its item's words, and line for
line only when no words match and the count holds (`keepTick`). Each item
is cut at 2,000 runes. Snippet and search text see the raw lines.

## The `tasks` clean mode

A checklist cleans in `tasks` and in nothing else. `EffectiveCleanMode`
answers `tasks` for every checklist, whatever preference the row held before
it became one, so auto-clean and an unspecified `POST …/clean` run it without
being told. `CheckCleanMode` is the one rule for `PATCH cleaned_mode` and
`POST …/clean {mode}`: a checklist takes anything but `tasks` as 400
`cleaned_mode must be tasks for a checklist`; a plain note takes `tasks` as
400 `cleaned_mode must be polished or structured`. The check runs against the
kind as the PATCH leaves it, so `{kind: checklist, cleaned_mode: tasks}` is one
request. A `tasks` preference left on a note switched back to plain is ignored,
not run, and comes back into force if the note becomes a checklist again.

The prompt (`cleanup.TasksPrompt`, `noteTasksSystemPrompt`) is "the list
as it stands → the list it was meant to be": it composes the shared item
rules above and adds the three a whole list needs — every line's meaning is
kept, a line that is already one thing word for word, a line holding
several things one item each, a sentence spoken to the app the things it
named; the groups the list has are kept and an item joins an existing group
when its own words say so ("chicken from Costco" under Costco), two lines
naming one thing are one item; done stays done, an open line is never
marked done, and a duplicate merges into an open item if either was open.
Its user prompt names the list's title first, so the list's own name is
never an item — the ring speaks the title before every line ("Business
ideas by Priyanka Seated pool for dogs", owner tenant). The answer is JSON,
`{"items":[{"text":…,"done":…,"children":[…]}]}`, in the list's order.
Until 2026-09-29 the prompt asked for "granular, actionable tasks" in the
person's words with done lines verbatim and in order, which is what turned
the owner's `Add milk, eggs and protein powder to the shopping list` into
"Add milk to…", "Add eggs to…", "Add protein powder…" (live Split up,
2026-09-29).

The answer is checked against the body rather than trusted
(`cleanup.SplitOutput`), because adopting the view (the first act in Split
up, or Use this list) writes it over the body:

- it must parse as items (`ParseItems`' shape, at most 500 counting
  sub-items) — else the fixed verdict `the cleanup model returned nothing
  usable` and the previous view is kept;
- an item whose words are not the body's words, in order
  (`llm.VerifySubsequence`; a group's name — Walmart, Party — is a body
  word), is dropped and `TasksItemsDropped` counts it, a dropped parent's
  children lifted to the top level; the rest of the answer is stored. This
  is what catches `Make a list` — the model inventing an antecedent for
  "it" — while keeping the split beside it. A reply with nothing left is
  `nothing usable`;
- **tick safety**, refused whole: a `- [x]` body line with no done answer
  item whose words are the line's or a sub-sequence of them (a done line
  tidied) is a lost tick; an open answer item with a done body line's words
  is a reopened one, unless the body also had an open line with those words
  (they merge, open wins) — and so is a childless open answer item whose
  words are part of a done line's and of no open line's (`- [x] Milk and
  eggs` split into an open Milk and a done Eggs), a parent exempt because a
  group's name over done lines is a group; a done answer item with no done
  body line's words is an invented one, and one with an open body line's
  words that no open answer item has closed the open one of a pair. A view
  that changed a tick is worse than the view it would replace. A line with
  no letter or digit is no item to any of this (`itemText`), so a typed
  `- [x] —` blocks nothing.

The stored view is task-list lines, a sub-item indented two spaces, in
`cleaned_body` as before; `stale` and `auto_clean` are unchanged. The owner
decided on 2026-09-29 to keep Split up and improve it — this is round 6's
half of that (`docs/backlog.md`, "Round 6"); the round-5 proposal to drop it
(PR-D4) was reversed before it merged.

## Editing the list

Body order is display order for the open items, and a reorder is one body
write. The grip at a row's left edge is the one control for the order and
the level (`ChecklistRow.tsx`; the list, the drag wiring and every body
write are `ChecklistEditor.tsx`, the Done disclosure `ChecklistDone.tsx`):
a drag up or down lifts the row and the list re-sorts under the pointer
(`useDragReorder`, the pinned group's gesture, shared), nothing is written
while it is in the air, and on release `moveItem` rewrites the body once and
the editor saves at once, as it does for a tick — a discrete act, not typing.
The up and down arrow keys on a focused grip move the row one slot. A tap on
the grip — a lift that never moved — opens the row's menu: Move up, Move
down, Move to top, Move to bottom, Make a sub-item, Move up a level, Delete.
That is the single-pointer path WCAG 2.5.7 asks
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

A drag on the grip sideways changes the row's level instead (owner,
2026-09-29; R6-CL-2). The first 10 px of travel decide the axis — more
across than along and the drag is sideways, else it is the reorder above —
and the axis is locked from then on, so a vertical drag that drifts never
changes a level and a sideways one never re-sorts. Sideways, the row keeps
its slot and every indent step (`--space-6`, 24 px at a 16 px root) to the
right is one level in, to the left one level out, clamped to the one level
there is; the lifted row previews what release would do — set in or out by
the step, with a 3 px bar in the accent at its start (`data-nest-preview`)
— and shows nothing when the move is one `nest` would refuse: the first
open row (it has nothing to go under), a row already a sub-item, a done
neighbour. On release the write is the same `nestUnder` / `unnest` Tab and
the menu make, saved at once and said ("Made a sub-item" / "Moved up a
level"); a pointer that came back under a step writes nothing. A drag past
the slop on either axis is no longer a tap, so a wobble on the handle does
not open its menu.
The right and left arrow keys on a focused grip do the same as the drag,
beside the up and down that move it, and the grip's description says so;
Tab and Shift+Tab in the field are unchanged. The pinned group passes no
`onShift` and its drag has one axis, as before.

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
parent is dropped on a sub-item's slot. The sideways drag on the grip and
→/← on it ("Editing the list" above) go through the same two functions, so
every path nests the same way and refuses the same rows.

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
surprise). The worker and Split up write the person's own groups as
sub-items ("The append rule"); export and the search text are unchanged. A
third level is `MAX_DEPTH` plus one `data-depth` rule in `checklist.css`.

Done items keep their line where it stands; the Done section is the view's
grouping, not the body's. It is a disclosure — the `<h2>` holds a button
with `aria-expanded` — remembered per note for the session in
`sessionStorage` (`chintan.checklist-done.<id>`, open by default, the
NoteTabs pattern), with Uncheck all and Delete done beside it. Uncheck all
flips every `[x]` to `[ ]` in place (`uncheckAll`). Delete done drops every
done line, and a done parent's sub-items with it (`removeDone`), and offers
Undo in the shell's toast for six seconds — no typed word and no dialog
(OF-DEL): Undo writes the previous body back and saves, unless the list
changed since (see Split up). Done rows have no grip, because their order
is the body's and nothing shows it; a drag among them would move lines
whose places are invisible.

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
asks. The drag-right gesture to nest was on this list in round 5 (Tab and
the menu cover keyboard and finger; a horizontal threshold on a vertical
drag is a second gesture to learn) and came off it on 2026-09-29 at the
owner's asking: a spoken list is nested with a thumb, on a phone, where Tab
does not exist and the menu is two taps away — so the handle that already
moves a row moves it sideways too, with the axis decided in the first 10 px
and a preview before anything is written ("Editing the list" above).

### Split up

The Split up tab shows the worker's `tasks` proposal in the Items editor
itself — the same rows, grips, boxes, fields, add row and Done (decision
R6-CL-D1, option a, 2026-09-29) — so the proposal can be read as it would be
lived with, and taken with whatever act comes first. The first act there — a
tick, a drag, a Tab, a keystroke — replaces the body with the proposal and
applies that act in the same save; the caption above the rows says so
("Ticking, moving or editing here replaces your list with the split
version."), Use this list takes it outright, and either way the shell's
toast offers Undo for six seconds (the OF-DEL pattern: no question first for
what can be undone), which writes the body as it stood back, saves, and
shows the proposal again until the note refetches: neither save wrote the
view itself (`service/notes.go` clears `stale` only for a body byte-equal to
it, trailing whitespace aside, and the adopted body has the act applied), so
from the next refetch — the detail query's 30 s `staleTime`, or reopening
the note — the tab shows the stale notice over inert rows, and Use this list
still takes the proposal. From then on the tab shows the body — the split
list with the changes made since (`adoptedSplits`, per proposal) — and an
act there is an ordinary edit, saved as the Items tab saves it. A proposal
older than the note, and one a regeneration is about to replace, is drawn
`inert` (one attribute on the wrapper, not a prop through every control)
with the stale notice above it, until Regenerate or Use this list; a stale
proposal taken by an act would put a list that predates the note's later
changes over the body in one save. `inert` also removes the rows from the
accessibility tree — the disabled boxes before round 6 could still be read
by a screen reader; a stale or pending proposal cannot — so a screen-reader
user has the stale notice for why, and Use this list and Regenerate as the
two controls left. Delete done as the first act adopts like any other: the
editor shows its own "N done items deleted" toast before it writes, so the
adoption's toast lands last and is the one standing, and its Undo restores
the list as it stood (DB6-2). Either Undo — the adoption's, and Delete
done's in the editor — refuses when the note's body is no longer what that
Undo's act wrote, saying "The list changed since — nothing undone." rather
than writing a captured body over a recording that filed in by refetch
inside the six seconds. The body it compares is read from the note editor's
own mirror (`NoteEditor.current`, handed to the editor as `currentBody`),
not from the panel or the editor: the toast is the shell's and stands
through a tab switch, which unmounts them, so a body they mirrored would
stay equal to the one they last wrote and the Undo would go through. The
person's own acts since do not block it, so Undo still takes back the
adoption and the ticks since (DB6-3). The editor is `{ noteId, body,
currentBody, onChange, onSave }` and knows nothing of whose body it is: the
Items tab hands it the note editor, Split up hands it `adopt`. The find bar
still counts nothing in this tab.

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
