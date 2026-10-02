import { parseChecklist, type ChecklistItem } from './checklist.ts';

/**
 * A checklist for the clipboard, in both forms a paste can take
 * (`CopyButton`'s `text` and `html`).
 *
 * `text` is what a messaging app gets — WhatsApp, Messages, Signal and any
 * plain field take text/plain only: the title, a blank line, then one line
 * per item as `☐ Milk` / `☑ Eggs`, two spaces of indent per level. The box
 * glyphs rather than `[ ]` / `[x]`: every current phone font has them, they
 * read as a list and not as code, and the apps that would strip a symbol
 * show `[x]` as four characters anyway, so the ASCII form gains nothing.
 * Items stay in body order with the done ones in place: a done sub-item
 * moved to the end would come out from under its parent, and the tree is
 * the thing worth sharing.
 *
 * `html` is what a rich field takes — Mail, Notes, Docs: the title in bold
 * and a real `<ul>` with nested lists, a done item struck through (`<s>`).
 * No box glyphs there, since the list draws its own bullets.
 *
 * An item with no words is nothing to share and is left out of both.
 */
export function checklistClipboard(title: string, body: string): { text: string; html: string } {
  const items = parseChecklist(body).filter((item) => item.text.trim() !== '');
  const name = title.trim();
  const lines = items.map((item) => `${'  '.repeat(item.depth)}${item.done ? '☑' : '☐'} ${item.text.trim()}`);
  const text = [name, lines.join('\n')].filter(Boolean).join('\n\n');
  const html = `${name ? `<p><strong>${escapeHtml(name)}</strong></p>` : ''}${list(items, 0, 0).html}`;
  return { text, html };
}

/** The items from `at` that stand at `depth` or deeper, as one `<ul>`, and where the walk stopped. */
function list(items: readonly ChecklistItem[], at: number, depth: number): { html: string; next: number } {
  let html = '';
  let i = at;
  for (let item = items[i]; item && item.depth >= depth; item = items[i]) {
    const words = escapeHtml(item.text.trim());
    const sub = list(items, i + 1, item.depth + 1);
    html += `<li>${item.done ? `<s>${words}</s>` : words}${sub.html}</li>`;
    i = sub.next;
  }
  return { html: html ? `<ul>${html}</ul>` : '', next: i };
}

function escapeHtml(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
