import { showToast } from '@/components/Toast.tsx';

/**
 * How long an archived note has left.
 *
 * `purge_after` is optional in the contract — a note archived before an
 * instance had retention configured carries none — and feeding the absent
 * value straight into arithmetic renders **"Deletes in NaN days"** on the
 * archive list. The absence is a real state with a real answer ("no deletion
 * date"), so it is modelled rather than divided by.
 *
 * Pure, and separated from the screen, because the interesting cases are dates:
 * missing, unparseable, already past, today, and a fortnight out.
 */

export type PurgeCountdown =
  /** The server gave no date, or gave one that is not a date. */
  | { kind: 'none' }
  /** The purge window has run out; the sweeper has simply not run yet. */
  | { kind: 'due' }
  | { kind: 'days'; days: number };

/**
 * Whole days from `now` until `purgeAfter`, rounded up.
 *
 * Rounded up rather than down so a note with eleven hours left reads "Deletes
 * in 1 day" rather than "Deletes in 0 days" — a countdown that reaches zero
 * while the note is still there reads as broken.
 */
export function purgeCountdown(
  purgeAfter: string | null | undefined,
  now: number = Date.now(),
): PurgeCountdown {
  if (!purgeAfter) return { kind: 'none' };

  const at = Date.parse(purgeAfter);
  if (!Number.isFinite(at)) return { kind: 'none' };

  const remaining = at - now;
  if (remaining <= 0) return { kind: 'due' };

  return { kind: 'days', days: Math.ceil(remaining / 86_400_000) };
}

/** The sentence the archive list and the note screen both show. */
export function describePurge(countdown: PurgeCountdown): string {
  switch (countdown.kind) {
    case 'none':
      return 'No deletion date';
    case 'due':
      return 'Due to be deleted';
    case 'days':
      return `Deletes in ${String(countdown.days)} ${countdown.days === 1 ? 'day' : 'days'}`;
    default: {
      const exhaustive: never = countdown;
      return exhaustive;
    }
  }
}

/**
 * The Undo toast after a delete on Home, which is an archive: the note (or
 * the notes) can be had back for the purge window, and this is the one place
 * the app says so at the moment it matters. `undo` restores them; the caller
 * knows which mutation that is.
 */
export function showDeleted(count: number, undo: () => void): void {
  showToast({
    message: `${count === 1 ? 'Deleted' : `${String(count)} notes deleted`} · kept in Archive for 30 days`,
    action: { label: 'Undo', onSelect: undo },
  });
}
