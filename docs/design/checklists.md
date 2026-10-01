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

**The line rule.** Every reader — the editor (`parseChecklist`, the `ITEM`
pattern in `checklist.ts`) and the backend (`cleanup.ParseLine`, used by
`cleanup.ItemsFromLines` and by every tick, reopen and match in
`pipeline/append.go`) — reads an item line one way: an indent of spaces and
tabs, a tab counting as two spaces; `- [`; a box of a space (open), `x` or
`X` (done); `]`; at most one space; then the text as written. So `- [ ]Milk`
is an item, `\t- [x] Candles` is a done sub-item, and ` - [ ] Plates` (one
space) is top level. Every two columns of indent is one level down, clamped
as below; a trailing `\r` is not part of the text. Any other
non-blank line is prose. The rule is pinned by example in
`backend/internal/cleanup/testdata/checklist-lines.json`, which the Go
suite (`cleanup/items_test.go`) and the Vitest suite (`checklist.test.ts`)
both assert, so a change to one reader that the other does not share fails
a test; its `max_depth` is asserted against both `cleanup.MaxDepth` and
`MAX_DEPTH`, so the two constants cannot drift either. Until R7-19 (2026-09-30) the backend required the space after the
box and read depth only from a two-space prefix, so a line typed `- [ ]Milk`
was an item in the editor and prose to the worker, and its tick never
carried. Three differences are left on purpose, since they change no
line's meaning: the editor keeps an item's text byte for byte (it writes
back on every keystroke) while the Go readers collapse whitespace and skip
an item with no words; the editor shows an indented prose line as a
top-level item, while `ItemsFromLines` reads it as a child, because it also
reads the clean artefact's box-less lines (`cleanup.RenderItems`); and a
tick or reopen in the worker writes the line back in the normal form
(`- [x] ` with its one space).

A sub-item is two spaces of indent under its parent — `  - [ ] Plates` under
`- [ ] Party` — and two more for each level below it, three levels in all:
top, sub-item, sub-sub-item (`MAX_DEPTH = 2` in `checklist.ts`,
`cleanup.MaxDepth` in Go; owner feedback F1, round 8, 2026-09-30, which
replaced CL-D1's one level of 2026-09-27 — that decision named "a real list
asks" as its own trigger, and one did). Not four: a fourth level leaves
about twelve characters of text at 320 px. Both parsers read the indent as
a depth clamped to the item before's plus one and to the maximum — a jump of
two levels reads as one, a fourth level written elsewhere reads as the third
and flattens to it on the first save (clamped, never dropped), a child with
no parent is top level — and write it back as two spaces per level, so a
body indented in another editor round-trips byte for byte. The Go form is
`cleanup.ParseLines`, which every depth read in `pipeline/append.go` goes
through (`depths`). A capture marker or a blank line does not reset its
clamp, since the editor never sees either (the API strips markers and the
editor drops blanks), so a marker between a parent and the sub-item a later
recording merged under it does not cut them apart. A prose line does: the
editor shows it as a top-level item, so the item after it is at most a
sub-item to every reader (the fixture's "prose between items" case).
`ItemsFromLines` differs on one thing only: it reads an *indented* prose
line at its indent's level (it also reads the clean artefact's box-less
lines), where the editor shows it at the top level.
`cleanup.ParseLine` alone returns the indent's raw level. Every body from
before three levels is at depth 0 or 1 and reads exactly as it did; the one
change in meaning is a line indented four or more columns under a sub-item,
now a sub-sub-item (no owner list had one). Until 2026-09-26 such a line
missed the item pattern altogether: it showed as an open row whose text was
the raw syntax and was rewritten to `- [ ]   - [x] Candles` on the first
save. The worker used to append at the end of the body with no indent, so
a filed item was top level by construction; since 2026-09-29 it writes a
group the person spoke — "eggs from Walmart" — as a parent line with its
things two spaces in ("The append rule" below), and Tidy up list writes the
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
now above it (the parser clamps an orphan, so nothing breaks), while a line
that still has one of the person's own lines under it, at any depth, stays
where it is with that line; and the row's
"3 of 7 done" counts every item whatever its depth, as Keep's count does.
Export and the search text see the raw lines, indent and all, which is
Markdown.

## The append rule

When the destination note is a checklist, the recording becomes the items it
named, one open line each — `- [ ] Chickpeas`, `- [ ] Green gram` — grouped
as the person grouped them, and nothing else. The items come from one model
call in place of the transcript cleanup (`Pipeline.extractItems`,
`cleanup.ItemsPrompt`): the prompt reads the **raw** transcript with the
list's title beside it and answers a one-level tree (a list may hold three,
but the extraction makes one — round 8 owner decision 2, default "no", since
over-nesting speech is the risk and no spoken case has asked; turning it on
is the extraction's `maxDepth` and one prompt sentence, gated by
`TestLiveEval/items`),
`{"items":[{"text":"Walmart","children":[{"text":"Eggs"}]},{"text":"Milk"}]}`.
What an item is lives in one rule block, `cleanup.checklistItemRules`,
shared with the tasks prompt behind Tidy up list (`docs/design/prompts.md`): one thing the
person wants, in their own words, language and script, quantity kept; every
word about the list rather than on it left out — "add", "to my list", the
list's own name, also when the recording opens with that name to file it
("Shopping list eggs from Walmart" → Walmart › Eggs) — so "Add milk, eggs
and protein powder to the shopping list" is Milk, Eggs, Protein powder and
never "Add milk" (owner, 2026-09-29); "X and Y" split; **group as the person
grouped** — a place, a person, an occasion or a category the things are
named under is the parent, the things its children, never invented and never
the list's own name; a remove/tick/change request
returned as spoken; garbling fixed and fillers dropped, nothing else
changed, nothing lost. A model that answers the old shape, bare strings,
still parses as flat items; a grandchild is flattened into the children,
after its parent. "One level only: a child has no children" is the items
prompt's own rule, not the shared block's, so Tidy up list can keep a list's
three levels. It runs in the `cleaning` status, under the cleanup
op and deadline, and stores the tree one line per item at `clean_key`, a
child's line two spaces in per level (`cleanup.RenderItems`), so a retry does
not call again.

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
  done, joins that block (the line and every deeper line after it): each
  child that is not already one of the line's **direct** sub-items is added
  after the block's last line, whatever that line's depth — a sub-sub-item
  with a child's words is a different thing in a different group — a
  direct sub-item already there and done is reopened, and a done parent
  that gained or reopened a child is reopened (a parent with an open part
  is not done — the editor's own rule). "chicken from Costco" over a list
  that has Costco › Meat gives Costco › Meat, Chicken, not a second Costco;
- an item **without children** whose words match **any** line is not added
  again: open, it is a duplicate and dropped; done, it is reopened, and so
  is every line it stands under, up to the top (`parentOf` walked to the
  top level), because "add milk" over a ticked Milk means milk is wanted
  again and a done item has no open descendant;
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

**DB6-9 is open** (the owner's): whether a blank line or a capture marker
ends a parent's block for the merge. As it stands they do, which is the rule
from before three levels, and three levels change nothing about it. The
known cost is a duplicate: a list `Costco › Meat`, a blank, then
`<c_2>` with `  - [ ] Rice` (a sub-item of Costco to every reader, a later
recording's paragraph), and a recording of Costco › Rice — the block ends
at the blank, the Rice under the marker is not found, and a second Rice is
added after Meat.

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
rules above and adds the four a whole list needs — every line's meaning is
kept, a line that is already one thing word for word, a line holding
several things one item each, a sentence spoken to the app the things it
named; the groups the list has are kept and an item joins an existing group
when its own words say so ("chicken from Costco" under Costco), two lines
naming one thing are one item; every item keeps its level, up to three, and
no level is added that the list does not have; done stays done, an open line is never
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
(`cleanup.SplitOutput`), because Tidy up list writes it over the body:

- it must parse as items (`ParseItems`' shape, at most 500 counting
  sub-items, nested at most three levels, a deeper item flattened into the
  third after its parent) — else the fixed verdict `the cleanup model returned nothing
  usable` and the previous view is kept;
- an item whose words are not the body's words, in order
  (`llm.VerifySubsequence`; a group's name — Walmart, Party — is a body
  word), is dropped and `TasksItemsDropped` counts it, a dropped parent's
  children lifted to its level; the rest of the answer is stored. This
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
  `- [x] —` blocks nothing. The checks run at every depth;
- **a done item has no open descendant** (DB6-11, settled 2026-09-30 as "the
  parent reopens", the owner's default): a done answer item with an open
  item under it is stored open, so `- [x] Costco` › `- [x] Meat` beside
  `- [ ] chicken from costco` tidies to an open Costco › Meat (done),
  Chicken, whether the model reopened Costco or left it done. The lost-tick
  and reopened checks exempt that only when the list put the open item
  there: its own body line names the group ("chicken from costco" under
  Costco), the body already had it open under that line, or it is itself
  such a reopened group one level down. A group the model invents over an
  open line — `- [x] Milk`, `- [ ] Eggs` answered as Milk › Eggs — still
  loses Milk's tick and is refused. The editor's `toggleItem` and
  the merge keep the same rule.

The stored view is task-list lines, two spaces of indent per level, in
`cleaned_body` as before; `stale` and `auto_clean` are unchanged. The owner
decided on 2026-09-29 to keep Split up and improve it — this is round 6's
half of that (`docs/backlog.md`, "Round 6"); the round-5 proposal to drop it
(PR-D4) was reversed before it merged. Round 8 (F8) kept the mode and moved
it out of a tab into Tidy up list ("Tidy up" below), and turned
`auto_clean` off for a checklist.

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
its slot and every indent step (`--space-6`, 24 px at a 16 px root, read
live) to the right is one level in, to the left one level out. The hook
does not clamp; the editor cuts the levels down to the largest move that
fits — out to the top, in to one under the row shown above, never past the
third level, and no further than the row's own sub-items allow — so one
release can move several levels (50 px right from the top level under a
sub-item is two), and a drag that asks for one level too many moves as far
as it can. A drag is placed by eye; Tab and the arrows, one level a press,
refuse instead. The preview shows what release would do: every open row
whose depth the write would change — the lifted row and the sub-items it
carries — is drawn at that depth (`data-preview-depth`, absolute, read from
the same `shiftLevel` write, one rule per level that outranks `data-depth`),
the lifted row with a 3 px bar in the accent at its start, the indent
animating with the motion tokens and snapping under reduced motion. It
shows nothing when not even one level fits (the refusals under
"Sub-items"). On release the write is the same `shiftLevel`
Tab and the menu make, saved at once and said once, where it ended; a
pointer that came back under a step writes nothing. Until round 8 the
preview was relative (`data-nest-preview` ±1) and its "one out" rule set the
indent to zero, so a third-level row dragged one level out was drawn at the
top level. A drag past
the slop on either axis is no longer a tap, so a wobble on the handle does
not open its menu.
The right and left arrow keys on a focused grip move one level per press,
beside the up and down that move it, and the grip's description says so;
Tab and Shift+Tab in the field are unchanged. The pinned group passes no
`onShift` and its drag has one axis, as before.

### Sub-items

Up to three levels (`MAX_DEPTH`, "Data model"; round 8, F1), and Keep's
keys: Tab in an item's field takes it one level in under the open row shown
above it, Shift+Tab one level out; the grip's menu carries the same two as
"Make a sub-item" and "Move up a level" for a finger and for anyone who does
not know the keys, 44 px each. A row goes at most one level under the row
above it (`maxIn = min(MAX_DEPTH, above.depth + 1)`), so under `Costco ▸
Meat` one Tab on a top-level `Rice` makes it Meat's sibling and a second its
child, as Workflowy's and Keep's single step does. A screen reader hears
where the row went, its words cut at forty characters: "Made a sub-item of
“Costco”", "Moved up a level, under “Party”", or "Now a top-level item";
the row carries `aria-level`, and the field's name is "Item 3", "Sub-item
3" or "Sub-item 3, level 3". The keys are described on the field itself
(`aria-describedby`), where they act, since a reader in the field never
hears the grip's description. A sub-item is set in by one spacing step per
level (`data-depth`, `--space-6`) with the same drawn box and grip after it;
the indent alone says "part of the row above". There is no narrower step on
a small phone: at 320 px the third level still leaves 118 px of text, which
wraps downward with its box on the first line.

An indent is refused when there is no row above (the first open item can
never be a sub-item), when the row is already as deep as the row above
allows, when the row above or anything it stands under is done, or when the
row's own sub-items would go past the third level — indenting is an
explicit act about levels, and quietly flattening a grandchild is never
what it means. From the grip each is said with a fixed sentence ("Nothing
above to nest under", "Already a sub-item", "Already three levels deep",
"Cannot nest under a done item", "Its sub-items are already three levels
deep", and "Already a top-level item" for a step out), and the menu's item
is disabled; a sideways drag is refused only when not even one level
fits. From the field the Tab is left to the browser, so focus moves
on and the list is never a keyboard trap.

The row above is the one a person sees, not the body's previous line
(`shiftLevel`'s `above`): with `Milk`, `[x] Eggs`, `Bread` in the body the open rows
are Milk and Bread, and Tab on Bread makes it Milk's sub-item, moving its
line above Eggs's — a done line's place shows nowhere, so nothing visible
moves, and ticking Milk then takes Bread with it as the eye expects. Nesting
under the previous body line instead would have made Bread the sub-item of a
row sitting in Done: indented under Milk on screen, orphaned when Milk was
ticked, and pulled under Eggs when Eggs was reopened. Up a level is in place,
and the sub-items that followed under the same parent become the row's own,
as an outliner does. A parent made a sub-item takes its children one level
down with it, and an indent that would push one of them past the third
level is refused (`canNest`); a parent dropped on a sub-item's slot is
clamped instead, since a drop is placed by eye. `shiftLevel` is the one
function for a change of level, and the editor reads every path through
one planner (`plan`): Tab, →/← on the grip, the menu, the sideways drag and
its preview ("Editing the list" above), so every path nests the same way,
refuses the same rows, and previews what it writes.

Ticking a parent ticks its sub-items — the parent is the whole job, and a
finished job has no open parts — and the whole block is held for the tick's
beat and moves to Done together, its depth kept, so a finished parent reads
as a block there too. Reopening a sub-item reopens every item it stands
under, because a parent with an open part is not done. Reopening a parent leaves its
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
surprise). The worker and Tidy up list write the person's own groups as
sub-items ("The append rule"); export and the search text are unchanged.

Done items keep their line where it stands; the Done section is the view's
grouping, not the body's. It is a disclosure — the `<h2>` holds a button
with `aria-expanded` — remembered per note for the session in
`sessionStorage` (`chintan.checklist-done.<id>`, open by default, the
NoteTabs pattern), with Uncheck all and Delete done beside it. Uncheck all
flips every `[x]` to `[ ]` in place (`uncheckAll`). Delete done drops every
done line, and a done parent's sub-items with it (`removeDone`), and offers
Undo in the shell's toast for six seconds — no typed word and no dialog
(OF-DEL): Undo writes the previous body back and saves, unless the list
changed since (see Tidy up). A tick, which moves the row out of sight
into Done, offers an Undo for four seconds as "<item> done" (R7-13), the
item's words cut at forty characters: Undo writes the body from before the
tick back, so the row returns to its place, and only while the body is
still exactly the tick's — after any later act, the person's own included,
it says "The list changed since — nothing undone." rather than undo that
act too. So only the last tick is undoable. Reopening a done row offers
none, and the status line still says "Marked done". The tick's toast is
weak (`ToastNotice.weak`): it never takes the place of a standing Delete
done or Tidy Undo, so a tick right after Delete done leaves that Undo
standing, and the tick is said by the status line alone. Done rows have no grip, because their order
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
lift the row, so the grip is where every pointer lifts; nesting past three
levels (round 8, F1, which replaced CL-D1's one level) — a fourth leaves
about twelve characters of text at 320 px and wants an outline UI
(collapse, guide lines per level), and no list has asked for it. The drag-right gesture to nest was on this list in round 5 (Tab and
the menu cover keyboard and finger; a horizontal threshold on a vertical
drag is a second gesture to learn) and came off it on 2026-09-29 at the
owner's asking: a spoken list is nested with a thumb, on a phone, where Tab
does not exist and the menu is two taps away — so the handle that already
moves a row moves it sideways too, with the axis decided in the first 10 px
and a preview before anything is written ("Editing the list" above).

### Tidy up

A checklist has two tabs, Items · Recordings (R8-F8, 2026-09-30, superseding
R6-CL-D1 a). The Split up tab — the worker's `tasks` proposal drawn in a
second Items editor, whose first act replaced the body — is gone, and with
it `adoptedSplits`, the `inert` stale rows, the caption and Use this list. A
remembered or linked `?tab=cleaned` on a checklist lands on Items.

In its place the ⋮ menu has **Tidy up list** (after Pin; only for a
checklist with an open item; off offline and while one runs, when it reads
"Tidying…"). It calls the same `POST /v1/notes/{id}/clean`, where the
server picks `tasks` for a checklist, and keeps the request in module state
(`useTidyList.ts`: the view as it was, and when), so it survives a tab
switch and leaving the note within the session. "Tidying the list…" stands
above the rows, which stay editable. The note is polled on the
`cleanPollInterval` ladder by a timer of its own — TanStack restarts an
observer's `refetchInterval` on every update to the query, and a filing
recording's poll rewrites the note every 1.5 s — until `cleanSettled`, then
one of four things happens:

- **Fresh** (`!stale`, the editor's body equal to the server's, mode
  `tasks`): the view is written as the body and saved, and the toast says
  "List tidied: N lines → M items." with Undo for six seconds (OF-DEL). Undo
  writes the list back, under the `UNDO_STALE` rule: it refuses when the
  body is no longer the tidied one ("The list changed since — nothing
  undone."), read from the note editor's own mirror (`NoteEditor.current`)
  because the toast outlives any panel.
- **The same** (equal to the body, trailing whitespace aside): "Already
  tidy." and no write.
- **Changed meanwhile** (stale, or a draft that differs): "The list changed
  while tidying — nothing replaced." with Tidy again.
- **Failed** (`cleaned_error`, a refused request, or a minute with no
  answer): "Couldn't tidy the list." with Try again.

There is no preview before it lands; the Undo is the preview, and
`SplitOutput`'s guards (below) refuse an answer that drops, invents or
re-ticks an item. The result is applied only by the device that asked, and
only while the list is unchanged; elsewhere it sits unused in
`cleaned_body`.

**Prose to list.** The Details switch PATCHes `kind: checklist` with the
`proseToChecklist` body exactly as before, so no word is lost; once that
save has landed, and if any item has two or more words, it starts a tidy.
Its toast reads "Made a checklist: N paragraphs → M items.", and Undo gives
back the paragraph items. A list turned back into prose before the answer
lands drops it.

**No auto-clean for a checklist.** `auto_clean` is ignored for a checklist
— stored, not refused, so an old row is harmless — in both places the
worker regenerates a view of its own accord (`pipeline.cleanNoteAfter`,
after an append or a regeneration, and `service.autoCleanAfterBodyWrite`,
after a recording is moved or deleted): its items are split at capture
(`extractItems`), so a view per recording was a model call for a proposal
nobody adopted.

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
