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

/** `312 requests`, `1 request`. */
export function formatRequests(requests: number): string {
  return `${String(requests)} ${requests === 1 ? 'request' : 'requests'}`;
}

/** `8.7 MB`, or `0.4 MB` — one unit, so the figure sits still beside the minutes. */
export function formatMegabytes(bytes: number): string {
  return `${(Math.max(0, bytes) / 1_000_000).toFixed(1)} MB`;
}

/**
 * What one GB stored for one month costs at S3 Standard in us-east-1, in
 * dollars, as published in 2026. It is here, named, because the backend
 * deliberately attaches no price to storage: it reports byte-days, and this
 * screen turns them into an ESTIMATE. The real charge is on the account's
 * bill, in whatever region and class the bucket is, after the free tier, and
 * with request and transfer costs this figure knows nothing about.
 */
export const S3_STANDARD_USD_PER_GB_MONTH = 0.023;

/**
 * The gigabyte the price above is quoted per: AWS bills storage in binary
 * gigabytes (2^30 bytes, what the bill calls "GB"), so the decimal figure
 * would read about 7% high against it.
 */
const BYTES_PER_GB = 1024 ** 3;

/** `0.02 GB·days`, `12.40 GB·days` — the unit storage is billed in, scaled to gigabytes. */
export function formatGigabyteDays(byteDays: number): string {
  return `${(Math.max(0, byteDays) / BYTES_PER_GB).toFixed(2)} GB·days`;
}

/**
 * The month's storage so far, priced: byte-days become GB-months by dividing
 * by the days in the month, then cost at `S3_STANDARD_USD_PER_GB_MONTH`.
 * Microdollars, like every other figure here, so `formatDollars` renders it.
 * An estimate — see the constant.
 */
export function estimateStorageMicros(byteDays: number, month: string): number {
  const days = daysIn(month);
  if (days === 0 || byteDays <= 0) return 0;
  const gbMonths = byteDays / BYTES_PER_GB / days;
  return Math.round(gbMonths * S3_STANDARD_USD_PER_GB_MONTH * MICROS_PER_DOLLAR);
}

/**
 * `≈ $0.002`, or `under $0.001` for a figure that is real but rounds away —
 * a month of personal recordings costs a few tenths of a cent to store, and
 * "$0.000" would read as nothing rather than as almost nothing.
 */
export function formatEstimatedDollars(micros: number): string {
  if (micros > 0 && micros < 500) return 'under $0.001';
  return `≈ ${formatDollars(micros)}`;
}

/** `41 recordings`, `1 recording`; likewise notes. */
export function formatCount(count: number, singular: string, plural = `${singular}s`): string {
  return `${String(count)} ${count === 1 ? singular : plural}`;
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
 * What the AWS half of the Total is: this user's estimated share of the
 * instance's bill when the backend has apportioned one, else the whole
 * instance figure as before; `null` when AWS has not been recorded at all.
 */
export type TotalBasis = 'share' | 'instance';

export function totalBasis(aws: UsageAwsWire | null | undefined): TotalBasis | null {
  if (!aws) return null;
  return aws.share_micros !== null && aws.share_micros !== undefined ? 'share' : 'instance';
}

/**
 * Providers plus AWS — the user's share of it when there is one, the instance
 * figure otherwise — when AWS has been recorded; `null` otherwise, so the
 * screen leaves the Total line out rather than repeat the providers' figure
 * under a heading that promises more.
 */
export function combinedMicros(usage: UsageWire, aws: UsageAwsWire | null | undefined = usage.aws): number | null {
  const basis = totalBasis(aws);
  if (!aws || !basis) return null;
  return usage.cost_micros + (basis === 'share' ? (aws.share_micros ?? 0) : aws.month_micros);
}

export interface DayBar {
  date: string;
  costMicros: number;
  calls: number;
  /** Authenticated API requests that day; 0 from a backend that does not count them. */
  apiRequests: number;
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
      apiRequests: row?.api_requests ?? 0,
      today: date === today,
    });
  }
  return bars;
}

/**
 * The pipeline stages in the order a recording meets them, then the two
 * calls a person asks for by hand: the whole-note rewrite and a question.
 */
export const OPS: readonly { key: string; label: string }[] = [
  { key: 'transcribe', label: 'Transcribe' },
  { key: 'route', label: 'Route' },
  { key: 'cleanup', label: 'Clean up' },
  { key: 'clean_note', label: 'Clean note' },
  { key: 'ask', label: 'Ask' },
];

/**
 * The vendor behind the language model. The worker meters its OpenAI-compatible
 * endpoint under the wire key `openai` whatever is at the other end, and the
 * stack points that endpoint at MiniMax (`LLM_BASE_URL` in the template). An
 * instance pointed elsewhere is mislabelled here until this says otherwise —
 * which is why the label leads with the role, which is always right.
 */
const LLM_VENDOR = 'MiniMax';

/** What the wire's metering keys are called on screen. */
export const PROVIDER_LABELS: Readonly<Record<string, string>> = {
  groq: 'Groq',
  openai: `Language model (${LLM_VENDOR})`,
};

/**
 * One line per provider that charged this month, the biggest bill first, so
 * the split under "Providers" answers "which one" at a glance. A provider
 * the frontend has no name for is shown by its wire key, capitalised.
 */
export function providerRows(
  providers: Record<string, UsageTotalsWire> | undefined,
): { key: string; label: string; totals: UsageTotalsWire }[] {
  if (!providers) return [];
  return Object.entries(providers)
    .map(([key, totals]) => ({
      key,
      label: PROVIDER_LABELS[key] ?? key.charAt(0).toUpperCase() + key.slice(1),
      totals,
    }))
    .sort((a, b) => b.totals.cost_micros - a.totals.cost_micros || a.key.localeCompare(b.key));
}

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
