/**
 * Registers Background Sync, and listens for the worker asking us to flush.
 *
 * v1 shipped the listener side of this in the service worker with an empty
 * handler, for a tag that nothing ever registered — so it looked implemented
 * in a review and did nothing at runtime. Both halves are here.
 */

/** Must match `SYNC_TAG` in `src/sw.ts`. */
export const SYNC_TAG = 'chintan-flush-queue';

interface SyncManagerLike {
  register(tag: string): Promise<void>;
}

function syncManager(registration: ServiceWorkerRegistration): SyncManagerLike | null {
  const candidate = (registration as unknown as { sync?: SyncManagerLike }).sync;
  return candidate && typeof candidate.register === 'function' ? candidate : null;
}

/**
 * Asks the browser to flush the queue when connectivity returns, even if the
 * tab has been closed.
 *
 * Best effort by design: Background Sync is Chromium-only, and everywhere else
 * the foreground reconnect path in `useOfflineQueue` is the whole story. This
 * is an upgrade, never a dependency.
 */
export async function registerBackgroundSync(): Promise<boolean> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return false;
  try {
    const registration = await navigator.serviceWorker.ready;
    const sync = syncManager(registration);
    if (!sync) return false;
    await sync.register(SYNC_TAG);
    return true;
  } catch {
    // Denied by permission policy, or already registered. Neither is worth
    // surfacing — the foreground path still runs.
    return false;
  }
}

/** Runs `onFlush` when the service worker asks the page to drain the queue. */
export function listenForFlushRequests(onFlush: () => void): () => void {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) {
    return () => {};
  }
  const handler = (event: MessageEvent): void => {
    if ((event.data as { type?: string } | null)?.type === 'FLUSH_QUEUE') onFlush();
  };
  navigator.serviceWorker.addEventListener('message', handler);
  return () => {
    navigator.serviceWorker.removeEventListener('message', handler);
  };
}
