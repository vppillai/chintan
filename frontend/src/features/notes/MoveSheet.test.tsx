import { describe, expect, it } from 'vitest';

import { formatRowTime } from './groups.ts';
import { optionMeta } from './MoveSheet.tsx';

/**
 * The line under each Move option: what tells five "Voice note …" rows apart
 * (review 2026-09-21, T42) — when the note was touched, how its text begins,
 * and the tags the line showed before.
 */
describe('a Move option’s meta line', () => {
  const at = '2026-08-06T09:14:00.000Z';
  const when = formatRowTime(at);

  it('cuts prose at forty characters on a trimmed edge, then the tags', () => {
    expect(
      optionMeta({
        updated_at: at,
        snippet: 'Ridge tiles on the south slope have slipped.  Two quotes before the rain.',
        tags: ['house'],
      }),
    ).toBe(`${when} · Ridge tiles on the south slope have slip… · house`);
  });

  it('says a checklist’s open items, not its syntax', () => {
    expect(
      optionMeta({
        updated_at: at,
        kind: 'checklist',
        snippet: '- [ ] Call Ellis\n- [x] Buy tiles\n- [ ] Book the scaffold',
      }),
    ).toBe(`${when} · Call Ellis · Book the scaffold`);
  });

  it('is only the date for a note with no text', () => {
    expect(optionMeta({ updated_at: at, snippet: '' })).toBe(when);
    expect(optionMeta({ updated_at: at })).toBe(when);
  });
});
