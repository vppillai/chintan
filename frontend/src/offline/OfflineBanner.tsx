import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { useCallback, useSyncExternalStore } from 'react';

import { useOfflineQueue } from './useOfflineQueue.ts';

/**
 * Says plainly when the app is offline and how much is waiting.
 *
 * Stale data is never presented as live data, and absent data is never
 * presented as saved data. The banner is state-driven for that reason: there
 * is no query persister, so on an offline cold start the cache is empty until
 * something fetches, and an unconditional "Offline — showing saved notes."
 * would sit directly above "You are offline and no notes are cached on this
 * device yet.".
 */
export function OfflineBanner() {
  const { online, queued, flushing } = useOfflineQueue();
  const cached = useHasCachedNotes();

  if (online && queued === 0) return null;

  return (
    <div className="offline-banner" role="status" aria-live="polite">
      {!online && (
        <span>
          {cached
            ? 'Offline — showing saved notes.'
            : 'Offline — nothing is saved on this device yet.'}
        </span>
      )}
      {queued > 0 && (
        <span>
          {flushing ? 'Syncing' : 'Waiting to sync'}{' '}
          <span className="numeric">{queued}</span>{' '}
          {queued === 1 ? 'change' : 'changes'}.
        </span>
      )}
    </div>
  );
}

/** True when at least one note is actually sitting in the query cache. */
function useHasCachedNotes(): boolean {
  const client = useQueryClient();

  const subscribe = useCallback(
    (onChange: () => void) => client.getQueryCache().subscribe(onChange),
    [client],
  );
  const snapshot = useCallback(() => hasCachedNotes(client), [client]);

  return useSyncExternalStore(subscribe, snapshot, () => false);
}

/**
 * Exported for the test: all three note query shapes count, because the library
 * is an infinite query (`{ pages: [...] }`), Search's corpus is a flat page, and
 * the device's own copy is a bare array. All three live under the `notes` key
 * prefix precisely so this one lookup sees every one of them.
 */
export function hasCachedNotes(client: QueryClient): boolean {
  return client
    .getQueryCache()
    .findAll({ queryKey: ['notes'] })
    .some((query) => countItems(query.state.data) > 0);
}

function countItems(data: unknown): number {
  // The IndexedDB corpus, read straight back as a list of notes.
  if (Array.isArray(data)) return data.length;
  if (typeof data !== 'object' || data === null) return 0;
  const candidate = data as { items?: unknown; pages?: unknown };
  if (Array.isArray(candidate.items)) return candidate.items.length;
  if (Array.isArray(candidate.pages)) {
    return candidate.pages.reduce<number>((total, page) => total + countItems(page), 0);
  }
  /*
   * One cached note, from `useCachedNote`. It counts: a user reading a note off
   * the device under a banner reading "nothing is saved on this device yet" is
   * the same untruth as the one this banner exists to stop, pointing the other
   * way.
   */
  const note = data as { id?: unknown; title?: unknown };
  if (typeof note.id === 'string' && typeof note.title === 'string') return 1;
  return 0;
}
