import { useQueryClient } from '@tanstack/react-query';
import { useEffect } from 'react';

import { refreshAppendedNote } from '@/api/queries.ts';

/**
 * The service worker's word that a recording filed while this window was
 * open (`sw.ts`, the `push` handler): the captures poll is asked again at
 * once and the note that grew is refetched, so a focused Home shows the
 * receipt instead of a notification, and a phone with the app open does not
 * wait for the next poll to learn what the ring just sent.
 */
export function usePushWakeup(): void {
  const queryClient = useQueryClient();
  useEffect(() => {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
    const container = navigator.serviceWorker;
    const onMessage = (event: MessageEvent): void => {
      const data = event.data as { type?: string; note_id?: string | null } | null;
      if (data?.type !== 'CAPTURES_CHANGED') return;
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
      if (data.note_id) refreshAppendedNote(queryClient, data.note_id);
    };
    container.addEventListener('message', onMessage);
    return () => {
      container.removeEventListener('message', onMessage);
    };
  }, [queryClient]);
}
