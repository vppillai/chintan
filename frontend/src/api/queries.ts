/**
 * TanStack Query bindings. Server state lives here; nothing else caches it.
 *
 * Query keys are built by one function per resource so an invalidation cannot
 * miss a key by typing the array out differently at the call site.
 */

import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
} from '@tanstack/react-query';

import { useApi } from './ApiProvider.tsx';
import type { ChintanApi } from './endpoints.ts';
import { isTerminalStatus } from './schema.ts';
import type {
  CaptureListQuery,
  CaptureWire,
  NoteListQuery,
  NoteUpdateWire,
  Page,
  SettingsWire,
} from './schema.ts';

export const queryKeys = {
  notes: (query: NoteListQuery = {}) => ['notes', query] as const,
  /**
   * The flat corpus Search filters locally.
   *
   * Deliberately NOT `notes(query)`. Both shapes used to share `['notes', …]`
   * while one is a `Page<NoteWire>` and the other a `useInfiniteQuery`'s
   * `{ pages }`, so whichever screen was visited first decided what the other
   * one found in the cache: Search first crashed the whole app on Notes
   * (`data.pages` undefined), and Notes first — the common path — silently gave
   * local search an empty corpus and told the user their note did not exist.
   *
   * Still prefixed `notes` so `invalidateQueries({ queryKey: ['notes'] })`
   * refreshes it along with the library.
   */
  notesCorpus: (query: NoteListQuery = {}) => ['notes', 'corpus', query] as const,
  note: (noteId: string) => ['note', noteId] as const,
  captures: (query: CaptureListQuery = {}) => ['captures', query] as const,
  capture: (captureId: string) => ['capture', captureId] as const,
  pendingCaptures: () => ['captures', { status: 'pending' as const }] as const,
  search: (q: string) => ['search', q] as const,
  tags: () => ['tags'] as const,
  settings: () => ['settings'] as const,
};

/* ---------------------------------------------------------------------------
   Notes
   --------------------------------------------------------------------------- */

export function useNotes(query: NoteListQuery = {}) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: queryKeys.notes(query),
    queryFn: ({ pageParam }) =>
      api.listNotes({ ...query, ...(pageParam ? { cursor: pageParam } : {}) }),
    initialPageParam: undefined as string | undefined,
    // An absent or empty cursor means the collection is exhausted. Returning
    // `undefined` is what stops TanStack asking for another page forever.
    getNextPageParam: (last: Page<unknown>) => last.cursor || undefined,
  });
}

export function useNote(noteId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.note(noteId ?? ''),
    queryFn: () => api.getNote(noteId as string),
    enabled: Boolean(noteId),
  });
}

export function useUpdateNote(noteId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: NoteUpdateWire) => api.updateNote(noteId, body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(noteId) });
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

export function useSearch(q: string) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.search(q),
    queryFn: () => api.search(q),
    enabled: q.trim().length > 0,
    staleTime: 15_000,
  });
}

export function useTags() {
  const api = useApi();
  return useQuery({ queryKey: queryKeys.tags(), queryFn: () => api.listTags() });
}

/* ---------------------------------------------------------------------------
   Settings
   --------------------------------------------------------------------------- */

export function useSettings() {
  const api = useApi();
  return useQuery({ queryKey: queryKeys.settings(), queryFn: () => api.getSettings() });
}

export function useSaveSettings() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: SettingsWire) => api.putSettings(body),
    // The contract returns what was *stored*, not what was sent, so the
    // response replaces the cache rather than the optimistic value.
    onSuccess: (stored) => {
      queryClient.setQueryData(queryKeys.settings(), stored);
    },
  });
}

/* ---------------------------------------------------------------------------
   Captures
   --------------------------------------------------------------------------- */

/** How long to keep polling a capture before giving up and offering Retry. */
export const CAPTURE_POLL_MAX_MS = 10 * 60_000;
const CAPTURE_POLL_MIN_MS = 2_000;
const CAPTURE_POLL_MAX_INTERVAL_MS = 20_000;

/**
 * Backoff for the progress card's poll.
 *
 * Bounded and widening: a capture that is still running after five minutes is
 * not going to be helped by asking every two seconds, and a stuck pipeline
 * should not cost the user a request per second of standing still.
 */
export function capturePollInterval(startedAt: number, now: number = Date.now()): number | false {
  const elapsed = now - startedAt;
  if (elapsed > CAPTURE_POLL_MAX_MS) return false;
  const widened = CAPTURE_POLL_MIN_MS * 2 ** Math.floor(elapsed / 60_000);
  return Math.min(widened, CAPTURE_POLL_MAX_INTERVAL_MS);
}

/**
 * Every capture the server still considers in flight.
 *
 * This is what makes the progress card survive a reload: the pending set is
 * server state, not a JavaScript variable. v1 held the in-flight capture id in
 * a module-level field, so a refresh stranded the audio with no UI able to
 * find it again.
 */
export function usePendingCaptures(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.pendingCaptures(),
    queryFn: () => api.listCaptures({ status: 'pending' }),
    enabled,
    refetchInterval: (query) => {
      const items = query.state.data?.items ?? [];
      const active = items.some((capture) => !isTerminalStatus(capture.status));
      return active ? CAPTURE_POLL_MIN_MS * 2 : false;
    },
    staleTime: 0,
  });
}

export function useCapture(captureId: string | undefined, startedAt: number) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.capture(captureId ?? ''),
    queryFn: () => api.getCapture(captureId as string),
    enabled: Boolean(captureId),
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      if (status && isTerminalStatus(status)) return false;
      return capturePollInterval(startedAt);
    },
    // A single transient poll failure must not orphan the capture. v1 nulled
    // the capture id on any error, which lost the only handle to the in-flight
    // recording; here the query simply keeps its last good data and retries.
    retry: false,
    staleTime: 0,
  });
}

export function useRetryCapture(): UseMutationResult<CaptureWire, Error, string> {
  const api: ChintanApi = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (captureId: string) => api.retryCapture(captureId),
    onSuccess: (capture) => {
      queryClient.setQueryData(queryKeys.capture(capture.id), capture);
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
    },
  });
}

export function useSetCaptureTarget() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      captureId,
      target,
    }: {
      captureId: string;
      target: { note_id: string } | { new_note_title: string };
    }) => api.setCaptureTarget(captureId, target),
    onSuccess: (capture) => {
      queryClient.setQueryData(queryKeys.capture(capture.id), capture);
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}
