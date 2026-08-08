import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import type { ChintanApi } from '@/api/endpoints.ts';
import { useOnline } from '@/hooks/useOnline.ts';

import { listenForFlushRequests, registerBackgroundSync } from './backgroundSync.ts';
import type { QueuedMutation } from './db.ts';
import { narrow } from './payloads.ts';
import { count, flush } from './queue.ts';
import { QUEUED_EDIT_KEY } from './queuedEdits.ts';

/**
 * Applies one queued mutation against the live API.
 *
 * The idempotency key is the one minted at enqueue time, so replaying an entry
 * whose first attempt actually reached the server returns the original
 * response rather than applying it twice.
 */
export async function runMutation(
  api: ChintanApi,
  mutation: QueuedMutation,
): Promise<void> {
  const typed = narrow(mutation);
  // A payload that does not match its kind was written by an older build.
  // Dropping it is right: replaying it with missing fields would fail forever
  // and block everything queued behind it.
  if (!typed) return;

  switch (typed.kind) {
    case 'updateNote':
      await api.updateNote(typed.payload.noteId, typed.payload.body, typed.idempotencyKey);
      return;
    case 'archiveNote':
      await api.archiveNote(typed.payload.noteId);
      return;
    case 'restoreNote':
      await api.restoreNote(typed.payload.noteId);
      return;
    case 'setCaptureTarget':
      await api.setCaptureTarget(
        typed.payload.captureId,
        typed.payload.target,
        typed.idempotencyKey,
      );
      return;
    case 'retryCapture':
      await api.retryCapture(typed.payload.captureId, typed.idempotencyKey);
      return;
    case 'createCapture':
      // Capture uploads are not generic mutations: they need a presigned URL
      // minted fresh. They resume from the audio still on disk instead — see
      // features/capture/buffer.unconfirmedCaptures.
      return;
    default: {
      const exhaustive: never = typed;
      return exhaustive;
    }
  }
}

export const OFFLINE_QUEUE_KEY = ['offline', 'queue'] as const;

export interface OfflineQueueState {
  online: boolean;
  queued: number;
  flushing: boolean;
  flushNow: () => Promise<void>;
}

/**
 * Drains the offline queue whenever a connection is available.
 *
 * Modelled as a query rather than an effect on purpose: "reconcile this when
 * the network comes back" is precisely what TanStack's online manager already
 * does, via `refetchOnReconnect`. Wiring it by hand to a `navigator.onLine`
 * effect would mean calling setState from an effect body and reimplementing
 * the reconnect detection that is already here.
 *
 * Background Sync, where the browser supports it, is registered by the service
 * worker in the PWA phase and covers the case where the tab is closed. v1 had
 * the shape of that and none of the substance: a `sync` listener whose handler
 * was empty, for a tag nothing ever registered.
 */
export function useOfflineQueue(): OfflineQueueState {
  const api = useApi();
  const online = useOnline();
  const queryClient = useQueryClient();

  const query = useQuery({
    queryKey: OFFLINE_QUEUE_KEY,
    queryFn: async () => {
      // Offline: report the depth without burning attempt budgets against a
      // network that is not there.
      if (typeof navigator !== 'undefined' && !navigator.onLine) return count();
      const result = await flush((mutation) => runMutation(api, mutation));

      /*
       * Reconcile whatever the flush just changed.
       *
       * Without this the queue drained and nothing else in the app noticed: the
       * note screen went on saying "Saved on this device — will sync" for an
       * edit the server already had, and the note itself still rendered the
       * pre-edit text it had fetched. Local state recorded, real outcome never
       * reported back.
       *
       * `queued-edit` is deliberately not under this query's own key, so
       * refreshing it here cannot re-trigger the flush that is running.
       */
      if (result.applied > 0 || result.failed > 0) {
        void queryClient.invalidateQueries({ queryKey: [QUEUED_EDIT_KEY] });
        void queryClient.invalidateQueries({ queryKey: ['note'] });
        void queryClient.invalidateQueries({ queryKey: ['notes'] });
      }

      return count();
    },
    /*
     * Always, never paused. The queue lives in IndexedDB, so reading its depth
     * is a local read that works exactly as well with no connection — and
     * offline is precisely when the user needs to be told how much is waiting.
     * Under the default `online` mode TanStack paused this query the moment the
     * connection dropped, so `queued` stayed 0 and the banner said nothing
     * about the edits sitting on the device. The queryFn's own
     * `navigator.onLine` guard is what stops it attempting a flush.
     */
    networkMode: 'always',
    refetchOnReconnect: true,
    refetchOnWindowFocus: true,
    // A slow safety net for the case where neither event fires — a captive
    // portal that lets `navigator.onLine` stay true throughout.
    refetchInterval: 60_000,
    staleTime: 0,
    retry: false,
  });

  const flushNow = useCallback(async () => {
    await queryClient.refetchQueries({ queryKey: OFFLINE_QUEUE_KEY });
  }, [queryClient]);

  // Background Sync is the upgrade path: it flushes even after the tab is
  // closed, where the browser supports it. Registration is best-effort and the
  // reconnect refetch above remains the baseline everywhere.
  useEffect(() => {
    if ((query.data ?? 0) === 0) return;
    void registerBackgroundSync();
  }, [query.data]);

  // Subscription to an external system, so it belongs in an effect.
  useEffect(
    () =>
      listenForFlushRequests(() => {
        void flushNow();
      }),
    [flushNow],
  );

  return {
    online,
    queued: query.data ?? 0,
    flushing: query.isFetching,
    flushNow,
  };
}
