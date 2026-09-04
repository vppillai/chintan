/**
 * How the library is laid out in time.
 *
 * Rows are grouped by the day they were last touched — Today, Yesterday, the
 * weekday for the rest of the last week, then the month, then the year — and
 * each row shows the time of day if it was today, else the date. Pure, so the
 * boundaries (midnight, a week ago, New Year) are testable with a fixed clock.
 *
 * Everything here is in the device's local time zone: "Today" has to mean the
 * user's today, not UTC's.
 */

import type { CaptureWire, NoteWire } from '@/api/schema.ts';

export interface NoteGroup {
  label: string;
  notes: NoteWire[];
}

const DAY_MS = 86_400_000;

function startOfDay(at: Date): number {
  return new Date(at.getFullYear(), at.getMonth(), at.getDate()).getTime();
}

/** Whole local days between two instants, positive when `at` is earlier. */
function daysAgo(at: Date, now: Date): number {
  return Math.round((startOfDay(now) - startOfDay(at)) / DAY_MS);
}

function parse(iso: string): Date | null {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? null : date;
}

/** The heading a note files under, given the clock. */
export function groupLabel(iso: string, now: number = Date.now()): string {
  const at = parse(iso);
  if (!at) return 'Undated';
  const today = new Date(now);
  const days = daysAgo(at, today);

  if (days <= 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 7) return new Intl.DateTimeFormat(undefined, { weekday: 'long' }).format(at);
  if (at.getFullYear() === today.getFullYear()) {
    return new Intl.DateTimeFormat(undefined, { month: 'long' }).format(at);
  }
  return String(at.getFullYear());
}

/**
 * Newest first, grouped by `groupLabel`. Order within a group is preserved
 * from the sort, so the server's newest-first paging and the device cache
 * agree on what the top of "Today" is.
 */
export function groupByDay(notes: readonly NoteWire[], now: number = Date.now()): NoteGroup[] {
  const sorted = [...notes].sort((a, b) => b.updated_at.localeCompare(a.updated_at));
  const groups: NoteGroup[] = [];
  for (const note of sorted) {
    const label = groupLabel(note.updated_at, now);
    const last = groups.at(-1);
    if (last && last.label === label) last.notes.push(note);
    else groups.push({ label, notes: [note] });
  }
  return groups;
}

/**
 * The row's right-hand column: `HH:MM` for today, otherwise the date, with the
 * year only once it is not this one. Empty for an unparseable timestamp rather
 * than the raw string — a row is not the place to debug the wire format.
 */
export function formatRowTime(iso: string, now: number = Date.now()): string {
  const at = parse(iso);
  if (!at) return '';
  const today = new Date(now);
  if (daysAgo(at, today) <= 0) {
    return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' }).format(at);
  }
  if (at.getFullYear() === today.getFullYear()) {
    return new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' }).format(at);
  }
  return new Intl.DateTimeFormat(undefined, {
    day: 'numeric',
    month: 'short',
    year: 'numeric',
  }).format(at);
}

/**
 * A moment, for a recording row or a note's "Updated" line: `Today 14:02`,
 * `Yesterday 18:40`, then `28 Aug 07:55`, with the year once it is not this
 * one. The time of day is always there — two recordings on the same day are
 * told apart by it and by nothing else.
 */
export function describeMoment(iso: string, now: number = Date.now()): string {
  const at = parse(iso);
  if (!at) return '';
  const time = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' }).format(at);
  const days = daysAgo(at, new Date(now));
  if (days <= 0) return `Today ${time}`;
  if (days === 1) return `Yesterday ${time}`;
  return `${formatRowTime(iso, now)} ${time}`;
}

/**
 * Today, for the library's heading: "Thursday, 4 September" in the device's
 * locale — weekday, day and month, never the year, because the heading is a
 * place to be rather than a timestamp. The date the notes are grouped under
 * is the same clock, so "Today" in the list and this line cannot disagree.
 */
export function describeToday(now: number = Date.now()): string {
  return new Intl.DateTimeFormat(undefined, {
    weekday: 'long',
    day: 'numeric',
    month: 'long',
  }).format(new Date(now));
}

/** `M:SS`, for the total audio behind a note. */
export function formatDurationShort(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${String(minutes)}:${String(seconds).padStart(2, '0')}`;
}

/**
 * "3 recordings · 4:12", when the payload carries the captures.
 *
 * The list endpoint does not: `Note` on the wire has no capture count or
 * duration (`docs/api/openapi.yaml`), only `NoteDetail` has `captures`. So on
 * the library this is `null` today and the meta line shows tags alone. It is
 * written against the detail shape on purpose — the moment the list gains a
 * `captures` summary, the rows say how much audio is behind them without a
 * client change.
 */
export function describeRecordings(
  note: NoteWire & { captures?: readonly Pick<CaptureWire, 'duration_ms'>[] },
): string | null {
  const captures = note.captures;
  if (!captures || captures.length === 0) return null;
  const count = captures.length;
  const total = captures.reduce((sum, capture) => sum + (capture.duration_ms ?? 0), 0);
  const label = `${String(count)} ${count === 1 ? 'recording' : 'recordings'}`;
  return total > 0 ? `${label} · ${formatDurationShort(total)}` : label;
}
