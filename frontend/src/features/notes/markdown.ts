import { createElement, Fragment, type ReactNode } from 'react';

/**
 * The cleaned view's Markdown, rendered.
 *
 * The worker writes the cleaned note as Markdown — headings, lists, a little
 * emphasis — and this turns it into elements. A minimal renderer written
 * here rather than a library: the dialect is the small one the worker
 * produces, the output is React elements and never HTML, so nothing in the
 * text can become markup, and the whole thing is smaller than a library's
 * option parsing. Anything it does not recognise is a paragraph, which is
 * the safe way to be wrong about prose.
 *
 * Block grammar, one line at a time: `#`–`######` headings, `-`/`*`/`+` and
 * `1.` list items (a following line without a marker continues the item),
 * `>` quotes, `---` rules, blank lines between paragraphs. Inline: `**strong**`,
 * `*em*` / `_em_`, `` `code` ``. Raw HTML is text.
 */

export type Block =
  | { kind: 'heading'; level: 1 | 2 | 3 | 4 | 5 | 6; text: string }
  | { kind: 'paragraph'; text: string }
  | { kind: 'list'; ordered: boolean; items: string[] }
  | { kind: 'quote'; text: string }
  | { kind: 'rule' };

const HEADING = /^(#{1,6})\s+(.+?)\s*#*\s*$/;
const BULLET = /^\s{0,3}[-*+]\s+(.*)$/;
const NUMBERED = /^\s{0,3}\d{1,9}[.)]\s+(.*)$/;
const QUOTE = /^\s{0,3}>\s?(.*)$/;
const RULE = /^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/;

type HeadingLevel = Extract<Block, { kind: 'heading' }>['level'];

/** One to six hashes; the regex above admits nothing else. */
function headingLevel(hashes: string): HeadingLevel {
  return Math.min(6, Math.max(1, hashes.length)) as HeadingLevel;
}

/** The document as blocks. Pure, so the grammar is testable without React. */
export function parseBlocks(source: string): Block[] {
  const blocks: Block[] = [];
  const lines = source.replace(/\r\n?/g, '\n').split('\n');
  let paragraph: string[] = [];
  let quote: string[] = [];

  const flushParagraph = (): void => {
    if (paragraph.length > 0) blocks.push({ kind: 'paragraph', text: paragraph.join(' ') });
    paragraph = [];
  };
  const flushQuote = (): void => {
    if (quote.length > 0) blocks.push({ kind: 'quote', text: quote.join(' ') });
    quote = [];
  };
  const flush = (): void => {
    flushParagraph();
    flushQuote();
  };

  for (const raw of lines) {
    const line = raw.trimEnd();
    if (line.trim() === '') {
      flush();
      continue;
    }

    const heading = HEADING.exec(line);
    if (heading) {
      flush();
      blocks.push({ kind: 'heading', level: headingLevel(heading[1] ?? '#'), text: heading[2] ?? '' });
      continue;
    }

    if (RULE.test(line)) {
      flush();
      blocks.push({ kind: 'rule' });
      continue;
    }

    const bullet = BULLET.exec(line);
    const numbered = bullet ? null : NUMBERED.exec(line);
    const item = bullet ?? numbered;
    if (item) {
      flush();
      const ordered = numbered !== null;
      const last = blocks.at(-1);
      if (last?.kind === 'list' && last.ordered === ordered) last.items.push(item[1] ?? '');
      else blocks.push({ kind: 'list', ordered, items: [item[1] ?? ''] });
      continue;
    }

    const quoted = QUOTE.exec(line);
    if (quoted) {
      flushParagraph();
      quote.push(quoted[1] ?? '');
      continue;
    }

    // A plain line straight after a list item continues that item (Markdown's
    // lazy continuation); after a quote, the quote; otherwise it is prose.
    const last = blocks.at(-1);
    if (last?.kind === 'list' && paragraph.length === 0 && quote.length === 0 && raw.startsWith(' ')) {
      last.items[last.items.length - 1] = `${last.items.at(-1) ?? ''} ${line.trim()}`;
      continue;
    }
    if (quote.length > 0) {
      quote.push(line.trim());
      continue;
    }
    paragraph.push(line.trim());
  }
  flush();
  return blocks;
}

/** `**strong**`, `*em*` / `_em_` and `` `code` ``, as elements. Everything else is text. */
const INLINE = /(\*\*[^*\n]+?\*\*|`[^`\n]+?`|\*[^*\s][^*\n]*?\*|_[^_\s][^_\n]*?_)/g;

export function renderInline(text: string): ReactNode {
  const parts = text.split(INLINE);
  if (parts.length === 1) return text;
  return parts.map((part, index) => {
    if (part.startsWith('**') && part.endsWith('**') && part.length > 4) {
      return createElement('strong', { key: index }, part.slice(2, -2));
    }
    if (part.startsWith('`') && part.endsWith('`') && part.length > 2) {
      return createElement('code', { key: index }, part.slice(1, -1));
    }
    if ((part.startsWith('*') || part.startsWith('_')) && part.length > 2) {
      return createElement('em', { key: index }, part.slice(1, -1));
    }
    return part;
  });
}

/** `text` with its inline marks unwrapped: `**strong**`, `*em*` / `_em_`, `` `code` `` become their words. */
export function plainInline(text: string): string {
  return text
    .split(INLINE)
    .map((part) => {
      if (part.startsWith('**') && part.endsWith('**') && part.length > 4) return part.slice(2, -2);
      if (part.startsWith('`') && part.endsWith('`') && part.length > 2) return part.slice(1, -1);
      if ((part.startsWith('*') || part.startsWith('_')) && part.length > 2) return part.slice(1, -1);
      return part;
    })
    .join('');
}

/**
 * The document as plain text, for a body that is shown verbatim: headings
 * and paragraphs as lines with their marks unwrapped, one blank line between
 * blocks; bullets as one line each without the marker; a numbered list with
 * its numbers, because "the third step" is meaning and a dash is not; a
 * quote as its words; a rule as nothing.
 */
export function toPlainText(source: string): string {
  return parseBlocks(source)
    .map((block) => {
      switch (block.kind) {
        case 'heading':
        case 'paragraph':
        case 'quote':
          return plainInline(block.text);
        case 'list':
          return block.items
            .map((item, index) =>
              block.ordered ? `${String(index + 1)}. ${plainInline(item)}` : plainInline(item),
            )
            .join('\n');
        default:
          return '';
      }
    })
    .filter((text) => text.length > 0)
    .join('\n\n');
}

/**
 * The rendered document. Headings are stepped down one level: the note's
 * title is the screen's h1, so the view's `#` is an h2 beneath it and its
 * `######` bottoms out at h6.
 */
export function renderMarkdown(source: string): ReactNode {
  return createElement(
    Fragment,
    null,
    parseBlocks(source).map((block, index) => {
      switch (block.kind) {
        case 'heading':
          return createElement(
            `h${String(Math.min(6, block.level + 1))}`,
            { key: index },
            renderInline(block.text),
          );
        case 'list':
          return createElement(
            block.ordered ? 'ol' : 'ul',
            { key: index },
            block.items.map((item, itemIndex) =>
              createElement('li', { key: itemIndex }, renderInline(item)),
            ),
          );
        case 'quote':
          return createElement(
            'blockquote',
            { key: index },
            createElement('p', null, renderInline(block.text)),
          );
        case 'rule':
          return createElement('hr', { key: index });
        default:
          return createElement('p', { key: index }, renderInline(block.text));
      }
    }),
  );
}
