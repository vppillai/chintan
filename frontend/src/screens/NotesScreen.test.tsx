import { onlineManager } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { SERVER_SEARCH_DEBOUNCE_MS } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { onAFakeClock } from '@/test/clock.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';
import { setCanHover } from '@/test/setup.ts';

import { Toast, dismissToast } from '@/components/Toast.tsx';

import { NotesScreen, chipScrollBy } from './NotesScreen.tsx';

const ARCHIVED_NOTES = TEST_NOTES.map((note) => ({
  ...note,
  id: `${note.id}-archived`,
  archived: true,
  purge_after: new Date(Date.now() + 12 * 86_400_000).toISOString(),
}));

function mount(fetchImpl: typeof fetch, path = '/') {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter initialEntries={[path]}>
        <NotesScreen />
        {/* The shell's, in the app; here so the Undo the bar offers can be pressed. */}
        <Toast />
      </MemoryRouter>
    </TestProviders>,
  );
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

/**
 * The stub most tests want: active notes, archived notes and tags, each from
 * its own endpoint, exactly as the API serves them.
 */
function library(
  overrides: { active?: NoteWire[]; archived?: NoteWire[]; tags?: string[] } = {},
): typeof fetch {
  const active = overrides.active ?? TEST_NOTES;
  const archived = overrides.archived ?? ARCHIVED_NOTES;
  const tags = overrides.tags ?? ['house', 'books'];
  return vi.fn<typeof fetch>(async (input) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/v1/tags')) {
      return json({ items: tags.map((name) => ({ name, count: 1 })) });
    }
    if (url.pathname.endsWith('/v1/notes')) {
      const state = url.searchParams.get('state') ?? 'active';
      const tag = url.searchParams.get('tag');
      const items = (state === 'archived' ? archived : active).filter(
        (note) => !tag || (note.tags ?? []).includes(tag),
      );
      return json({ items });
    }
    if (url.pathname.endsWith('/v1/search')) return json({ items: [] });
    return json({ items: [] });
  });
}

function goOffline(): void {
  Object.defineProperty(navigator, 'onLine', { value: false, configurable: true });
  onlineManager.setOnline(false);
  window.dispatchEvent(new Event('offline'));
}

afterEach(() => {
  Object.defineProperty(navigator, 'onLine', { value: true, configurable: true });
  onlineManager.setOnline(true);
  dismissToast();
  vi.useRealTimers();
});

/**
 * Delete through the row's ⋮, which a mouse reveals by resting on the row,
 * and answer the confirm that names the note (owner, 2026-09-27).
 */
async function deleteFromRow(
  user: ReturnType<typeof userEvent.setup>,
  title: string,
): Promise<void> {
  await screen.findByRole('button', { name: new RegExp(title, 'i') });
  await user.click(screen.getByRole('button', { name: 'More', description: new RegExp(`^${title}`) }));
  await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
  const dialog = await screen.findByRole('dialog', { name: `Delete “${title}”?` });
  expect(dialog).toHaveTextContent('It is kept in the Archive for 30 days, then gone for good.');
  await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
  await waitFor(() => {
    expect(screen.queryByRole('dialog')).toBeNull();
  });
}

describe('the heading is Notes with the count beside it; the brand leads the row', () => {
  it('is one h1 whose text starts with Notes, then the count; the wordmark is beside it, with no date', async () => {
    mount(library());
    await screen.findByRole('button', { name: /roof repair/i });

    const heading = screen.getByRole('heading', { level: 1 });
    const weekday = new Intl.DateTimeFormat(undefined, { weekday: 'long' }).format(new Date());
    expect(heading).toHaveAccessibleName(/^Notes/);
    expect(within(heading).getByText(String(TEST_NOTES.length))).toHaveClass('numeric');
    // The brand shares the heading row (round-3 T17), out of the h1, so a
    // screen reader hears "Notes, 2" and no more; it is the shell's lockup,
    // and the date line is gone (owner feedback 2026-09-26) — the group
    // label under the row says Today.
    const row = heading.parentElement!;
    const brand = row.querySelector('.library-brand');
    expect(brand?.querySelector('.wordmark')).toHaveTextContent(/^Chintan$/);
    expect(row).not.toHaveTextContent(weekday);
    // And only the one h1 on the screen.
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1);
  });

  it('holds the count back until something has answered', () => {
    mount(library());
    const heading = screen.getByRole('heading', { level: 1 });
    expect(within(heading).queryByText(/^\d+\+?$/)).toBeNull();
  });
});

describe('the library never claims an empty library it cannot see', () => {
  it('says it is offline rather than inviting a first note', async () => {
    /*
     * TanStack *pauses* an offline query rather than failing it, so neither
     * `isLoading` nor `isError` was ever true and the brand-new-user empty
     * state rendered — directly under a banner reading "Offline — showing saved
     * notes.". To a user with a full library walking into a tunnel, their
     * entire library appeared to have been deleted.
     */
    goOffline();
    const fetchImpl = library();
    mount(fetchImpl);

    expect(
      await screen.findByText(/offline and no notes are cached/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/tap Record/i)).toBeNull();
    expect(fetchImpl, 'a paused query must not reach the network').not.toHaveBeenCalled();
  });

  it('invites the first recording for a genuinely empty library', async () => {
    mount(library({ active: [], archived: [], tags: [] }));
    expect(await screen.findByText(/tap Record to start your first note/i)).toBeInTheDocument();
    expect(screen.queryByText(/offline/i)).toBeNull();
    // And offers no way into nothing: no "Archive · 0" row, no "Checklists · 0" chip.
    expect(screen.queryByRole('link', { name: /^Archive/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /^Checklists/ })).toBeNull();
  });
});

describe('a failed load offers a control, not a gesture the app lacks', () => {
  it('renders a Try again button that refetches', async () => {
    const user = userEvent.setup();
    let calls = 0;
    // 403 rather than 500 on purpose: the client retries 5xx with backoff, and
    // this test is about the button, not the client's retry policy.
    const fetchImpl = vi.fn<typeof fetch>(async () => {
      calls += 1;
      return json({ type: 'about:blank', title: 'Not permitted', status: 403 }, 403);
    });

    mount(fetchImpl);

    const retry = await screen.findByRole('button', { name: /try again/i });
    const before = calls;
    await user.click(retry);

    await waitFor(() => {
      expect(calls).toBeGreaterThan(before);
    });
  });

  it('surfaces the problem detail so a 401 reads as "sign in again"', async () => {
    mount(
      vi.fn<typeof fetch>(async () =>
        new Response(
          JSON.stringify({
            type: 'about:blank',
            title: 'Your session has expired',
            status: 401,
            detail: 'Sign in again to see your notes.',
          }),
          { status: 401, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    );

    expect(await screen.findByText('Sign in again to see your notes.')).toBeInTheDocument();
  });
});

describe('rows are grouped by the day they were touched', () => {
  it('files today and yesterday under their own headings, older ones under the month', async () => {
    const now = Date.now();
    const notes: NoteWire[] = [
      { ...TEST_NOTES[0]!, id: 'today', title: 'Today note', updated_at: new Date(now).toISOString() },
      {
        ...TEST_NOTES[0]!,
        id: 'yesterday',
        title: 'Yesterday note',
        updated_at: new Date(now - 86_400_000).toISOString(),
      },
      {
        ...TEST_NOTES[0]!,
        id: 'older',
        title: 'Older note',
        updated_at: new Date(now - 40 * 86_400_000).toISOString(),
      },
    ];
    mount(library({ active: notes }));

    const today = await screen.findByRole('region', { name: 'Today' });
    expect(within(today).getByRole('button', { name: /today note/i })).toBeInTheDocument();
    expect(
      within(screen.getByRole('region', { name: 'Yesterday' })).getByRole('button', {
        name: /yesterday note/i,
      }),
    ).toBeInTheDocument();
    // Older than a week: a month heading, never a weekday.
    const headings = screen.getAllByRole('heading', { level: 2 }).map((el) => el.textContent);
    expect(headings[0]).toBe('Today');
    expect(headings[1]).toBe('Yesterday');
    expect(headings[2]).not.toMatch(/day$/);
  });

  it('shows a clock time on a row from today and a date otherwise', async () => {
    const notes: NoteWire[] = [
      { ...TEST_NOTES[0]!, id: 'today', title: 'Today note', updated_at: new Date().toISOString() },
      TEST_NOTES[1]!,
    ];
    mount(library({ active: notes }));

    const today = await screen.findByRole('button', { name: /today note/i });
    expect(within(today).getByText(/^\d{2}:\d{2}/)).toBeInTheDocument();
    const older = screen.getByRole('button', { name: /reading list/i });
    expect(within(older).getByText(/Aug/)).toBeInTheDocument();
  });

  it('shows the tags on the meta line', async () => {
    mount(library());
    const row = await screen.findByRole('button', { name: /roof repair/i });
    expect(within(row).getByText('house')).toBeInTheDocument();
  });
});

describe('search narrows the list as you type, from what is already on the device', () => {
  it('filters to matching notes and marks the hit', async () => {
    const user = userEvent.setup();
    mount(library());
    await screen.findByRole('button', { name: /roof repair/i });

    await user.type(screen.getByRole('searchbox', { name: /search notes/i }), 'ridge');

    expect(await screen.findByRole('button', { name: /roof repair/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /reading list/i })).toBeNull();
    expect(screen.getByText('Ridge', { selector: 'mark' })).toBeInTheDocument();
    // Ranked, not grouped: the day headings step aside while searching.
    expect(screen.queryByRole('heading', { level: 2 })).toBeNull();
  });

  it('asks the server for the word once typing pauses, while the device answers every keystroke', async () => {
    /*
     * QA D8: "flashing" at 60 ms a key sent eight `GET /v1/search` requests —
     * one per letter — whose answers landed out of order. The corpus on the
     * device narrows the list at once; the server is worth one request, for
     * the word.
     */
    const user = userEvent.setup({ delay: null });
    const asked: string[] = [];
    const base = library();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(String(input));
      if (url.pathname.endsWith('/v1/search')) {
        asked.push(url.searchParams.get('q') ?? '');
        return json({ items: [] });
      }
      return base(input, init);
    });
    mount(fetchImpl);
    await screen.findByRole('button', { name: /roof repair/i });

    await user.type(screen.getByRole('searchbox', { name: /search notes/i }), 'ridge');

    // The device has already answered: only the matching note is on screen,
    // and the count says the server is still to come.
    expect(screen.getByRole('button', { name: /roof repair/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /reading list/i })).toBeNull();
    expect(screen.getByText(/1 result so far/)).toBeInTheDocument();
    expect(asked).toEqual([]);

    await waitFor(() => {
      expect(asked).toEqual(['ridge']);
    });
    await waitFor(() => {
      expect(screen.getByText(/^1 result$/)).toBeInTheDocument();
    });
  });

  it('says nothing matches, naming the term', async () => {
    const user = userEvent.setup();
    mount(library());
    await screen.findByRole('button', { name: /roof repair/i });

    await user.type(screen.getByRole('searchbox', { name: /search notes/i }), 'chimney');

    expect(await screen.findByText(/nothing matches “chimney”/i)).toBeInTheDocument();
  });

  it('adds what only the server found, after the local hits', async () => {
    const user = userEvent.setup();
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = new URL(String(input));
      if (url.pathname.endsWith('/v1/search')) {
        return json({
          items: [
            {
              note_id: 'page-two',
              title: 'Chimney flashing',
              excerpt: '…the chimney flashing was resealed…',
              matched_in: ['transcript'],
            },
          ],
        });
      }
      if (url.pathname.endsWith('/v1/notes')) {
        return json({ items: url.searchParams.get('state') === 'archived' ? [] : TEST_NOTES });
      }
      return json({ items: [] });
    });
    mount(fetchImpl);
    await screen.findByRole('button', { name: /roof repair/i });

    await user.type(screen.getByRole('searchbox', { name: /search notes/i }), 'chimney');

    expect(await screen.findByRole('button', { name: /chimney flashing/i })).toBeInTheDocument();
    expect(screen.getByText(/1 result/)).toBeInTheDocument();
  });

  it('says the server search failed rather than that the note does not exist', async () => {
    /*
     * Online, but the API is unreachable — a captive portal, or a dead gateway.
     * The one case where the user most needs to know a note they own was not
     * actually looked for.
     */
    const user = userEvent.setup();
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = new URL(String(input));
      if (url.pathname.endsWith('/v1/search')) throw new TypeError('Failed to fetch');
      if (url.pathname.endsWith('/v1/notes')) {
        return json({ items: url.searchParams.get('state') === 'archived' ? [] : TEST_NOTES });
      }
      return json({ items: [] });
    });
    mount(fetchImpl);
    await screen.findByRole('button', { name: /roof repair/i });

    // The client retries a network failure with jittered backoff — up to
    // 0.4 + 0.8 + 1.6 s of it — after the field's own debounce, so the notice
    // is a few seconds away. That delay is the app's; the test moves a fake
    // clock past the whole of it rather than sleeping it.
    await onAFakeClock(async () => {
      await user.type(screen.getByRole('searchbox', { name: /search notes/i }), 'roof');
      await act(() => vi.advanceTimersByTimeAsync(SERVER_SEARCH_DEBOUNCE_MS));
      // The backoff is jittered, so the clock is stepped until the notice lands.
      for (let step = 0; step < 20 && !screen.queryByText(/server search did not respond/i); step += 1) {
        await act(() => vi.advanceTimersByTimeAsync(500));
      }
      expect(screen.getByText(/server search did not respond/i)).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /roof repair/i })).toBeInTheDocument();
  });

  it('keeps the query in the URL', async () => {
    mount(library(), '/?q=roof');
    expect(await screen.findByRole('searchbox', { name: /search notes/i })).toHaveValue('roof');
    expect(await screen.findByRole('button', { name: /roof repair/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /reading list/i })).toBeNull();
  });
});

describe('the chips filter the list', () => {
  it('offers All, one chip per tag, and Archived with its count', async () => {
    const fetchImpl = library();
    mount(fetchImpl);
    expect(await screen.findByRole('button', { name: 'house' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'books' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true');
    expect(
      await screen.findByRole('button', { name: `Archived · ${String(ARCHIVED_NOTES.length)}` }),
    ).toBeInTheDocument();
    // The chip names come from the notes on the device, not a fifth GET (round-3 T46).
    const urls = vi.mocked(fetchImpl).mock.calls.map((call) => new URL(String(call[0])).pathname);
    expect(urls.some((url) => url.endsWith('/v1/tags'))).toBe(false);
  });

  it('hides the Archived chip while nothing is archived, and offers the archive as a row at the end', async () => {
    // Round-3 T17: "Archived · 0" was a filter with nothing behind it.
    const user = userEvent.setup();
    mount(library({ archived: [] }));
    await screen.findByRole('button', { name: /roof repair/i });
    expect(screen.queryByRole('button', { name: /^Archived/ })).toBeNull();

    const row = screen.getByRole('link', { name: /^Archive/ });
    expect(row).toHaveAttribute('href', '/?view=archived');
    // And the row hides the zero the chip hides (QA 2026-09-21, finding 13).
    expect(row).toHaveTextContent(/^Archive$/);
    await user.click(row);
    expect(await screen.findByText(/nothing is archived/i)).toBeInTheDocument();
    // In the archive itself the chip is there, pressed, so All is one tap away.
    expect(screen.getByRole('button', { name: /^Archived/ })).toHaveAttribute('aria-pressed', 'true');
  });

  it('narrows to a tag, and All clears it', async () => {
    const user = userEvent.setup();
    mount(library());
    await screen.findByRole('button', { name: /reading list/i });

    await user.click(screen.getByRole('button', { name: 'house' }));

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /reading list/i })).toBeNull();
    });
    expect(screen.getByRole('button', { name: /roof repair/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'house' })).toHaveAttribute('aria-pressed', 'true');

    await user.click(screen.getByRole('button', { name: 'All' }));
    expect(await screen.findByRole('button', { name: /reading list/i })).toBeInTheDocument();
  });

  it('shows the archive, with each row saying when it is purged', async () => {
    const user = userEvent.setup();
    mount(library());
    await screen.findByRole('button', { name: /roof repair/i });

    await user.click(await screen.findByRole('button', { name: /^Archived/ }));

    const rows = await screen.findAllByText(/deletes in 12 days/i);
    expect(rows).toHaveLength(ARCHIVED_NOTES.length);
    expect(screen.getByRole('button', { name: /^Archived/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  });

  it('says so when the archive is empty', async () => {
    mount(library({ archived: [] }), '/?view=archived');
    expect(await screen.findByText(/nothing is archived/i)).toBeInTheDocument();
  });

  it('is headed "Archived · N" (QA 2026-09-21, finding 6)', async () => {
    // Headed "Notes · 0" with the pressed chip off a phone's screen, the
    // archive read as Home.
    mount(library(), '/?view=archived');
    await screen.findByRole('button', { name: /roof repair/i });

    const heading = screen.getByRole('heading', { level: 1 });
    expect(heading).toHaveAccessibleName(/^Archived/);
    expect(within(heading).getByText(String(ARCHIVED_NOTES.length))).toHaveClass('numeric');
    expect(screen.getByRole('button', { name: /^Archived ·/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  });

  it('scrolls the chip row so the pressed chip sits a gutter in from the edge, and no further', () => {
    // The pressed "Archived · 0" chip sat at x 629–741 in a 412 px row
    // scrolled to its start (finding 6). jsdom lays nothing out, so the
    // arithmetic is what is tested; the row, not scrollIntoView, is what
    // moves, because Chromium would otherwise start Tab from the chip.
    const row = { left: 0, right: 412 };
    expect(chipScrollBy(row, { left: 629, right: 741 }, 16)).toBe(741 - (412 - 16));
    expect(chipScrollBy(row, { left: 100, right: 180 }, 16)).toBe(0);
    expect(chipScrollBy(row, { left: -60, right: 20 }, 16)).toBe(-60 - 16);
  });

  it('never divides by a missing purge date', async () => {
    mount(
      library({ archived: [{ ...TEST_NOTES[0]!, archived: true, purge_after: null }] }),
      '/?view=archived',
    );
    expect(await screen.findByText(/no deletion date/i)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain('NaN');
  });
});

describe('deleting from the library', () => {
  it('drops the chip of a tag whose last note was deleted', async () => {
    /*
     * QA D16: two notes tagged `bulkmobile`, both deleted. The notes went,
     * the chip stayed, and pressing it said "No notes are tagged bulkmobile"
     * until a reload — `['tags']` was never invalidated. One row at a time
     * now (there is no multi-select; owner, 2026-09-29), and the
     * invalidation is the same.
     */
    const user = userEvent.setup();
    setCanHover(true);
    let active: NoteWire[] = TEST_NOTES.map((note) => ({ ...note, tags: ['bulkmobile'] }));
    // The archived page lists what went, so the device's copy stops calling it active.
    const archivedNow: NoteWire[] = [];
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(String(input));
      const method = init?.method ?? 'GET';
      if (method === 'DELETE' && url.pathname.includes('/v1/notes/')) {
        const id = decodeURIComponent(url.pathname.split('/v1/notes/')[1] ?? '');
        active = active.filter((note) => note.id !== id);
        archivedNow.push({ ...TEST_NOTES.find((note) => note.id === id)!, archived: true });
        return json({});
      }
      // Tags are derived from the active notes, as the real endpoint derives them.
      if (url.pathname.endsWith('/v1/tags')) {
        const names = new Set(active.flatMap((note) => note.tags ?? []));
        return json({ items: [...names].map((name) => ({ name, count: 1 })) });
      }
      if (url.pathname.endsWith('/v1/notes')) {
        return json({ items: url.searchParams.get('state') === 'archived' ? archivedNow : active });
      }
      return json({ items: [] });
    });
    mount(fetchImpl);

    expect(await screen.findByRole('button', { name: 'bulkmobile' })).toBeInTheDocument();
    for (const note of TEST_NOTES) {
      await deleteFromRow(user, note.title);
    }

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /roof repair/i })).toBeNull();
    });
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'bulkmobile' })).toBeNull();
    });
  });
});

/**
 * Infinite scroll (backlog U3): the next page is asked for as the reader
 * nears the end of this one, and the day groups run on across pages. The
 * button survives for the keyboard and the screen reader, hidden until it is
 * focused; where there is no observer at all it is simply shown.
 */
describe('the next page arrives as the list is scrolled', () => {
  const PAGE_ONE = TEST_NOTES.map((note) => ({ ...note, updated_at: new Date().toISOString() }));
  const PAGE_TWO: NoteWire[] = [
    {
      ...TEST_NOTES[0]!,
      id: 'older-page',
      title: 'Older page note',
      updated_at: new Date(Date.now() - 40 * 86_400_000).toISOString(),
    },
  ];

  function pagedLibrary() {
    const cursors: (string | null)[] = [];
    const base = library({ active: PAGE_ONE });
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(String(input));
      // The unfiltered active list only: the corpus and the Checklists chip's
      // count are lists of their own and page on their own.
      if (
        url.pathname.endsWith('/v1/notes') &&
        (url.searchParams.get('state') ?? 'active') === 'active' &&
        !url.searchParams.has('include') &&
        !url.searchParams.has('kind')
      ) {
        const cursor = url.searchParams.get('cursor');
        cursors.push(cursor);
        return cursor === 'page-2' ? json({ items: PAGE_TWO }) : json({ items: PAGE_ONE, cursor: 'page-2' });
      }
      return base(input, init);
    });
    return { fetchImpl, cursors };
  }

  /** A controllable IntersectionObserver: the test decides when the sentinel is near. */
  function stubObserver() {
    const instances: {
      callback: IntersectionObserverCallback;
      options?: IntersectionObserverInit | undefined;
      observed: Element[];
    }[] = [];
    class FakeObserver {
      observed: Element[] = [];
      constructor(
        public callback: IntersectionObserverCallback,
        public options?: IntersectionObserverInit,
      ) {
        instances.push(this);
      }
      observe(element: Element) {
        this.observed.push(element);
      }
      disconnect() {}
      unobserve() {}
      takeRecords() {
        return [];
      }
    }
    vi.stubGlobal('IntersectionObserver', FakeObserver);
    return {
      instances,
      near: () => {
        const live = instances.at(-1);
        if (!live) throw new Error('nothing is observing');
        act(() => {
          live.callback(
            [{ isIntersecting: true } as IntersectionObserverEntry],
            live as unknown as IntersectionObserver,
          );
        });
      },
    };
  }

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('fetches the next page when the sentinel comes within a viewport, and the groups continue', async () => {
    const observer = stubObserver();
    const api = pagedLibrary();
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /roof repair/i });

    // Nothing asked for yet: the sentinel is observed, not yet near.
    await waitFor(() => {
      expect(observer.instances.at(-1)?.observed.length).toBe(1);
    });
    expect(api.cursors).toEqual([null]);
    // One viewport of margin below the scroll container.
    expect(observer.instances.at(-1)?.options?.rootMargin).toBe('0px 0px 100% 0px');
    // The button is still there for the keyboard, out of sight.
    const button = screen.getByRole('button', { name: 'Load more' });
    expect(button).toHaveClass('visually-hidden');

    observer.near();

    expect(await screen.findByRole('button', { name: /older page note/i })).toBeInTheDocument();
    expect(api.cursors).toEqual([null, 'page-2']);
    // The new rows file under their own day group beneath today's.
    const headings = screen.getAllByRole('heading', { level: 2 }).map((el) => el.textContent);
    expect(headings[0]).toBe('Today');
    expect(headings.length).toBe(2);
    // The last page: nothing left to load, so nothing left to press.
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull();
    });
  });

  it('shows the button outright where there is no observer', async () => {
    vi.stubGlobal('IntersectionObserver', undefined);
    const user = userEvent.setup();
    const api = pagedLibrary();
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /roof repair/i });

    const button = await screen.findByRole('button', { name: 'Load more' });
    expect(button).not.toHaveClass('visually-hidden');
    await user.click(button);

    expect(await screen.findByRole('button', { name: /older page note/i })).toBeInTheDocument();
    expect(api.cursors).toEqual([null, 'page-2']);
  });
});

/**
 * The instant search matches what the server matches (backlog B5): the words
 * of every note's body, fetched once as a corpus and written to the device.
 */
describe('the search corpus', () => {
  it('fetches the searchable bodies once, apart from the list, and searches them', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = new URL(String(input));
      if (url.pathname.endsWith('/v1/tags')) return json({ items: [] });
      if (url.pathname.endsWith('/v1/notes')) {
        if ((url.searchParams.get('state') ?? 'active') === 'archived') return json({ items: [] });
        // Only the corpus request carries the body the server searches.
        const corpus = url.searchParams.get('include') === 'search_text';
        return json({
          items: TEST_NOTES.map((note) =>
            corpus
              ? { ...note, search_text: `${note.snippet ?? ''} the tiler can start on the fourteenth`.toLowerCase() }
              : note,
          ),
        });
      }
      return json({ items: [] });
    });

    // A word that is in no title, tag or snippet — only deep in the body.
    mount(fetchImpl, '/?q=fourteenth');

    expect(await screen.findByRole('button', { name: /roof repair/i })).toBeInTheDocument();
    expect(screen.getByText(/2 results/)).toBeInTheDocument();

    const requests = fetchImpl.mock.calls.map(([input]) => new URL(String(input)));
    const corpus = requests.filter((url) => url.searchParams.get('include') === 'search_text');
    const lists = requests.filter(
      (url) => url.pathname.endsWith('/v1/notes') && !url.searchParams.has('include'),
    );
    // One corpus request, at the contract's largest page; the list itself
    // stays small and never asks for the text.
    expect(corpus).toHaveLength(1);
    expect(corpus[0]?.searchParams.get('limit')).toBe('200');
    expect(lists.length).toBeGreaterThan(0);
  });
});

describe('pull to refresh', () => {
  /** A finger on the shell's scroll container, which the test wraps the screen in. */
  function touch(type: string, clientY: number): Event {
    const event = new Event(type, { bubbles: true, cancelable: true });
    Object.defineProperty(event, 'touches', {
      value: type === 'touchend' ? [] : [{ clientY }],
    });
    Object.defineProperty(event, 'changedTouches', { value: [{ clientY }] });
    return event;
  }

  it('asks for the notes and captures again when pulled down at the top', async () => {
    const fetchImpl = library();
    render(
      <TestProviders api={testApiContext(fetchImpl)}>
        <MemoryRouter initialEntries={['/']}>
          <main className="app__main">
            <NotesScreen />
          </main>
        </MemoryRouter>
      </TestProviders>,
    );
    await screen.findByText('Roof repair');
    const before = vi.mocked(fetchImpl).mock.calls.length;
    const main = document.querySelector('.app__main') as HTMLElement;

    act(() => {
      main.dispatchEvent(touch('touchstart', 0));
      main.dispatchEvent(touch('touchmove', 200));
    });
    expect(screen.getByText('Release to refresh')).toBeInTheDocument();
    act(() => {
      main.dispatchEvent(touch('touchend', 200));
    });
    expect(screen.getByText('Refreshing…')).toBeInTheDocument();

    await waitFor(() => {
      const urls = vi
        .mocked(fetchImpl)
        .mock.calls.slice(before)
        .map((call) => new URL(String(call[0])).pathname);
      expect(urls.some((url) => url.endsWith('/v1/notes'))).toBe(true);
      expect(urls.some((url) => url.endsWith('/v1/captures'))).toBe(true);
      // The chips read the notes, so `/v1/tags` is no longer asked (round-3 T46).
      expect(urls.some((url) => url.endsWith('/v1/tags'))).toBe(false);
    });
    await waitFor(() => {
      expect(screen.queryByText('Refreshing…')).toBeNull();
    });
  });
});
