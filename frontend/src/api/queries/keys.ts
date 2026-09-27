/**
 * Query keys, built by one function per resource so an invalidation cannot
 * miss a key by typing the array out differently at the call site — and the
 * invalidations several resources share. Its own module so `notes.ts` and
 * `captures.ts` can both read it without importing each other.
 */

import type { QueryClient } from '@tanstack/react-query';

import type { CaptureListQuery, NoteListQuery } from '../schema.ts';

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
};

/** The offline search corpus (`useSearchCorpus`); outside the `['notes']` prefix on purpose. */
export const SEARCH_CORPUS_KEY = ['search-corpus'] as const;

/**
 * What a note moving between states leaves stale: every list under `['notes']`
 * — active, archived, the device's copies — and the tag chips, which are
 * derived from the active notes on the server. The chips used to be left out,
 * so a tag whose last note had just been deleted forever kept its chip, and
 * pressing it said "No notes are tagged …" until a reload (QA D16).
 */
export function invalidateNoteLists(queryClient: QueryClient): void {
  void queryClient.invalidateQueries({ queryKey: ['notes'] });
  void queryClient.invalidateQueries({ queryKey: queryKeys.tags() });
}
