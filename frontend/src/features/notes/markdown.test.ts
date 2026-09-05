import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import { parseBlocks, renderMarkdown } from './markdown.ts';

/**
 * The cleaned view's renderer: the small dialect the worker writes, as
 * elements, with nothing in the text ever becoming markup.
 */
describe('parseBlocks', () => {
  it('reads headings, paragraphs, both kinds of list, quotes and rules', () => {
    const blocks = parseBlocks(
      [
        '# Roof repair',
        '',
        'Ridge tiles have slipped.',
        'Flashing needs replacing.',
        '',
        '## To do',
        '- Get two quotes',
        '- Book the earlier one',
        '  which is probably Ellis',
        '',
        '1. Call Ellis',
        '2) Call the other one',
        '',
        '> Nine hundred is fine.',
        '',
        '---',
        '',
        'Done.',
      ].join('\n'),
    );
    expect(blocks).toEqual([
      { kind: 'heading', level: 1, text: 'Roof repair' },
      { kind: 'paragraph', text: 'Ridge tiles have slipped. Flashing needs replacing.' },
      { kind: 'heading', level: 2, text: 'To do' },
      { kind: 'list', ordered: false, items: ['Get two quotes', 'Book the earlier one which is probably Ellis'] },
      { kind: 'list', ordered: true, items: ['Call Ellis', 'Call the other one'] },
      { kind: 'quote', text: 'Nine hundred is fine.' },
      { kind: 'rule' },
      { kind: 'paragraph', text: 'Done.' },
    ]);
  });

  it('treats what it does not know as prose, and a lone dash as prose too', () => {
    expect(parseBlocks('-\n\n<b>bold</b>\n\n1.5 metres of flashing')).toEqual([
      { kind: 'paragraph', text: '-' },
      { kind: 'paragraph', text: '<b>bold</b>' },
      { kind: 'paragraph', text: '1.5 metres of flashing' },
    ]);
  });

  it('handles Windows line endings and blank input', () => {
    expect(parseBlocks('# A\r\n\r\nB')).toEqual([
      { kind: 'heading', level: 1, text: 'A' },
      { kind: 'paragraph', text: 'B' },
    ]);
    expect(parseBlocks('')).toEqual([]);
    expect(parseBlocks('\n\n  \n')).toEqual([]);
  });
});

describe('renderMarkdown', () => {
  const html = (source: string): string => renderToStaticMarkup(renderMarkdown(source));

  it('steps headings down one level, beneath the note title', () => {
    expect(html('# Title\n\n###### Deep')).toBe('<h2>Title</h2><h6>Deep</h6>');
  });

  it('renders emphasis, strong and code inline, and nothing else', () => {
    expect(html('Get **two** quotes, *soon*, from `Ellis` and _others_.')).toBe(
      '<p>Get <strong>two</strong> quotes, <em>soon</em>, from <code>Ellis</code> and <em>others</em>.</p>',
    );
    // A lone asterisk or an unclosed marker is text.
    expect(html('2 * 3 and **unfinished')).toBe('<p>2 * 3 and **unfinished</p>');
  });

  it('never turns text into markup', () => {
    expect(html('<script>alert(1)</script> & <img src=x onerror=y>')).toBe(
      '<p>&lt;script&gt;alert(1)&lt;/script&gt; &amp; &lt;img src=x onerror=y&gt;</p>',
    );
    expect(html('- <b>x</b>')).toBe('<ul><li>&lt;b&gt;x&lt;/b&gt;</li></ul>');
  });

  it('renders lists, quotes and rules as their elements', () => {
    expect(html('- a\n- b\n\n1. c\n\n> q\n\n---')).toBe(
      '<ul><li>a</li><li>b</li></ul><ol><li>c</li></ol><blockquote><p>q</p></blockquote><hr/>',
    );
  });
});
