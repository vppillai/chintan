/**
 * Reading the cached corpus, as query state.
 *
 * Deliberately separate hooks rather than a fallback inside `useNotes`.
 * TanStack *pauses* a server query when the browser reports no connection —
 * that is the behaviour the library screen already depends on, and the reason
 * an offline library must never look like a fetch that failed — so the cache
 * cannot be reached from inside a query that is not running. It is also not
 * server state: IndexedDB is on this device and always available, which is why
 * these declare `networkMode: 'always'`.
 *
 * The keys sit under the `notes` prefix on purpose, so
 * `invalidateQueries({ queryKey: ['notes'] })` after a mutation refreshes what
 * the device has stored along with what the server has.
 */

import { useQuery } from '@tanstack/react-query';
import { useEffect } from 'react';

import { useApi, useSession } from '@/api/ApiProvider.tsx';
import type { ChintanApi } from '@/api/endpoints.ts';
import { ApiError } from '@/api/problem.ts';
import type { NoteDetailWire, NoteState, NoteWire } from '@/api/schema.ts';
import type { Session } from '@/api/session.ts';

import { cacheNoteDetail, cachedNote, cachedNotes, notesWithoutBody } from './notesCache.ts';

export const cacheKeys = {
  notes: (state: NoteState) => ['notes', 'offline', state] as const,
  note: (noteId: string) => ['notes', 'offline', 'note', noteId] as const,
};

export interface CachedNotesOptions {
  /**
   * Fetch the first page's bodies once its rows are on the device. Only the
   * library asks for this. The capture screen and Ask read the same rows, and
   * a cold launch of the Record shortcut must not have twenty GETs competing
   * with `getUserMedia` for the connection a couple of seconds in.
   */
  prefetchBodies?: boolean;
}

export function useCachedNotes(
  state: NoteState = 'active',
  { prefetchBodies: wantBodies = false }: CachedNotesOptions = {},
) {
  const query = useQuery({
    queryKey: cacheKeys.notes(state),
    queryFn: () => cachedNotes(state),
    networkMode: 'always',
    /*
     * Always stale, deliberately. What is on the device changes as a side
     * effect of every successful read elsewhere in the app, and this key is
     * shared by every screen that consults it — so a cached "empty" answer from
     * the moment the library first mounted would be handed straight to Search a
     * second later, which is how offline search came to report that a note the
     * user had just been looking at did not exist.
     */
    staleTime: 0,
    retry: false,
  });
  usePrefetchBodies(wantBodies && state === 'active' ? query.data : undefined);
  return query;
}

/** How many of the newest notes are fetched in full for offline reading. */
export const PREFETCH_BODIES = 20;

/** `${id}@${version}` of every body asked for this session, fetched or not. */
const attempted = new Set<string>();
let prefetching = false;
/** Rows landed while a pass was running, so the pass goes once more when it ends. */
let rowsArrivedMeanwhile = false;

/**
 * Fetches the bodies of the newest notes into IndexedDB while the app is idle.
 *
 * A list row is not a note: offline, every row was tappable and an unopened
 * one landed on "Not on this device". After the list arrives — every write to
 * the device invalidates this hook's key, so the rows are the trigger — the
 * first page's bodies are fetched one at a time, in an idle callback so the
 * fetch never competes with the list rendering, and only for rows the device
 * holds as a list row alone. Each body is asked for once per session per
 * version; a failure leaves the row as it was, and the next session asks again.
 */
function usePrefetchBodies(rows: readonly NoteWire[] | undefined): void {
  const api = useApi();
  const session = useSession();
  useEffect(() => {
    if (!rows || rows.length === 0) return;
    if (typeof navigator !== 'undefined' && !navigator.onLine) return;
    return whenIdle(() => {
      void prefetchBodies(api, session);
    });
  }, [rows, api, session]);
}

/** Safari has no `requestIdleCallback`; a short delay is the next best "later". */
function whenIdle(work: () => void): () => void {
  if (typeof requestIdleCallback === 'function') {
    const handle = requestIdleCallback(work, { timeout: 10_000 });
    return () => {
      cancelIdleCallback(handle);
    };
  }
  const handle = setTimeout(work, 2_000);
  return () => {
    clearTimeout(handle);
  };
}

/**
 * Runs passes until one ends with no rows having landed during it.
 *
 * Home fires four list GETs at mount — active, archived, the Checklists
 * chip's `kind=checklist`, the search corpus — and each one's write
 * re-triggers the hook. A trigger that found a pass running used to be
 * dropped, and when the one-row checklist page landed first that pass fetched
 * one body while the other three pages landed during its GET: five cold loads
 * stored 20, 1, 1, 20 and 1 bodies. `attempted` keeps the extra pass cheap —
 * it asks only for rows no pass has asked for yet.
 */
async function prefetchBodies(api: ChintanApi, session: Session): Promise<void> {
  if (prefetching) {
    rowsArrivedMeanwhile = true;
    return;
  }
  prefetching = true;
  try {
    let again = true;
    while (again) {
      rowsArrivedMeanwhile = false;
      again = (await prefetchPass(api, session)) && rowsArrivedMeanwhile;
    }
  } finally {
    prefetching = false;
  }
}

/**
 * One pass over the newest rows without a body. False when it stopped early —
 * the connection or the session went — since another pass would end the same
 * way; the next list arrival tries again.
 */
async function prefetchPass(api: ChintanApi, session: Session): Promise<boolean> {
  try {
    const wanted = (await notesWithoutBody(PREFETCH_BODIES)).filter(
      (note) => !attempted.has(`${note.id}@${String(note.version)}`),
    );
    for (const note of wanted) {
      /*
       * Sign-out drops the token, empties the device, and then leaves for
       * Cognito's `/logout`. A body written in between is one person's note
       * left on the device for the next — the very thing sign-out exists to
       * prevent — so the session is checked on both sides of the GET: it can
       * go while the request is in the air.
       */
      if (!session.isAuthenticated()) return false;
      attempted.add(`${note.id}@${String(note.version)}`);
      try {
        const detail = await api.getNote(note.id);
        if (!session.isAuthenticated()) return false;
        await cacheNoteDetail(detail);
      } catch (error) {
        // The connection went, or the session did: the rest would fail the
        // same way, so stop here and let the next list arrival try again. A
        // single refusal — a note purged since the list was read — skips only
        // that note.
        if (error instanceof ApiError && (error.isOffline || error.isUnauthorized)) return false;
      }
    }
    return true;
  } catch {
    /* Storage denied: no offline copy this time, and the screen is unaffected. */
    return false;
  }
}

/**
 * The cached full note, or null.
 *
 * `requireDetail` is not optional here: a list row has no `body`, and handing
 * one to the note screen would render a real title over an empty textarea. The
 * first keystroke would then queue a PATCH that erases the note.
 */
export function useCachedNote(noteId: string | undefined) {
  return useQuery({
    queryKey: cacheKeys.note(noteId ?? ''),
    queryFn: async (): Promise<NoteDetailWire | null> =>
      ((await cachedNote(noteId as string, { requireDetail: true })) as
        | NoteDetailWire
        | null) ?? null,
    enabled: Boolean(noteId),
    networkMode: 'always',
    staleTime: 5_000,
    retry: false,
  });
}
