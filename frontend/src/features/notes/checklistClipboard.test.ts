import { describe, expect, it } from 'vitest';

import { checklistClipboard } from './checklistClipboard.ts';

/**
 * What Copy puts on the clipboard for a checklist: a messaging-friendly text
 * with box glyphs and the indent, and an HTML list with the levels nested
 * and done items struck through — the same items, the same order.
 */
describe('checklistClipboard', () => {
  const body = '- [ ] Costco\n  - [x] Plates & cups\n    - [ ] Paper ones\n  - [ ] Candles\n- [ ] Milk';

  it('writes the title, a blank line, then one glyph line per item, indented by level, done in place', () => {
    expect(checklistClipboard('Party', body).text).toBe(
      'Party\n\n☐ Costco\n  ☑ Plates & cups\n    ☐ Paper ones\n  ☐ Candles\n☐ Milk',
    );
  });

  it('writes a nested list with done items struck through and the text escaped', () => {
    expect(checklistClipboard('Party <1>', body).html).toBe(
      '<p><strong>Party &lt;1&gt;</strong></p>' +
        '<ul><li>Costco<ul><li><s>Plates &amp; cups</s><ul><li>Paper ones</li></ul></li><li>Candles</li></ul></li><li>Milk</li></ul>',
    );
  });

  it('leaves out items with no words and a blank title', () => {
    expect(checklistClipboard('  ', '- [ ] Milk\n- [ ] \n- [x] Eggs')).toEqual({
      text: '☐ Milk\n☑ Eggs',
      html: '<ul><li>Milk</li><li><s>Eggs</s></li></ul>',
    });
  });

  it('a sub-item whose parent was empty still comes out, one level up', () => {
    expect(checklistClipboard('', '- [ ] \n  - [ ] Plates\n- [ ] Milk')).toEqual({
      text: '  ☐ Plates\n☐ Milk',
      html: '<ul><li>Plates</li><li>Milk</li></ul>',
    });
  });
});
