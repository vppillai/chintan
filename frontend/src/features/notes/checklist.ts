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
 * under `- [ ] Party`. One level (`MAX_DEPTH`, owner decision CL-D1,
 * 2026-09-27): what a spoken list needs, and what Keep offers. The depth is
 * clamped to one under the item before it and to `MAX_DEPTH`, so a jump of
 * two levels reads as one, a deeper body written elsewhere flattens to one
 * level on its first save here, and a child with no parent is top level.
 * Until 2026-09-26 an indented line did not match the item pattern at all:
 * `  - [x] Candles` showed as an open row whose text was the raw syntax and
 * was rewritten to `- [ ]   - [x] Candles` on the first save. The editor
 * nests and un-nests through `nestUnder` and `unnest`; the worker appends
 * at the end with no indent, so a filed item is top level by construction. An item that
 * moves takes the lines nested under it (`blockOf`), so a list is never torn
 * from its sub-items, and ticking a parent ticks them (`toggleItem`).
 */

export interface ChecklistItem {
  text: string;
  done: boolean;
  /** 0 at the top level; one more for each two spaces of indent under the item before. */
  depth: number;
}

/** One line of the body: the indent, the marker, the box, the text. Case-insensitive on the x. */
const ITEM = /^( *)- \[( |x|X)\] ?(.*)$/;

/**
 * How deep an item may nest: one level, as Keep allows (CL-D1). A third
 * level is this constant plus a `data-depth` rule in checklist.css; nothing
 * in a voice-first list has asked for one.
 */
export const MAX_DEPTH = 1;

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
      const depth = Math.min(Math.floor((match[1] ?? '').length / 2), parentDepth + 1, MAX_DEPTH);
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
 * its sub-items with it, as Keep does — the parent is the whole job, and a
 * finished job has no open parts. Reopening a parent leaves its sub-items
 * as they are: they were finished on their own terms, and the person can
 * tick the parent again once the reopened part is done. Reopening a
 * sub-item reopens its parent, because a parent with an open part is not
 * done. Every flipped line stays where it stands.
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
    const parent = parentOf(items, index);
    if (parent !== null) (items[parent] as ChecklistItem).done = false;
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

/**
 * Whether `nestUnder` would change anything: `under` stands above `index`
 * and there is a level left to go — one under `under`, never past
 * `MAX_DEPTH`. The first open item has no row above it, so it can never be
 * a sub-item; an item already as deep as `under` allows stays. Neither
 * `under` nor its own parent may be done: an open part under a finished job
 * is what `toggleItem` never writes, and a body written elsewhere with a
 * done parent over an open sub-item must not gain a second one through it.
 */
export function canNest(items: readonly ChecklistItem[], index: number, under: number): boolean {
  const item = items[index];
  const parent = items[under];
  if (!item || !parent || under >= index || parent.done) return false;
  const grandparent = parentOf(items, under);
  if (grandparent !== null && items[grandparent]?.done) return false;
  return Math.min(MAX_DEPTH, parent.depth + 1) > item.depth;
}

/**
 * Makes the item at `index` a sub-item of `under`, the open row shown above
 * it — a child when `under` is top level, a sibling under the same parent
 * when it is a sub-item itself. Its block moves to follow `under`'s in the
 * body when done lines stand between them: the Done section is the view's
 * grouping, and the row a person sees above is the one they mean, so a body
 * of `Milk`, `[x] Eggs`, `Bread` nests Bread under Milk and lets Eggs's line
 * slip below (its place shows nowhere). Its own sub-items come along,
 * clamped to `MAX_DEPTH`, so a parent nested this way keeps its children as
 * siblings. When `canNest` says no, a normalising no-op.
 */
export function nestUnder(body: string, index: number, under: number): string {
  return withItems(body, (items) => {
    if (!canNest(items, index, under)) return;
    const item = items[index] as ChecklistItem;
    const parent = items[under] as ChecklistItem;
    const delta = Math.min(MAX_DEPTH, parent.depth + 1) - item.depth;
    const block = blockOf(items, index).map((i) => items[i] as ChecklistItem);
    for (const member of block) member.depth = Math.min(MAX_DEPTH, member.depth + delta);
    items.splice(index, block.length);
    // Right after the parent's own block — `under` is before `index`, so its
    // indices did not move — where it reads as the parent's last sub-item.
    const at = (blockOf(items, under).at(-1) ?? under) + 1;
    items.splice(at, 0, ...block);
  });
}

/**
 * Brings the item at `index` up one level, in place; the sub-items that
 * followed it under the same parent become its own. A top-level item, or a
 * bad index, is a normalising no-op.
 */
export function unnest(body: string, index: number): string {
  return withItems(body, (items) => {
    const item = items[index];
    if (!item || item.depth === 0) return;
    for (const i of blockOf(items, index)) (items[i] as ChecklistItem).depth -= 1;
  });
}

/**
 * Moves the block at `from` so that it starts where the block at `to`
 * started — above it moving up, below it moving down, which is the slot the
 * dragged row was shown in. The block takes the depth of the item at `to`,
 * so a row dropped on a sub-item's slot becomes a sub-item, its own children
 * shifted with it and clamped to `MAX_DEPTH` — a parent dropped there takes
 * its children along as siblings. The same index, one out of range, or a `to` inside the
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
