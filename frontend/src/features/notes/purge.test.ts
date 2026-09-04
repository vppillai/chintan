import { describe, expect, it } from 'vitest';

import { describePurge, purgeCountdown } from './purge.ts';

/**
 * The failure these guard against is "Deletes in NaN days", which is what
 * `Math.ceil((Date.parse(undefined) - now) / 86400000)` renders. Every branch
 * that can produce it is a case below.
 */

const NOW = Date.parse('2026-08-08T12:00:00.000Z');

describe('purgeCountdown', () => {
  it('counts whole days to the purge date', () => {
    expect(purgeCountdown('2026-08-18T12:00:00.000Z', NOW)).toEqual({ kind: 'days', days: 10 });
  });

  it('rounds a part-day up, so a note with hours left never reads as zero', () => {
    expect(purgeCountdown('2026-08-08T23:00:00.000Z', NOW)).toEqual({ kind: 'days', days: 1 });
  });

  it('reports a missing purge date as its own state, not as a number', () => {
    expect(purgeCountdown(undefined, NOW)).toEqual({ kind: 'none' });
    expect(purgeCountdown(null, NOW)).toEqual({ kind: 'none' });
    expect(purgeCountdown('', NOW)).toEqual({ kind: 'none' });
  });

  it('reports an unparseable date as missing rather than as NaN days', () => {
    expect(purgeCountdown('not-a-date', NOW)).toEqual({ kind: 'none' });
  });

  it('reports a date already past as due', () => {
    expect(purgeCountdown('2026-08-01T12:00:00.000Z', NOW)).toEqual({ kind: 'due' });
  });
});

describe('describePurge', () => {
  it('never renders NaN, for any input the contract allows', () => {
    for (const value of [undefined, null, '', 'not-a-date', '2026-08-18T12:00:00.000Z']) {
      expect(describePurge(purgeCountdown(value, NOW))).not.toContain('NaN');
    }
  });

  it('says plainly when there is no date', () => {
    expect(describePurge({ kind: 'none' })).toBe('No deletion date');
  });

  it('agrees with itself about singular and plural', () => {
    expect(describePurge({ kind: 'days', days: 1 })).toBe('Deletes in 1 day');
    expect(describePurge({ kind: 'days', days: 2 })).toBe('Deletes in 2 days');
  });
});
