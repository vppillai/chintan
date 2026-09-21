import { render } from '@testing-library/react';
import { IDBFactory } from 'fake-indexeddb';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { resetDatabaseHandle } from './db.ts';
import { cacheNoteDetail, cacheNoteList, cachedNote } from './notesCache.ts';
import { PREFETCH_BODIES, useCachedNotes } from './useNotesCache.ts';

/**
 * The idle prefetch of note bodies.
 *
 * Offline, a list row alone lands on "Not on this device"; these prove the
 * first page's bodies reach the device on their own, and only those.
 */

function row(id: string, over: Partial<NoteWire> = {}): NoteWire {
  return {
    id,
    title: `Note ${id}`,
    snippet: `Snippet of ${id}.`,
    updated_at: '2026-08-06T09:14:00.000Z',
    version: 1,
    archived: false,
    ...over,
  };
}

/** `GET /v1/notes/{id}` for any id: the row plus a body. Anything else is empty. */
function detailFetch(): ReturnType<typeof vi.fn<typeof fetch>> {
  return vi.fn<typeof fetch>(async (input) => {
    const url = new URL(String(input));
    const detail = /\/v1\/notes\/([^/]+)$/.exec(url.pathname);
    const body = detail ? { ...row(detail[1] ?? ''), body: `Body of ${detail[1] ?? ''}.`, captures: [] } : { items: [] };
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  });
}

function Library() {
  useCachedNotes('active');
  return null;
}

function mount(fetchImpl: typeof fetch) {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <Library />
    </TestProviders>,
  );
}

const fetchedIds = (fetchImpl: ReturnType<typeof vi.fn<typeof fetch>>): string[] =>
  fetchImpl.mock.calls
    .map(([input]) => /\/v1\/notes\/([^/]+)$/.exec(new URL(String(input)).pathname)?.[1] ?? '')
    .filter((id) => id !== '');

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  resetDatabaseHandle();
  // jsdom has no requestIdleCallback; the browser's runs when it is idle,
  // which here is at once.
  vi.stubGlobal('requestIdleCallback', (work: IdleRequestCallback) => {
    queueMicrotask(() => {
      work({ didTimeout: false, timeRemaining: () => 50 });
    });
    return 1;
  });
  vi.stubGlobal('cancelIdleCallback', () => undefined);
});

describe('the first page\'s bodies reach the device on their own', () => {
  it('fetches the body of every bodiless row once the list is on the device', async () => {
    // Ids are unique across this file: a body asked for once is not asked
    // for again in the same session, which is what the module remembers.
    await cacheNoteList([row('p1-a'), row('p1-b', { updated_at: '2026-08-05T00:00:00.000Z' })]);
    const fetchImpl = detailFetch();

    mount(fetchImpl);

    await vi.waitFor(async () => {
      expect(await cachedNote('p1-b', { requireDetail: true })).not.toBeNull();
    });
    expect(await cachedNote('p1-a', { requireDetail: true })).not.toBeNull();
    expect(fetchedIds(fetchImpl).sort()).toEqual(['p1-a', 'p1-b']);
  });

  it('leaves a note the device already holds in full alone, and skips the archive', async () => {
    await cacheNoteDetail({ ...row('p2-full'), body: 'Already here.', captures: [] });
    await cacheNoteList([row('p2-row'), row('p2-archived', { archived: true })]);
    const fetchImpl = detailFetch();

    mount(fetchImpl);

    await vi.waitFor(async () => {
      expect(await cachedNote('p2-row', { requireDetail: true })).not.toBeNull();
    });
    expect(fetchedIds(fetchImpl)).toEqual(['p2-row']);
  });

  it('stops at the first page, newest first', async () => {
    const rows = Array.from({ length: PREFETCH_BODIES + 5 }, (_, index) =>
      row(`p3-${String(index).padStart(2, '0')}`, {
        updated_at: `2026-07-${String(1 + index).padStart(2, '0')}T00:00:00.000Z`,
      }),
    );
    await cacheNoteList(rows);
    const fetchImpl = detailFetch();

    mount(fetchImpl);

    await vi.waitFor(() => {
      expect(fetchedIds(fetchImpl)).toHaveLength(PREFETCH_BODIES);
    });
    // The five oldest — the second page — are not asked for.
    const asked = new Set(fetchedIds(fetchImpl));
    for (const skipped of rows.slice(0, 5)) expect(asked.has(skipped.id)).toBe(false);
    for (const wanted of rows.slice(5)) expect(asked.has(wanted.id)).toBe(true);
  });

  it('asks the network for nothing while offline', async () => {
    await cacheNoteList([row('p4-a')]);
    const fetchImpl = detailFetch();
    const onLine = vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false);

    mount(fetchImpl);

    // Long enough for the (immediate) idle callback and any fetch to have run.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(fetchImpl).not.toHaveBeenCalled();
    onLine.mockRestore();
  });
});
