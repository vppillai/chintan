import { describe, expect, it } from 'vitest';

import type { NoteDetailWire, NoteWire } from '@/api/schema.ts';

import { cacheNoteDetail, cacheNoteList, cachedNote, cachedNotes, forgetNote } from './notesCache.ts';

/**
 * The device's copy of the corpus.
 *
 * `src/test/setup.ts` swaps in a fresh IndexedDB before every case, so each of
 * these starts with an empty device.
 */

function row(over: Partial<NoteWire> = {}): NoteWire {
  return {
    id: 'roof-repair',
    title: 'Roof repair',
    snippet: 'Ridge tiles on the south slope have slipped.',
    updated_at: '2026-08-06T09:14:00.000Z',
    version: 3,
    archived: false,
    ...over,
  };
}

function detail(over: Partial<NoteDetailWire> = {}): NoteDetailWire {
  return { ...row(), body: 'Ridge tiles on the south slope have slipped.', ...over };
}

describe('what the device keeps', () => {
  it('writes every row of a list, not just the first', async () => {
    // The first version of this opened the write transaction and then awaited a
    // read inside it. IndexedDB commits a transaction the moment its microtask
    // queue drains with nothing outstanding, so the transaction closed and every
    // put after the first await was silently dropped — silently, because the
    // caller swallows cache failures by design. The symptom was an offline
    // library that was always empty.
    await cacheNoteList([
      row({ id: 'a', title: 'A', updated_at: '2026-08-01T00:00:00.000Z' }),
      row({ id: 'b', title: 'B', updated_at: '2026-08-02T00:00:00.000Z' }),
      row({ id: 'c', title: 'C', updated_at: '2026-08-03T00:00:00.000Z' }),
    ]);

    expect((await cachedNotes('active')).map((note) => note.id)).toEqual(['c', 'b', 'a']);
  });

  it('separates the archive from the library', async () => {
    await cacheNoteList([
      row({ id: 'live', archived: false }),
      row({ id: 'gone', archived: true }),
    ]);

    expect((await cachedNotes('active')).map((note) => note.id)).toEqual(['live']);
    expect((await cachedNotes('archived')).map((note) => note.id)).toEqual(['gone']);
  });

  it('refuses to hand a list row to the note screen', async () => {
    await cacheNoteList([row()]);

    // A list response carries no `body`. Serving this as a note would render a
    // real title over an empty textarea, and the first keystroke would queue a
    // PATCH that erased the note.
    expect(await cachedNote('roof-repair', { requireDetail: true })).toBeNull();
    expect(await cachedNote('roof-repair')).not.toBeNull();
  });

  it('does not let a list row overwrite the full note it already has', async () => {
    await cacheNoteDetail(detail());
    // The library is visited again straight after opening the note, which is the
    // ordinary path back. Losing the body here would mean the note is readable
    // offline only until the user taps Back.
    await cacheNoteList([row()]);

    const kept = await cachedNote('roof-repair', { requireDetail: true });
    expect(kept).not.toBeNull();
    expect((kept as NoteDetailWire).body).toContain('Ridge tiles');
  });

  it('takes a newer list row over an older full note', async () => {
    await cacheNoteDetail(detail({ version: 3, title: 'Roof repair' }));
    await cacheNoteList([
      row({ version: 4, title: 'Roof repair — Ellis', updated_at: '2026-08-07T00:00:00.000Z' }),
    ]);

    const [note] = await cachedNotes('active');
    expect(note?.title).toBe('Roof repair — Ellis');
  });

  it('forgets a note that was destroyed', async () => {
    await cacheNoteDetail(detail());
    await forgetNote('roof-repair');

    expect(await cachedNote('roof-repair')).toBeNull();
    expect(await cachedNotes('active')).toEqual([]);
  });
});
