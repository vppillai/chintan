import { render } from '@testing-library/react';
import { IDBFactory } from 'fake-indexeddb';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';
import { TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import { resetDatabaseHandle } from './db.ts';
import { cacheNoteDetail, cacheNoteList, cachedNote } from './notesCache.ts';
import { cacheKeys, PREFETCH_BODIES, useCachedNotes } from './useNotesCache.ts';

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

/** The library: the one screen that asks for the bodies. */
function Library() {
  useCachedNotes('active', { prefetchBodies: true });
  return null;
}

/** The capture screen and Ask read the same rows and ask for nothing more. */
function Reader() {
  useCachedNotes('active');
  return null;
}

function mount(
  fetchImpl: typeof fetch,
  Screen: () => null = Library,
  queryClient = testQueryClient(),
) {
  const api = testApiContext(fetchImpl);
  render(
    <TestProviders api={api} queryClient={queryClient}>
      <Screen />
    </TestProviders>,
  );
  return api;
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

  it('fetches the whole first page when most of it lands while the first body is in the air', async () => {
    // Home fires four list GETs at mount — active, archived, the Checklists
    // chip's `kind=checklist`, the search corpus — and each one's write
    // re-triggers this hook. When the one-row checklist page landed first the
    // prefetch began on that row alone, and every re-trigger that arrived
    // during its GET found the pass running and was dropped, with nothing to
    // start it again: five cold loads stored 20, 1, 1, 20 and 1 bodies.
    await cacheNoteList([row('p7-00')]);
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const detail = detailFetch();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      await gate;
      return detail(input, init);
    });
    const queryClient = testQueryClient();

    mount(fetchImpl, Library, queryClient);
    await vi.waitFor(() => {
      expect(fetchImpl).toHaveBeenCalledTimes(1);
    });

    // The first page lands, and its write invalidates the rows as `remember`
    // does, while that one GET is still in the air.
    const rest = Array.from({ length: PREFETCH_BODIES - 1 }, (_, index) =>
      row(`p7-${String(index + 1).padStart(2, '0')}`, {
        updated_at: `2026-07-${String(1 + index).padStart(2, '0')}T00:00:00.000Z`,
      }),
    );
    await cacheNoteList(rest);
    await queryClient.invalidateQueries({ queryKey: ['notes', 'offline'] });
    await vi.waitFor(() => {
      expect(queryClient.getQueryData(cacheKeys.notes('active'))).toHaveLength(PREFETCH_BODIES);
    });
    // Let the re-render commit and the (immediate) idle callback run.
    await new Promise((resolve) => setTimeout(resolve, 10));
    release();

    await vi.waitFor(() => {
      expect(fetchedIds(fetchImpl)).toHaveLength(PREFETCH_BODIES);
    });
    expect(new Set(fetchedIds(fetchImpl)).size).toBe(PREFETCH_BODIES);
  });

  it('asks for nothing unless the screen opts in', async () => {
    // A cold launch of the Record shortcut reads these rows too, and twenty
    // GETs two seconds in would compete with `getUserMedia` for the connection.
    await cacheNoteList([row('p6-a')]);
    const fetchImpl = detailFetch();

    mount(fetchImpl, Reader);

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it('writes nothing once the session is gone, even a body already in the air', async () => {
    // Sign-out drops the token and empties the device, then leaves for
    // Cognito. A GET that resolves in between must not put one person's note
    // back on the device for the next.
    await cacheNoteList([row('p5-a')]);
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    const detail = detailFetch();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      await gate;
      return detail(input, init);
    });

    const api = mount(fetchImpl);

    await vi.waitFor(() => {
      expect(fetchImpl).toHaveBeenCalledTimes(1);
    });
    api.session.clear();
    release();

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(await cachedNote('p5-a', { requireDetail: true })).toBeNull();
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
