import { onlineManager } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';
import { LONG_PRESS_MS } from '@/hooks/useLongPress.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';
import { setCanHover } from '@/test/setup.ts';

import { NotesScreen } from './NotesScreen.tsx';

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
});

/**
 * The desktop way into selection: a pointer that can hover gets a checkbox at
 * the row's left edge, and clicking it starts the mode with that row selected.
 */
async function startSelecting(
  user: ReturnType<typeof userEvent.setup>,
  title: string,
): Promise<void> {
  const row = await screen.findByRole('button', { name: new RegExp(title, 'i') });
  await user.hover(row);
  await user.click(screen.getByRole('checkbox', { name: new RegExp(`^Select ${title}`, 'i') }));
  await screen.findByRole('toolbar', { name: 'Bulk actions' });
}

describe('the heading is Notes, with the count beside it and the day above', () => {
  it('is one h1 whose text starts with Notes, then the count, then today in words', async () => {
    mount(library());
    await screen.findByRole('button', { name: /roof repair/i });

    const heading = screen.getByRole('heading', { level: 1 });
    const weekday = new Intl.DateTimeFormat(undefined, { weekday: 'long' }).format(new Date());
    expect(heading).toHaveAccessibleName(/^Notes/);
    expect(heading).toHaveTextContent(weekday);
    expect(within(heading).getByText(String(TEST_NOTES.length))).toHaveClass('numeric');
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
    expect(screen.queryByText(/tap record/i)).toBeNull();
    expect(fetchImpl, 'a paused query must not reach the network').not.toHaveBeenCalled();
  });

  it('invites the first recording for a genuinely empty library', async () => {
    mount(library({ active: [], archived: [], tags: [] }));
    expect(await screen.findByText(/tap record to make your first note/i)).toBeInTheDocument();
    expect(screen.queryByText(/offline/i)).toBeNull();
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

    await user.type(screen.getByRole('searchbox', { name: /search notes/i }), 'roof');

    // The client retries a network failure with jittered backoff, so the notice
    // is a few seconds away. That delay is the app's, not the test's.
    expect(
      await screen.findByText(/server search did not respond/i, undefined, { timeout: 8_000 }),
    ).toBeInTheDocument();
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
    mount(library());
    expect(await screen.findByRole('button', { name: 'house' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'books' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true');
    expect(
      await screen.findByRole('button', { name: `Archived · ${String(ARCHIVED_NOTES.length)}` }),
    ).toBeInTheDocument();
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

  it('never divides by a missing purge date', async () => {
    mount(
      library({ archived: [{ ...TEST_NOTES[0]!, archived: true, purge_after: null }] }),
      '/?view=archived',
    );
    expect(await screen.findByText(/no deletion date/i)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain('NaN');
  });
});

describe('doing something to several notes at once', () => {
  it('has no Select button in the header; a row is where selection starts', async () => {
    mount(library());
    await screen.findByRole('button', { name: /roof repair/i });
    expect(screen.queryByRole('button', { name: 'Select' })).toBeNull();
    expect(screen.queryByRole('toolbar')).toBeNull();
  });

  it('archives every selected note and leaves selection mode', async () => {
    const user = userEvent.setup();
    setCanHover(true);
    const archived: string[] = [];
    const base = library();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      if (method === 'DELETE' && url.includes('/v1/notes/')) {
        archived.push(decodeURIComponent(url.split('/v1/notes/')[1] ?? ''));
        return json({});
      }
      return base(input, init);
    });
    mount(fetchImpl);

    await startSelecting(user, 'Roof repair');
    // Every row is a checkbox now, and the one that started it is checked.
    const checkboxes = screen.getAllByRole('checkbox');
    expect(checkboxes).toHaveLength(TEST_NOTES.length);
    expect(checkboxes[0]).toBeChecked();

    const bar = screen.getByRole('toolbar', { name: 'Bulk actions' });
    expect(
      within(bar).getByText((_content, el) => el?.textContent === '1 selected'),
    ).toBeInTheDocument();
    // Count · Select all · Archive · Delete forever · Cancel, in that order.
    expect(within(bar).getAllByRole('button').map((el) => el.textContent)).toEqual([
      'Select all',
      'Archive',
      'Delete forever',
      'Cancel',
    ]);

    await user.click(within(bar).getByRole('button', { name: 'Archive' }));
    await user.click(await screen.findByRole('button', { name: 'Archive them' }));

    await waitFor(() => {
      expect(archived).toEqual([TEST_NOTES[0]?.id]);
    });
    // Back to the plain list: no bar, no row checkboxes.
    await waitFor(() => {
      expect(screen.queryByRole('toolbar')).toBeNull();
    });
    expect(screen.queryAllByRole('checkbox', { checked: true })).toEqual([]);
  });

  it('starts selecting on a long press, with a haptic tick', async () => {
    const vibrate = vi.fn();
    Object.defineProperty(navigator, 'vibrate', { value: vibrate, configurable: true });
    mount(library());
    const row = await screen.findByRole('button', { name: /reading list/i });

    fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 12, clientY: 12 });
    await act(() => new Promise((resolve) => setTimeout(resolve, LONG_PRESS_MS + 60)));
    fireEvent.pointerUp(row, { pointerType: 'touch' });
    fireEvent.click(row);

    const bar = await screen.findByRole('toolbar', { name: 'Bulk actions' });
    expect(
      within(bar).getByText((_content, el) => el?.textContent === '1 selected'),
    ).toBeInTheDocument();
    expect(vibrate).toHaveBeenCalledWith(10);
    // Without hover there is no checkbox before the press, and the press did
    // not also open the note.
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument();
  });

  it('Shift-click on a checkbox selects the range since the last one', async () => {
    const user = userEvent.setup();
    setCanHover(true);
    const now = Date.now();
    const notes: NoteWire[] = ['One', 'Two', 'Three', 'Four'].map((title, index) => ({
      ...TEST_NOTES[0]!,
      id: title.toLowerCase(),
      title,
      updated_at: new Date(now - index * 60_000).toISOString(),
    }));
    mount(library({ active: notes, tags: [] }));

    await startSelecting(user, 'One');
    const boxes = screen.getAllByRole('checkbox');
    await user.keyboard('{Shift>}');
    await user.click(boxes[2]!);
    await user.keyboard('{/Shift}');

    expect(boxes.slice(0, 3).map((box) => (box as HTMLInputElement).checked)).toEqual([
      true,
      true,
      true,
    ]);
    expect(boxes[3]).not.toBeChecked();
    expect(
      screen.getByText((_content, el) => el?.textContent === '3 selected'),
    ).toBeInTheDocument();
  });

  it('Escape leaves selection mode', async () => {
    const user = userEvent.setup();
    setCanHover(true);
    mount(library());
    await startSelecting(user, 'Roof repair');

    await user.keyboard('{Escape}');

    await waitFor(() => {
      expect(screen.queryByRole('toolbar')).toBeNull();
    });
  });

  it('deletes every selected note forever: archives, then purges, behind a typed confirmation', async () => {
    const user = userEvent.setup();
    setCanHover(true);
    const archived: string[] = [];
    let purged: string[] = [];
    const base = library();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      if (method === 'DELETE' && url.includes('/v1/notes/')) {
        archived.push(decodeURIComponent(url.split('/v1/notes/')[1] ?? ''));
        return json({});
      }
      if (method === 'POST' && url.endsWith('/v1/notes/purge')) {
        const body = JSON.parse(String(init?.body)) as { note_ids: string[] };
        purged = body.note_ids;
        return json({ results: purged.map((id) => ({ note_id: id, status: 'purged' })) });
      }
      return base(input, init);
    });
    mount(fetchImpl);

    await startSelecting(user, 'Roof repair');
    await user.click(screen.getByRole('button', { name: 'Select all' }));
    await user.click(screen.getByRole('button', { name: 'Delete forever' }));

    // Gated: the confirm stays disabled until the word is typed.
    const confirm = await screen.findByRole('button', { name: 'Delete them forever' });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText('Type "delete" to confirm'), 'delete');
    await user.click(confirm);

    await waitFor(() => {
      expect(purged).toEqual(TEST_NOTES.map((note) => note.id));
    });
    // Every note was archived first, because the server only purges archived notes.
    expect([...archived].sort()).toEqual(TEST_NOTES.map((note) => note.id).sort());
    await waitFor(() => {
      expect(screen.queryByRole('toolbar')).toBeNull();
    });
  });

  it('drops the chip of a tag whose last note was deleted forever', async () => {
    /*
     * QA D16: two notes tagged `bulkmobile`, select all, delete forever. The
     * notes went, the chip stayed, and pressing it said "No notes are tagged
     * bulkmobile" until a reload — `['tags']` was never invalidated.
     */
    const user = userEvent.setup();
    setCanHover(true);
    let active: NoteWire[] = TEST_NOTES.map((note) => ({ ...note, tags: ['bulkmobile'] }));
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(String(input));
      const method = init?.method ?? 'GET';
      if (method === 'DELETE' && url.pathname.includes('/v1/notes/')) return json({});
      if (method === 'POST' && url.pathname.endsWith('/v1/notes/purge')) {
        const body = JSON.parse(String(init?.body)) as { note_ids: string[] };
        active = active.filter((note) => !body.note_ids.includes(note.id));
        return json({ results: body.note_ids.map((id) => ({ note_id: id, status: 'purged' })) });
      }
      // Tags are derived from the active notes, as the real endpoint derives them.
      if (url.pathname.endsWith('/v1/tags')) {
        const names = new Set(active.flatMap((note) => note.tags ?? []));
        return json({ items: [...names].map((name) => ({ name, count: 1 })) });
      }
      if (url.pathname.endsWith('/v1/notes')) {
        return json({ items: url.searchParams.get('state') === 'archived' ? [] : active });
      }
      return json({ items: [] });
    });
    mount(fetchImpl);

    expect(await screen.findByRole('button', { name: 'bulkmobile' })).toBeInTheDocument();
    await startSelecting(user, 'Roof repair');
    await user.click(screen.getByRole('button', { name: 'Select all' }));
    await user.click(screen.getByRole('button', { name: 'Delete forever' }));
    await user.type(screen.getByLabelText('Type "delete" to confirm'), 'delete');
    await user.click(screen.getByRole('button', { name: 'Delete them forever' }));

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /roof repair/i })).toBeNull();
    });
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: 'bulkmobile' })).toBeNull();
    });
  });

  it('selects and deselects everything with one control', async () => {
    const user = userEvent.setup();
    setCanHover(true);
    mount(library());

    await startSelecting(user, 'Roof repair');
    await user.click(screen.getByRole('button', { name: 'Select all' }));
    expect(
      await screen.findByText(
        (_content, el) => el?.textContent === `${String(TEST_NOTES.length)} selected`,
      ),
    ).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Deselect all' }));
    expect(
      await screen.findByText((_content, el) => el?.textContent === '0 selected'),
    ).toBeInTheDocument();
  });

  it('restores every selected archived note', async () => {
    const user = userEvent.setup();
    setCanHover(true);
    const restored: string[] = [];
    const base = library();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if ((init?.method ?? 'GET') === 'POST' && url.endsWith('/restore')) {
        const id = url.split('/v1/notes/')[1]?.split('/restore')[0] ?? '';
        restored.push(decodeURIComponent(id));
        return json({});
      }
      return base(input, init);
    });
    mount(fetchImpl, '/?view=archived');

    await startSelecting(user, 'Roof repair');
    for (const checkbox of screen.getAllByRole('checkbox')) {
      if (!(checkbox as HTMLInputElement).checked) await user.click(checkbox);
    }
    // The active list's action is not offered here.
    expect(screen.queryByRole('button', { name: 'Archive' })).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Restore' }));
    await user.click(await screen.findByRole('button', { name: 'Restore them' }));

    await waitFor(() => {
      expect(restored.sort()).toEqual(ARCHIVED_NOTES.map((n) => n.id).sort());
    });
  });

  it('empties the archive: select all, then delete forever, in one batch call', async () => {
    // The feature request this closes: no way to clear the whole archive at
    // once. "Select all" plus this is that, using the real batch endpoint —
    // one POST /v1/notes/purge naming every id, not N individual calls.
    const user = userEvent.setup();
    setCanHover(true);
    let purgeBody: unknown = null;
    const base = library();
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      if ((init?.method ?? 'GET') === 'POST' && url.endsWith('/v1/notes/purge')) {
        purgeBody = JSON.parse(String(init?.body));
        return json({
          results: ARCHIVED_NOTES.map((n) => ({ note_id: n.id, status: 'purged' })),
        });
      }
      return base(input, init);
    });
    mount(fetchImpl, '/?view=archived');

    await startSelecting(user, 'Roof repair');
    await user.click(await screen.findByRole('button', { name: 'Select all' }));
    await user.click(screen.getByRole('button', { name: 'Delete forever' }));

    const dialog = await screen.findByRole('dialog');
    expect(await screen.findByRole('button', { name: 'Delete them forever' })).toBeDisabled();
    await user.type(await screen.findByLabelText(/type "delete" to confirm/i), 'delete');
    await user.click(await screen.findByRole('button', { name: 'Delete them forever' }));

    await waitFor(() => {
      expect(purgeBody).toEqual({ note_ids: ARCHIVED_NOTES.map((n) => n.id) });
    });
    expect(dialog).not.toBeInTheDocument();
  });

  it('leaves the plain list untouched when not selecting', async () => {
    // The default path — a real <button> that navigates — must still be what
    // renders until a row is selected; and with no hover there is no checkbox
    // at all.
    mount(library());
    await screen.findByText(TEST_NOTES[0]?.title ?? '');
    expect(screen.queryByRole('checkbox')).toBeNull();
    expect(screen.getAllByRole('button').some((el) => el.className.includes('note-row'))).toBe(
      true,
    );
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
      if (url.pathname.endsWith('/v1/notes') && (url.searchParams.get('state') ?? 'active') === 'active' && !url.searchParams.has('include')) {
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

  it('asks for the notes, tags and captures again when pulled down at the top', async () => {
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
      expect(urls.some((url) => url.endsWith('/v1/tags'))).toBe(true);
    });
    await waitFor(() => {
      expect(screen.queryByText('Refreshing…')).toBeNull();
    });
  });
});
