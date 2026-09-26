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
 * but the one line that changed.
 */

export interface ChecklistItem {
  text: string;
  done: boolean;
}

/** One line of the body: the marker, the box, the text. Case-insensitive on the x. */
const ITEM = /^- \[( |x|X)\] ?(.*)$/;

/** The server's snippet is the body's first 500 runes, then "..." when there was more. */
const SNIPPET_RUNES = 500;

/** The body as items, in body order. */
export function parseChecklist(body: string): ChecklistItem[] {
  const items: ChecklistItem[] = [];
  for (const line of body.split(/\r?\n/)) {
    const match = ITEM.exec(line);
    if (match) {
      // As written, spaces and all: the editor writes back on every
      // keystroke, and a trim here would eat the space after each word.
      items.push({ text: match[2] ?? '', done: match[1] !== ' ' });
      continue;
    }
    // A prose line: kept as an open item, normalised on the next write.
    if (line.trim() !== '') items.push({ text: line, done: false });
  }
  return items;
}

function itemLine(item: ChecklistItem): string {
  return `- [${item.done ? 'x' : ' '}] ${item.text}`;
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

/** `[ ]` ↔ `[x]` on the item at `index`, in place. */
export function toggleItem(body: string, index: number): string {
  return withItems(body, (items) => {
    const item = items[index];
    if (item) items[index] = { ...item, done: !item.done };
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
 * `index` is null — the "Add an item" row.
 */
export function insertItemAfter(body: string, index: number | null, text = ''): string {
  return withItems(body, (items) => {
    const at = index === null ? items.length : Math.min(index + 1, items.length);
    items.splice(at, 0, { text: text.replace(/[\r\n]+/g, ' ').trim(), done: false });
  });
}

export function removeItem(body: string, index: number): string {
  return withItems(body, (items) => {
    items.splice(index, 1);
  });
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
    .map((text): ChecklistItem => ({ text, done: false }));
  return serialiseChecklist(items);
}

/**
 * Checklist → plain: each item is a paragraph of its own text. A done item
 * keeps its words and loses its mark — there is no notation for "done" in
 * prose, and dropping the text would be dropping the user's own words.
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
