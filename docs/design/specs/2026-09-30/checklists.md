# R8 spec: checklists (F1 deeper sub-items, F8 the Split up tab)

Design only. Read against main `3ac3b2c`. Mockups: `mock-checklists-depth.svg` (three levels at 360 px, and the sideways-drag preview) and `mock-checklists-tidy.svg` (F8: the menu action and its Undo).

## Real usage (owner tenant, prod, read-only on orb; counts only, no content)

- Note rows: 32 in total. 6 have ever been checklists (1 live, 5 deleted). 26 have been plain notes (7 live, 19 deleted).
- Depth: no checklist body has ever held a line deeper than one level. That is expected, since the editor clamps. One of the six lists used sub-items (2 of its 4 lines).
- Split up: 5 of the 6 checklists have a `tasks` view (`cleaned_mode=tasks`), and 2 of those had "Keep it updated" on. All 5 are now deleted. The only live checklist has 5 top-level items, no Split up view, and auto-clean off. It was built after round 6 (29 Sept), when items started being extracted at capture.
- In 2 of the 5 views, Split up changed the shape. One turned a one-line sentence into 3 items (the "Add milk, eggs…" case from before round 6). The other turned a 4-line list with 2 sub-items into 10 flat items, so it lost the person's nesting (that was before round 6, when the parser could only read flat lines).
- `op_clean_note_calls` sums to 60 across the usage rows. The rows hold more than one granularity, so the real number is lower. It covers plain-note `structured` cleans as well.

What this shows: since round 6, every checklist item made by voice is split at capture time. The only thing Split up still adds is splitting text that was never split: a converted prose note, a typed sentence, or a pre-round-6 list.

---

## F1: more than one level of sub-items

### Today (with code refs)

- **Decision CL-D1** (owner, 2026-09-27, "Keep parity"; `docs/backlog.md:480`, `checklists.md:61-65, 510-518`, round-6-proposals §5 "Deliberately not built: depth two"). The reasons given were:
  - a spoken list is a handful of items;
  - depth wants an outline UI;
  - depth makes the snippet count and the `tasks` view harder to reason about;
  - "a third level is one constant plus CSS if a real list asks".

  The owner is now asking for that third level, so this spec overrides CL-D1. The new feedback (F1) is the "real list asks" that CL-D1 itself named as the trigger.
- **Frontend.**
  - `checklist.ts:69` sets `MAX_DEPTH = 1`.
  - `parseChecklist` sets depth to `min(indent/2, prevDepth+1, MAX_DEPTH)`. `serialiseChecklist` writes two spaces per level.
  - `canNest` / `nestUnder` / `unnest` / `moveItem` / `removeItem` / `toggleItem` / `removeDone` / `blockOf` / `parentOf` are all written relative to depth (`>`/`<`), with `MAX_DEPTH` clamps. Two places assume one level:
    - `nestUnder` jumps straight to `under.depth+1`;
    - `toggleItem` reopens only the immediate parent.
- **Backend.**
  - `cleanup.ParseLine` (`items.go:299`) gives `depth = 1` for any indent of 2 columns or more. It is context-free.
  - `ItemsFromLines` builds a one-level tree. `ParseItems` clamps grandchildren into children. `RenderItems` and `RenderTaskList` emit only `""` or `"  "`.
  - `pipeline/append.go` hard-codes one level:
    - `depth == 1` in `mergeLeaf:687`;
    - `depth == 0` / `!= 1` in `mergeParent:703,716`;
    - `replaceChecklistItems:531,539,543`;
    - `parentOf:792` ("the top-level line above");
    - `checklistItems:605` (`"  "` only).
- **Tabs after R7-19.** A tab counts as two columns, so one tab is one level today, and a deeper indent reads as level 1. This is pinned by `testdata/checklist-lines.json`, case "a deeper indent is one level", and asserted by `items_test.go:198` and `checklist.test.ts`.
- **Prompts.** `checklistItemRules` (`items.go:80`), shared by both prompts, says "One level only: a child has no children". The tasks prompt (`prompt.go:87`) says "a sub-item indented two spaces under its parent".

### Options

| | What | Trade-off |
|---|---|---|
| A | Unlimited in storage, capped only in the editor's display | Untouched lines would have to round-trip their real depth while the editor shows a clamped one, so it needs two depths per item. Every pure edit gets harder to reason about, and the Go readers would need the same split. More code, no use case. |
| B | **One `MAX_DEPTH = 2` (three levels), the same in storage, the editor and both prompt parsers** | One constant on each side, pinned by the shared fixture. A deeper body read from elsewhere is clamped (no line lost) and flattens to level 3 on its first save, the same rule as today, one level further down. |
| C | `MAX_DEPTH = 3` (four levels) | Depth 3 leaves a 134 px text column at 360 px and 94 px at 320 px (about 12 characters), so most items wrap onto three lines. It needs a narrow-screen indent step and a cap on the visual indent. No list has asked for it. |

**Recommendation: B. Three levels (top, sub-item, sub-sub-item) everywhere. Owner decision: "3" means three levels (`MAX_DEPTH = 2`), with C as the alternative.**

### Width budget (verified in `mock-checklists-depth.svg`)

The row is a 44 px grip, an 8 px gap, a 44 px box, an 8 px gap, then the text (`checklist.css:216, 57`). The card sits inside a 20 px gutter with 4 px of padding and a 1 px border. Indent is `--space-6` (24 px) per level.

| Viewport | depth 0 | depth 1 | depth 2 |
|---|---|---|---|
| 360 px | 206 px | 182 px | 158 px (about 20 characters) |
| 320 px | 166 px | 142 px | 118 px (about 15 characters) |

Text wraps downward, and the box stays on the first line (`align-items:flex-start`). So there is **no separate visual cap and no narrow-screen step**: the cap is `MAX_DEPTH`, and after a parse nothing deeper exists. *Skipped: a smaller indent below 360 px. Add a `--checklist-indent` custom property, read by `levelPx()`, if a 320 px phone shows up.*

### GFM representation

- **On the way out:** two spaces per level, `"  ".repeat(depth)`. CommonMark nests a `- ` item at 2 or more columns past its parent's marker, so GitHub renders the same tree.
- **On the way in:** width is spaces plus 2 per tab, and depth is `floor(width/2)`. It is then clamped to `previousItemDepth + 1` and to `MAX_DEPTH`. So `\t\t- [ ] x` is level 3, and a jump from 0 to 3 reads as 1.
- **Non-item lines.** Blank, marker and prose lines do not reset the clamp in the Go per-body reader. That matches the editor, which never sees markers. This settles the depth half of DB6-9 as (b) without touching the merge's insertion rule, which still stops at a marker.

### Ticks (DB6-11 settled)

One invariant, at every depth: **a done item has no open descendant.**

- **Tick** an item and its whole block (every descendant) is ticked. This is unchanged and works at any depth because `blockOf` already uses `>`.
- **Reopen** an item and **every ancestor** is reopened: walk `parentOf` until reaching the top level. Today only the immediate parent is reopened.
- **Reopen a parent** and its descendants stay as they are. CL-D2's stated choice is unchanged.
- **The append merge** follows the same rule. `mergeLeaf` reopens all ancestors. `mergeParent` reopens the matched parent if it gained or reopened a child (as today), and its ancestors if that parent is nested (only reachable through Tidy, below).
- **DB6-11 (owner item, open since 29 Sept) is settled as (a), "the parent reopens".** `SplitOutput` turns a done answer item that has an open descendant into an open one. Its "lost tick" and "reopened" guards exempt exactly that case: a done body line whose words match an open answer item that has an open descendant. Pin both of DB6-11's bodies in `tasks_test.go`. This is marked as an owner decision because the morning queue lists it. The default is (a), since the invariant leaves no other consistent answer.

### Editor gestures (exact)

Throughout, "above" means **the open row shown directly above** (as today, `nest` in `ChecklistEditor.tsx:261`), and *d* is the row's depth.

- **The highest a row may go:** `maxIn = min(MAX_DEPTH, above.depth + 1)`. A row can be at most one level under the row it follows.
- **The first open row is never indentable.** It has no row above. This is unchanged.
- **Refused nest.** A nest is refused when any of these holds:
  - there is no row above;
  - *d* is already `maxIn`;
  - the new parent or any of its ancestors is done;
  - **the row's block would push a descendant past `MAX_DEPTH`.**

  The last refusal is new. Indenting is an explicit act about levels, and quietly flattening a grandchild is never what "indent" means. The refusal is announced from the grip as "Its sub-items are already three levels deep". From the field, focus moves on as today.
- **Tab** (field) is one level in, so *d* goes to *d+1*. Its new parent is the nearest open row above at depth *d*. For example, with `Costco ▸ Meat` followed by `Rice` at depth 0, Tab on Rice makes it Meat's sibling under Costco, not Meat's child. That matches Workflowy and Keep's single step.
- **Shift+Tab** is one level out, in place. The siblings that followed under the same parent become the row's own children. This is the outliner rule, and `unnest` already does it at any depth.
- **Grip →/←** do the same as Tab and Shift+Tab, one level per press.
- **Grip menu.** "Make a sub-item" (one in) and "Move up a level" (one out) keep their names and their disabled rules.
- **Sideways drag on the grip.**
  - The first 10 px decide the axis, as today (`useDragReorder.ts:57`).
  - Then `levels = trunc(dx / levelPx)` with `levelPx = --space-6`, which is 24 px at a 16 px root and is read live (DB6-32).
  - That is **clamped to [−d, maxIn − d]**, so one release can move several levels. For example, dragging 50 px right from depth 0 under a depth-1 row gives +2. Today the clamp is ±1 (`useDragReorder.ts:227`).
  - Release writes once through `shiftLevel(body, index, by, above)` (below).
  - Coming back under 24 px writes nothing. There is no hysteresis, as today.
  - The hook's `Levels` type widens to a number. The editor clamps it, because the hook does not know depths.
- **Preview.**
  - The lifted row gets `data-preview-depth={target}`, which is absolute, replacing the relative `data-nest-preview`.
  - CSS rules for 0, 1 and 2 sit under `[data-dragging]` and outrank `[data-depth]`. The row keeps the 3 px `--color-accent` bar.
  - There is no preview when `target === d` or the move would be refused.
  - Indent changes animate with the existing `padding-inline-start` transition (motion tokens). Under `prefers-reduced-motion` they snap.
- **Moves (reorder).** A parent's whole block moves (`blockOf`). The block takes the depth of the slot it lands in, and descendants are clamped to [0, `MAX_DEPTH`], as `moveItem` does today. A drop is placed by eye, so the move clamps instead of refusing. "Now a sub-item" or "Now a top-level item" stays on the status line.
- **Delete a parent** (Backspace in its emptied field, the menu, or the × under Done). Every descendant comes up one level, so the structure under it is kept. `removeItem` already does this.
- **Delete done.** A done item takes its whole block (`removeDone`, already depth-generic).
- **Enter after a parent** starts its first child at *d+1* (`insertItemAfter`, already generic).

**Pure-function changes in `checklist.ts`:**

- `MAX_DEPTH = 2`.
- Replace `nestUnder` with `shiftLevel(body, index, by, above)`:
  - when `by > 0`: target `t = min(d+by, maxIn)`; refuse if `t ≤ d` or `max(block depth) + (t−d) > MAX_DEPTH`; set `delta = t−d`; move the block to just after `above`'s block, as today (done lines slip below);
  - when `by < 0`: run `unnest` |by| times, stopping at 0.
- `canNest(items, index, above, by = 1)` becomes the predicate for that.
- `toggleItem` reopens every ancestor.
- Keep `nestUnder` / `unnest` as thin wrappers only if tests want them. Otherwise delete `nestUnder`.

### Screen readers

- **The row** `<li>` gets `aria-level={depth+1}`. ARIA 1.2 allows it on `listitem`.
- **The field's label:**
  - "Item 3" at depth 0;
  - "Sub-item 3" at depth 1;
  - "Sub-item 3, level 3" at depth 2.
- **What is announced** (the existing polite region), cut at 40 characters like the tick toast:
  - going in: "Made a sub-item of “Costco”";
  - going out: "Moved up a level, under “Party”", or "Now a top-level item" at depth 0.
- **One release, several levels:** the final position is announced once.
- **Refusals** are announced from the grip only, as today (DB6-22).
- **Field hint** (`ChecklistEditor.tsx:500`): unchanged. **Grip hint:** "right and left arrows change its level" (unchanged).

### Rendering

- `checklist.css:48`: `[data-depth='1']` stays at `--space-6`. Add `[data-depth='2'] { padding-inline-start: calc(2 * var(--space-6)); }`.
- Done rows keep their depth, as today.
- Tokens only. There are no new colours, so both themes inherit everything.
- No guide lines (minimal UI). *Add them only if three levels prove hard to read.*

### Backend

**`cleanup/items.go`**

- Add `const MaxDepth = 2` (the TS twin is `MAX_DEPTH`).
- `ParseLine` returns the **raw** level, `width/2`. The comment changes from "1 for two or more" to "the indent's level, unclamped".
- Add `ParseLines(lines []string) []Line` with `Line{Text string; Done bool; Depth int; OK bool}`. This is the per-body reader, with depth clamped to previous item+1 and to `MaxDepth`. Non-items get `OK=false` and do not reset the clamp. It is the Go form of `parseChecklist`, and every `pipeline/append.go` reader uses it instead of per-line depth.
- `ItemsFromLines` builds the tree with a stack, using the clamped depth. A box-less artefact or prose line's depth comes from its indent, clamped the same way. The intentional difference (the editor reads indented prose as top level) is kept.
- `RenderItems` and `RenderTaskList` recurse with `"  "` × depth.
- `parseItems(raw, limit, maxDepth)`:
  - the extraction passes **1**: `ItemsPrompt` stays at one level (see Prompts);
  - Tidy passes `MaxDepth`;
  - anything deeper than `maxDepth` is flattened into the deepest allowed level, after its parent, which generalises today's grandchild clamp;
  - `countItems`, `flatten` and `itemText` are unchanged.

**`pipeline/append.go`**

- `parseChecklistLine` keeps its folded words but takes the depth from `cleanup.ParseLines(lines)`. Add a helper `depths(lines) []int`. Because `mergeParent` inserts lines, it is recomputed after each insert. *`ponytail:` O(n²) over a list of a few hundred lines; cache it if a list grows past that.*
- `parentOf(lines, i)`: the nearest item line above with **depth < depth(i)**.
- `mergeLeaf`: on reopen, walk `parentOf` up to the top and reopen each done ancestor.
- `mergeParent`:
  - still matches a **top-level** line, because extraction parents are top level;
  - the block is the following lines with depth > 0 (currently `== 1`), stopping at a marker, blank or prose (insertion rule unchanged);
  - a child's existing line is searched for **among the parent's direct children** (depth == parent+1). A grandchild with the same words is a different thing in a different group;
  - new children are inserted at the block's end at depth 1.
- `replaceChecklistItems:531-546`: `depth == 0` stays (a top-level line). `depth != 1` becomes `depth == 0` (the block runs while depth > 0). `parentTaken` uses the new `parentOf`. The shared-parent rule ("a taken top-level line with a sub-item left under it") checks every descendant.
- `checklistItems`: keep any even run of leading spaces, `min(len/2, MaxDepth)` levels, as `"  "` × level.
- `keepTick` and `withBox`: no change. They are depth-blind and keep the indent.

**`cleanup/prompt.go`**

- `SplitOutput`:
  - recursive keep/drop: a dropped item's children take its place at its level;
  - recursive `check`: `parent` means "has children";
  - the tick-safety maps are already built over `flatten`, so they work at any depth;
  - add the DB6-11 normalisation and exemption;
  - render through the recursive `RenderTaskList`.
  - `MaxChecklistItems = 500` is unchanged.

**Prompts**

- Move "One level only: a child has no children" **out of** `checklistItemRules` and into the `itemsSystemPrompt` wrapper (worded the same). Extraction behaviour does not change, and the round-6 15/15 battery stays valid.
- The `noteTasksSystemPrompt` header becomes: "…a sub-item indented two spaces under its parent, two more for each level, at most three levels."
- Add one rule to the tasks prompt: "Keep every item at the level it has, up to three levels; put an item under an existing group or sub-group when its own words say so; never add a level the list does not have." The JSON shape is already recursive (`children` of children).
- **Owner decision:** should extraction itself produce depth 2 ("for the party, from Costco plates and cups" → Party ▸ Costco ▸ Plates)? **Default: no.** Over-nesting is the risk, no spoken case has asked for it, and the parser is already able to accept it later by changing the extraction's `maxDepth` to 2 and one prompt sentence, gated by `TestLiveEval/items`.

**Never lose items.** No reader drops a line because of its depth: deeper lines are clamped, never removed. Every existing body is at depth 0 or 1 (the tenant read above), and those parse identically. The only change in what an existing body means is that a line with 4 or more columns under a level-2 line now reads as level 3. No owner body has one.

### Tests

**Shared fixture `backend/internal/cleanup/testdata/checklist-lines.json`**

- Add a top-level `"max_depth": 2`. Both suites assert it equals their constant. This is the drift guard for `MAX_DEPTH` / `MaxDepth`.
- Change "a deeper indent is one level" to "four spaces is the third level": `Party/Plates/Paper ones` → depths 0, 1, 2.
- Add cases:
  - "a jump of two levels reads as one": `- [ ] A\n    - [ ] B` → 0, 1;
  - "past the maximum reads as the maximum": a 6-space line → 0, 1, 2, 2;
  - "two tabs are two levels": `\t\t` → 2 under a level-1 line;
  - "a shallower line after a deep one": 0, 2, 1 → 0, 1, 1;
  - "Indic at depth 2".

**Go (`items_test.go`)**

- `ParseLines` and `ItemsFromLines` match the fixture, flattening the tree with depth.
- `RenderItems` ↔ `ItemsFromLines` round-trip at three levels.
- `parseItems` with maxDepth 1 flattens a 3-level reply in order. With maxDepth 2 it keeps it, and flattens a 4-level reply into level 3.

**Go (`tasks_test.go`)**

- A 3-level body tidied unchanged is accepted.
- A dropped level-2 parent lifts its children to level 2.
- Both DB6-11 bodies: `- [x] Costco\n  - [x] Meat\n- [ ] chicken from costco` with the answer Costco (open) ▸ Meat (done), Chicken (open) is accepted. With the answer Costco (done) ▸ …, Chicken (open), the stored list has Costco open.
- A lost tick on a level-3 line is refused.

**Go (`pipeline/checklist_append_test.go`)**

- `TestMergeLeafReopensEveryAncestor`: Milk at level 3 under done Party ▸ done Costco.
- `TestMergeParentMatchesDirectChildrenOnly`.
- `TestParentOfAtDepthTwo`.
- `TestChecklistItemsKeepsTwoLevelsOfIndent`.
- `TestReplaceChecklistItemsKeepsAThreeLevelBlock`: regenerating a recording whose parent holds a level-3 line typed by hand.
- The twelve existing merge cases stay green unchanged.

**Vitest (`checklist.test.ts`)**

- The fixture, via its existing reader.
- `shiftLevel` (+1 from depth 0 under a depth-1 row lands at depth 1 as a sibling; +2 lands at depth 2 as a child), each refusal (first row, `maxIn`, a done ancestor, a descendant past the max), and −2 in place with adoption.
- `toggleItem`: reopening a level-3 item reopens both ancestors; ticking the top ticks all three levels.
- `moveItem` clamps a 3-level block dropped on a level-2 slot.
- `removeItem` lifts a 3-level block one level.

**`ChecklistEditor.test.tsx`**

- Tab twice walks 0 → 1 → 2. A third Tab is left to the browser, so focus moves.
- A grip drag 50 px right under a level-2 row writes depth 2 once and announces "Made a sub-item of …".
- 30 px left from depth 2 writes depth 1.
- 20 px writes nothing.
- The preview carries `data-preview-depth`.
- The grip's →/← each move one level.
- The field's label at depth 2 is "Sub-item N, level 3", and `aria-level` is 3.

**e2e `checklist.spec.ts`**

- Touch-drag (Pixel `hasTouch`) two levels in, survives a reload, `data-depth='2'`.
- A vertical drag still reorders a 3-level block intact.

**Live eval gate** (owner key)

- `TestLiveEval/(items|tasks) -count=3`: items must be unchanged.
- Add a tasks fixture with a 3-level body that should come back unchanged.

### Risks

- The DB6-8 / DB6-9 regenerate paths in `replaceChecklistItems` are the subtlest code here. Keep the twelve merge cases and the DB6-8 test as the gate, and land them before any UI.
- Tidy over a 3-level list could flatten it if the model ignores the level rule. The guards keep every tick, but not the shape. Mitigations: the tasks eval fixture, and Undo.
- The hook's levels widen past ±1. The pinned group passes no `onShift`, so it is unaffected.

---

## F8: the Split up tab

### Today

- For a checklist, the Cleaned tab is labelled "Split up" (`NoteDetailScreen.tsx:407`). It renders `ChecklistEditor` over the worker's `tasks` view (`CleanedPanel.tsx:48-170`).
- The first act there, or "Use this list", writes the proposal over the body, with a 6 s Undo. This is `adoptedSplits` module state, with `inert` while the view is stale or pending (R6-CL-D1 (a), owner, 29 Sept).
- "Keep it updated" (auto_clean) regenerates the view after each append (`clean_note.go:364`).
- A plain note becomes a checklist through the Details switch (`NoteDrawer.tsx:122-133`). That conversion is deterministic: `proseToChecklist` makes each paragraph one item, so a dictated paragraph is one long item until Split up is used.

### The problem

- Since round 6, every recording filed into a checklist is already split and grouped at capture (`extractItems`), so a proposal over a list that has only been spoken to is almost always the list itself.
- The tab is a second editable copy of the same list. The first keystroke there silently replaces the real one, which needs a caption, an Undo and a stale state.
- "Keep it updated" spends a model call after every recording on a view nobody adopts.
- Usage confirms it: the one live list has never generated a view.
- What is still worth keeping is the model's split for text that was never split (converted prose, a typed sentence, an old list), and its grouping.

### Options

| | What | Trade-off |
|---|---|---|
| 1 | Remove it entirely | Simplest. Loses the only model path for prose to items, which the brief says to keep. |
| 2 | Keep the tab only for checklists that came from prose | The tab and its proposal state stay. Which notes count as "from prose" is guesswork after the first edit. |
| 3 | Repurpose it as an "Organise" tab | Same two-copies problem. The tasks prompt already joins groups and merges duplicates, so "Organise" is a new label on what it already does. |
| 4 | **Fold it into one menu action, "Tidy up list", that writes the body with Undo, run automatically when a prose note becomes a checklist** | Removes the tab, `adoptedSplits`, `inert`, the stale notice, "Use this list" and checklist auto-clean. Keeps the model split, and its grouping ("Organise") through the same prompt. You no longer see the result before it lands; a 6 s Undo replaces the preview (the OF-DEL pattern the owner approved). |

**Recommendation: 4.** This overrides R6-CL-D1 (a). In round 6 the owner kept the tab while the per-recording split was unproven, and the round-6 proposal itself said "(b) is a deletion to schedule later". F8 is that review. The round-6 objection to (b) was a body rewritten under the finger by auto-clean. That does not apply here, because auto-clean is removed for checklists and Tidy runs only when asked.

### Interaction (exact)

**Checklist tabs.** The tabs become **Items · Recordings**. A remembered or URL tab of `cleaned` on a checklist falls back to `text` (`useNoteTab`). A plain note keeps Text · Cleaned · Recordings. Swiping between two tabs works as it does (F4's spec owns the animation).

**⋮ menu on a checklist** (`NoteActions.tsx`)

- Add **"Tidy up list"** after Pin. Owner decision on the label; the alternatives are "Split up" (the familiar word) and "Organise".
- While a tidy runs, the item reads "Tidying…" and is disabled. Offline it is disabled too, like Regenerate.
- It is not shown when the list has no open items.

**Running a tidy**

- Tidy calls the existing `POST /v1/notes/{id}/clean` (the server picks `tasks` for a checklist; no wire change).
- It stores `pendingTidy.set(note.id, { requestedAt, body })`. This is module state, so it survives tab switches and leaving the note within the session.
- A polite status line above the rows says "Tidying the list…". Rows stay editable.
- When the note refetches with `cleaned.generated_at ≥ requestedAt` (the existing poll in `useRegenerateCleaned`), one of four things happens:
  - **The view is fresh.** `!cleaned.stale` and the editor's `current().body` equals the server body. The editor writes `cleaned.body` as the body (`editor.edit` + `saveNow`) and shows the toast "List tidied: N lines → M items." with **Undo** for 6 s. Undo writes the previous body back, under the existing `UNDO_STALE` rule ("The list changed since — nothing undone.").
  - **It came back the same** (byte-equal to the body). The toast says "Already tidy." There is no write.
  - **The list changed meanwhile** (the view is stale, or the draft differs). The toast says "The list changed while tidying — nothing replaced." with the action "Tidy again".
  - **The tidy failed** (`cleaned_error`). The toast says "Couldn't tidy the list." with "Try again".
- The pending entry is deleted in every case.
- Screen readers hear the status line and the toast through the shell's existing live region.

**Prose to list** (Details → Checklist switch on)

1. One PATCH, `kind: checklist` plus the `proseToChecklist` body, exactly as today, so no word is ever lost.
2. Then, if any item has 2 or more words, the editor starts a tidy. The toast reads "Made a checklist: N paragraphs → M items." with Undo, and Undo restores the paragraph items.
3. If the person edits before the tidy lands, the "changed meanwhile" toast offers Tidy again.

Owner decision: auto-tidy on conversion. **Default: yes.** "No" means the person taps Tidy themselves.

**Checklist to plain:** unchanged.

### Changes

**Frontend**

- `NoteDetailScreen.tsx`: the checklist tab list, and the `cleaned` fallback.
- `CleanedPanel.tsx`: delete the checklist branch, `adoptedSplits`, `adopt`, the "Use this list" caption and `inert`. The panel becomes plain-only.
- New `features/notes/useTidyList.ts`: the pending map, the effect that applies the result, the toasts. It reuses `useRegenerateCleaned`.
- `NoteActions.tsx`: the menu item.
- `NoteDrawer.tsx`: the switch chains a tidy.
- `ChecklistEditor.tsx`: the status-line prop, since its `currentBody` / `onChange` / `onSave` props are the existing seam.
- `cleaned.ts`: drop the `tasks: 'Split up'` label.
- `styles/checklist.css`: delete the `.cleaned__body[inert]` rules (`:378-395`). `styles/cleaned.css`: delete the Split up notes.

**Backend (S)**

- `pipeline/clean_note.go`: `autoCleanAfterAppend` and `cleanNoteAfter` skip `kind == checklist`.
- `service/notes.go`: `auto_clean` is ignored for a checklist. It is not refused, so an old row is harmless.
- `tasks` mode, `TasksPrompt`, `SplitOutput` and `cleaned_body` all stay, as the transport for Tidy.

**Docs**

- `checklists.md`: the "Split up" section becomes "Tidy up". Note the `tasks` mode's apply path and auto-clean.
- `prompts.md`: the tasks row.
- `backlog.md`: R8 rows, plus R6-CL-D1 marked superseded.
- `openapi.yaml`: the `auto_clean` description ("ignored for a checklist").

**Tests**

- `CleanedPanel.test.tsx`: delete the checklist cases.
- `NoteDetailScreen.test.tsx`: a checklist shows two tabs, and `?tab=cleaned` lands on Items.
- New `useTidyList.test.tsx`, covering:
  - fresh view → one PATCH and the Undo toast;
  - Undo → the previous body;
  - stale → no PATCH and the "changed" toast;
  - the same body → "Already tidy" with no PATCH;
  - an error → the retry toast;
  - pending survives an unmount and remount.
- `ChecklistNote.test.tsx`: the switch converts, then tidies.
- `e2e/checklist.spec.ts`: replace the Split up test with menu → Tidy → rows replaced → Undo restores.
- Go `clean_note_test.go`: no auto-clean after an append to a checklist with `auto_clean=true`.

### Risks

- **No preview.** Mitigation: the Undo, and `SplitOutput`'s guards (nothing invented, no tick lost or invented).
- **Several devices** (or a second tab): the result is applied only by the device that requested it, and only while the body is unchanged. Elsewhere the result sits unused in `cleaned_body`, which is harmless.
- **Tab state.** A deep link or a remembered tab of `cleaned` on a checklist must not land on an empty panel. This is covered by the fallback test.

---

## Owner decisions (recommended default first)

1. **F1 depth:** three levels, `MAX_DEPTH = 2`. The alternative is four.
2. **F1 extraction depth:** voice extraction stays one level, and only editing and Tidy go deeper. The alternative is letting the items prompt nest two levels, gated by the live eval.
3. **DB6-11:** a done parent that gains an open child reopens, at every depth, in both Tidy and the merge.
4. **F8:** remove the Split up tab, and add "Tidy up list" to the ⋮ menu, applied with a 6 s Undo. This supersedes R6-CL-D1 (a).
5. **F8 label:** "Tidy up list". The alternatives are "Split up" and "Organise".
6. **F8:** converting a plain note to a checklist tidies it automatically, with Undo.

## Streams

| # | Stream | Kind | Files | Effort |
|---|---|---|---|---|
| A | Depth in both parsers, the shared fixture, the merge and SplitOutput (DB6-11), the prompts' level rules | be (+ `checklist.ts` pure functions, since the shared fixture must flip both suites in one PR) | `backend/internal/cleanup/{items.go,items_test.go,prompt.go,tasks_test.go,testdata/checklist-lines.json}`, `backend/internal/pipeline/{append.go,checklist_append_test.go}`, `backend/internal/provider/{fake/fake.go,testdata/eval/fixtures.json}`, `frontend/src/features/notes/{checklist.ts,checklist.test.ts}` | M |
| B | Editor UI for three levels: Tab, multi-level drag, preview, a11y, CSS | fe | `frontend/src/features/notes/{ChecklistEditor.tsx,ChecklistRow.tsx,ChecklistEditor.test.tsx}`, `frontend/src/hooks/useDragReorder.ts`, `frontend/src/styles/checklist.css` (depth rules), `frontend/e2e/checklist.spec.ts` | M |
| C | Split up tab removed, Tidy up action, auto-tidy on conversion, no checklist auto-clean | fe + be (S) | `frontend/src/features/notes/{NoteDetailScreen.tsx,CleanedPanel.tsx,useTidyList.ts,NoteActions.tsx,NoteDrawer.tsx,cleaned.ts}` + tests, `styles/{checklist.css,cleaned.css}` (Split up rules), `backend/internal/pipeline/clean_note.go`, `service/notes.go`, `docs/api/openapi.yaml` | M |

**Order:**

1. A goes first.
2. B goes after A, because it uses `shiftLevel`.
3. C is independent of A and B except for `checklist.css`, where it touches different sections. C also touches `NoteDetailScreen.tsx`, so coordinate with the F4 (swipe) and F2 (back navigation) specs.
4. Each stream updates its own sections of `docs/design/checklists.md`:
   - A: "Data model", "The append rule", "Merging", "tasks mode".
   - B: "Editing the list", "Sub-items".
   - C: "Split up", which becomes "Tidy up".

**Overall:** F1 is M–L (A M + B M); F8 is M.
