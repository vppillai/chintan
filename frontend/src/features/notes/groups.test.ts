import { describe, expect, it } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';

import {
  describeMoment,
  describeRecordings,
  formatDurationShort,
  formatRowTime,
  groupByDay,
  groupLabel,
} from './groups.ts';

/** A Thursday afternoon, local time, so weekday and month boundaries are fixed. */
const NOW = new Date(2026, 8, 3, 14, 30).getTime();

function at(daysAgo: number, hour = 9): string {
  const date = new Date(NOW);
  date.setDate(date.getDate() - daysAgo);
  date.setHours(hour, 5, 0, 0);
  return date.toISOString();
}

function note(id: string, updatedAt: string): NoteWire {
  return { id, title: id, updated_at: updatedAt, version: 1, archived: false };
}

describe('groupLabel', () => {
  it('names today and yesterday', () => {
    expect(groupLabel(at(0), NOW)).toBe('Today');
    expect(groupLabel(at(1), NOW)).toBe('Yesterday');
  });

  it('uses the weekday for the rest of the last week', () => {
    // 3 September 2026 is a Thursday; two days back is Tuesday.
    expect(groupLabel(at(2), NOW)).toBe('Tuesday');
    expect(groupLabel(at(6), NOW)).toBe('Friday');
  });

  it('falls back to the month a week or more ago, within the year', () => {
    expect(groupLabel(at(7), NOW)).toBe('August');
    expect(groupLabel(new Date(2026, 0, 15).toISOString(), NOW)).toBe('January');
  });

  it('uses the year for anything older than that', () => {
    expect(groupLabel(new Date(2025, 11, 30).toISOString(), NOW)).toBe('2025');
  });

  it('is decided by the local calendar day, not by 24-hour windows', () => {
    // Just after midnight this morning is still "Today", however few hours ago.
    expect(groupLabel(at(0, 0), NOW)).toBe('Today');
    // Late last night is "Yesterday" even though it is under 24 hours away.
    expect(groupLabel(at(1, 23), NOW)).toBe('Yesterday');
  });

  it('does not throw on garbage', () => {
    expect(groupLabel('not a date', NOW)).toBe('Undated');
  });
});

describe('groupByDay', () => {
  it('groups newest first and keeps each group in order', () => {
    const groups = groupByDay(
      [note('old', at(40)), note('today-early', at(0, 8)), note('today-late', at(0, 12)), note('yesterday', at(1))],
      NOW,
    );
    expect(groups.map((group) => group.label)).toEqual(['Today', 'Yesterday', 'July']);
    expect(groups[0]?.notes.map((n) => n.id)).toEqual(['today-late', 'today-early']);
  });

  it('is empty for no notes', () => {
    expect(groupByDay([], NOW)).toEqual([]);
  });
});

describe('formatRowTime', () => {
  it('shows a clock time for today', () => {
    expect(formatRowTime(at(0, 14), NOW)).toMatch(/^\d{2}:\d{2}/);
  });

  it('shows a date without the year for this year', () => {
    const text = formatRowTime(at(30), NOW);
    expect(text).toMatch(/Aug/);
    expect(text).not.toMatch(/2026/);
  });

  it('adds the year for older notes', () => {
    expect(formatRowTime(new Date(2025, 11, 30).toISOString(), NOW)).toMatch(/2025/);
  });

  it('is empty rather than the raw string for garbage', () => {
    expect(formatRowTime('nope', NOW)).toBe('');
  });
});

describe('describeRecordings', () => {
  it('is absent when the payload has no captures, as the list endpoint does not', () => {
    expect(describeRecordings(note('a', at(0)))).toBeNull();
    expect(describeRecordings({ ...note('a', at(0)), captures: [] })).toBeNull();
  });

  it('counts and totals the recordings when they are present', () => {
    expect(
      describeRecordings({
        ...note('a', at(0)),
        captures: [{ duration_ms: 72_000 }, { duration_ms: 180_000 }, { duration_ms: null }],
      }),
    ).toBe('3 recordings · 4:12');
    expect(describeRecordings({ ...note('a', at(0)), captures: [{ duration_ms: 36_000 }] })).toBe(
      '1 recording · 0:36',
    );
  });

  it('leaves the duration off when none is known', () => {
    expect(describeRecordings({ ...note('a', at(0)), captures: [{}] })).toBe('1 recording');
  });
});

describe('describeMoment', () => {
  it('names the day and always the time, so two recordings on one day differ', () => {
    // The time is whatever the device's locale writes — "14:05" or "2:05 PM".
    const clock = (iso: string) =>
      new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' }).format(
        new Date(iso),
      );
    expect(describeMoment(at(0, 14), NOW)).toBe(`Today ${clock(at(0, 14))}`);
    expect(describeMoment(at(1, 18), NOW)).toBe(`Yesterday ${clock(at(1, 18))}`);
    // The date from `formatRowTime`, then the time.
    expect(describeMoment(at(9, 7), NOW)).toBe(`${formatRowTime(at(9, 7), NOW)} ${clock(at(9, 7))}`);
  });

  it('is empty for an unparseable timestamp rather than the raw string', () => {
    expect(describeMoment('not a date', NOW)).toBe('');
  });
});

describe('formatDurationShort', () => {
  it('pads seconds', () => {
    expect(formatDurationShort(41_000)).toBe('0:41');
    expect(formatDurationShort(665_000)).toBe('11:05');
  });
});
