/**
 * Query keys, built by one function per resource so an invalidation cannot
 * miss a key by typing the array out differently at the call site — and the
 * invalidations several resources share. Its own module so `notes.ts` and
 * `captures.ts` can both read it without importing each other.
 */

import type { QueryClient, QueryKey } from '@tanstack/react-query';

import type { CaptureListQuery, NoteListQuery, NoteState } from '../schema.ts';

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
  ask: (askId: string) => ['ask', askId] as const,
  devices: () => ['devices'] as const,
  pushKey: () => ['push', 'key'] as const,
  pushSubscriptions: () => ['push', 'subscriptions'] as const,
  /*
   * The device's copies (`useNotesCache`), under the `notes` prefix on purpose
   * so pull-to-refresh and the offline banner's lookup see them with the
   * server lists; `isNoteListKey` is how a caller leaves them out.
   */
  offlineNotes: (state: NoteState) => [...OFFLINE_NOTES_KEY, state] as const,
  offlineNote: (noteId: string) => [...OFFLINE_NOTES_KEY, 'note', noteId] as const,
  /** Both under `['captures']`, so every invalidation of the capture lists re-reads them too. */
  recordedHere: () => ['captures', 'recorded-here'] as const,
  unsentCaptures: () => ['captures', 'unsent'] as const,
  // Keyed on the capture's write version as well as its id: transcribing
  // again replaces the segments behind the same id.
  captureArtifacts: (captureId: string, version: number) =>
    ['capture-artifacts', captureId, version] as const,
  unsentWork: () => ['auth', 'unsent-work'] as const,
  /** The flush query (`useOfflineQueue`); its depth is what the offline banner counts. */
  offlineQueue: () => ['offline', 'queue'] as const,
  /*
   * A note's queued PATCH. Deliberately not under `['offline', 'queue']`:
   * the flush invalidates these from inside its own queryFn, which would loop
   * if they shared a prefix.
   */
  queuedEdits: () => ['queued-edit'] as const,
  queuedEdit: (noteId: string) => ['queued-edit', noteId] as const,
};

/** The prefix every device copy sits under: what `remember` tells once per tick. */
export const OFFLINE_NOTES_KEY = ['notes', 'offline'] as const;

/** `['notes', { …NoteListQuery }]` — the server lists, not the device's `['notes', 'offline', …]`. */
export function isNoteListKey(key: QueryKey): boolean {
  return key[0] === 'notes' && typeof key[1] === 'object' && key[1] !== null;
}

/** The offline search corpus (`useSearchCorpus`); outside the `['notes']` prefix on purpose. */
export const SEARCH_CORPUS_KEY = ['search-corpus'] as const;

/**
 * What a note moving between states leaves stale: every server list under
 * `['notes']` — active and archived — and the tag chips, which are derived
 * from the active notes on the server. The chips used to be left out, so a
 * tag whose last note had just been deleted forever kept its chip, and
 * pressing it said "No notes are tagged …" until a reload (QA D16).
 *
 * The device's copies are left alone: each is re-read from IndexedDB in full
 * on every invalidation (`cachedNotes`), and `remember` already tells their
 * readers once the refetched list has been written to the device, which is
 * the moment they have something new to read.
 */
export function invalidateNoteLists(queryClient: QueryClient): void {
  void queryClient.invalidateQueries({
    queryKey: ['notes'],
    predicate: (query) => isNoteListKey(query.queryKey),
  });
  void queryClient.invalidateQueries({ queryKey: queryKeys.tags() });
}
