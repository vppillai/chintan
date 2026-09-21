import { describe, expect, it } from 'vitest';

import {
  checklistToProse,
  describeProgress,
  insertItemAfter,
  openItemsText,
  parseChecklist,
  progressOf,
  proseToChecklist,
  removeItem,
  serialiseChecklist,
  setItemText,
  snippetIsCut,
  toggleItem,
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
      expect(parseChecklist(body)).toEqual(items);
    });
  }

  it('serialises back to one normalised line per item', () => {
    const body = 'Ridge tiles have slipped.\n\n- [X] Call Ellis\r\n- [ ] Milk';
    expect(serialiseChecklist(parseChecklist(body))).toBe(
      '- [ ] Ridge tiles have slipped.\n- [x] Call Ellis\n- [ ] Milk',
    );
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
