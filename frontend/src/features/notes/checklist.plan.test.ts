import { describe, expect, it } from 'vitest';

import {
  clampLevels,
  mergeItems,
  openBlock,
  parseChecklist,
  pasteItems,
  planLevel,
  planMove,
  previewDepths,
  splitItem,
  type Entry,
} from './checklist.ts';

/** The open rows as the editor shows them, with no tick held. */
function rows(items: ReturnType<typeof parseChecklist>): Entry[] {
  return items.map((item, index) => ({ item, index })).filter(({ item }) => !item.done);
}

/**
 * The Items tab's level planner, without a render: what a drag previews is
 * what release writes, and a refusal is a fixed sentence. The editor's own
 * tests drive the same functions through the grip and the keys.
 */
describe('planLevel', () => {
  it('nests one under the row above, refuses the first row, and says why a step cannot go', () => {
    const items = parseChecklist('- [ ] Milk\n- [ ] Bread\n  - [ ] Rye\n- [x] Eggs\n- [ ] Jam');
    const open = rows(items);
    expect(planLevel(items, open, 0, 1)).toEqual({ refusal: 'Nothing above to nest under' });
    expect(planLevel(items, open, 1, 1)).toEqual({ depth: 1 });
    expect(planLevel(items, open, 1, -1)).toEqual({ refusal: 'Already a top-level item' });
    // Rye is already one under Bread.
    expect(planLevel(items, open, 2, 1)).toEqual({ refusal: 'Already a sub-item' });
    // Jam's row above is Rye (the done Eggs sits in Done): two levels in fit.
    expect(planLevel(items, open, 3, 2)).toEqual({ depth: 2 });
    expect(planLevel(items, open, 3, 0)).toEqual({ refusal: '' });
  });

  it('refuses under a done row, and past the fourth level for the row’s own sub-items', () => {
    const done = parseChecklist('- [x] Party\n- [ ] Cake');
    expect(planLevel(done, rows(done), 0, 1)).toEqual({ refusal: 'Nothing above to nest under' });
    const held = [{ item: done[0]!, index: 0 }, { item: done[1]!, index: 1 }];
    expect(planLevel(done, held, 1, 1)).toEqual({ refusal: 'Cannot nest under a done item' });
    const deep = parseChecklist('- [ ] A\n- [ ] B\n  - [ ] C\n    - [ ] D\n      - [ ] E');
    expect(planLevel(deep, rows(deep), 1, 1)).toEqual({ refusal: 'Its sub-items are already four levels deep' });
  });
});

describe('clampLevels and previewDepths', () => {
  const items = parseChecklist('- [ ] Milk\n- [ ] Bread\n  - [ ] Rye\n- [ ] Jam');
  const open = rows(items);

  it('cuts a drag down to the largest move that fits, never below one', () => {
    expect(clampLevels(items, open, 3, 5)).toBe(2);
    expect(clampLevels(items, open, 3, -3)).toBe(-3);
    expect(clampLevels(items, open, 2, -3)).toBe(-1);
    // Not even one fits (Rye is as deep as Bread allows): it stays one, so
    // the release is refused and said; with nothing above it is left as asked.
    expect(clampLevels(items, open, 2, 3)).toBe(1);
    expect(clampLevels(items, open, 0, 2)).toBe(2);
  });

  it('previews the depths release would write, for the rows that change, and nothing for a refused drag', () => {
    expect(previewDepths('- [ ] Milk\n- [ ] Bread\n  - [ ] Rye\n- [ ] Jam', items, open, { id: '3', levels: 1 })).toEqual(
      new Map([[3, 1]]),
    );
    const parent = parseChecklist('- [ ] Milk\n- [ ] Bread\n  - [ ] Rye');
    // Bread goes under Milk and carries Rye with it.
    expect(previewDepths('- [ ] Milk\n- [ ] Bread\n  - [ ] Rye', parent, rows(parent), { id: '1', levels: 1 })).toEqual(
      new Map([
        [1, 1],
        [2, 2],
      ]),
    );
    expect(previewDepths('- [ ] Milk\n- [ ] Bread', items, open, { id: '0', levels: 1 })).toEqual(new Map());
    expect(previewDepths('- [ ] Milk', items, open, null)).toEqual(new Map());
  });
});

describe('planMove and openBlock', () => {
  const body = '- [ ] Party\n  - [ ] Cake\n- [ ] Milk\n- [ ] Bread';
  const items = parseChecklist(body);
  const open = rows(items);

  it('moves a parent past its own block and says when the slot changed its level', () => {
    expect(openBlock(items, open, 0)).toEqual([0, 1]);
    const down = planMove(body, items, open, 0, 1);
    expect(down).toEqual({ next: '- [ ] Milk\n- [ ] Party\n  - [ ] Cake\n- [ ] Bread', gripAt: 1, said: null });
    const under = planMove(body, items, open, 2, 1);
    expect(under).toEqual({ next: '- [ ] Party\n  - [ ] Milk\n  - [ ] Cake\n- [ ] Bread', gripAt: 1, said: 'Now a sub-item' });
  });

  it('refuses at either end, and says which', () => {
    expect(planMove(body, items, open, 0, -1)).toEqual({ refusal: 'Already at the top' });
    expect(planMove(body, items, open, 3, 4)).toEqual({ refusal: 'Already at the bottom' });
  });
});

/**
 * The field's own edits — a Backspace at the start, an Enter in the words, a
 * paste of several lines — as pure writes, without a render.
 */
describe('mergeItems', () => {
  it('joins the words onto the row above, says where the join is, and keeps the parent’s level', () => {
    const body = '- [ ] Party\n  - [ ] Plates\n    - [ ] Paper ones\n- [x] Eggs\n- [ ] Milk';
    // A sub-item into its parent: the parent stays top level, its words grow,
    // and the merged item's own sub-item comes up to hang from the parent.
    expect(mergeItems(body, 1, 0)).toEqual({
      next: '- [ ] PartyPlates\n  - [ ] Paper ones\n- [x] Eggs\n- [ ] Milk',
      caret: 5,
    });
    // Into the open row shown above, past a done line in the body.
    expect(mergeItems(body, 4, 2)).toEqual({
      next: '- [ ] Party\n  - [ ] Plates\n    - [ ] Paper onesMilk\n- [x] Eggs',
      caret: 10,
    });
    // A bad pair is a normalising no-op.
    expect(mergeItems(body, 0, 4).next).toBe(body);
  });
});

describe('splitItem', () => {
  it('cuts the words at the caret into two items at the same level, dropping a selection', () => {
    const body = '- [ ] Party\n  - [ ] Paper plates\n    - [ ] Big';
    expect(splitItem(body, 1, 6)).toBe('- [ ] Party\n  - [ ] Paper \n  - [ ] plates\n    - [ ] Big');
    expect(splitItem(body, 1, 5, 12)).toBe('- [ ] Party\n  - [ ] Paper\n  - [ ] \n    - [ ] Big');
    expect(splitItem(body, 1, 0)).toBe('- [ ] Party\n  - [ ] \n  - [ ] Paper plates\n    - [ ] Big');
  });
});

describe('pasteItems', () => {
  it('reads each pasted line by the line rule, at the item’s level, with the tail after the last', () => {
    const body = '- [ ] Party\n  - [ ] Plates and cups\n- [ ] Milk';
    // Pasted over " and " in "Plates and cups": "Plates" + "Paper", then the
    // rest, then "cups" after the last line; prefixes and levels respected.
    expect(pasteItems(body, 1, 6, 11, 'Paper\n- [x] Forks\n  - [ ] Spoons\n')).toEqual({
      next: '- [ ] Party\n  - [ ] PlatesPaper\n  - [x] Forks\n    - [ ] Spoonscups\n- [ ] Milk',
      focus: 3,
      caret: 6,
    });
    // An empty field takes the first line's mark; depth is clamped to four levels.
    const deep = '- [ ] A\n  - [ ] B\n    - [ ] C\n      - [ ] ';
    expect(pasteItems(deep, 3, 0, 0, '- [x] D\n  - [ ] E')).toEqual({
      next: '- [ ] A\n  - [ ] B\n    - [ ] C\n      - [x] D\n      - [ ] E',
      focus: 4,
      caret: 1,
    });
    // One line is the browser's paste.
    expect(pasteItems(body, 0, 0, 0, 'Cake')).toBeNull();
    expect(pasteItems(body, 0, 0, 0, 'Cake\n')).toBeNull();
  });
});
