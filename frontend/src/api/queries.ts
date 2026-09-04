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
  type InfiniteData,
  type QueryClient,
  type QueryKey,
  type UseMutationResult,
} from '@tanstack/react-query';

import { cacheNoteDetail, cacheNoteList, forgetNote } from '@/offline/notesCache.ts';

import { useApi } from './ApiProvider.tsx';
import type { ChintanApi } from './endpoints.ts';
import { isTerminalStatus } from './schema.ts';
import type {
  CaptureListQuery,
  CaptureWire,
  NoteDetailWire,
  NoteListQuery,
  NoteWire,
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
  usage: (month: string | undefined) => ['usage', month ?? 'current'] as const,
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

/**
 * `enabled: false` holds the request back without unmounting the hook — the
 * capture screen's target chooser uses it so the notes list does not compete
 * with `getUserMedia` for the first seconds of a launch.
 */
export function useNotes(query: NoteListQuery = {}, { enabled = true }: { enabled?: boolean } = {}) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useInfiniteQuery({
    enabled,
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

/**
 * The offline search corpus: every active note with its `search_text`, written
 * to the device so the instant search matches what `GET /v1/search` matches.
 *
 * Its own request, not a flag on the library's list. The list is fetched on
 * every visit and renders none of the text, so it stays small; this asks once
 * per session (and again five minutes later, or when a recording is filed —
 * see `refreshAppendedNote`) for the whole corpus, page by page, and hands
 * each page to the cache. The key sits outside the `['notes']` prefix on
 * purpose: archiving one note should not refetch every body.
 *
 * The result is a count, not the notes. The screens read the corpus back from
 * the device (`useCachedNotes`), which is the one place it has to be.
 */
export const SEARCH_CORPUS_KEY = ['search-corpus'] as const;

/** A page of two hundred is the contract's maximum. */
const CORPUS_PAGE = 200;

export function useSearchCorpus(enabled = true) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: SEARCH_CORPUS_KEY,
    queryFn: async () => {
      let cursor: string | undefined;
      let count = 0;
      do {
        const page = await api.listNotes({
          include: 'search_text',
          limit: CORPUS_PAGE,
          ...(cursor ? { cursor } : {}),
        });
        remember(() => cacheNoteList(page.items), queryClient);
        count += page.items.length;
        cursor = page.cursor || undefined;
      } while (cursor);
      return { count, fetchedAt: Date.now() };
    },
    enabled,
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  });
}

export function useNote(noteId: string | undefined) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: queryKeys.note(noteId ?? ''),
    queryFn: async () => {
      const previous = queryClient.getQueryData<NoteDetailWire>(queryKeys.note(noteId ?? ''));
      const note = await api.getNote(noteId as string);
      // The only place a full note — body and captures — enters the device.
      remember(() => cacheNoteDetail(note), queryClient);
      /*
       * A capture of this note crossed into `appended` since the last read:
       * the body on screen just grew, and so did the list's snippet and the
       * corpus. The same reconciliation the library's poll does, because the
       * library's poll is not running while this screen is.
       */
      if (
        previous &&
        newlyAppendedNoteIds(previous.captures ?? [], note.captures ?? []).length > 0
      ) {
        void queryClient.invalidateQueries({ queryKey: ['notes'] });
        void queryClient.invalidateQueries({ queryKey: SEARCH_CORPUS_KEY });
      }
      return note;
    },
    enabled: Boolean(noteId),
    /*
     * A note with a recording still moving through the pipeline is about to
     * change under the reader, and nothing else on this screen would notice:
     * the filing row's poll lives on the library and stops when the user
     * leaves it. A note opened while its own capture was still at "Uploaded"
     * sat on the pre-recording body indefinitely — one GET, then silence. So
     * while any of its captures is non-terminal the note asks again on the
     * filing cadence, and stops the moment the last one settles.
     */
    refetchInterval: (query) => capturePollInterval(query.state.data?.captures ?? []),
  });
}

/**
 * Writes a note the user has just saved into every place the app holds it.
 *
 * The PATCH's answer used to go nowhere but the editor's own reducer. With the
 * provider's thirty-second `staleTime`, leaving the note and opening it again
 * handed the editor the cached pre-edit body; the next save carried the old
 * version and was answered 409, and the screen accused "a voice capture or
 * another device" of a change the user had made themselves a moment earlier —
 * offering, as "Keep my edits", to overwrite that edit with the stale text.
 * The library rows kept the old title and snippet for the same reason.
 *
 * So the saved note replaces the detail query, its row is rewritten in every
 * cached list (the offline keys under the same prefix hold arrays, not pages,
 * and are refreshed through the device cache instead), the device's copy is
 * updated, and the lists are marked stale so the next visit re-sorts them —
 * the row's `updated_at` moved, and only the server knows the true order.
 */
export function recordSavedNote(queryClient: QueryClient, saved: NoteDetailWire): void {
  queryClient.setQueryData<NoteDetailWire>(queryKeys.note(saved.id), (current) =>
    // The captures are the cache's: a save changes none of them, and the
    // caller may not have carried them.
    current
      ? { ...current, ...saved, ...(current.captures ? { captures: current.captures } : {}) }
      : saved,
  );
  queryClient.setQueriesData<InfiniteData<Page<NoteWire>>>(
    { queryKey: ['notes'], predicate: (query) => isNoteListKey(query.queryKey) },
    (data) =>
      data
        ? {
            ...data,
            pages: data.pages.map((page) => ({
              ...page,
              items: page.items.map((item) => (item.id === saved.id ? rowOf(item, saved) : item)),
            })),
          }
        : data,
  );
  remember(() => cacheNoteDetail(saved), queryClient);
  void queryClient.invalidateQueries({ queryKey: ['notes'] });
  // A tag added or removed changes the chips as well as the row.
  void queryClient.invalidateQueries({ queryKey: queryKeys.tags() });
}

/** `['notes', { …NoteListQuery }]` — the server lists, not the device's `['notes', 'offline', …]`. */
function isNoteListKey(key: QueryKey): boolean {
  return key[0] === 'notes' && typeof key[1] === 'object' && key[1] !== null;
}

/** A list row rewritten from the saved note. The body itself never sits in a list. */
function rowOf(row: NoteWire, saved: NoteDetailWire): NoteWire {
  // An absent language means "inherits the default" again, so the row's
  // previous value must go rather than survive the spread.
  const { language: _previous, ...rest } = row;
  return {
    ...rest,
    title: saved.title,
    updated_at: saved.updated_at,
    version: saved.version,
    ...(saved.aliases ? { aliases: saved.aliases } : {}),
    ...(saved.tags ? { tags: saved.tags } : {}),
    // The server derives the snippet; when its answer omits one, the row's
    // stands until the list is refetched.
    ...(saved.snippet ? { snippet: saved.snippet } : {}),
    ...(saved.language ? { language: saved.language } : {}),
  };
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
   Usage
   --------------------------------------------------------------------------- */

/**
 * `GET /v1/usage` for one month — the current one when none is given. Stale
 * after a minute: the counters move only when a capture finishes, and a
 * screen someone is looking at while one is filing should catch up.
 */
export function useUsage(month?: string) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.usage(month),
    queryFn: () => api.getUsage(month),
    staleTime: 60_000,
  });
}

/* ---------------------------------------------------------------------------
   Captures
   --------------------------------------------------------------------------- */

/**
 * How long a recording that produced nothing keeps saying so. A `no_content`
 * row has nothing to open and nothing to retry, so it is the one receipt that
 * is allowed to expire on its own; every other stopped capture stays until
 * the user acts on it (see `isFilingRelevant`).
 */
const RECENTLY_SETTLED_MS = 10 * 60 * 1000;

/** Newest-first, and twenty is more than one person records before the first has filed. */
const CAPTURE_LIST_LIMIT = 20;

/**
 * How often to ask while something is still moving through the pipeline.
 *
 * Two cadences. A capture's first half-minute is when it is most likely to
 * flip — the median pipeline is ~4 s with a target note and 4–9 s when routed
 * (docs/ops/log-review-2026-09-04.md §3) — and a fixed 4 s poll added a median
 * 2 s of pure waiting on top of that. So a young capture is asked after every
 * 1.5 s, and once nothing in flight is younger than thirty seconds the poll
 * relaxes to 4 s: a capture that old is waiting on a provider, and asking
 * more often would only spend the user's battery watching it not move.
 */
export const CAPTURE_POLL_FAST_MS = 1_500;
export const CAPTURE_POLL_FAST_WINDOW_MS = 30_000;
export const CAPTURE_POLL_INTERVAL_MS = 4_000;

/**
 * The next poll delay for a set of captures, or `false` when nothing is
 * moving. Pure, so the cadence is testable without a query client.
 */
export function capturePollInterval(
  items: readonly CaptureWire[],
  now: number = Date.now(),
): number | false {
  const moving = items.filter((capture) => !isTerminalStatus(capture.status));
  if (moving.length === 0) return false;
  const young = moving.some((capture) => within(capture.created_at, CAPTURE_POLL_FAST_WINDOW_MS, now));
  return young ? CAPTURE_POLL_FAST_MS : CAPTURE_POLL_INTERVAL_MS;
}

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
 * `appended` always too — the row is the one place that says "your recording
 * is in this note, here it is", and it stays until the user opens the note or
 * dismisses it (`FilingRow` remembers which, per device). It used to fade
 * after ten minutes, which meant a recording made on the walk home had no
 * receipt by the time the user sat down to read it. `no_content` alone
 * expires on its own: there is nothing to open and nothing to do.
 */
export function isFilingRelevant(capture: CaptureWire, now: number = Date.now()): boolean {
  switch (capture.status) {
    case 'failed':
    case 'spend_capped':
    case 'needs_target':
    case 'appended':
      return true;
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
    refetchInterval: (query) => capturePollInterval(query.state.data?.items ?? []),
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
  // The body just grew by a transcript; the corpus on the device should know
  // the words in it before the user goes looking for them.
  void queryClient.invalidateQueries({ queryKey: SEARCH_CORPUS_KEY });
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
