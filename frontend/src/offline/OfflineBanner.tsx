import { useOfflineQueue } from './useOfflineQueue.ts';

/**
 * Says plainly when the app is offline and how much is waiting.
 *
 * v1's service worker set an `X-Offline` header on cached responses that
 * nothing read, so stale data was presented as live data with no indication
 * either way.
 */
export function OfflineBanner() {
  const { online, queued, flushing } = useOfflineQueue();

  if (online && queued === 0) return null;

  return (
    <div className="offline-banner" role="status" aria-live="polite">
      {!online && <span>Offline — showing saved notes.</span>}
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
