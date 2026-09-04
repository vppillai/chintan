import { describe, expect, it } from 'vitest';

import { usage, usageEmpty } from '@/api/__fixtures__/responses.ts';

import {
  currentMonth,
  dayBars,
  dayLabel,
  formatAudioMinutes,
  formatCalls,
  formatDollars,
  monthLabel,
  opRows,
} from './usage.ts';

describe('money, from microdollars', () => {
  it('keeps three decimals under a dollar, where a month of this app lives', () => {
    expect(formatDollars(40_791)).toBe('$0.041');
    expect(formatDollars(1_371)).toBe('$0.001');
    expect(formatDollars(0)).toBe('$0.000');
  });

  it('drops to cents from a dollar up', () => {
    expect(formatDollars(1_000_000)).toBe('$1.00');
    expect(formatDollars(5_000_000)).toBe('$5.00');
    expect(formatDollars(12_345_678)).toBe('$12.35');
  });
});

describe('the other figures', () => {
  it('says minutes, to a tenth, and never seconds', () => {
    expect(formatAudioMinutes(1391.2)).toBe('23.2 min');
    expect(formatAudioMinutes(28.5)).toBe('0.5 min');
    expect(formatAudioMinutes(undefined)).toBe('0.0 min');
  });

  it('counts calls in words', () => {
    expect(formatCalls(1)).toBe('1 call');
    expect(formatCalls(118)).toBe('118 calls');
  });

  it('keeps the calendar in UTC, as the API does', () => {
    // 23:30 on the 31st in UTC-5 is already September in UTC.
    expect(currentMonth(new Date('2026-09-01T04:30:00Z'))).toBe('2026-09');
    expect(monthLabel('2026-09')).toMatch(/September 2026/);
    expect(dayLabel('2026-09-04')).toMatch(/4/);
    expect(dayLabel('2026-09-04')).toMatch(/Sep/);
  });
});

describe('a bar per day of the month', () => {
  it('fills the calendar, zero where the API sent no row, and marks today', () => {
    // The fixture month is January 2026 with rows on the 3rd and 4th only.
    const bars = dayBars(usage, new Date('2026-01-04T12:00:00Z'));

    expect(bars).toHaveLength(31);
    expect(bars[0]).toEqual({ date: '2026-01-01', costMicros: 0, calls: 0, today: false });
    expect(bars[2]).toMatchObject({ date: '2026-01-03', costMicros: 951, calls: 2, today: false });
    expect(bars[3]).toMatchObject({ date: '2026-01-04', costMicros: 420, calls: 1, today: true });
    expect(bars.filter((bar) => bar.today)).toHaveLength(1);
  });

  it('marks no day as today when looking at another month', () => {
    const bars = dayBars(usage, new Date('2026-09-04T12:00:00Z'));
    expect(bars.some((bar) => bar.today)).toBe(false);
  });

  it('knows how long each month is', () => {
    expect(dayBars({ ...usageEmpty, month: '2026-02' })).toHaveLength(28);
    expect(dayBars({ ...usageEmpty, month: '2028-02' })).toHaveLength(29);
    expect(dayBars({ ...usageEmpty, month: '2026-04' })).toHaveLength(30);
  });
});

describe('the split by stage', () => {
  it('lists the stages in the order a recording meets them, only those that ran', () => {
    expect(opRows(usage.ops).map((row) => row.label)).toEqual(['Transcribe', 'Route', 'Clean up']);
    expect(opRows({ cleanup: { cost_micros: 1, calls: 1 } }).map((row) => row.key)).toEqual([
      'cleanup',
    ]);
    expect(opRows(usageEmpty.ops)).toEqual([]);
  });

  it('still shows a stage the API adds later, rather than dropping its cost', () => {
    const rows = opRows({ summarise: { cost_micros: 7, calls: 1 } });
    expect(rows).toHaveLength(1);
    expect(rows[0]?.label).toBe('summarise');
  });
});
