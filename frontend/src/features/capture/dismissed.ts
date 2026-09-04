/**
 * Filing rows the user has closed.
 *
 * A "Filed" row stays at the top of the library until the user acts on it —
 * opens the note or dismisses it — rather than fading out on a timer. That
 * decision has to outlive the screen: the library remounts every time a note
 * is opened and closed, and a reload or app restart must not resurrect a row
 * that was dismissed a minute earlier. There is no server-side "seen" flag to
 * sync with (`appended`, `failed` and `no_content` stay in `GET /v1/captures`
 * by contract), so this is a per-device decision, kept in `localStorage`.
 *
 * Capped to the most recent ids. The poll only ever shows the newest twenty
 * captures, so anything older than a couple of hundred dismissals can never
 * come back, and an unbounded list would grow for the life of the install.
 */

export const DISMISSED_KEY = 'chintan.filing.dismissed';

/** Plenty of headroom over the twenty rows the poll can show. */
export const DISMISSED_LIMIT = 200;

function read(): string[] {
  try {
    const raw = localStorage.getItem(DISMISSED_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    // Storage blocked or corrupt: nothing is dismissed, which only means a row
    // the user closed comes back. Never a reason to fail the library.
    return [];
  }
}

function write(ids: readonly string[]): void {
  try {
    localStorage.setItem(DISMISSED_KEY, JSON.stringify(ids.slice(-DISMISSED_LIMIT)));
  } catch {
    // Quota or private mode. The in-memory answer given to the caller still
    // holds for this session.
  }
}

/** Every capture id the user has dismissed, oldest first. */
export function loadDismissed(): ReadonlySet<string> {
  return new Set(read());
}

/** Records a dismissal and returns the new set. Newest last, so the cap drops the oldest. */
export function dismissCapture(captureId: string, current: ReadonlySet<string>): ReadonlySet<string> {
  const next = Array.from(current).filter((id) => id !== captureId);
  next.push(captureId);
  const kept = next.slice(-DISMISSED_LIMIT);
  write(kept);
  return new Set(kept);
}
