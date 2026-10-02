# Checklist notes

A note can be a checklist: recordings filed into it become items, items are
ticked, done items sink under a Done heading, and every checklist is one
filter away. This note is the whole feature — what is stored, what the
worker writes, how Tidy up list rewrites a list, how the Items tab edits it —
and why the body stays the single source of truth. Code, backend:
`model.NoteIndex.Kind` (`backend/internal/model/types.go`),
`Pipeline.extractItems` (`backend/internal/pipeline/clean.go`),
`checklistItems`, `mergeChecklistItems` and `replaceChecklistItems`
(`backend/internal/pipeline/append.go`), the shared item rules, the items
prompt, `cleanup.Item`, `ParseItems`, `ParseLine`, `ParseLines` and
`ItemsFromLines` (`backend/internal/cleanup/items.go`), `TasksPrompt` and
`SplitOutput` (`backend/internal/cleanup/prompt.go`), `service.CheckCleanMode`
and `EffectiveCleanMode` (`backend/internal/service/note_clean.go`).
Frontend: the parser and every body edit (`frontend/src/features/notes/checklist.ts`),
the Items tab (`ChecklistEditor.tsx`, `ChecklistRow.tsx`, `ChecklistDone.tsx`),
the drag (`frontend/src/hooks/useDragReorder.ts`), Tidy up list
(`useTidyList.ts`, the ⋮ item in `NoteActions.tsx`), the sheet
(`frontend/src/styles/checklist.css`).

## Data model

`NoteIndex.Kind` is `""` for a plain note or `model.NoteKindChecklist`
(`"checklist"`): a promoted DynamoDB attribute like `language`, written only
when set, so a row without it is a plain note; `NoteItemAttributes` includes
it, so `chintanctl reconcile` re-promotes it with the rest. The wire maps
`""` to `"note"`, and `kind` is required on every `Note` and `NoteDetail`,
the search-corpus rows included. `GET /v1/notes?kind=checklist` filters
before the page is cut, as `?tag=` does, so a page holds up to `limit`
matches and the cursor is set only when more exist.

A checklist body is GitHub task-list syntax, one item per line — `- [ ] text`
open, `- [x] text` done — and nothing else. Blank lines are ignored by every
reader. The server never converts a body: switching `kind` is a PATCH that
carries the converted body from the client.

**The line rule.** Every reader — the editor (`parseChecklist`, the `ITEM`
pattern in `checklist.ts`) and the backend (`cleanup.ParseLine`, used by
`ItemsFromLines` and by every tick, reopen and match in `pipeline/append.go`)
— reads an item line one way: an indent of spaces and tabs, a tab counting as
two spaces; `- [`; a box of a space (open), `x` or `X` (done); `]`; at most
one space; then the text as written. So `- [ ]Milk` is an item,
`\t- [x] Candles` is a done sub-item, and ` - [ ] Plates` (one space) is top
level. Every two columns of indent is one level down; a trailing `\r` is not
part of the text; any other non-blank line is prose. `ParseLine` alone
returns the indent's raw level; the clamp below is the readers'. The rule is pinned by
example in `backend/internal/cleanup/testdata/checklist-lines.json`, which
`cleanup/items_test.go` and `checklist.test.ts` both assert, so a change to
one reader that the other does not share fails a test; its `max_depth` is
asserted against both `cleanup.MaxDepth` and `MAX_DEPTH`, so the two
constants cannot drift. Three differences change no line's meaning and are
kept: the editor keeps an item's text byte for byte (it writes back on every
keystroke) while the Go readers collapse whitespace and skip an item with no
words; the editor shows an indented prose line as a top-level item, while
`ItemsFromLines` reads it at its indent's level, because it also reads the
clean artefact's box-less lines (`cleanup.RenderItems`); and a tick or reopen
in the worker writes the line back in the normal form (`- [x] `, one space).

**Four levels.** A sub-item is two spaces of indent under its parent —
`  - [ ] Plates` under `- [ ] Party` — and two more for each level below it,
four levels in all (`MAX_DEPTH = 3` in `checklist.ts`, `cleanup.MaxDepth = 3`
in Go; 0 is the top). Not five: each step is one spacing token (`--space-6`,
24 px), the grip and the box keep their 44 px at every level, so at 320 px
the fourth level leaves about 94 px of text and a fifth about 70 px — four or
five words a line; deeper than four reads as an outline, not a list, and
wants an outline's UI. Both parsers read the indent as a depth clamped to
the item before's plus one and to the maximum — a jump of two levels reads
as one, a deeper line reads as the fourth and flattens to it on the first
save (clamped, never dropped), a child with no parent is top level — and
write it back as two spaces per level, so a body indented in another editor
round-trips byte for byte. The Go form is `cleanup.ParseLines`, which every
depth read in `pipeline/append.go` goes through (`depths`). A capture marker
or a blank line does not reset the clamp, because the editor never sees
either (the API strips markers, the editor drops blanks), so a marker between
a parent and a sub-item a later recording merged under it does not cut them
apart. A prose line does: the editor shows it as a top-level item, so the
item after it is at most a sub-item to every reader (the fixture's "prose
between items" case).

The row's "3 of 7 done" counts every item at every depth. Export and the
search text see the raw lines, which are Markdown.

## The append rule

When the destination note is a checklist, the recording becomes the items it
named, one open line each, grouped as the person grouped them, and nothing
else. The items come from one model call in place of the transcript cleanup
(`Pipeline.extractItems`, `cleanup.ItemsPrompt`): the prompt reads the
**raw** transcript with the list's title beside it and answers a one-level
tree, `{"items":[{"text":"Walmart","children":[{"text":"Eggs"}]},{"text":"Milk"}]}`.
What an item is lives in one rule block, `cleanup.checklistItemRules`, shared
with the tasks prompt behind Tidy up list — one thing the person wants, in
their own words, every word about the list rather than on it left out, and
**grouped as the person grouped them**: a place, a person, an occasion or a
category the things are named under is the parent, never invented and never
the list's own name (`docs/design/prompts.md` has the text). "One level only"
is the items prompt's own rule, and `ParseItems` clamps a deeper reply to
one: what one breath names is a handful of things under a group at most,
and a second level from speech is a guess. The call runs in the `cleaning`
status, under the cleanup op and deadline, and stores the tree one line per
item at `clean_key`, a child's line two spaces in per level
(`cleanup.RenderItems`), so a retry does not call again. The router's spans
and the targeted path's instruction strip are not applied to a checklist's
content: the extraction handles those words itself, so an item is never at
the mercy of where a span ended.

### A new list

The router's `new` decision carries a `kind` — `checklist` when the speaker
names a list (shopping list, groceries, to-do, packing list, "add X to the Y
list") or dictates things to tick off one by one; `note` otherwise and in
doubt (`routing.SystemPrompt`, "Kind") — and `Pipeline.route` writes it, with
the language, in the note's one create (`NotesService.CreateNoteOnce`, under
an id derived from the capture's, so a retry after a crash finds that note
rather than making another), so `run()` takes the `extractItems` branch for
that same recording and "add milk to the shopping list" with no Shopping list
yet ends as a checklist whose body is `- [ ] Milk`. The parse is strict
(`RouteDecision.Checklist`): `checklist` in any case makes one, anything else
is a plain note, because a plain note the person can convert while a
checklist they did not ask for has already turned their prose into items.
`RouterNewNoteKind{Kind}` counts what the model answers in production.

An append never carries a kind: a list-shaped sentence filed into an
existing plain note is cleaned into it, and converting is the person's call.
A routing fault (`route.go`, the `decideTarget` error branch) and a capture
parked at `needs_target` both end in a plain note too.

Three outcomes besides items:

- **No items** (`{"items":[]}`): the recording only told the app what to do
  — "create a shopping list". The capture is `no_content`, as an
  instruction-only recording is for a plain note; the note exists and gets
  no item. `ChecklistItemsExtracted{Outcome=none}` counts it.
- **Not a list** (no JSON object, no `items` array, an empty completion, more
  than `MaxItemsPerRecording` = 100 items counting sub-items): the recording
  is appended as one item with its line breaks collapsed, and
  `ChecklistItemsDiscarded{Reason=unusable}` counts it. Dictation is never
  lost to a bad reply.
- **Verbatim checklist**: no model call; the raw transcript — never the
  router's span-cut text — is one item with its line breaks collapsed.

The prompt is the only guard on what an item is. `ParseItems` checks the
shape (a JSON object with an `items` array of objects or bare strings, at
most 100 counting sub-items, each text at most `MaxChecklistItemRunes` =
2,000 runes, an item with no text dropped and its children lifted) and
nothing about the words: a subsequence check against the transcript would
refuse the garbling fix the prompt asks for, and dropping an item equal to
the title would silently lose "add batteries" to a list titled Batteries. An
item the prompt should not have produced is visible and one tap from gone; a
dropped one is lost. So an invented item is caught only by the live
evaluation (`provider.TestLiveEval/items`; `docs/design/prompts.md`). A
request that is not an add — "remove milk from the list" — comes back as one
open item exactly as spoken, never turned into an add of the thing named;
nothing applies such a request to the list.

### Merging into what the list has

A recording's items are not simply appended: what already has a line in the
list joins it first (`mergeChecklistItems`; the cases that pin it are
`TestMergeChecklistItems` in `pipeline/checklist_append_test.go`), and only
the rest goes under the recording's marker, in the recording's order. Three
rules:

- an item **with children** whose words match a **top-level** line, open or
  done, joins that block (the line and every deeper line after it): each
  child that is not already one of the line's **direct** sub-items is added
  after the block's last line, whatever that line's depth — a sub-sub-item
  with a child's words is a different thing in a different group — a direct
  sub-item already there and done is reopened, and a done parent that gained
  or reopened a child is reopened (a parent with an open part is not done —
  the editor's own rule). "chicken from Costco" over a list that has Costco ›
  Meat gives Costco › Meat, Chicken, not a second Costco;
- an item **without children** whose words match **any** line is not added
  again: open, it is a duplicate and dropped; done, it is reopened, and so is
  every line it stands under, up to the top (`parentOf`), because "add milk"
  over a ticked Milk means milk is wanted again and a done item has no open
  descendant;
- matching folds case, punctuation and whitespace (`llm.FoldWords`, the one
  fold every reader uses: an Indic vowel sign or virama is part of its word,
  so പാൽ and പുൽ are two items, while the two spellings of a chillu and a
  stray zero-width joiner are folded by `llm.FoldScript`, so a dictated and a
  typed Malayalam item are one) and never reads indent; a marker or a blank
  line is left where it stands, an insertion never crosses a marker, and
  either ends a parent's block (`TestMergeParentBlockStillEndsAtABlankLine`;
  the known cost is a second Rice when Costco › Rice is spoken over Costco ›
  Meat, a blank, and a later recording's `  - [ ] Rice`); a merge never
  ticks a line, only ever flips `[x]` → `[ ]` (an item that arrives done is a
  tick a regeneration carried). `ChecklistItemsMerged{Outcome=joined|deduped|reopened}`
  counts what happened, once per body that landed.

A recording whose every item joined leaves a bare marker as a trailer. The
honest limit: a merged child lives under its parent, in whatever paragraph
that parent's line is in, not under the recording that spoke it, so deleting
the recording that owns the parent's paragraph takes the merged child with
it — the paragraph rule a plain note has. Regenerating either recording is
safe: `replaceChecklistItems` always goes by words for a checklist, so the
earlier items are found wherever they stand, a parent with another child
still under it is left standing and joined again through the merge, and the
rest goes where the first removed line stood (`regenerate.md`). A first
append written but never marked (the worker died between the body write and
the capture's completion) is retried the same way, its own items standing in
for the artefact copy it does not have yet (`ownItems`;
`TestARetryOfAnInterruptedFirstChecklistAppendKeepsASiblingsMergedChild`).
Only a recording that never had an artefact — appended while the list was
verbatim — whose new words stand nowhere under its marker, or nowhere in the
list when its marker is bare, has its paragraph cut and replaced whole; a
bare marker alone proves nothing, since every save from the Items tab
carries the markers to the end.
The match is exact folded words: "Costco" and "Costco wholesale" are two
parents (a `ponytail:` ceiling in `append.go`; parent-name synonyms are the
upgrade if a real list asks).

The capture marker keeps its place on the line before the first item
(`<marker>\n- [ ] A\n- [ ] B`, after `\n\n` when the body has content), so
`CutCaptureParagraph` finds exactly this recording's items: deleting or
moving the recording removes them and nothing else, and a moved recording
lands in the target in chronological position, its items still ticked. That
holds until the first save from the Items tab: `serialiseChecklist` writes
no blank line between recordings, so `CarryCaptureMarkers` finds no
paragraph boundary to keep a marker at and carries every marker to the end,
after which delete and move cut nothing (a tick is such a save). A plain
note's marker moves the same way once its paragraph is edited; a checklist
is simply edited far more often than it is dictated into. A tick follows its
item's words, and line for line only when no words match and the count holds
(`keepTick`). Each item is cut at 2,000 runes
(`TestChecklistItemsIsOneBoundedLinePerItem`).

## The `tasks` clean mode

A checklist cleans in `tasks` and in nothing else. `EffectiveCleanMode`
answers `tasks` for every checklist, whatever preference the row holds, so an
unspecified `POST …/clean` runs it without being told. `CheckCleanMode` is
the one rule for `PATCH cleaned_mode` and `POST …/clean {mode}`: a checklist
takes anything but `tasks` as 400 `cleaned_mode must be tasks for a
checklist`; a plain note takes `tasks` as 400 `cleaned_mode must be polished
or structured`. The check runs against the kind as the PATCH leaves it, so
`{kind: checklist, cleaned_mode: tasks}` is one request. A `tasks` preference
left on a note switched back to plain is ignored, not run.

The prompt (`cleanup.TasksPrompt`, `noteTasksSystemPrompt`) is "the list as
it stands → the list it was meant to be": the shared item rules plus the
four a whole list needs — every line's meaning kept; the list's groups kept
and joined when an item's own words say so ("chicken from Costco" under
Costco); every level kept, up to four, and none added; done stays done, and
a duplicate merges into an open item if either is open. Its user prompt
names the list's title first, so the list's own name is never an item (a
ring speaks the title before every line). The answer is JSON,
`{"items":[{"text":…,"done":…,"children":[…]}]}`, in the list's order.

The answer is checked against the body rather than trusted
(`cleanup.SplitOutput`), because Tidy up list writes it over the body:

- it must parse as items (`ParseItems`' shape, at most `MaxChecklistItems`
  = 500 counting sub-items, nested at most four levels, a deeper item
  flattened into the fourth after its parent) — else the fixed verdict `the
  cleanup model returned nothing usable` and the previous view is kept;
- an item whose words are not the body's words, in order
  (`llm.VerifySubsequence`; a group's name — Walmart, Party — is a body
  word), is dropped (`dropInvented`; the warning log carries the count), a
  dropped parent's children lifted to its level; the rest is stored. This
  catches `Make a list` — the model inventing an antecedent for "it" — while
  keeping the split beside it. A reply with nothing left is `nothing usable`;
- **coverage**, refused whole (`tickSafety`, `an open item was lost`): every
  open body line — a prose line with words included — must still be in the
  kept answer by words: an item that is the line, a part of it (one line
  split into several) or that holds it (several lines merged into one).
  Because the answer is written over the body, dropped text refuses the
  whole answer rather than shortening the list;
- **tick safety**, refused whole: a `- [x]` body line with no done answer
  item whose words are the line's or a sub-sequence of them is a lost tick;
  an open answer item with a done body line's words is a reopened one,
  unless the body also had an open line with those words (they merge, open
  wins) — a childless open item whose words are part of a done line's and
  of no open line's counts too (`- [x] Milk and eggs` split into an open Milk
  and a done Eggs), a parent exempt because a group's name over done lines
  is a group; a done answer item with no done body line's words is an
  invented one. A view that changed a tick is worse than the view it would
  replace. A line with no letter or digit is no item to any of this
  (`itemText`), so a typed `- [x] —` blocks nothing;
- **a done item has no open descendant** (`reopenParents`): a done answer
  item with an open item under it is stored open, so `- [x] Costco` ›
  `- [x] Meat` beside `- [ ] chicken from costco` tidies to an open Costco ›
  Meat (done), Chicken, whether the model reopened Costco or left it done.
  The lost-tick and reopened checks exempt that only when the list put the
  open item there: its own body line names the group, the body already had
  it open under that line, or it is itself such a reopened group one level
  down. A group the model invents over an open line — `- [x] Milk`,
  `- [ ] Eggs` answered as Milk › Eggs — still loses Milk's tick and is
  refused. The editor's `toggleItem` and the merge keep the same rule.

The stored view is task-list lines, two spaces of indent per level, in
`cleaned_body`. **No auto-clean for a checklist:** `auto_clean` is ignored
for one — stored, not refused — in both places the worker regenerates a view
of its own accord (`pipeline.cleanNoteAfter`, after an append or a
regeneration; `service.autoCleanAfterBodyWrite`, after a recording is moved
or deleted): its items are split at capture, so a view per recording would
be a model call for a view nobody adopts.

## Editing the list

The Items tab is the checklist's editor; a checklist has two tabs, Items ·
Recordings, and a remembered or linked `?tab=cleaned` lands on Items. Body
order is display order for the open items, and a reorder is one body write.
The grip at a row's left edge is the one control for the order and the
level (`ChecklistRow.tsx`; the list, the drag wiring and every body write
are `ChecklistEditor.tsx`, the Done disclosure `ChecklistDone.tsx`):

- **Drag up or down** lifts the row and the list re-sorts under the pointer
  (`useDragReorder`, the pinned group's gesture, shared); nothing is written
  in the air, and on release `moveItem` rewrites the body once and the
  editor saves at once, as for a tick — a discrete act, not typing. A body
  that changes under a lifted row (a recording landing by refetch, a
  conflict's answer) drops the row, because the slots it was moving between
  are gone. The drag's draft shows the lifted row alone
  while it is in the air, and the block it carries snaps together on release. Pull-to-refresh stands down for a `touchmove` the lifted row has
  already prevented, or a downward drag at the top would pull the page.
- **Drag sideways** changes the level instead. The first `GESTURE_SLOP_PX`
  (10 px, `hooks/gesture.ts`) of travel decide the axis, locked from then
  on, so a vertical drag that drifts never changes a level and a sideways
  one never re-sorts. Every indent step (`--space-6`, 24 px at a 16 px root,
  read live) to the right is one level in, to the left one out; the editor
  cuts the levels to the largest move that fits (`clampLevels`), so one
  release can move several levels and a drag that asks for one too many
  moves as far as it can — a drag is placed by eye where Tab refuses. The
  preview draws every row whose depth would change at that depth
  (`data-preview-depth`, absolute, outranking `data-depth`), the lifted row
  with a 3 px accent bar, the indent animating on the motion tokens and
  snapping under reduced motion; a pointer that comes back under a step
  writes nothing. The pinned group passes no `onShift` and has one axis.
- **A tap on the grip** — a lift that never moved — opens the row's menu:
  Move up, Move down, Move to top, Move to bottom, Make a sub-item, Move up a
  level, Delete: the single-pointer path WCAG 2.5.7 asks for, on the grip
  rather than a ⋮ of its own because a phone's width has no room for a grip,
  a box, a dictated sentence and a ⋮ in one row. The hook reports the tap
  before it swallows the browser's click: once the list holds the pointer
  capture that click is targeted at the list, not the grip, so the menu
  could not be opened by it. A drag past the slop on
  either axis is not a tap, so a wobble does not open the menu.
- **Keys on a focused grip**: up and down move the row one slot, right and
  left change its level; the grip's description says so.

### Sub-items

Up to four levels ("Data model"), and Keep's keys: Tab in an item's field
takes it one level in under the open row shown above it, Shift+Tab one level
out; the grip's menu carries the same two as "Make a sub-item" and "Move up
a level", 44 px each. A row goes at most one level under the row above it
(`planLevel`: `min(MAX_DEPTH, above.depth + 1)`), so under `Costco ▸ Meat`
one Tab on a top-level `Rice` makes it Meat's sibling and a second its child,
as Workflowy's and Keep's single step does. A screen reader hears where the
row went, its words cut at `TOAST_NAME_MAX` (40) characters: "Made a sub-item
of “Costco”", "Moved up a level, under “Party”", or "Now a top-level item";
the row carries `aria-level`, the field's name is "Item 3", "Sub-item 3",
"Sub-item 3, level 3" or "Sub-item 3, level 4", and the keys are described
on the field itself (`aria-describedby`), where they act. A sub-item is set
in by one spacing step per level (`data-depth`; the fourth is
`calc(3 * var(--space-6))`) with the same drawn box and grip after it; the
indent alone says "part of the row above" (`checklist.css`).

An indent is refused when there is no row above, when the row is already as
deep as the row above allows, when the row above or anything it stands under
is done, or when the row's own sub-items would go past the fourth level
(`canNest`) — indenting is an explicit act about levels, and quietly
flattening a grandchild is never what it means. From the grip each refusal
is a fixed sentence ("Nothing above to nest under", "Already a sub-item",
"Already four levels deep", "Cannot nest under a done item", "Its sub-items
are already four levels deep", "Already a top-level item") and the menu's
item is disabled; a sideways drag is refused only when not even one level
fits; from the field the Tab is left to the browser, so the list is never a
keyboard trap.

The row above is the one a person sees, not the body's previous line
(`shiftLevel`'s `above`): with `Milk`, `[x] Eggs`, `Bread` in the body, Tab
on Bread makes it Milk's sub-item, moving its line above Eggs's, so ticking
Milk takes Bread with it as the eye expects; nesting under the body's
previous line would make Bread the sub-item of a row sitting in Done. Up a
level is in place, and the sub-items that followed under the same parent
become the row's own, as an outliner does. A parent made a sub-item takes
its children down with it; a parent dropped on a sub-item's slot is clamped
instead, because a drop is placed by eye. `shiftLevel` is the one function
for a change of level, and every path — Tab, →/← on the grip, the menu, the
sideways drag and its preview — goes through one planner (`planLevel`,
`planMove`, `previewDepths`; `checklist.plan.test.ts`), so every path nests,
refuses and previews the same way.

Ticking a parent ticks its sub-items — the parent is the whole job — and the
block is held for the tick's beat and moves to Done together, its depth
kept. Reopening a sub-item reopens every item it stands under, because a
parent with an open part is not done. Reopening a parent leaves its
sub-items done: they were finished on their own terms, and the person can
tick the parent again once the reopened part is done — Keep's behaviour, the
one that never un-does work by implication. Nothing completes a parent by
counting its children. Delete done takes a done parent's sub-items with it
(`removeDone`); Uncheck all reopens every line (`uncheckAll`).

Enter at the end of a parent starts its first sub-item (`insertItemAfter`),
where the eye is; a top-level line there would take the parent's sub-items
for its own. Deleting a parent — Backspace in its emptied field, the menu's
Delete, the × under Done — brings its sub-items up a level (`removeItem`):
the job is gone, not its first part. Moving a parent from the grip moves its
block (`moveItem`): Move down and the down arrow step past its own children,
a block that ends the list cannot move down, a row dropped on a sub-item's
slot becomes one and a sub-item dropped on a top-level slot comes out, and a
move that changes the level is said ("Now a sub-item" / "Now a top-level
item") because neither the menu nor the arrows offered one. A parent dropped
one slot down stands on its own sub-item's slot, which nothing can mean, and
goes back where it was.

### Done, and the one Undo rule

Done items keep their line where it stands; the Done section is the view's
grouping, not the body's. It is a disclosure — the `<h2>` holds a button
with `aria-expanded` — remembered per note for the session in
`sessionStorage` (`chintan.checklist-done.<id>`, open by default, the
NoteTabs pattern), with Uncheck all and Delete done beside it. Delete done
offers Undo in the shell's toast ("N done items deleted") for `TOAST_MS`
(`components/Toast.tsx`, 6 s) — no typed word and no dialog for what can be
undone. A tick, which moves the row out of sight into Done, offers an Undo
for `TICK_TOAST_MS` (`ChecklistEditor.tsx`, 4 s) as "<item> done", the item's
words cut at `TOAST_NAME_MAX`: shorter because a tick is small, and the next
tick replaces it, so only the last tick is undoable. Undo writes the body
from before the tick back, so the row returns to its place. Done rows have
no grip: their order is the body's and nothing shows it.

**The one Undo rule.** Every Undo a list offers — a tick's, Delete done's and
Tidy up list's — is `undoIfUnchanged` in `ChecklistEditor.tsx`: it writes
the captured body back only while the body is still exactly what the act
wrote, and otherwise says "The list changed since — nothing undone." A
recording filed in by refetch, a Tidy answer, and the person's own later
tick or typing all count as a change, so an Undo never takes back an act it
did not offer to; comparing with the editor's last write instead would
silently revert the person's own later edits. A standing Undo can be
replaced rather than refused: a Tidy answer landing by poll and a note
Delete each put their own toast in its place, and the replaced Undo is never
applied to the new body. The tests: `ChecklistEditor.test.tsx` "a tick's Undo refuses after the
person's own later edit, rather than undo it too", `ChecklistNote.test.tsx`
"the tidy's Undo refuses after the person's own later tick, rather than undo
it too" and `NoteDetailScreen.test.tsx` "a note Delete takes a standing
Delete done Undo's place; its Undo restores the note, never the done
items".

Reopening a done row offers no Undo and is said as "Reopened". A tick is
said once: by its toast, a live region, or by the status line ("Marked
done") when the toast was kept away — the tick's toast is weak
(`ToastNotice.weak`) and never takes the place of a standing Delete done or
Tidy Undo. The status line is the shell's one region (`announce`,
`components/StatusRegion.tsx`), which the editor, the Tidy notice and the
autosave indicator all speak through.

Every save from the Items tab carries the markers to the end ("Merging"
above), and a reorder is such a save; a marker on every item is rejected
because a reorder can separate a recording's items, after which "delete this
recording's items" is a surprise or a lie. Autosave and the conflict prompt
are the plain note's: a 409 whose only difference is an appended item still
offers Keep both (`additionTo` / `withAddition`), which places the new item
after the reordered draft. Hold-to-lift on the row, as the pinned group has,
is rejected too: a row's words are a field, and a hold on them should select
words, so the grip is where every pointer lifts.

### Tidy up

The ⋮ menu has **Tidy up list** (after Pin; only for a checklist with an
open item, because the model splits and groups what is still to do; disabled
offline and while one runs, when it reads "Tidying…"). It calls
`POST /v1/notes/{id}/clean`, where the server picks `tasks` for a checklist,
and keeps the request in module state (`useTidyList.ts`: the view as it was,
and when), so it survives a tab switch and leaving the note within the
session. "Tidying the list…" stands above the rows, which stay editable. The
note is polled on the `cleanPollInterval` ladder (`cleaned.ts`) by
`usePollNote`, a timer of its own shared with the Cleaned tab
(`CleanedPanel.tsx`) rather than TanStack's `refetchInterval`, which restarts
on every update to the query while a filing recording's poll rewrites the
note every 1.5 s, until `cleanSettled`, then one of four things happens:

- **Fresh** (`!stale`, the editor's body equal to the server's, mode
  `tasks`): the view is written as the body and saved, and the toast says
  "List tidied: N lines → M items." with Undo for `TOAST_MS`, under the one
  Undo rule, read from the note editor's own mirror (`NoteEditor.current`)
  because the toast outlives any panel.
- **The same** (equal to the body, trailing whitespace aside): "Already
  tidy." and no write.
- **Changed meanwhile** (stale, or a draft that differs): "The list changed
  while tidying — nothing replaced." with Tidy again.
- **Failed** (`cleaned_error`, a refused request, or `CLEAN_POLL_TIMEOUT_MS`
  = 60 s with no answer): "Couldn't tidy the list." with Try again.

There is no preview before it lands; the Undo is the preview, and
`SplitOutput`'s guards refuse an answer that drops, invents or re-ticks an
item. The result is applied only by the device that asked, and only while
the list is unchanged; elsewhere it sits unused in `cleaned_body`.

**Prose to list.** The Details switch (`NoteDrawer.tsx`) PATCHes
`kind: checklist` with the `proseToChecklist` body, so no word is lost; once
that save has landed, and if any item has two or more words, it starts a
tidy, because a dictated paragraph is otherwise one long item. Its toast
reads "Made a checklist: N paragraphs → M items.", and Undo gives back the
paragraph items. A list turned back into prose before the answer lands drops
it.

## Why the body stays the single source of truth

Every reader — the row's "3 of 7 done", the Items tab, the offline corpus,
Ask, the export — derives from the body, and every writer writes the body:
the worker's append, the editor's save, delete and move, the client's
conversion between kinds. Ticking an item is a body edit that flips `[ ]` to
`[x]` in place (a parent's sub-items with it), so the recording's marker
stays attached to its item and the delete/move rule keeps working after any
number of ticks.

The alternative — items as rows, or a done-set beside the body — would put
the state of a task in two places and make every existing invariant
conditional: the exactly-once append guard reads the body; the autosave
protocol (`append-vs-autosave.md`) conditions on the body's ETag; the cleaned
view is regenerated from the body and is never written by the API. A
checklist that is a body with a line format inherits all of that for the
cost of a parser on each side, and `chintanctl export` needs no change
because a task list is Markdown.

Tests: `cleanup/items_test.go`, `cleanup/tasks_test.go`,
`pipeline/checklist_append_test.go`, `provider.TestLiveEval/items`;
`checklist.test.ts`, `checklist.plan.test.ts`, `ChecklistEditor.test.tsx`,
`ChecklistNote.test.tsx`; end to end, `frontend/e2e/checklist.spec.ts`.

History: `docs/backlog.md` (the depth decisions, Split up becoming Tidy up
list, the one Undo rule) and `docs/design/specs/2026-09-30/checklists.md`.
