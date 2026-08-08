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

import type { NoteDetailWire, NoteState } from '@/api/schema.ts';

import { cachedNote, cachedNotes } from './notesCache.ts';

export const cacheKeys = {
  notes: (state: NoteState) => ['notes', 'offline', state] as const,
  note: (noteId: string) => ['notes', 'offline', 'note', noteId] as const,
};

export function useCachedNotes(state: NoteState = 'active') {
  return useQuery({
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
