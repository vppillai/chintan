/**
 * A checklist note's body, as items.
 *
 * The body of a checklist is GitHub task-list syntax, one item per line:
 * `- [ ] text` open, `- [x] text` done. That is the whole format — the worker
 * appends a recording as one such line per item it named, the cleanup model
 * rewrites the list as more of them, and `chintanctl export` needs nothing
 * special because it is Markdown. Everything here is pure: the editor reads
 * the body through `parseChecklist`, changes the items, and writes it back
 * through `serialiseChecklist`, so every write leaves the body normalised.
 *
 * Two tolerances on the way in, neither on the way out. Blank lines are
 * dropped — the worker's append separators, a stray Enter. A line that is
 * not an item at all (prose from before the note was a checklist, a line
 * someone typed in another editor) is shown as an open item with its text,
 * and becomes one on the next write; the alternative, hiding it, would lose
 * text the user can see nowhere else. `[X]` is read as done as well as `[x]`.
 *
 * Order is the body's. Done items are shown below the open ones, but that is
 * the view's grouping: toggling an item flips its own line where it stands,
 * so a body edited here and one the worker appends to disagree about nothing
 * but the one line that changed. A reorder (`moveItem`) is the one edit that
 * changes the order, and it is one write on release.
 *
 * A sub-item is two spaces of indent under its parent — `  - [ ] Plates`
 * under `- [ ] Party` — and two more for each level below, three levels in
 * all (`MAX_DEPTH`; owner feedback F1 of round 8, replacing CL-D1's one).
 * The depth is clamped to one under the item before it and to `MAX_DEPTH`,
 * so a jump of two levels reads as one, a deeper body written elsewhere
 * flattens to the third level on its first save here (clamped, never
 * dropped), and a child with no parent is top level.
 * Until 2026-09-26 an indented line did not match the item pattern at all:
 * `  - [x] Candles` showed as an open row whose text was the raw syntax and
 * was rewritten to `- [ ]   - [x] Candles` on the first save. The editor
 * changes a row's level through `shiftLevel`; the worker appends
 * at the end with no indent, so a filed item is top level by construction. An item that
 * moves takes the lines nested under it (`blockOf`), so a list is never torn
 * from its sub-items, and ticking a parent ticks them (`toggleItem`): a
 * done item has no open descendant, at any depth.
 */

export interface ChecklistItem {
  text: string;
  done: boolean;
  /** 0 at the top level; one more for each two spaces of indent under the item before. */
  depth: number;
}

/**
 * One line of the body: the indent, the marker, the box, the text.
 * Case-insensitive on the x, and the space after the box is optional. The
 * indent is spaces and tabs, a tab counting as two spaces, so a list indented
 * in another editor keeps its sub-items instead of reading as prose. The Go
 * readers apply the same rule (backend `cleanup.ParseLine`), and both test
 * suites assert it against backend/internal/cleanup/testdata/checklist-lines.json.
 */
const ITEM = /^([ \t]*)- \[( |x|X)\] ?(.*)$/;

/** An indent's width in spaces, a tab counting as two. */
function indentWidth(indent: string): number {
  let width = 0;
  for (const ch of indent) width += ch === '\t' ? 2 : 1;
  return width;
}

/**
 * How deep an item may nest: 0 is the top level, so 2 is three levels — top,
 * sub-item, sub-sub-item. A fourth leaves about twelve characters of text on
 * a 320 px screen. The Go readers' twin is `cleanup.MaxDepth`, and both
 * suites assert the shared fixture's `max_depth` against their own.
 */
export const MAX_DEPTH = 2;

/** The server's snippet is the body's first 500 runes, then "..." when there was more. */
const SNIPPET_RUNES = 500;

/** The body as items, in body order. */
export function parseChecklist(body: string): ChecklistItem[] {
  const items: ChecklistItem[] = [];
  for (const line of body.split(/\r?\n/)) {
    const match = ITEM.exec(line);
    if (match) {
      // At most one level under the item before it and never past
      // MAX_DEPTH: a two-level jump reads as one, and a child with no parent
      // is top level.
      const parentDepth = items[items.length - 1]?.depth ?? -1;
      const depth = Math.min(Math.floor(indentWidth(match[1] ?? '') / 2), parentDepth + 1, MAX_DEPTH);
      // As written, spaces and all: the editor writes back on every
      // keystroke, and a trim here would eat the space after each word.
      items.push({ text: match[3] ?? '', done: match[2] !== ' ', depth });
      continue;
    }
    // A prose line: kept as an open item, normalised on the next write.
    if (line.trim() !== '') items.push({ text: line, done: false, depth: 0 });
  }
  return items;
}

function itemLine(item: ChecklistItem): string {
  return `${'  '.repeat(item.depth)}- [${item.done ? 'x' : ' '}] ${item.text}`;
}

/** The items as a body, one per line. */
export function serialiseChecklist(items: readonly ChecklistItem[]): string {
  return items.map(itemLine).join('\n');
}

/** Rewrites one item, by its position in `parseChecklist(body)`. */
function withItems(
  body: string,
  change: (items: ChecklistItem[]) => void,
): string {
  const items = parseChecklist(body);
  change(items);
  return serialiseChecklist(items);
}

/**
 * `[ ]` ↔ `[x]` on the item at `index`, in place. Ticking a parent ticks
 * every item under it, as Keep does — the parent is the whole job, and a
 * finished job has no open parts. Reopening a parent leaves its sub-items
 * as they are: they were finished on their own terms, and the person can
 * tick the parent again once the reopened part is done. Reopening a
 * sub-item reopens every item it stands under, up to the top level,
 * because a parent with an open part is not done. Every flipped line stays
 * where it stands.
 */
export function toggleItem(body: string, index: number): string {
  return withItems(body, (items) => {
    const item = items[index];
    if (!item) return;
    const done = !item.done;
    if (done) {
      for (const i of blockOf(items, index)) (items[i] as ChecklistItem).done = true;
      return;
    }
    item.done = false;
    for (let parent = parentOf(items, index); parent !== null; parent = parentOf(items, parent)) {
      (items[parent] as ChecklistItem).done = false;
    }
  });
}

export function setItemText(body: string, index: number, text: string): string {
  return withItems(body, (items) => {
    const item = items[index];
    // One line per item is the format; a pasted line break would split it.
    if (item) items[index] = { ...item, text: text.replace(/[\r\n]+/g, ' ') };
  });
}

/**
 * A new open item after the item at `index`, or at the end of the list when
 * `index` is null — the "Add an item" row. It sits at the depth of the item
 * it follows, so Enter in a sub-item starts another sub-item; after a parent
 * with sub-items it is the parent's first sub-item, as an outliner does,
 * because the new row appears right under the parent, and a top-level line
 * there would have taken the parent's sub-items for its own.
 */
export function insertItemAfter(body: string, index: number | null, text = ''): string {
  return withItems(body, (items) => {
    const at = index === null ? items.length : Math.min(index + 1, items.length);
    const item = index === null ? undefined : items[index];
    // A line deeper than the item right after it means the item is a parent.
    const depth = item ? item.depth + ((items[at]?.depth ?? 0) > item.depth ? 1 : 0) : 0;
    items.splice(at, 0, { text: text.replace(/[\r\n]+/g, ' ').trim(), done: false, depth });
  });
}

/**
 * Drops the item at `index`. A parent's sub-items come up a level rather
 * than hanging from whichever of them the parser would read as the new
 * parent: they were the job's parts, and the job is gone, not the first
 * part.
 */
export function removeItem(body: string, index: number): string {
  return withItems(body, (items) => {
    for (const i of blockOf(items, index).slice(1)) (items[i] as ChecklistItem).depth -= 1;
    items.splice(index, 1);
  });
}

/** The indices of `index` and every item nested under it: the lines that move and go together. */
export function blockOf(items: readonly ChecklistItem[], index: number): number[] {
  const block = [index];
  const depth = items[index]?.depth ?? 0;
  for (let i = index + 1; i < items.length && (items[i]?.depth ?? 0) > depth; i += 1) block.push(i);
  return block;
}

/** The index of the item `index` is nested under, or null at the top level. */
function parentOf(items: readonly ChecklistItem[], index: number): number | null {
  const depth = items[index]?.depth ?? 0;
  for (let i = index - 1; i >= 0; i -= 1) if ((items[i]?.depth ?? 0) < depth) return i;
  return null;
}

/** The depth the item at `index` would go to, `by` levels in under the row `above`; its own depth when it cannot. */
function levelIn(items: readonly ChecklistItem[], index: number, above: number, by: number): number {
  const item = items[index];
  const row = items[above];
  if (!item || !row) return 0;
  // At most one level under the row it follows, and never past MAX_DEPTH.
  return Math.max(item.depth, Math.min(item.depth + by, row.depth + 1, MAX_DEPTH));
}

/**
 * Whether `shiftLevel(body, index, by, above)` with `by > 0` would change
 * anything. It would not when there is no row `above` standing before
 * `index` (the first open item can never be a sub-item); when the item is
 * already as deep as the row above allows, one under it and never past
 * `MAX_DEPTH`; when the row above or any item it stands under is done, since
 * an open part under a finished job is what `toggleItem` never writes, and a
 * body written elsewhere with a done parent over an open sub-item must not
 * gain a second one through it; and when the item's own sub-items would go
 * past `MAX_DEPTH` — indenting is an explicit act about levels, and quietly
 * flattening a grandchild is never what it means.
 */
export function canNest(items: readonly ChecklistItem[], index: number, above: number, by = 1): boolean {
  const item = items[index];
  if (!item || !items[above] || above >= index || by <= 0) return false;
  for (let i: number | null = above; i !== null; i = parentOf(items, i)) if (items[i]?.done) return false;
  const delta = levelIn(items, index, above, by) - item.depth;
  const deepest = Math.max(...blockOf(items, index).map((i) => items[i]?.depth ?? 0));
  return delta > 0 && deepest + delta <= MAX_DEPTH;
}

/**
 * Changes the level of the item at `index` by `by`, the row `above` being
 * the open row shown above it.
 *
 * In (`by > 0`): one level per step, at most one under `above` and never
 * past `MAX_DEPTH` — so under a sub-item a top-level row becomes the
 * sub-item's sibling, and a second step makes it its child. Its block moves
 * to follow `above`'s in the body when done lines stand between them: the
 * Done section is the view's grouping, and the row a person sees above is
 * the one they mean, so a body of `Milk`, `[x] Eggs`, `Bread` nests Bread
 * under Milk and lets Eggs's line slip below (its place shows nowhere). Its
 * own sub-items come along, each the same number of levels deeper. When
 * `canNest` says no, a normalising no-op.
 *
 * Out (`by < 0`): one level per step, in place, stopping at the top; the
 * sub-items that followed it under the same parent become its own, as an
 * outliner does.
 */
export function shiftLevel(body: string, index: number, by: number, above: number): string {
  return withItems(body, (items) => {
    const item = items[index];
    if (!item) return;
    if (by < 0) {
      for (let step = 0; step < -by && item.depth > 0; step += 1) {
        for (const i of blockOf(items, index)) (items[i] as ChecklistItem).depth -= 1;
      }
      return;
    }
    if (!canNest(items, index, above, by)) return;
    const delta = levelIn(items, index, above, by) - item.depth;
    const block = blockOf(items, index).map((i) => items[i] as ChecklistItem);
    for (const member of block) member.depth += delta;
    items.splice(index, block.length);
    // Right after the row above's own block — `above` is before `index`, so
    // its indices did not move — where it reads as that row's next sibling
    // or last sub-item.
    const at = (blockOf(items, above).at(-1) ?? above) + 1;
    items.splice(at, 0, ...block);
  });
}

/**
 * One level in under `under`: `shiftLevel` by one, kept for the editor's
 * Tab and grip until it calls `shiftLevel` itself.
 */
export function nestUnder(body: string, index: number, under: number): string {
  return shiftLevel(body, index, 1, under);
}

/** One level out, in place: `shiftLevel` by minus one. A top-level item, or a bad index, is a normalising no-op. */
export function unnest(body: string, index: number): string {
  return shiftLevel(body, index, -1, index);
}

/**
 * Moves the block at `from` so that it starts where the block at `to`
 * started — above it moving up, below it moving down, which is the slot the
 * dragged row was shown in. The block takes the depth of the item at `to`,
 * so a row dropped on a sub-item's slot becomes a sub-item, its own children
 * shifted with it and clamped to `MAX_DEPTH`: a drop is placed by eye, so
 * it clamps where an indent refuses, and a block dropped deeper than its
 * levels allow keeps its deepest lines at the third level. The same index, one out of range, or a `to` inside the
 * block itself (a parent cannot land under its own child) is a normalising
 * no-op, as `toggleItem` is for a bad index.
 */
export function moveItem(body: string, from: number, to: number): string {
  return withItems(body, (items) => {
    const source = items[from];
    const target = items[to];
    if (from === to || !source || !target) return;
    const indices = blockOf(items, from);
    if (indices.includes(to)) return;
    const block = indices.map((i) => items[i] as ChecklistItem);
    const delta = target.depth - source.depth;
    items.splice(from, block.length);
    // Moving down, the block's own lines have left the list above the slot.
    const at = to > from ? to - block.length + 1 : to;
    for (const item of block) item.depth = Math.max(0, Math.min(MAX_DEPTH, item.depth + delta));
    items.splice(at, 0, ...block);
  });
}

/** Every item open again, in place. */
export function uncheckAll(body: string): string {
  return withItems(body, (items) => {
    for (const item of items) item.done = false;
  });
}

/**
 * Drops every done item — and, for a done item, its whole block, since a
 * sub-item of a finished parent has nothing to hang from — and keeps the
 * rest where it stands.
 */
export function removeDone(body: string): string {
  const kept: ChecklistItem[] = [];
  let droppedDepth: number | null = null;
  for (const item of parseChecklist(body)) {
    if (droppedDepth !== null && item.depth > droppedDepth) continue;
    droppedDepth = null;
    if (item.done) {
      droppedDepth = item.depth;
      continue;
    }
    kept.push(item);
  }
  return serialiseChecklist(kept);
}

/**
 * Plain → checklist: each non-empty paragraph is one open item, its internal
 * line breaks and runs of whitespace collapsed to single spaces, because an
 * item is one line by definition.
 */
export function proseToChecklist(body: string): string {
  const items = body
    .split(/\r?\n[ \t]*\r?\n/)
    .map((paragraph) => paragraph.replace(/\s+/g, ' ').trim())
    .filter((text) => text.length > 0)
    .map((text): ChecklistItem => ({ text, done: false, depth: 0 }));
  return serialiseChecklist(items);
}

/**
 * Checklist → plain: each item is a paragraph of its own text. A done item
 * keeps its words and loses its mark — there is no notation for "done" in
 * prose, and dropping the text would be dropping the user's own words. A
 * sub-item loses its indent the same way.
 */
export function checklistToProse(body: string): string {
  return parseChecklist(body)
    .map((item) => item.text)
    .filter((text) => text.length > 0)
    .join('\n\n');
}

export interface Progress {
  done: number;
  total: number;
}

export function progressOf(items: readonly ChecklistItem[]): Progress {
  return {
    done: items.filter((item) => item.done).length,
    total: items.length,
  };
}

/**
 * "3 of 7 done" — the meta line's fact for a checklist, in place of the word
 * count. `partial` says the count came from a cut snippet, so the total is
 * shown as a floor: "3 of 7+ done".
 */
export function describeProgress({ done, total }: Progress, partial = false): string {
  if (total === 0 && !partial) return 'No items';
  return `${String(done)} of ${String(total)}${partial ? '+' : ''} done`;
}

/**
 * Whether a list row's snippet is the whole body or the first 500 runes of
 * it: the server appends "..." past that, so the rune count says.
 */
export function snippetIsCut(snippet: string): boolean {
  return Array.from(snippet).length > SNIPPET_RUNES;
}

/** The open items as one line of plain text, for a list row's snippet. */
export function openItemsText(items: readonly ChecklistItem[]): string {
  return items
    .filter((item) => !item.done && item.text.length > 0)
    .map((item) => item.text)
    .join(' · ');
}
