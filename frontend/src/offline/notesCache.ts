/**
 * The cached note corpus.
 *
 * IndexedDB holds the note corpus for offline reading and instant search.
 * Without a notes store, opening a note with no connection reports "No note
 * with that identifier. It may have been archived or purged" about a note the
 * user was looking at one screen earlier, and searching for it says it does
 * not exist.
 *
 * Everything the app reads from the server passes through here on the way to
 * the screen, so the cache is a side effect of using the app rather than a sync
 * the user has to think about.
 *
 * Two rules the shape of the store exists to enforce:
 *
 *   A list row is not a note. `GET /v1/notes` returns no `body` and no
 *   `captures`, so a cached row is marked `detail: false` and is never handed
 *   to the note screen as a complete note — presenting an empty body as the
 *   note's text is how an offline read silently destroys work when the user
 *   then types into it.
 *
 *   Newer wins. A list row must not overwrite a detail record that is at least
 *   as fresh, or opening a note and then returning to the library would throw
 *   away the only full copy on the device.
 */

import type { NoteDetailWire, NoteWire } from '@/api/schema.ts';

import { openChintanDB, type CachedNote } from './db.ts';

function record(note: NoteWire | NoteDetailWire, detail: boolean): CachedNote {
  return {
    id: note.id,
    note,
    detail,
    archived: note.archived,
    updatedAt: note.updated_at,
    cachedAt: Date.now(),
  };
}

/**
 * True when `next` should replace `existing`.
 *
 * A detail record is never replaced by a list row of the same vintage, and
 * nothing is replaced by something the server says is older. `version` is the
 * authority where both records have one; `updated_at` is the fallback, and it
 * is fixed-width RFC3339 by contract so a string compare is chronological.
 */
function supersedes(next: CachedNote, existing: CachedNote | undefined): boolean {
  if (!existing) return true;
  const newer =
    next.note.version > existing.note.version || next.updatedAt > existing.updatedAt;
  if (newer) return true;
  if (existing.detail && !next.detail) return false;
  /*
   * Same vintage, both list rows: a row carrying `search_text` (from the
   * corpus request) is not replaced by the plain library row that arrives on
   * every visit, or the words the search relies on would vanish between one
   * corpus fetch and the next.
   */
  if (
    !next.detail &&
    typeof existing.note.search_text === 'string' &&
    typeof next.note.search_text !== 'string'
  ) {
    return false;
  }
  return true;
}

/** Caches one full note, as returned by `GET /v1/notes/{id}`. */
export async function cacheNoteDetail(note: NoteDetailWire): Promise<void> {
  const db = await openChintanDB();
  const next = record(note, true);
  const existing = await db.get('notes', note.id);
  if (!supersedes(next, existing)) return;
  await db.put('notes', next);
}

/**
 * Caches a page of list rows.
 *
 * Every record is read *before* the write transaction opens, and every `put` is
 * then issued without an intervening `await`. An IndexedDB transaction commits
 * itself the moment its microtask queue drains with no request outstanding, so
 * awaiting a read inside the write transaction closes it and the puts that
 * follow are silently lost — silently, because the caller deliberately swallows
 * cache failures. The symptom is an offline library that is always empty.
 */
export async function cacheNoteList(notes: readonly NoteWire[]): Promise<void> {
  if (notes.length === 0) return;
  const db = await openChintanDB();

  const existing = new Map((await db.getAll('notes')).map((entry) => [entry.id, entry]));

  const tx = db.transaction('notes', 'readwrite');
  const store = tx.objectStore('notes');
  for (const note of notes) {
    const next = record(note, false);
    if (!supersedes(next, existing.get(note.id))) continue;
    void store.put(next);
  }
  await tx.done;
}

/**
 * The cached copy of one note, or null.
 *
 * `requireDetail` is what stops a list row being served to the note screen. The
 * screen would render a real title over an empty body, and the first keystroke
 * would queue a PATCH that erases everything the note contained.
 */
export async function cachedNote(
  noteId: string,
  options: { requireDetail?: boolean } = {},
): Promise<NoteDetailWire | NoteWire | null> {
  const db = await openChintanDB();
  const found = await db.get('notes', noteId);
  if (!found) return null;
  if (options.requireDetail && !found.detail) return null;
  return found.note;
}

/** Every cached note in the given state, newest first — the library's order. */
export async function cachedNotes(
  state: 'active' | 'archived' = 'active',
): Promise<NoteWire[]> {
  const db = await openChintanDB();
  const all = await db.getAllFromIndex('notes', 'byUpdatedAt');
  return all
    .filter((entry) => entry.archived === (state === 'archived'))
    .map((entry) => entry.note)
    .reverse();
}

/**
 * Forgets one note.
 *
 * Called when the server says it is gone for good. Leaving it would make the
 * offline library disagree with the online one about a note the user
 * deliberately destroyed, which is the one disagreement that is not tolerable.
 */
export async function forgetNote(noteId: string): Promise<void> {
  const db = await openChintanDB();
  await db.delete('notes', noteId);
}
