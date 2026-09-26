import { describe, expect, it } from 'vitest';

import {
  blockOf,
  checklistToProse,
  describeProgress,
  insertItemAfter,
  moveItem,
  openItemsText,
  parseChecklist,
  progressOf,
  proseToChecklist,
  removeDone,
  removeItem,
  serialiseChecklist,
  setItemText,
  snippetIsCut,
  toggleItem,
  uncheckAll,
} from './checklist.ts';

describe('parsing a checklist body', () => {
  const cases: { name: string; body: string; items: { text: string; done: boolean }[] }[] = [
    {
      name: 'open and done items, in body order',
      body: '- [ ] Milk\n- [x] Eggs\n- [ ] Bread',
      items: [
        { text: 'Milk', done: false },
        { text: 'Eggs', done: true },
        { text: 'Bread', done: false },
      ],
    },
    {
      name: 'blank lines are dropped',
      body: '\n- [ ] Milk\n\n\n- [x] Eggs\n   \n',
      items: [
        { text: 'Milk', done: false },
        { text: 'Eggs', done: true },
      ],
    },
    {
      name: 'a legacy prose line is an open item',
      body: 'Ridge tiles have slipped.\n- [x] Call Ellis',
      items: [
        { text: 'Ridge tiles have slipped.', done: false },
        { text: 'Call Ellis', done: true },
      ],
    },
    {
      name: '[X] uppercase is done',
      body: '- [X] Shout',
      items: [{ text: 'Shout', done: true }],
    },
    {
      name: 'CRLF line endings',
      body: '- [ ] One\r\n- [x] Two\r\n',
      items: [
        { text: 'One', done: false },
        { text: 'Two', done: true },
      ],
    },
    {
      name: 'an item with no text yet is kept — it is the row being typed into',
      body: '- [ ] Milk\n- [ ] ',
      items: [
        { text: 'Milk', done: false },
        { text: '', done: false },
      ],
    },
    {
      name: 'a bullet without a box is prose, not an item',
      body: '- Milk',
      items: [{ text: '- Milk', done: false }],
    },
    {
      name: 'the text is kept as written — the space being typed after a word included',
      body: '- [ ] Bread ',
      items: [{ text: 'Bread ', done: false }],
    },
    { name: 'nothing', body: '', items: [] },
  ];

  for (const { name, body, items } of cases) {
    it(name, () => {
      expect(parseChecklist(body)).toEqual(items.map((item) => ({ ...item, depth: 0 })));
    });
  }

  it('serialises back to one normalised line per item', () => {
    const body = 'Ridge tiles have slipped.\n\n- [X] Call Ellis\r\n- [ ] Milk';
    expect(serialiseChecklist(parseChecklist(body))).toBe(
      '- [ ] Ridge tiles have slipped.\n- [x] Call Ellis\n- [ ] Milk',
    );
  });
});

const NESTED = '- [ ] Party\n  - [ ] Plates\n  - [x] Candles\n- [x] Eggs\n- [ ] Bread';

describe('sub-items: two spaces of indent under the parent', () => {
  it('reads the depth from the indent, one level under the item before at most', () => {
    expect(parseChecklist(NESTED).map((item) => item.depth)).toEqual([0, 1, 1, 0, 0]);
    // A jump of two levels is clamped to one; a child with no parent is top level.
    expect(parseChecklist('- [ ] A\n    - [ ] deep').at(-1)?.depth).toBe(1);
    expect(parseChecklist('  - [ ] orphan').at(0)?.depth).toBe(0);
    expect(parseChecklist('- [ ] A\n  - [ ] B\n      - [ ] C').map((item) => item.depth)).toEqual([0, 1, 2]);
  });

  it('round-trips a nested body byte for byte', () => {
    expect(serialiseChecklist(parseChecklist(NESTED))).toBe(NESTED);
  });

  it('shows an indented done line as a done item, not as an open row of raw syntax (probe 2026-09-26)', () => {
    // Before, `  - [x] Candles` missed the pattern, showed as open text and
    // was rewritten to `- [ ]   - [x] Candles` on the first save.
    expect(parseChecklist('- [ ] Party\n  - [x] Candles').at(1)).toEqual({ text: 'Candles', done: true, depth: 1 });
    expect(toggleItem('- [ ] Party\n  - [x] Candles', 0)).toBe('- [x] Party\n  - [x] Candles');
  });

  it('a new item under a sub-item is a sub-item; the add row adds at the top level', () => {
    expect(insertItemAfter(NESTED, 1, 'Cups')).toBe(
      '- [ ] Party\n  - [ ] Plates\n  - [ ] Cups\n  - [x] Candles\n- [x] Eggs\n- [ ] Bread',
    );
    expect(insertItemAfter(NESTED, null, 'Jam')).toBe(`${NESTED}\n- [ ] Jam`);
  });

  it('a block is the item and every item nested under it', () => {
    const items = parseChecklist(NESTED);
    expect(blockOf(items, 0)).toEqual([0, 1, 2]);
    expect(blockOf(items, 1)).toEqual([1]);
    expect(blockOf(items, 4)).toEqual([4]);
    expect(blockOf(items, 9)).toEqual([9]);
  });

  it('prose conversion flattens: the indent has no notation in prose', () => {
    expect(checklistToProse(NESTED)).toBe('Party\n\nPlates\n\nCandles\n\nEggs\n\nBread');
    expect(proseToChecklist('One\n\nTwo')).toBe('- [ ] One\n- [ ] Two');
  });
});

describe('reordering, uncheck all and delete done', () => {
  const FLAT = '- [ ] Milk\n- [x] Eggs\n- [ ] Bread\n- [ ] Butter';

  it('moves an item to the slot of another: above it going up, below it going down', () => {
    expect(moveItem(FLAT, 3, 0)).toBe('- [ ] Butter\n- [ ] Milk\n- [x] Eggs\n- [ ] Bread');
    expect(moveItem(FLAT, 0, 3)).toBe('- [x] Eggs\n- [ ] Bread\n- [ ] Butter\n- [ ] Milk');
    // Over a done line: the done line keeps its place in the body.
    expect(moveItem(FLAT, 0, 2)).toBe('- [x] Eggs\n- [ ] Bread\n- [ ] Milk\n- [ ] Butter');
  });

  it('the same slot or a bad index is a normalising no-op', () => {
    expect(moveItem(FLAT, 1, 1)).toBe(FLAT);
    expect(moveItem(FLAT, 0, 9)).toBe(FLAT);
    expect(moveItem(FLAT, 9, 0)).toBe(FLAT);
    expect(moveItem('Milk\n\nEggs', 0, 0)).toBe('- [ ] Milk\n- [ ] Eggs');
  });

  it('a block moves whole, and a parent cannot land under its own child', () => {
    expect(moveItem(NESTED, 0, 4)).toBe('- [x] Eggs\n- [ ] Bread\n- [ ] Party\n  - [ ] Plates\n  - [x] Candles');
    expect(moveItem(NESTED, 0, 1)).toBe(NESTED);
  });

  it('dropping on a sub-item slot nests the moved item', () => {
    expect(moveItem(NESTED, 4, 1)).toBe('- [ ] Party\n  - [ ] Bread\n  - [ ] Plates\n  - [x] Candles\n- [x] Eggs');
    // And a sub-item dropped on a top-level slot comes out to the top level.
    expect(moveItem(NESTED, 1, 4)).toBe('- [ ] Party\n  - [x] Candles\n- [x] Eggs\n- [ ] Bread\n- [ ] Plates');
  });

  it('reopens every item in place', () => {
    expect(uncheckAll(FLAT)).toBe('- [ ] Milk\n- [ ] Eggs\n- [ ] Bread\n- [ ] Butter');
    expect(uncheckAll(NESTED)).toBe('- [ ] Party\n  - [ ] Plates\n  - [ ] Candles\n- [ ] Eggs\n- [ ] Bread');
  });

  it('drops the done items and keeps the rest in order', () => {
    expect(removeDone(FLAT)).toBe('- [ ] Milk\n- [ ] Bread\n- [ ] Butter');
    expect(removeDone('- [x] A\n- [x] B')).toBe('');
    expect(removeDone('- [ ] A')).toBe('- [ ] A');
  });

  it('a done sub-item goes and its open parent stays; a done parent takes its children', () => {
    expect(removeDone(NESTED)).toBe('- [ ] Party\n  - [ ] Plates\n- [ ] Bread');
    expect(removeDone('- [x] P\n  - [ ] C\n- [ ] D')).toBe('- [ ] D');
  });
});

describe('editing items in place', () => {
  const body = '- [ ] Milk\n- [x] Eggs\n- [ ] Bread';

  it('toggles one item and leaves the order alone', () => {
    expect(toggleItem(body, 0)).toBe('- [x] Milk\n- [x] Eggs\n- [ ] Bread');
    expect(toggleItem(body, 1)).toBe('- [ ] Milk\n- [ ] Eggs\n- [ ] Bread');
    // Out of range: nothing changes but the normalisation.
    expect(toggleItem(body, 7)).toBe(body);
  });

  it('rewrites the text of one item, one line only', () => {
    expect(setItemText(body, 2, 'Sourdough')).toBe('- [ ] Milk\n- [x] Eggs\n- [ ] Sourdough');
    expect(setItemText(body, 0, 'Oat\nmilk')).toBe('- [ ] Oat milk\n- [x] Eggs\n- [ ] Bread');
  });

  it('inserts a new open item after one, or at the end', () => {
    expect(insertItemAfter(body, 0)).toBe('- [ ] Milk\n- [ ] \n- [x] Eggs\n- [ ] Bread');
    expect(insertItemAfter(body, null, 'Butter')).toBe(`${body}\n- [ ] Butter`);
    expect(insertItemAfter('', null, 'First')).toBe('- [ ] First');
  });

  it('removes one item', () => {
    expect(removeItem(body, 1)).toBe('- [ ] Milk\n- [ ] Bread');
    expect(removeItem('- [ ] Only', 0)).toBe('');
  });

  it('normalises a legacy body on any write', () => {
    expect(toggleItem('Ridge tiles.\n\nCall Ellis', 1)).toBe('- [ ] Ridge tiles.\n- [x] Call Ellis');
  });
});

describe('converting between prose and a checklist', () => {
  it('makes each paragraph one item, collapsing its line breaks', () => {
    const prose = 'Ridge tiles on the south\nslope have slipped.\n\n\nGet two   quotes.\n\n';
    expect(proseToChecklist(prose)).toBe(
      '- [ ] Ridge tiles on the south slope have slipped.\n- [ ] Get two quotes.',
    );
    expect(proseToChecklist('')).toBe('');
    expect(proseToChecklist('One\r\n\r\nTwo')).toBe('- [ ] One\n- [ ] Two');
  });

  it('makes each item a paragraph, done items keeping their words', () => {
    expect(checklistToProse('- [ ] Milk\n- [x] Eggs\n- [ ] ')).toBe('Milk\n\nEggs');
    expect(checklistToProse('')).toBe('');
  });

  it('round-trips a checklist through prose and back as open items', () => {
    const list = '- [ ] Milk\n- [x] Eggs';
    expect(proseToChecklist(checklistToProse(list))).toBe('- [ ] Milk\n- [ ] Eggs');
  });
});

describe('progress and the row snippet', () => {
  const items = parseChecklist('- [x] Milk\n- [x] Eggs\n- [ ] Bread\n- [ ] Butter');

  it('counts done over total', () => {
    expect(progressOf(items)).toEqual({ done: 2, total: 4 });
    expect(describeProgress(progressOf(items))).toBe('2 of 4 done');
    expect(describeProgress({ done: 0, total: 0 })).toBe('No items');
  });

  it('marks a count taken from a cut snippet as a floor', () => {
    expect(describeProgress({ done: 2, total: 4 }, true)).toBe('2 of 4+ done');
    expect(snippetIsCut('- [ ] Milk')).toBe(false);
    expect(snippetIsCut(`${'x'.repeat(500)}...`)).toBe(true);
    // Runes, not UTF-16 units: 500 Malayalam letters is not cut.
    expect(snippetIsCut('മ'.repeat(500))).toBe(false);
  });

  it('shows the open items as one line', () => {
    expect(openItemsText(items)).toBe('Bread · Butter');
    expect(openItemsText(parseChecklist('- [x] Done\n- [ ] '))).toBe('');
  });
});
