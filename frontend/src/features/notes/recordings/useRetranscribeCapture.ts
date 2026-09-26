import { useMutation, useQueryClient } from '@tanstack/react-query';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { queryKeys } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';

import { failureText, type Notice } from './labels.ts';

/**
 * Asks the server to transcribe one recording again. The 202 carries the
 * capture back at `transcribing`, which is written straight onto the cached
 * note so the row wears the stage strip before the refetch; the note query
 * then follows the pipeline as it does for a new recording. A 404 is a
 * backend older than the route, and is said as "not yet" rather than as a
 * missing recording.
 */
export function useRetranscribeCapture(noteId: string, onNotice: (notice: Notice) => void) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (captureId: string) => api.retranscribeCapture(captureId),
    onSuccess: (capture) => {
      queryClient.setQueryData<NoteDetailWire>(queryKeys.note(noteId), (current) =>
        current?.captures
          ? {
              ...current,
              captures: current.captures.map((row) => (row.id === capture.id ? capture : row)),
            }
          : current,
      );
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(noteId) });
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
    },
    onError: (error) => {
      onNotice({
        text:
          error instanceof ApiError && error.isNotFound
            ? 'Transcribing again is not available on this server yet.'
            : failureText(error),
        tone: 'error',
      });
    },
  });
}
