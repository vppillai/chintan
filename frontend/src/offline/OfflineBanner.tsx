import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { useCallback, useSyncExternalStore } from 'react';

import { useOfflineQueue } from './useOfflineQueue.ts';

/**
 * Says plainly when the app is offline and how much is waiting.
 *
 * v1's service worker set an `X-Offline` header on cached responses that
 * nothing read, so stale data was presented as live data with no indication
 * either way.
 *
 * The banner is state-driven for the same reason. It used to claim "Offline —
 * showing saved notes." unconditionally, so an offline cold start — there is no
 * query persister, so the cache is empty until something fetches — put that
 * sentence directly above "You are offline and no notes are cached on this
 * device yet.". Presenting absent data as saved data is the same failure the
 * comment above criticises v1 for, pointing the other way.
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
 * Exported for the test: both note query shapes count, because the library is
 * an infinite query (`{ pages: [...] }`) and Search's corpus is a flat page.
 */
export function hasCachedNotes(client: QueryClient): boolean {
  return client
    .getQueryCache()
    .findAll({ queryKey: ['notes'] })
    .some((query) => countItems(query.state.data) > 0);
}

function countItems(data: unknown): number {
  if (typeof data !== 'object' || data === null) return 0;
  const candidate = data as { items?: unknown; pages?: unknown };
  if (Array.isArray(candidate.items)) return candidate.items.length;
  if (Array.isArray(candidate.pages)) {
    return candidate.pages.reduce<number>((total, page) => total + countItems(page), 0);
  }
  return 0;
}
