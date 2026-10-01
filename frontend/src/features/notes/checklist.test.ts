import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  MAX_DEPTH,
  blockOf,
  canNest,
  checklistToProse,
  describeProgress,
  insertItemAfter,
  moveItem,
  nestUnder,
  openItemsText,
  parseChecklist,
  progressOf,
  proseToChecklist,
  removeDone,
  removeItem,
  serialiseChecklist,
  setItemText,
  shiftLevel,
  snippetIsCut,
  toggleItem,
  uncheckAll,
  unnest,
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
    // Three levels (round 8, F1): a fourth written elsewhere reads as the
    // third and flattens to it on the first save here.
    expect(MAX_DEPTH).toBe(2);
    expect(parseChecklist('- [ ] A\n  - [ ] B\n    - [ ] C').map((item) => item.depth)).toEqual([0, 1, 2]);
    expect(serialiseChecklist(parseChecklist('- [ ] A\n  - [ ] B\n    - [ ] C\n      - [ ] D'))).toBe(
      '- [ ] A\n  - [ ] B\n    - [ ] C\n    - [ ] D',
    );
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

  it('a new item after a parent is its first sub-item, not a top-level line that would take them', () => {
    expect(insertItemAfter('- [ ] Party\n  - [ ] Plates\n  - [ ] Cups', 0)).toBe(
      '- [ ] Party\n  - [ ] \n  - [ ] Plates\n  - [ ] Cups',
    );
    // A parent whose only sub-item is done is still a parent.
    expect(insertItemAfter('- [ ] Party\n  - [x] Plates\n- [ ] Bread', 0, 'Cups')).toBe(
      '- [ ] Party\n  - [ ] Cups\n  - [x] Plates\n- [ ] Bread',
    );
    // Not a parent: the same level, as before.
    expect(insertItemAfter('- [ ] Party\n- [ ] Bread', 0)).toBe('- [ ] Party\n- [ ] \n- [ ] Bread');
  });

  it('removing a parent brings its sub-items up a level rather than under the first of them', () => {
    expect(removeItem('- [ ] Party\n  - [ ] Plates\n  - [x] Cups\n- [ ] Bread', 0)).toBe(
      '- [ ] Plates\n- [x] Cups\n- [ ] Bread',
    );
    // A sub-item's removal moves nothing else.
    expect(removeItem(NESTED, 1)).toBe('- [ ] Party\n  - [x] Candles\n- [x] Eggs\n- [ ] Bread');
  });

  it('nothing nests under a done row, or beside an open sub-item of a done parent', () => {
    // A body written elsewhere: a done parent over an open sub-item. Tab on
    // Bread must not make it a second one through the open sibling.
    const odd = parseChecklist('- [x] Party\n  - [ ] Plates\n- [ ] Bread');
    expect(canNest(odd, 2, 1)).toBe(false);
    expect(nestUnder('- [x] Party\n  - [ ] Plates\n- [ ] Bread', 2, 1)).toBe('- [x] Party\n  - [ ] Plates\n- [ ] Bread');
    // Nor under a done row itself — the row above during the tick's beat.
    expect(canNest(parseChecklist('- [x] Milk\n- [ ] Bread'), 1, 0)).toBe(false);
  });

  it('a block is the item and every item nested under it', () => {
    const items = parseChecklist(NESTED);
    expect(blockOf(items, 0)).toEqual([0, 1, 2]);
    expect(blockOf(items, 1)).toEqual([1]);
    expect(blockOf(items, 4)).toEqual([4]);
    expect(blockOf(items, 9)).toEqual([9]);
  });

  it('nests an item under the row above it, one level at most, and the first item never', () => {
    expect(nestUnder('- [ ] A\n- [ ] B', 1, 0)).toBe('- [ ] A\n  - [ ] B');
    expect(canNest(parseChecklist('- [ ] A\n- [ ] B'), 1, 0)).toBe(true);
    // Under a sub-item: a sibling, under the same parent, right after the row it nests under.
    expect(nestUnder(NESTED, 4, 1)).toBe('- [ ] Party\n  - [ ] Plates\n  - [ ] Bread\n  - [x] Candles\n- [x] Eggs');
    // A sub-item under a sibling sub-item: its child, the third level.
    expect(canNest(parseChecklist(NESTED), 2, 1)).toBe(true);
    expect(nestUnder(NESTED, 2, 1)).toBe('- [ ] Party\n  - [ ] Plates\n    - [x] Candles\n- [x] Eggs\n- [ ] Bread');
    // Already as deep as the row above allows: one under it at most.
    expect(canNest(parseChecklist(NESTED), 1, 0)).toBe(false);
    // Nothing above the first item; `under` must stand above; a bad index.
    expect(canNest(parseChecklist(NESTED), 0, 0)).toBe(false);
    expect(nestUnder(NESTED, 0, 4)).toBe(NESTED);
    expect(nestUnder(NESTED, 9, 0)).toBe(NESTED);
    expect(nestUnder('Milk\n\nEggs', 0, 0)).toBe('- [ ] Milk\n- [ ] Eggs');
  });

  it('nests under the row a person sees above: done lines between them slip below', () => {
    // Milk and Bread are the open rows; Eggs sits in Done. Bread under Milk
    // is a child of Milk in the body, not of the done Eggs line.
    expect(nestUnder('- [ ] Milk\n- [x] Eggs\n- [ ] Bread', 2, 0)).toBe('- [ ] Milk\n  - [ ] Bread\n- [x] Eggs');
    // After the parent's whole block, done sub-items included.
    expect(nestUnder('- [ ] Party\n  - [x] Plates\n- [x] Eggs\n- [ ] Bread', 3, 0)).toBe(
      '- [ ] Party\n  - [x] Plates\n  - [ ] Bread\n- [x] Eggs',
    );
    // Already in place: only the depth changes.
    expect(nestUnder('- [ ] Party\n  - [x] Plates\n- [ ] Bread', 2, 0)).toBe('- [ ] Party\n  - [x] Plates\n  - [ ] Bread');
  });

  it('brings an item up a level in place, and the sub-items that followed become its own', () => {
    expect(unnest(NESTED, 1)).toBe('- [ ] Party\n- [ ] Plates\n  - [x] Candles\n- [x] Eggs\n- [ ] Bread');
    expect(unnest(NESTED, 2)).toBe('- [ ] Party\n  - [ ] Plates\n- [x] Candles\n- [x] Eggs\n- [ ] Bread');
    expect(unnest(NESTED, 0)).toBe(NESTED);
    expect(unnest(NESTED, 9)).toBe(NESTED);
  });

  it('a parent made a sub-item takes its children one level down with it', () => {
    expect(nestUnder('- [ ] A\n- [ ] P\n  - [ ] C', 1, 0)).toBe('- [ ] A\n  - [ ] P\n    - [ ] C');
    // The same when a parent is dropped on a sub-item's slot.
    expect(moveItem('- [ ] A\n  - [ ] B\n- [ ] P\n  - [ ] C', 2, 1)).toBe('- [ ] A\n  - [ ] P\n    - [ ] C\n  - [ ] B');
  });

  it('ticking a parent ticks its sub-items; reopening a sub-item reopens its parent; reopening a parent leaves them', () => {
    expect(toggleItem(NESTED, 0)).toBe('- [x] Party\n  - [x] Plates\n  - [x] Candles\n- [x] Eggs\n- [ ] Bread');
    expect(toggleItem('- [x] P\n  - [x] C\n  - [x] D', 1)).toBe('- [ ] P\n  - [ ] C\n  - [x] D');
    expect(toggleItem('- [x] P\n  - [x] C', 0)).toBe('- [ ] P\n  - [x] C');
    // A sub-item ticked on its own leaves its parent open.
    expect(toggleItem(NESTED, 1)).toBe('- [ ] Party\n  - [x] Plates\n  - [x] Candles\n- [x] Eggs\n- [ ] Bread');
  });

  it('prose conversion flattens: the indent has no notation in prose', () => {
    expect(checklistToProse(NESTED)).toBe('Party\n\nPlates\n\nCandles\n\nEggs\n\nBread');
    expect(proseToChecklist('One\n\nTwo')).toBe('- [ ] One\n- [ ] Two');
  });
});

// Three levels (round 8, F1).
const COSTCO = '- [ ] Costco\n  - [ ] Meat\n- [ ] Rice';
const DEEP = '- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n    - [ ] Cups\n  - [ ] Candles\n- [ ] Milk';

describe('three levels', () => {
  it('one step in under a sub-item is its sibling; two steps is its child', () => {
    // Rice (top level) under Meat (a sub-item): +1 is Meat's sibling under Costco, not Meat's child.
    expect(shiftLevel(COSTCO, 2, 1, 1)).toBe('- [ ] Costco\n  - [ ] Meat\n  - [ ] Rice');
    expect(shiftLevel(COSTCO, 2, 2, 1)).toBe('- [ ] Costco\n  - [ ] Meat\n    - [ ] Rice');
    // Several steps past what the row above allows stop at one under it.
    expect(shiftLevel('- [ ] A\n- [ ] B', 1, 2, 0)).toBe('- [ ] A\n  - [ ] B');
  });

  it('refuses an indent with no row above, past the row above, under a done row, or that pushes a sub-item past the third level', () => {
    const items = parseChecklist(COSTCO);
    expect(canNest(items, 0, 0)).toBe(false);
    expect(shiftLevel(COSTCO, 0, 1, 0)).toBe(COSTCO);
    // Meat is already one under Costco.
    expect(canNest(items, 1, 0)).toBe(false);
    // A done row above, or a done row it stands under.
    expect(canNest(parseChecklist('- [x] Costco\n  - [ ] Meat\n- [ ] Rice'), 2, 1)).toBe(false);
    expect(canNest(parseChecklist('- [ ] Costco\n  - [x] Meat\n- [ ] Rice'), 2, 1)).toBe(false);
    // Party's block is three levels deep already: one step in would make Plates a fourth.
    const deep = `- [ ] Top\n${DEEP}`;
    expect(canNest(parseChecklist(deep), 1, 0)).toBe(false);
    expect(shiftLevel(deep, 1, 1, 0)).toBe(deep);
    // Costco has a sub-item, so under Sub it may go one level in, not two.
    const two = '- [ ] Top\n  - [ ] Sub\n- [ ] Costco\n  - [ ] Meat';
    expect(canNest(parseChecklist(two), 2, 1, 2)).toBe(false);
    expect(shiftLevel(two, 2, 2, 1)).toBe(two);
    expect(shiftLevel(two, 2, 1, 1)).toBe('- [ ] Top\n  - [ ] Sub\n  - [ ] Costco\n    - [ ] Meat');
  });

  it('two steps out, in place, and the sub-items that followed become its own', () => {
    expect(shiftLevel(DEEP, 2, -2, 1)).toBe('- [ ] Party\n  - [ ] Costco\n- [ ] Plates\n  - [ ] Cups\n  - [ ] Candles\n- [ ] Milk');
    expect(shiftLevel(DEEP, 0, -1, 0)).toBe(DEEP);
  });

  it('reopening a third-level item reopens both items it stands under; ticking the top ticks all three levels', () => {
    const done = '- [x] Party\n  - [x] Costco\n    - [x] Plates\n- [ ] Milk';
    expect(toggleItem(done, 2)).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n- [ ] Milk');
    expect(toggleItem(DEEP, 0)).toBe(
      '- [x] Party\n  - [x] Costco\n    - [x] Plates\n    - [x] Cups\n  - [x] Candles\n- [ ] Milk',
    );
  });

  it('a three-level block dropped on a second-level slot is clamped, never dropped', () => {
    expect(moveItem(`${DEEP}\n- [ ] Top\n  - [ ] Sub`, 0, 7)).toBe(
      '- [ ] Milk\n- [ ] Top\n  - [ ] Sub\n  - [ ] Party\n    - [ ] Costco\n    - [ ] Plates\n    - [ ] Cups\n    - [ ] Candles',
    );
  });

  it('removing a parent lifts every level under it by one', () => {
    expect(removeItem(DEEP, 0)).toBe('- [ ] Costco\n  - [ ] Plates\n  - [ ] Cups\n- [ ] Candles\n- [ ] Milk');
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

// The editor and the Go readers (cleanup.ParseLine) read body lines by one
// rule; the backend's fixture is that rule by example, and the Go suite reads
// the same file (R7-19). Resolved from the Vitest root, as
// contract-requests.test.ts does, because import.meta.url is not a file: URL
// under the transform.
describe('the checklist line rule shared with the backend', () => {
  const fixture = JSON.parse(
    readFileSync(
      join(process.cwd(), '..', 'backend', 'internal', 'cleanup', 'testdata', 'checklist-lines.json'),
      'utf8',
    ),
  ) as {
    max_depth: number;
    cases: { name: string; body: string; items: { text: string; done: boolean; depth: number }[] }[];
  };

  it('has cases', () => {
    expect(fixture.cases.length).toBeGreaterThan(0);
  });
  // The drift guard for the depth: the Go suite asserts the same number
  // against cleanup.MaxDepth.
  it('has the same maximum depth', () => {
    expect(fixture.max_depth).toBe(MAX_DEPTH);
  });
  for (const { name, body, items } of fixture.cases) {
    it(name, () => {
      expect(parseChecklist(body)).toEqual(items);
    });
  }
});
