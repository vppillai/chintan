/**
 * TanStack Query bindings for notes: the lists, the detail, the search, and
 * every mutation that moves a note between states. Server state lives here;
 * nothing else caches it.
 */

import {
  focusManager,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryClient,
  type QueryKey,
} from '@tanstack/react-query';
import { useEffect } from 'react';

import { cacheNoteDetail, cacheNoteList, forgetNote } from '@/offline/notesCache.ts';

import { useApi } from '../ApiProvider.tsx';
import type { NoteCreateWire, NoteDetailWire, NoteListQuery, NoteWire, Page } from '../schema.ts';

import { newlyAppendedNoteIds } from './captures.ts';
import { SEARCH_CORPUS_KEY, invalidateNoteLists, queryKeys } from './keys.ts';

/**
 * Writes a note or a page of notes to the device, and never lets that failure
 * become the request's failure.
 *
 * Caching is a side effect of reading. A browser in private mode, a full quota
 * or a blocked-storage policy must degrade to "no offline copy", not to "your
 * notes would not load".
 */
export function remember(write: () => Promise<void>, queryClient?: QueryClient): void {
  void write()
    .then(() => {
      if (queryClient) tellDeviceReaders(queryClient);
    })
    .catch(() => {
      /* No offline copy this time. The screen is unaffected. */
    });
}

const deviceReadersPending = new WeakSet<QueryClient>();

/**
 * Tell any screen already reading the device that it has more to read — once
 * per tick, however many writes landed in it.
 *
 * Every invalidation re-reads the whole store (`cachedNotes`), and one Home
 * launch writes a page per list plus a page per two hundred notes of the
 * corpus. Scoped to `['notes', 'offline']` and never to `['notes']`:
 * invalidating the wider prefix from inside a notes query's own success
 * handler would refetch the query that just wrote, forever.
 */
function tellDeviceReaders(queryClient: QueryClient): void {
  if (deviceReadersPending.has(queryClient)) return;
  deviceReadersPending.add(queryClient);
  setTimeout(() => {
    deviceReadersPending.delete(queryClient);
    void queryClient.invalidateQueries({ queryKey: ['notes', 'offline'] });
  }, 0);
}

/** Past this far down the library, a refetch keeps every page the reader has loaded. */
const NEAR_TOP_PX = 400;

function nearTop(): boolean {
  if (typeof document === 'undefined') return true;
  const main = document.querySelector('.app__main');
  return (main ? main.scrollTop : window.scrollY) < NEAR_TOP_PX;
}

/** An infinite list cut back to its first page, so the next refetch asks for one. */
function firstPageOnly<T>(data: InfiniteData<T> | undefined): InfiniteData<T> | undefined {
  if (!data || data.pages.length <= 1) return data;
  return { pages: data.pages.slice(0, 1), pageParams: data.pageParams.slice(0, 1) };
}

/**
 * Pull-to-refresh on the library: every list and the device's copy asked
 * again, each list for its first page only.
 *
 * TanStack refetches an infinite query page by page, one after another, for
 * every page loaded — ten deep, ten sequential GETs to answer "is this
 * current?". The gesture only exists at the top of the list, so nothing the
 * reader can see is lost; scrolling down loads the rest again.
 */
export function refreshNoteLists(queryClient: QueryClient): Promise<void> {
  queryClient.setQueriesData<InfiniteData<Page<NoteWire>>>(
    { queryKey: ['notes'], predicate: (query) => isNoteListKey(query.queryKey) },
    firstPageOnly,
  );
  return queryClient.invalidateQueries({ queryKey: ['notes'] });
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
    /*
     * Coming back to the app near the top of the list asks for the first
     * page only (see `refreshNoteLists`). Deep in it, every loaded page is
     * refetched as before: cutting the list back would pull the rows out
     * from under the reader.
     */
    refetchOnWindowFocus: (focused) => {
      if (!focused.isStale()) return false;
      if (nearTop()) {
        const held = focused.state.data;
        const trimmed = firstPageOnly(held);
        if (trimmed && trimmed !== held) {
          focused.setData(trimmed as NonNullable<typeof held>, { updatedAt: focused.state.dataUpdatedAt, manual: true });
        }
      }
      return 'always';
    },
  });
}

/** A page of two hundred is the contract's maximum. */
const CORPUS_PAGE = 200;

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

/** The detail query's key and fetch, shared by every observer of one note. */
function useNoteQueryOptions(noteId: string | undefined) {
  const api = useApi();
  const queryClient = useQueryClient();
  return {
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
  };
}

/**
 * One note, body and captures. While any capture is still filing the note
 * screen asks after those captures on its own (`useInFlightCaptures`) and
 * this query is read again when one settles.
 */
export function useNote(noteId: string | undefined) {
  return useQuery({ ...useNoteQueryOptions(noteId), enabled: Boolean(noteId) });
}

/**
 * The open note asked for again while the caller waits for something only a
 * fresh read can show — a rewrite or a tidy, which a 202 promises and the
 * note's `cleaned` later carries. `ladder` gives the next delay for the time
 * since `since`, or false to stop; it must be a module-level function, as a
 * new one each render restarts the timer. `since` is null when there is
 * nothing to wait for.
 *
 * A timer of its own, not a `refetchInterval`: TanStack restarts an
 * observer's interval on every update to the query, and while a recording
 * is filing its poll rewrites the note every 1.5 s, so a 2 s interval never
 * fired and the rewrite never landed (R8-F8a, F8b). The refetch still goes
 * through the detail query, so the answer lands where the screen reads and
 * a read already in flight is joined; and a tick while the app is in the
 * background asks nothing, so a pocketed phone is left alone.
 */
export function usePollNote(
  noteId: string,
  since: number | null,
  ladder: (elapsedMs: number) => number | false,
): void {
  const queryClient = useQueryClient();
  useEffect(() => {
    if (since === null) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const next = (): void => {
      const delay = ladder(Date.now() - since);
      if (delay === false) return;
      timer = setTimeout(() => {
        if (focusManager.isFocused()) {
          void queryClient.refetchQueries({ queryKey: queryKeys.note(noteId), exact: true });
        }
        next();
      }, delay);
    };
    next();
    return () => {
      clearTimeout(timer);
    };
  }, [since, noteId, ladder, queryClient]);
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
 * updated, and the lists are marked stale — without a refetch — so the next
 * visit re-sorts them: the row's `updated_at` moved, and only the server
 * knows the true order.
 */
export function recordSavedNote(queryClient: QueryClient, saved: NoteDetailWire): void {
  queryClient.setQueryData<NoteDetailWire>(queryKeys.note(saved.id), (current) =>
    // The captures are the cache's: a save changes none of them, and the
    // caller may not have carried them.
    current
      ? { ...current, ...saved, ...(current.captures ? { captures: current.captures } : {}) }
      : saved,
  );
  patchNoteLists(queryClient, (item) => (item.id === saved.id ? rowOf(item, saved) : item));
  remember(() => cacheNoteDetail(saved), queryClient);
  /*
   * Marked stale, not refetched. Autosave runs this every few seconds of
   * typing, and invalidating the whole prefix refetched every loaded page of
   * every mounted list each time. The rows are patched above; the order is
   * the server's to decide and is asked for on the next visit.
   */
  void queryClient.invalidateQueries({
    queryKey: ['notes'],
    predicate: (query) => isNoteListKey(query.queryKey),
    refetchType: 'none',
  });
  // A tag added or removed changes the chips as well as the row.
  void queryClient.invalidateQueries({ queryKey: queryKeys.tags() });
}

/** `['notes', { …NoteListQuery }]` — the server lists, not the device's `['notes', 'offline', …]`. */
function isNoteListKey(key: QueryKey): boolean {
  return key[0] === 'notes' && typeof key[1] === 'object' && key[1] !== null;
}

type NoteLists = [QueryKey, InfiniteData<Page<NoteWire>> | undefined][];

/** Rewrites every row of every cached server list through `update`. */
function patchNoteLists(queryClient: QueryClient, update: (row: NoteWire) => NoteWire): void {
  queryClient.setQueriesData<InfiniteData<Page<NoteWire>>>(
    { queryKey: ['notes'], predicate: (query) => isNoteListKey(query.queryKey) },
    (data) =>
      data
        ? { ...data, pages: data.pages.map((page) => ({ ...page, items: page.items.map(update) })) }
        : data,
  );
}

/**
 * The lists as they stand, taken before an optimistic write so a refused
 * request can put them back. In-flight fetches are cancelled first, or one
 * landing between the write and the rollback would be overwritten by it.
 */
async function snapshotNoteLists(queryClient: QueryClient): Promise<NoteLists> {
  await queryClient.cancelQueries({ queryKey: ['notes'] });
  return queryClient.getQueriesData<InfiniteData<Page<NoteWire>>>({
    queryKey: ['notes'],
    predicate: (query) => isNoteListKey(query.queryKey),
  });
}

function restoreNoteLists(queryClient: QueryClient, lists: NoteLists | undefined): void {
  for (const [key, data] of lists ?? []) queryClient.setQueryData(key, data);
}

/* ---------------------------------------------------------------------------
   Pinned notes (2026-09-24 contract, B)

   Both writes are shown before the server answers — a pin is a one-tap
   promise about where a note sits, and waiting a round trip to move it reads
   as the tap not landing — and put back if the server refuses. The server
   owns the rank: a pin lands last among the pinned, a drag sends the whole
   order, and the lists are refetched afterwards either way so what is shown
   is what was stored.
   --------------------------------------------------------------------------- */

/**
 * `PATCH /v1/notes/{id} {pinned}`. The row's version rides along because the
 * endpoint requires one; the server may relax that for a pin-only PATCH.
 *
 * The answer is written back before the lists are refetched. Every pin bumps
 * the note's version, so until the refetch lands the cached row is one behind
 * the server, and "Pin, reopen ⋮, Unpin" — an ordinary mis-tap correction —
 * sent that stale version and was refused 409, leaving the note pinned with
 * no message (review 2026-09-24, R4-3). The rows keep the rank the server
 * chose, too, rather than the one guessed below.
 */
export function usePinNote() {
  const api = useApi();
  const queryClient = useQueryClient();
  // Both pin fields are optional on the row type (rows cached before pins
  // existed carry neither), so an absent `pinned` reads as not pinned and an
  // absent rank as none; the server always sends both.
  const fromServer = (saved: NoteWire) => ({
    pinned: saved.pinned ?? false,
    pin_rank: saved.pin_rank ?? null,
    version: saved.version,
    updated_at: saved.updated_at,
  });
  return useMutation({
    mutationFn: ({ note, pinned }: { note: Pick<NoteWire, 'id' | 'version'>; pinned: boolean }) =>
      api.updateNote(note.id, { version: note.version, pinned }),
    onSuccess: (saved, { note }) => {
      patchNoteLists(queryClient, (row) =>
        row.id === note.id ? { ...row, ...fromServer(saved) } : row,
      );
      queryClient.setQueryData<NoteDetailWire>(queryKeys.note(note.id), (current) =>
        current ? { ...current, ...fromServer(saved) } : current,
      );
    },
    onMutate: async ({ note, pinned }) => {
      const lists = await snapshotNoteLists(queryClient);
      // A new pin lands after every pin already there, as the server ranks it.
      const last = Math.max(
        -1000,
        ...lists.flatMap(([, data]) =>
          (data?.pages ?? []).flatMap((page) => page.items.map((row) => row.pin_rank ?? -1000)),
        ),
      );
      const rank = pinned ? last + 1000 : null;
      patchNoteLists(queryClient, (row) =>
        row.id === note.id ? { ...row, pinned, pin_rank: rank } : row,
      );
      const detail = queryClient.getQueryData<NoteDetailWire>(queryKeys.note(note.id));
      if (detail) {
        queryClient.setQueryData(queryKeys.note(note.id), { ...detail, pinned, pin_rank: rank });
      }
      return { lists, detail };
    },
    onError: (_error, { note }, context) => {
      restoreNoteLists(queryClient, context?.lists);
      if (context?.detail) queryClient.setQueryData(queryKeys.note(note.id), context.detail);
    },
    onSettled: (_result, _error, { note }) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(note.id) });
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
}

/** `POST /v1/notes/pins {ids}`: one request per drag, the whole pinned order. */
export function useReorderPins() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[]) => api.reorderPins({ ids }),
    onMutate: async (ids) => {
      const lists = await snapshotNoteLists(queryClient);
      const rank = new Map(ids.map((id, index) => [id, index * 1000]));
      patchNoteLists(queryClient, (row) => {
        const next = rank.get(row.id);
        return next === undefined ? row : { ...row, pinned: true, pin_rank: next };
      });
      return { lists };
    },
    onError: (_error, _ids, context) => {
      restoreNoteLists(queryClient, context?.lists);
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ['notes'] });
    },
  });
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


/**
 * `POST /v1/notes` with a title and a body written on this device — the Ask
 * panel's "Save as note". The recorder is the usual way a note comes to exist,
 * so this is the one place the client creates one whole; the lists are
 * invalidated rather than patched because the new row's day group and tag
 * chips are the server's to decide.
 */
export function useCreateNote() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: NoteCreateWire) => api.createNote(body),
    onSuccess: () => {
      invalidateNoteLists(queryClient);
    },
  });
}

/**
 * `POST /v1/notes/{id}/regenerate`: the note's recordings cleaned again with
 * the current prompts. The detail is refetched on the 202 rather than patched:
 * the captures have gone back to `transcribed` on the server, and reading them
 * is what starts `useNote`'s poll and puts the filing strip on the screen
 * until the last one lands.
 */
export function useRegenerateNote() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (noteId: string) => api.regenerateNote(noteId),
    onSuccess: (_queued, noteId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(noteId) });
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
      invalidateNoteLists(queryClient);
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
      invalidateNoteLists(queryClient);
    },
  });
}

/**
 * Undo of a delete, which is an archive: restore the note, and pin it again
 * if it was pinned. The server clears the pin on archive and `restoreNote`
 * comes back unpinned — a pin was a place on Home, and the note had left
 * Home — which is right for Restore in the archive and wrong for Undo, whose
 * promise is the note back as it was. Restore returns the row with its new
 * version, which is what the re-pin has to carry.
 */
export function useUndoDelete() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, pinned }: Pick<NoteWire, 'id' | 'pinned'>) => {
      const restored = await api.restoreNote(id);
      if (pinned) await api.updateNote(id, { version: restored.version, pinned: true });
    },
    onSuccess: (_result, { id }) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.note(id) });
      invalidateNoteLists(queryClient);
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
      invalidateNoteLists(queryClient);
    },
  });
}

/**
 * How long the search field has to hold still before the server is asked.
 *
 * The device's corpus answers every keystroke; this is only for the request
 * that costs a round trip. Typing a word at 60 ms a key sent one
 * `GET /v1/search` per letter, and the answers landed out of order.
 */
export const SERVER_SEARCH_DEBOUNCE_MS = 250;

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
