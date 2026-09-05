/**
 * Shaping `GET /v1/usage` for the You screen. Pure, so the arithmetic and the
 * calendar are testable with a fixed clock.
 *
 * Everything the API meters is in microdollars — the unit the spend cap uses —
 * and people think in dollars, so the conversion happens exactly here.
 */

import type { UsageAwsWire, UsageTotalsWire, UsageWire } from '@/api/schema.ts';

const MICROS_PER_DOLLAR = 1_000_000;

/**
 * `$0.041` under a dollar, `$1.23` from a dollar up.
 *
 * Three decimals below a dollar because a month of this app costs cents, and
 * "$0.04" hides the difference between a quiet week and a busy one; two above
 * because nobody reads tenths of a cent on a real bill.
 */
export function formatDollars(micros: number): string {
  const dollars = Math.max(0, micros) / MICROS_PER_DOLLAR;
  return `$${dollars.toFixed(dollars >= 1 ? 2 : 3)}`;
}

/** `23.2 min`, `0.5 min`; never seconds, because the figure sits beside costs and calls. */
export function formatAudioMinutes(seconds: number | undefined): string {
  const minutes = (seconds ?? 0) / 60;
  return `${minutes.toFixed(1)} min`;
}

export function formatCalls(calls: number): string {
  return `${String(calls)} ${calls === 1 ? 'call' : 'calls'}`;
}

/** `yyyy-mm`, UTC — the calendar the API keeps its rows in. */
export function currentMonth(now: Date = new Date()): string {
  return `${String(now.getUTCFullYear())}-${String(now.getUTCMonth() + 1).padStart(2, '0')}`;
}

/** `September 2026`, from a `yyyy-mm`. */
export function monthLabel(month: string): string {
  const at = new Date(`${month}-01T00:00:00Z`);
  if (Number.isNaN(at.getTime())) return month;
  return new Intl.DateTimeFormat(undefined, {
    month: 'long',
    year: 'numeric',
    timeZone: 'UTC',
  }).format(at);
}

/** `4 Sep`, from a `yyyy-mm-dd`. */
export function dayLabel(date: string): string {
  const at = new Date(`${date}T00:00:00Z`);
  if (Number.isNaN(at.getTime())) return date;
  return new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short', timeZone: 'UTC' }).format(
    at,
  );
}

/**
 * `as of 3 hours ago`, for the AWS figure's timestamp.
 *
 * The worker reads the Budget once a day, so the figure on screen can be most
 * of a day old and the reader should know roughly how old — but not to the
 * minute, which is why the steps are coarse. An unparseable timestamp is
 * shown as sent rather than dropped.
 */
export function asOfLabel(iso: string, now: Date = new Date()): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return `as of ${iso}`;
  const seconds = Math.max(0, Math.round((now.getTime() - at.getTime()) / 1000));
  if (seconds < 90) return 'as of a moment ago';
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `as of ${String(minutes)} minutes ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return hours === 1 ? 'as of an hour ago' : `as of ${String(hours)} hours ago`;
  const days = Math.round(hours / 24);
  return days === 1 ? 'as of yesterday' : `as of ${String(days)} days ago`;
}

/**
 * Providers plus AWS, when AWS has been recorded; `null` otherwise, so the
 * screen leaves the Total line out rather than repeat the providers' figure
 * under a heading that promises more.
 */
export function combinedMicros(usage: UsageWire, aws: UsageAwsWire | null | undefined = usage.aws): number | null {
  if (!aws) return null;
  return usage.cost_micros + aws.month_micros;
}

export interface DayBar {
  date: string;
  costMicros: number;
  calls: number;
  today: boolean;
}

function daysIn(month: string): number {
  const [year, monthIndex] = month.split('-').map(Number);
  if (!year || !monthIndex) return 0;
  return new Date(Date.UTC(year, monthIndex, 0)).getUTCDate();
}

/**
 * One bar per calendar day of the month, in order, zero where the API sent no
 * row. The API lists only days with usage; a chart of those alone would put
 * the 3rd next to the 19th and read as a streak. Future days are drawn empty
 * so the strip also shows how far into the month we are.
 */
export function dayBars(usage: UsageWire, now: Date = new Date()): DayBar[] {
  // `days` is required by contract; a stub or an older backend answering
  // without it must not take the You screen down with it.
  const byDate = new Map((usage.days ?? []).map((day) => [day.date, day]));
  const today = `${currentMonth(now)}-${String(now.getUTCDate()).padStart(2, '0')}`;
  const count = daysIn(usage.month);
  const bars: DayBar[] = [];
  for (let day = 1; day <= count; day += 1) {
    const date = `${usage.month}-${String(day).padStart(2, '0')}`;
    const row = byDate.get(date);
    bars.push({
      date,
      costMicros: row?.cost_micros ?? 0,
      calls: row?.calls ?? 0,
      today: date === today,
    });
  }
  return bars;
}

/** The pipeline stages in the order a recording meets them. */
export const OPS: readonly { key: string; label: string }[] = [
  { key: 'transcribe', label: 'Transcribe' },
  { key: 'route', label: 'Route' },
  { key: 'cleanup', label: 'Clean up' },
];

/** The stages that actually ran this month, in pipeline order, then any the API added since. */
export function opRows(
  ops: Record<string, UsageTotalsWire> | undefined,
): { key: string; label: string; totals: UsageTotalsWire }[] {
  if (!ops) return [];
  const known = OPS.flatMap(({ key, label }) => {
    const totals = ops[key];
    return totals ? [{ key, label, totals }] : [];
  });
  const extra = Object.entries(ops)
    .filter(([key]) => !OPS.some((op) => op.key === key))
    .map(([key, totals]) => ({ key, label: key, totals }));
  return [...known, ...extra];
}
