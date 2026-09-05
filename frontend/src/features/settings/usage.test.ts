import { describe, expect, it } from 'vitest';

import { usage, usageEmpty } from '@/api/__fixtures__/responses.ts';

import {
  asOfLabel,
  combinedMicros,
  currentMonth,
  dayBars,
  dayLabel,
  formatAudioMinutes,
  formatCalls,
  formatCount,
  formatDollars,
  formatMegabytes,
  formatRequests,
  monthLabel,
  opRows,
  providerRows,
  totalBasis,
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

  it('counts requests, recordings and notes in words too', () => {
    expect(formatRequests(1)).toBe('1 request');
    expect(formatRequests(312)).toBe('312 requests');
    expect(formatCount(41, 'recording')).toBe('41 recordings');
    expect(formatCount(1, 'note')).toBe('1 note');
  });

  it('says megabytes to a tenth, and never bytes or gigabytes', () => {
    expect(formatMegabytes(9_123_456)).toBe('9.1 MB');
    expect(formatMegabytes(400_000)).toBe('0.4 MB');
    expect(formatMegabytes(0)).toBe('0.0 MB');
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
    expect(bars[0]).toEqual({
      date: '2026-01-01',
      costMicros: 0,
      calls: 0,
      apiRequests: 0,
      today: false,
    });
    expect(bars[2]).toMatchObject({
      date: '2026-01-03',
      costMicros: 951,
      calls: 2,
      apiRequests: 12,
      today: false,
    });
    expect(bars[3]).toMatchObject({ date: '2026-01-04', costMicros: 1770, calls: 3, today: true });
    expect(bars.filter((bar) => bar.today)).toHaveLength(1);
  });

  it('marks no day as today when looking at another month', () => {
    const bars = dayBars(usage, new Date('2026-09-04T12:00:00Z'));
    expect(bars.some((bar) => bar.today)).toBe(false);
  });

  it('reads a day from a backend that does not count requests as zero of them', () => {
    const { api_requests: _counted, ...day } = usage.days[0]!;
    const bars = dayBars({ ...usage, days: [day] });
    expect(bars[2]).toMatchObject({ date: '2026-01-03', costMicros: 951, apiRequests: 0 });
  });

  it('knows how long each month is', () => {
    expect(dayBars({ ...usageEmpty, month: '2026-02' })).toHaveLength(28);
    expect(dayBars({ ...usageEmpty, month: '2028-02' })).toHaveLength(29);
    expect(dayBars({ ...usageEmpty, month: '2026-04' })).toHaveLength(30);
  });
});

describe('the split by stage', () => {
  it('lists the stages in the order a recording meets them, then the hand-asked calls, only those that ran', () => {
    expect(opRows(usage.ops).map((row) => row.label)).toEqual([
      'Transcribe',
      'Route',
      'Clean up',
      'Clean note',
      'Ask',
    ]);
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

describe('the split by provider', () => {
  it('names the providers as they write their names, the biggest bill first', () => {
    const rows = providerRows(usage.providers);
    expect(rows.map((row) => row.label)).toEqual(['MiniMax', 'Groq']);
    expect(rows[0]?.totals.cost_micros).toBe(2410);
  });

  it('shows a provider it has no name for by its wire name, capitalised', () => {
    expect(providerRows({ deepgram: { cost_micros: 5, calls: 1 } })[0]?.label).toBe('Deepgram');
  });

  it('is empty for a backend that does not split by provider, and for a quiet month', () => {
    expect(providerRows(undefined)).toEqual([]);
    expect(providerRows({})).toEqual([]);
  });
});

describe('the AWS line', () => {
  const now = new Date('2026-09-04T12:00:00Z');

  it('says roughly how old the figure is, in coarse steps, because it is read once a day', () => {
    expect(asOfLabel('2026-09-04T11:59:30Z', now)).toBe('as of a moment ago');
    expect(asOfLabel('2026-09-04T11:40:00Z', now)).toBe('as of 20 minutes ago');
    expect(asOfLabel('2026-09-04T11:00:00Z', now)).toBe('as of an hour ago');
    expect(asOfLabel('2026-09-04T09:00:00Z', now)).toBe('as of 3 hours ago');
    expect(asOfLabel('2026-09-03T12:00:00Z', now)).toBe('as of yesterday');
    expect(asOfLabel('2026-09-01T12:00:00Z', now)).toBe('as of 3 days ago');
  });

  it('shows an unreadable timestamp as sent rather than dropping it', () => {
    expect(asOfLabel('soon', now)).toBe('as of soon');
  });

  it('adds the instance AWS figure to the providers when that is all there is', () => {
    const aws = { month_micros: 3_120_000, as_of: '2026-09-04T09:00:00Z', budget_micros: null };
    expect(totalBasis(aws)).toBe('instance');
    expect(combinedMicros({ ...usage, aws })).toBe(3_122_721);
    expect(combinedMicros({ ...usage, aws: null })).toBeNull();
    expect(totalBasis(null)).toBeNull();
    // A backend from before the field existed: the generated fixture now
    // carries a reading, so the older shape is built by dropping the field.
    const { aws: _recorded, ...older } = usage;
    expect(combinedMicros(older)).toBeNull();
  });

  it('adds the user’s own share instead, when the backend has apportioned one', () => {
    // The fixture: 2,721 of providers plus a 123,456 share, not the 2,345,678 instance bill.
    expect(totalBasis(usage.aws)).toBe('share');
    expect(combinedMicros(usage)).toBe(126_177);
    // A share of nothing is still a share — a user who spent nothing owes nothing.
    const aws = { ...usage.aws!, share_micros: 0 };
    expect(combinedMicros({ ...usage, aws })).toBe(2_721);
    // Null is the backend saying it could not apportion, which is the instance figure again.
    expect(totalBasis({ ...aws, share_micros: null, share_basis: null })).toBe('instance');
  });
});
