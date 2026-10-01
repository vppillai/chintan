import { useSyncExternalStore } from 'react';

/**
 * The single polite live region, and the one way to speak through it.
 *
 * Route announcements, the checklist editor's "Made a sub-item of …" and the
 * autosave indicator's "Saved" are all said here rather than from live
 * regions of their own: a note screen had six to ten mounted at once, and a
 * tick fired three inside one second — the editor's, the toast's and the
 * indicator's — which VoiceOver answers by dropping the overlapping ones
 * (review 2026-10-01, FE-4). The region is always mounted and atomic, so
 * each `announce` replaces the last and is read whole; a live region added
 * to the DOM at the same time as its text is frequently not announced. A
 * module store, like `Toast`'s: the callers are deep in screens that
 * cannot hold a provider's setter.
 */

let current = '';
const listeners = new Set<() => void>();

/** Says `message` through the shell's region, replacing whatever it last said. */
export function announce(message: string): void {
  current = message;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function StatusRegion() {
  const message = useSyncExternalStore(subscribe, () => current, () => '');
  return (
    <div
      className="visually-hidden"
      role="status"
      aria-live="polite"
      aria-atomic="true"
      data-testid="status-region"
    >
      {message}
    </div>
  );
}
