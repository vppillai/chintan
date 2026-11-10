import { useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';
import { useNavigate } from 'react-router';

import { refreshAppendedNote } from '@/api/queries.ts';

import { pathToOpen } from './notificationOpen.ts';

/**
 * The service worker's word that a recording filed while this window was
 * open (`sw.ts`, the `push` handler): the captures poll is asked again at
 * once and the note that grew is refetched, so a focused Home shows the
 * receipt instead of a notification, and a phone with the app open does not
 * wait for the next poll to learn what the ring just sent.
 *
 * And its request to open a note, from a tapped notification: routed here,
 * in the app, and answered, so the worker does not navigate the document
 * out from under a recording or an upload (`notificationOpen.ts`).
 */
export function usePushWakeup(): void {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  useEffect(() => {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
    const container = navigator.serviceWorker;
    const onMessage = (event: MessageEvent): void => {
      const path = pathToOpen(event.data, import.meta.env.BASE_URL, window.location.origin);
      if (path !== null) {
        void navigate(path);
        event.ports[0]?.postMessage({ type: 'OPENED' });
        return;
      }
      const data = event.data as { type?: string; note_id?: string | null } | null;
      if (data?.type !== 'CAPTURES_CHANGED') return;
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
      if (data.note_id) refreshAppendedNote(queryClient, data.note_id);
    };
    container.addEventListener('message', onMessage);
    return () => {
      container.removeEventListener('message', onMessage);
    };
  }, [queryClient, navigate]);
}
