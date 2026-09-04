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

/**
 * How long a just-filed capture keeps its "Filed" row once appended, and how
 * long a recording that produced nothing keeps saying so. `status=all` is
 * newest-first and otherwise unbounded — this is what stops a note filed weeks
 * ago from resurfacing at the top of the library forever just because its
 * capture happens to be near the top of that list.
 */
const RECENTLY_SETTLED_MS = 10 * 60 * 1000;

/** Newest-first, and twenty is more than one person records before the first has filed. */
const CAPTURE_LIST_LIMIT = 20;

/** How often to ask while something is still moving through the pipeline. */
export const CAPTURE_POLL_INTERVAL_MS = 4_000;

function within(iso: string | null | undefined, windowMs: number, now: number): boolean {
  if (!iso) return false;
  const at = Date.parse(iso);
  return Number.isFinite(at) && now - at < windowMs;
}

/**
 * Whether the library's filing row has anything to say about a capture.
 *
 * Anything still moving, obviously. Of the stopped ones: `failed`,
 * `spend_capped` and `needs_target` always, because each has an action the
 * user must take and a capture waiting on the user must not vanish silently;
 * `appended` and `no_content` only briefly, because they are done and the row
 * is a receipt, not a history.
 */
export function isFilingRelevant(capture: CaptureWire, now: number = Date.now()): boolean {
  switch (capture.status) {
    case 'failed':
    case 'spend_capped':
    case 'needs_target':
      return true;
    case 'appended':
      return within(capture.appended_at, RECENTLY_SETTLED_MS, now);
    case 'no_content':
      return within(capture.created_at, RECENTLY_SETTLED_MS, now);
    default:
      return !isTerminalStatus(capture.status);
  }
}

/**
 * Every capture the library's filing row has something to say about.
 *
 * This is what makes the row survive a reload: the set is server state, not a
 * JavaScript variable. v1 held the in-flight capture id in a module-level
 * field, so a refresh stranded the audio with no UI able to find it again.
 *
 * ONE request per poll. This used to fire `pending`, `failed`, `needs_target`
 * and `all` in parallel every four seconds, plus all four again on every
 * window focus — ~120 invocations for a two-minute pipeline, while the user
 * was most likely driving on cellular. The newest twenty captures contain
 * everything those filters would have returned that is worth showing (see
 * `isFilingRelevant`), so the filtering happens here. Focus refetch is off
 * for this query alone: while anything is moving the interval already asks,
 * and once nothing is, a focus has nothing new to learn. Always stale, though:
 * the library is remounted every time the user comes back to it — including
 * from the capture screen, six hundred milliseconds after a Send — and that
 * mount is the one moment a fresh answer is owed.
 */
export function usePendingCaptures(enabled = true) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: queryKeys.pendingCaptures(),
    queryFn: async () => {
      const page = await api.listCaptures({ status: 'all', limit: CAPTURE_LIST_LIMIT });
      const items = page.items.filter((capture) => isFilingRelevant(capture));
      // Read from the cache rather than closed over: the library remounts on
      // every visit and the comparison has to be with the last poll, not the
      // last render.
      const previous = queryClient.getQueryData<{ items: CaptureWire[] }>(
        queryKeys.pendingCaptures(),
      );
      for (const noteId of newlyAppendedNoteIds(previous?.items, items)) {
        refreshAppendedNote(queryClient, noteId);
      }
      return { items };
    },
    enabled,
    refetchInterval: (query) => {
      const items = query.state.data?.items ?? [];
      const active = items.some((capture) => !isTerminalStatus(capture.status));
      return active ? CAPTURE_POLL_INTERVAL_MS : false;
    },
    refetchOnWindowFocus: false,
    staleTime: 0,
  });
}

/**
 * Notes whose text just changed under the app: captures that were not
 * `appended` on the previous poll and are now.
 *
 * Nothing else tells the note screen. The append is written by the worker,
 * not by this client, so no mutation here ever invalidated `['note', id]` —
 * and a note the user had open while recording into it (or opened from the
 * filing row's "Open the note") kept showing the body from before the
 * recording until a second visit. Restricted to transitions: a capture that
 * was already `appended` on the last poll has nothing new to say, and the
 * first poll after a cold start — with no previous answer — invalidates
 * nothing, because there is no cache yet to be stale.
 */
export function newlyAppendedNoteIds(
  previous: readonly CaptureWire[] | undefined,
  current: readonly CaptureWire[],
): string[] {
  if (!previous) return [];
  const before = new Map(previous.map((capture) => [capture.id, capture.status]));
  const noteIds = new Set<string>();
  for (const capture of current) {
    if (capture.status !== 'appended' || !capture.note_id) continue;
    if (before.get(capture.id) === 'appended') continue;
    noteIds.add(capture.note_id);
  }
  return Array.from(noteIds);
}

/**
 * A note the pipeline has just written to is stale everywhere the app holds
 * it: the detail query, every list under `['notes']` (snippet, updated_at,
 * ordering) and the device's copy, which lives under the same prefix and is
 * rewritten as a side effect of the detail refetch.
 */
export function refreshAppendedNote(queryClient: QueryClient, noteId: string): void {
  void queryClient.invalidateQueries({ queryKey: queryKeys.note(noteId) });
  void queryClient.invalidateQueries({ queryKey: ['notes'] });
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
