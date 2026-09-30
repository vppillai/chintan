import { describe, expect, it } from 'vitest';

import { addedItems, addedRange } from './NoteDetailScreen.tsx';

describe('what a recording added, for Show (R7-6b)', () => {
  it('finds the paragraph appended to the body', () => {
    const body = 'One.\n\nTwo.\n\nThree.';
    expect(addedRange('One.\n\nTwo.', body)).toEqual({ start: body.indexOf('Three.'), end: body.length });
  });

  it('falls back to the last paragraph when the body changed some other way meanwhile', () => {
    const body = 'One, edited.\n\nThree.\n';
    expect(addedRange('One.', body)).toEqual({ start: body.indexOf('Three.'), end: body.indexOf('Three.') + 6 });
    expect(addedRange('x', '')).toBeNull();
  });

  it('names the open items a checklist recording merged in', () => {
    expect([...addedItems('- [ ] Milk\n- [x] Eggs', '- [ ] Milk\n- [ ] Bread\n  - [ ] Rye\n- [x] Eggs')]).toEqual([
      'Bread',
      'Rye',
    ]);
  });
});
