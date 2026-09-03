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
  type QueryClient,
  type UseMutationResult,
} from '@tanstack/react-query';

import { cacheNoteDetail, cacheNoteList, forgetNote } from '@/offline/notesCache.ts';

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
  note: (noteId: string) => ['note', noteId] as const,
  captures: (query: CaptureListQuery = {}) => ['captures', query] as const,
  capture: (captureId: string) => ['capture', captureId] as const,
  pendingCaptures: () => ['captures', 'progress-card'] as const,
  search: (q: string) => ['search', q] as const,
  tags: () => ['tags'] as const,
  settings: () => ['settings'] as const,
};

/* ---------------------------------------------------------------------------
   Notes
   --------------------------------------------------------------------------- */

/**
 * Writes a note or a page of notes to the device, and never lets that failure
 * become the request's failure.
 *
 * Caching is a side effect of reading. A browser in private mode, a full quota
 * or a blocked-storage policy must degrade to "no offline copy", not to "your
 * notes would not load".
 */
function remember(write: () => Promise<void>, queryClient?: QueryClient): void {
  void write()
    .then(() => {
      /*
       * Tell any screen already reading the device that it has more to read.
       *
       * Scoped to `['notes', 'offline']` and never to `['notes']`: invalidating
       * the wider prefix from inside a notes query's own success handler would
       * refetch the query that just wrote, forever.
       */
      void queryClient?.invalidateQueries({ queryKey: ['notes', 'offline'] });
    })
    .catch(() => {
      /* No offline copy this time. The screen is unaffected. */
    });
}

export function useNotes(query: NoteListQuery = {}) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useInfiniteQuery({
    queryKey: queryKeys.notes(query),
    queryFn: async ({ pageParam }) => {
      const page = await api.listNotes({
        ...query,
        ...(pageParam ? { cursor: pageParam } : {}),
      });
      // Every list the user sees is a list they can see again offline.
      remember(() => cacheNoteList(page.items), queryClient);
      return page;
    },
    initialPageParam: undefined as string | undefined,
    // An absent or empty cursor means the collection is exhausted. Returning
    // `undefined` is what stops TanStack asking for another page forever.
    getNextPageParam: (last: Page<unknown>) => last.cursor || undefined,
  });
}

export function useNote(noteId: string | undefined) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: queryKeys.note(noteId ?? ''),
    queryFn: async () => {
      const note = await api.getNote(noteId as string);
      // The only place a full note — body and captures — enters the device.
      remember(() => cacheNoteDetail(note), queryClient);
      return note;
    },
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

/* ---------------------------------------------------------------------------
   Removing a note: archive → restore, or archive → purge

   Three mutations rather than one parameterised one, because they are three
   different promises. Archive is reversible, restore undoes it, and purge is
   irreversible and cascades to the audio and the transcripts.

   All three invalidate `['notes']` wholesale — the active list, the archive
   list and the search corpus all live under that prefix, and a note that moved
   between two of them must not be left rendered in both.
   --------------------------------------------------------------------------- */

export function useArchiveNote() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (noteId: string) => api.archiveNote(noteId),
    onSuccess: (_result, noteId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(noteId) });
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

export function useRestoreNote() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (noteId: string) => api.restoreNote(noteId),
    onSuccess: (_result, noteId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(noteId) });
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

export function useDeleteNoteForever() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (noteId: string) => api.deleteNoteForever(noteId),
    onSuccess: (_result, noteId) => {
      // The device forgets it too. An offline library that still lists a note
      // the user deliberately destroyed is the one disagreement between the two
      // copies that is not tolerable.
      remember(() => forgetNote(noteId), queryClient);
      // Removed, not invalidated: there is nothing left on the server to
      // refetch, and a refetch would 404 into an error the user cannot act on.
      queryClient.removeQueries({ queryKey: queryKeys.note(noteId) });
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

/**
 * Bulk archive and bulk restore, for a multi-select list.
 *
 * Neither operation has a batch endpoint — only purge does, because only
 * purge is destructive enough to need one call instead of N (see
 * `purgeNotesBatch`'s doc comment). Archiving or restoring several notes is
 * just several ordinary archive/restore calls run together; `allSettled`
 * rather than `all` so one note that is already gone does not stop the rest
 * from moving.
 */
export function useBulkArchiveNotes() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (noteIds: string[]) =>
      Promise.allSettled(noteIds.map((id) => api.archiveNote(id))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

export function useBulkRestoreNotes() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (noteIds: string[]) =>
      Promise.allSettled(noteIds.map((id) => api.restoreNote(id))),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

/**
 * Bulk purge — "empty the archive" is this, given every archived note's id.
 * Chunked at `MAX_PURGE_BATCH` because the server refuses a single request
 * naming more (`service.MaxPurgeBatch`), not because this client paginates on
 * its own initiative.
 */
const MAX_PURGE_BATCH = 100;

export function useBulkPurgeNotes() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (noteIds: string[]) => {
      const results = [];
      for (let start = 0; start < noteIds.length; start += MAX_PURGE_BATCH) {
        const chunk = noteIds.slice(start, start + MAX_PURGE_BATCH);
        const response = await api.purgeNotesBatch(chunk);
        results.push(...response.results);
      }
      return results;
    },
    onSuccess: (results) => {
      for (const result of results) {
        queryClient.removeQueries({ queryKey: queryKeys.note(result.note_id) });
      }
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

/**
 * Bulk delete from the live list: archive, then purge, in one gesture.
 *
 * The server refuses to purge an active note (service.purgeOne), and rightly —
 * a client working from a stale listing must not turn "clear my archive" into
 * "delete the notes I am still using". So deleting from the notes list is the
 * two server operations the user would otherwise perform by hand: archive each
 * selected note, then purge the ones that archived. A note that could not be
 * archived is left exactly where it was and reported by the purge as failed.
 */
export function useBulkDeleteNotes() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (noteIds: string[]) => {
      const archived = await Promise.allSettled(noteIds.map((id) => api.archiveNote(id)));
      const toPurge = noteIds.filter((_id, i) => archived[i]?.status === 'fulfilled');
      const results = [];
      for (let start = 0; start < toPurge.length; start += MAX_PURGE_BATCH) {
        const chunk = toPurge.slice(start, start + MAX_PURGE_BATCH);
        const response = await api.purgeNotesBatch(chunk);
        results.push(...response.results);
      }
      return results;
    },
    onSuccess: (results) => {
      for (const result of results) {
        if (result.status !== 'purged') continue;
        // Same as the single-note delete: the device forgets it too, and the
        // detail query is removed rather than refetched into a 404.
        remember(() => forgetNote(result.note_id), queryClient);
        queryClient.removeQueries({ queryKey: queryKeys.note(result.note_id) });
      }
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

/**
 * `GET /v1/search`. Never what the user waits for: the library filters its
 * cached corpus on every keystroke and this refines and extends the result,
 * because the server can see transcript text the client never downloaded.
 * `enabled` lets the caller hold it off — offline, or in the archive.
 */
export function useSearch(q: string, { enabled = true }: { enabled?: boolean } = {}) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.search(q),
    queryFn: () => api.search(q),
    enabled: enabled && q.trim().length > 0,
    staleTime: 30_000,
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
 * Every capture the progress card has something to say about.
 *
 * This is what makes the progress card survive a reload: the set is server
 * state, not a JavaScript variable. v1 held the in-flight capture id in a
 * module-level field, so a refresh stranded the audio with no UI able to find
 * it again.
 *
 * Fetches three server-side filters and merges them, rather than `pending`
 * alone. `CaptureIsPending` (backend `internal/service/capture_status.go`)
 * answers "is the pipeline still moving" — it excludes `failed`,
 * `spend_capped` and `needs_target` by design, because those are stopped, not
 * moving. But `ProgressCard` renders a distinct, actionable outcome for every
 * one of those three: an error with Retry, "daily cap reached", and "which
 * note should this go in?". None of them satisfy `CaptureIsPending`, so
 * polling `pending` alone means a capture that failed, or that is waiting on
 * the user to pick a note, simply vanishes — no error, no prompt, nothing —
 * indistinguishable from one that quietly succeeded. `failed` and
 * `needs_target` are themselves bounded (a capture leaves them the moment it
 * is retried or answered), so merging them in cannot resurrect old,
 * already-resolved history the way polling `all` would.
 */
/**
 * How long a just-filed capture keeps its "Filed" card once appended.
 *
 * `status=all` is newest-first and otherwise unbounded — this is what stops a
 * note filed weeks ago from resurfacing here forever just because its capture
 * happens to be near the top of that list.
 */
const RECENTLY_APPENDED_MS = 10 * 60 * 1000;

function recentlyAppended(capture: CaptureWire): boolean {
  if (capture.status !== 'appended' || !capture.appended_at) return false;
  const at = Date.parse(capture.appended_at);
  return Number.isFinite(at) && Date.now() - at < RECENTLY_APPENDED_MS;
}

export function usePendingCaptures(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.pendingCaptures(),
    queryFn: async () => {
      const [pending, failed, needsTarget, all] = await Promise.all([
        api.listCaptures({ status: 'pending' }),
        api.listCaptures({ status: 'failed' }),
        api.listCaptures({ status: 'needs_target' }),
        // `appended` satisfies none of the three filters above — it is the
        // pipeline's success state, not one it stopped on — so a capture that
        // finished had nothing to show for it: no "Filed", no way to jump
        // straight to the note it just wrote. This is the one filter that
        // reaches it, filtered client-side to recent, since `all` is every
        // capture the tenant has ever made.
        api.listCaptures({ status: 'all' }),
      ]);
      const byId = new Map<string, CaptureWire>();
      for (const page of [pending, failed, needsTarget]) {
        for (const capture of page.items) byId.set(capture.id, capture);
      }
      for (const capture of all.items) {
        if (recentlyAppended(capture)) byId.set(capture.id, capture);
      }
      return { items: Array.from(byId.values()) };
    },
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
