import { focusManager, type QueryClient } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { flushTasks } from '@/test/clock.ts';
import { TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import type { NoteDetailWire, NoteWire } from '../schema.ts';

import { OFFLINE_NOTES_KEY, invalidateNoteLists, queryKeys } from './keys.ts';
import { recordSavedNote, refreshNoteLists, remember, useNotes } from './notes.ts';

/**
 * What the library's lists cost: how many GETs a save, a pull and a return
 * to the app make. Counted at the fetch boundary.
 */

function row(n: number): NoteWire {
  return {
    id: `note-${String(n)}`,
    title: `Note ${String(n)}`,
    updated_at: new Date(Date.UTC(2026, 8, 1) - n * 60_000).toISOString(),
    version: 1,
    archived: false,
  };
}

const PAGE = 2;
const ROWS = Array.from({ length: 6 }, (_, n) => row(n));

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

function server() {
  const lists: string[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/v1/notes')) {
      lists.push(url.search);
      const start = Number(url.searchParams.get('cursor') ?? '0');
      const items = ROWS.slice(start, start + PAGE);
      const next = start + PAGE < ROWS.length ? String(start + PAGE) : '';
      return json({ items, cursor: next });
    }
    return json({ items: [] });
  });
  return { fetchImpl, lists };
}

/** The active list, three pages deep, with the requests so far forgotten. */
async function threePages() {
  const { fetchImpl, lists } = server();
  const queryClient: QueryClient = testQueryClient();
  const wrapper = ({ children }: { children: ReactNode }) => (
    <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
      {children}
    </TestProviders>
  );
  const { result } = renderHook(() => useNotes(), { wrapper });
  await waitFor(() => {
    expect(result.current.data?.pages).toHaveLength(1);
  });
  for (const pages of [2, 3]) {
    await act(() => result.current.fetchNextPage());
    await waitFor(() => {
      expect(result.current.data?.pages).toHaveLength(pages);
    });
  }
  lists.length = 0;
  return { lists, queryClient, result };
}

const aMoment = () => act(() => flushTasks());

function returnToTheApp(): void {
  act(() => {
    focusManager.setFocused(false);
    focusManager.setFocused(true);
  });
}

afterEach(() => {
  document.querySelector('.app__main')?.remove();
  focusManager.setFocused(undefined);
});

describe('the cost of keeping the lists current', () => {
  it('a save patches the row and asks for no list', async () => {
    // Autosave ran this every few seconds of typing, and it refetched every
    // loaded page of every mounted list each time.
    const { lists, queryClient, result } = await threePages();
    const saved: NoteDetailWire = { ...row(3), title: 'Renamed', version: 2, body: 'x' };

    act(() => {
      recordSavedNote(queryClient, saved);
    });
    await aMoment();

    expect(lists).toEqual([]);
    const titles = result.current.data?.pages.flatMap((page) => page.items.map((item) => item.title));
    expect(titles).toContain('Renamed');
    // Stale, so the next visit re-sorts it.
    expect(queryClient.getQueryState(queryKeys.notes())?.isInvalidated).toBe(true);
  });

  it('pull-to-refresh asks for the first page only', async () => {
    const { lists, queryClient, result } = await threePages();

    await act(() => refreshNoteLists(queryClient));

    expect(lists).toEqual(['']);
    await waitFor(() => {
      expect(result.current.data?.pages).toHaveLength(1);
    });
  });

  it('coming back to the app near the top asks for the first page only', async () => {
    const { lists } = await threePages();

    returnToTheApp();
    await waitFor(() => {
      expect(lists).toHaveLength(1);
    });
    await aMoment();
    expect(lists).toEqual(['']);
  });

  it('coming back deep in the list keeps every loaded page', async () => {
    const main = document.createElement('div');
    main.className = 'app__main';
    Object.defineProperty(main, 'scrollTop', { value: 5_000, configurable: true });
    document.body.append(main);
    const { lists, result } = await threePages();

    returnToTheApp();
    await waitFor(() => {
      expect(lists).toHaveLength(3);
    });
    expect(result.current.data?.pages).toHaveLength(3);
  });
});

describe('invalidateNoteLists', () => {
  it('marks the server lists and the tag chips stale, and leaves the device’s copies alone', () => {
    // Every invalidation of an offline key re-reads the whole store from
    // IndexedDB; `remember` tells those readers once the refetched list has
    // been written there, so a mutation has no reason to wake them first.
    const queryClient = testQueryClient();
    queryClient.setQueryData(queryKeys.notes(), { pages: [], pageParams: [] });
    queryClient.setQueryData(queryKeys.notes({ state: 'archived' }), { pages: [], pageParams: [] });
    queryClient.setQueryData(queryKeys.tags(), []);
    queryClient.setQueryData(queryKeys.offlineNotes('active'), []);
    queryClient.setQueryData(queryKeys.offlineNote('note-1'), null);

    invalidateNoteLists(queryClient);

    expect(queryClient.getQueryState(queryKeys.notes())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(queryKeys.notes({ state: 'archived' }))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(queryKeys.tags())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(queryKeys.offlineNotes('active'))?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(queryKeys.offlineNote('note-1'))?.isInvalidated).toBe(false);
  });
});

describe('remember', () => {
  it('tells the device’s readers once per tick, however many writes landed', async () => {
    const queryClient = testQueryClient();
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries');

    // The tick is a timer the writes' continuations set: run every one they leave.
    vi.useFakeTimers();
    try {
      remember(() => Promise.resolve(), queryClient);
      remember(() => Promise.resolve(), queryClient);
      remember(() => Promise.resolve(), queryClient);
      await vi.runAllTimersAsync();
    } finally {
      vi.useRealTimers();
    }

    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: OFFLINE_NOTES_KEY });
  });
});
