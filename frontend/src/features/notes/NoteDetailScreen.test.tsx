import { QueryClient } from '@tanstack/react-query';
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { onlineManager } from '@tanstack/react-query';

import { CAPTURE_POLL_FAST_MS, queryKeys } from '@/api/queries.ts';
import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';
import { routes } from '@/app/router.tsx';
import { INITIAL_CAPTURE } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { LOADING_PATIENCE_MS } from './NoteDetailScreen.tsx';
import { noteTabStorageKey } from './NoteTabs.tsx';

/**
 * The note screen against a small stateful server: PATCH checks the version
 * and stores the result, GET answers from what is stored. The QA pass found
 * the screen showing pre-edit text and losing a version check to the user's
 * own edit — see `recordSavedNote` — so these drive the app through the real
 * routes rather than the editor hook alone.
 */

interface StoredNote extends NoteDetailWire {
  body: string;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'content-type': status >= 400 ? 'application/problem+json' : 'application/json',
    },
  });
}

function snippetOf(body: string): string {
  return body.split('\n').find((line) => line.trim().length > 0)?.trim() ?? '';
}

function server(initial: StoredNote[]) {
  const notes = new Map(initial.map((note) => [note.id, note]));
  const patches: { version: number; status: number; body: Record<string, unknown> }[] = [];
  let gets = 0;

  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    const detail = /\/v1\/notes\/([^/]+)$/.exec(url.pathname);

    if (detail && method === 'GET') {
      gets += 1;
      const note = notes.get(detail[1] ?? '');
      return note ? json(note) : json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
    }
    if (detail && method === 'PATCH') {
      const note = notes.get(detail[1] ?? '');
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      if (!note) return json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
      if (body['version'] !== note.version) {
        patches.push({ version: Number(body['version']), status: 409, body });
        return json(
          {
            type: 'about:blank',
            title: 'Someone else changed this first',
            status: 409,
            current_version: note.version,
          },
          409,
        );
      }
      const next: StoredNote = {
        ...note,
        title: typeof body['title'] === 'string' ? body['title'] : note.title,
        body: typeof body['body'] === 'string' ? body['body'] : note.body,
        ...(Array.isArray(body['tags']) ? { tags: body['tags'] as string[] } : {}),
        ...(Array.isArray(body['aliases']) ? { aliases: body['aliases'] as string[] } : {}),
        version: note.version + 1,
        updated_at: new Date().toISOString(),
      };
      next.snippet = snippetOf(next.body);
      notes.set(next.id, next);
      patches.push({ version: Number(body['version']), status: 200, body });
      // What the real endpoint returns: the index row, without the body.
      const { body: _body, captures: _captures, ...row } = next;
      return json(row);
    }
    if (url.pathname.endsWith('/v1/notes')) {
      const state = url.searchParams.get('state') ?? 'active';
      const items = [...notes.values()]
        .filter((note) => note.archived === (state === 'archived'))
        .map(({ body: _body, captures: _captures, ...row }) => row);
      return json({ items });
    }
    return json({ items: [] });
  });

  return {
    fetchImpl,
    patches,
    notes,
    get gets() {
      return gets;
    },
  };
}

const ROOF: StoredNote = {
  id: 'roof-repair',
  title: 'Roof repair',
  body: 'v1 body',
  snippet: 'v1 body',
  aliases: [],
  tags: [],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 1,
  archived: false,
  captures: [],
};

/** A recording that has landed, so the Recordings tab has a row to show. */
const FILED: CaptureWire = {
  id: 'cap-old',
  status: 'appended',
  created_at: '2026-08-06T09:10:00.000Z',
  version: 1,
  note_id: 'roof-repair',
  duration_ms: 12_000,
  has_peaks: false,
  has_segments: false,
};

function mount(fetchImpl: typeof fetch, path: string) {
  // The provider's own `staleTime`, not the test default of zero: the defect
  // lives in the thirty seconds during which a re-open is answered from cache.
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        refetchOnWindowFocus: false,
        refetchOnReconnect: false,
        staleTime: 30_000,
      },
      mutations: { retry: false },
    },
  });
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(
    <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return { router, queryClient };
}

describe('what was just saved is what the app shows next', () => {
  it('re-opens the note with the saved text, and the next save carries the new version', async () => {
    /*
     * QA D2: edit, wait for "Saved", back to the library, tap the note again
     * within thirty seconds, add a tag. The re-opened note showed the body
     * from before the edit; the tag's PATCH carried the old version and was
     * answered 409, and the conflict prompt offered to overwrite the user's
     * own edit as "Keep my edits".
     */
    const user = userEvent.setup();
    const api = server([ROOF]);
    const { router, queryClient } = mount(api.fetchImpl, '/notes/roof-repair');

    const body = await screen.findByRole('textbox', { name: 'Note body' });
    await waitFor(() => {
      expect(body).toHaveValue('v1 body');
    });
    await user.type(body, ' more words here.');
    await user.tab();
    expect(await screen.findByText('Saved')).toBeInTheDocument();
    expect(api.patches).toEqual([
      expect.objectContaining({ version: 1, status: 200 }),
    ]);

    // The cache the next mount reads from already holds the saved note.
    expect(queryClient.getQueryData<NoteDetailWire>(queryKeys.note('roof-repair'))).toEqual(
      expect.objectContaining({ body: 'v1 body more words here.', version: 2 }),
    );

    await user.click(screen.getByRole('button', { name: /back to\s*notes/i }));
    await screen.findByRole('heading', { name: /^Notes/ });
    // QA D3: the library row is stale after an edit.
    const row = await screen.findByRole('button', { name: /roof repair/i });
    expect(within(row).getByText('v1 body more words here.')).toBeInTheDocument();

    await user.click(row);
    expect(router.state.location.pathname).toBe('/notes/roof-repair');
    expect(await screen.findByRole('textbox', { name: 'Note body' })).toHaveValue(
      'v1 body more words here.',
    );

    await user.click(screen.getByRole('button', { name: 'Tags' }));
    await user.type(screen.getByRole('textbox', { name: 'Add a tag' }), 'vtag{Enter}');

    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches[1]).toEqual(expect.objectContaining({ version: 2, status: 200 }));
    expect(api.patches[1]?.body['body']).toBe('v1 body more words here.');
    expect(screen.queryByText(/changed elsewhere/i)).toBeNull();
    expect(api.notes.get('roof-repair')?.tags).toEqual(['vtag']);
  });

  it('shows the new title in the library straight after a rename', async () => {
    const user = userEvent.setup();
    const api = server([ROOF]);
    mount(api.fetchImpl, '/notes/roof-repair');

    const title = await screen.findByRole('textbox', { name: 'Note title' });
    await waitFor(() => {
      expect(title).toHaveValue('Roof repair');
    });
    await user.clear(title);
    await user.type(title, 'Renamed');
    await user.tab();
    expect(await screen.findByText('Saved')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /back to\s*notes/i }));
    expect(await screen.findByRole('button', { name: /^renamed/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /roof repair/i })).toBeNull();
  });
});

describe('a note whose recording is still filing keeps asking', () => {
  // Waits through two real poll ticks (CAPTURE_POLL_FAST_MS each); the default
  // 5 s budget is enough on a laptop and not on the CI runner.
  it('shows the appended text once the pipeline writes it, without leaving the screen', async () => {
    /*
     * QA D7: "Record into this", then open the note while the filing row still
     * reads "Uploaded". The screen made exactly one GET and sat on the old
     * body indefinitely — the poll that notices an append belongs to the
     * library, which is not mounted here.
     */
    const moving: CaptureWire = {
      id: 'cap-new',
      status: 'transcribing',
      created_at: new Date().toISOString(),
      version: 1,
      note_id: 'roof-repair',
      duration_ms: 5_000,
    };
    const api = server([{ ...ROOF, body: 'Only paragraph.', captures: [moving] }]);
    mount(api.fetchImpl, '/notes/roof-repair');

    const body = await screen.findByRole('textbox', { name: 'Note body' });
    await waitFor(() => {
      expect(body).toHaveValue('Only paragraph.');
    });

    // The worker finishes between two polls.
    act(() => {
      api.notes.set('roof-repair', {
        ...(api.notes.get('roof-repair') as StoredNote),
        body: 'Only paragraph.\n\nThe gutter is leaking again.',
        version: 2,
        captures: [{ ...moving, status: 'appended' }],
      });
    });

    await waitFor(
      () => {
        expect(screen.getByRole('textbox', { name: 'Note body' })).toHaveValue(
          'Only paragraph.\n\nThe gutter is leaking again.',
        );
      },
      { timeout: CAPTURE_POLL_FAST_MS * 3 },
    );
    expect(api.gets).toBeGreaterThan(1);
    // Settled: nothing left to ask about, so the polling stops.
    await new Promise((resolve) => setTimeout(resolve, CAPTURE_POLL_FAST_MS + 200));
    const after = api.gets;
    await new Promise((resolve) => setTimeout(resolve, CAPTURE_POLL_FAST_MS + 200));
    expect(api.gets).toBe(after);
  }, 20_000);
});

describe('the note screen is shaped for reading', () => {
  it('has one h1, which names the title field', async () => {
    // axe `page-has-heading-one`: the title is an input, so the screen had no
    // heading at all. The label is the heading; the input keeps its name.
    const api = server([ROOF]);
    mount(api.fetchImpl, '/notes/roof-repair');

    const title = await screen.findByRole('textbox', { name: 'Note title' });
    await waitFor(() => {
      expect(title).toHaveValue('Roof repair');
    });
    const headings = screen.getAllByRole('heading', { level: 1 });
    expect(headings).toHaveLength(1);
    expect(headings[0]).toHaveTextContent('Note title');
  });

  it('grows the body with its text rather than scrolling inside a fixed box', async () => {
    /*
     * QA D18: twelve fixed rows with an inner scrollbar — a long note scrolled
     * inside a box inside the scrolling page, and a short note wasted three
     * hundred pixels. Where `field-sizing: content` is missing (jsdom, older
     * WebKit) the hook measures the scroll height and sets the height to it.
     */
    const lineHeight = 24;
    const scrollHeight = vi
      .spyOn(HTMLTextAreaElement.prototype, 'scrollHeight', 'get')
      .mockImplementation(function (this: HTMLTextAreaElement) {
        return this.value.split('\n').length * lineHeight + 2 * 12;
      });
    try {
      const user = userEvent.setup({ delay: null });
      const api = server([ROOF]);
      mount(api.fetchImpl, '/notes/roof-repair');

      const body = await screen.findByRole('textbox', { name: 'Note body' });
      await waitFor(() => {
        expect(body).toHaveValue('v1 body');
      });
      expect(body).toHaveAttribute('rows', '6');
      expect(body.style.blockSize).toBe(`${String(lineHeight + 24)}px`);

      await user.type(body, '{Enter}two{Enter}three{Enter}four');
      expect(body.style.blockSize).toBe(`${String(4 * lineHeight + 24)}px`);
    } finally {
      scrollHeight.mockRestore();
    }
  });
});

/**
 * A note never opened on this device, with no connection. QA D17 saw
 * "Loading…" for sixteen seconds and counting in two runs out of four —
 * the browser reporting a connection over a dead link, and a request that
 * hung rather than failed.
 */
describe('an uncached note offline is not an endless Loading', () => {
  afterEach(() => {
    Object.defineProperty(navigator, 'onLine', { value: true, configurable: true });
    onlineManager.setOnline(true);
    vi.useRealTimers();
  });

  it('says at once that the note is not on this device when the browser is offline', async () => {
    Object.defineProperty(navigator, 'onLine', { value: false, configurable: true });
    onlineManager.setOnline(false);
    window.dispatchEvent(new Event('offline'));
    const api = server([]);
    mount(api.fetchImpl, '/notes/reading-list');

    expect(
      await screen.findByText(/you’re offline and this note isn’t saved on this device/i),
    ).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Not on this device' })).toBeInTheDocument();
    expect(screen.queryByText('Loading…')).toBeNull();
    expect(api.fetchImpl).not.toHaveBeenCalled();
  });

  it('stops waiting on a request that hangs, says what it knows, and offers to try again', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true, toFake: ['setTimeout', 'clearTimeout'] });
    // Online as far as the browser can tell; the server never answers.
    const hanging = vi.fn<typeof fetch>(() => new Promise<Response>(() => {}));
    const { router } = mount(hanging, '/notes/reading-list');

    // The back guard seeds the library beneath a deep link and pushes the note
    // back on top; the screen under test is the one mounted after that.
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/notes/reading-list');
      expect(router.state.location.key).not.toBe('default');
    });
    expect(await screen.findByText('Loading…')).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(LOADING_PATIENCE_MS + 50);
    });

    expect(screen.getByText(/the server hasn’t answered yet/i)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Not on this device' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument();
    expect(screen.queryByText('Loading…')).toBeNull();
  });
});

/**
 * The note as panels under one strip — Text · Recordings (N) — so the
 * recordings of a long note are one tap away rather than a scroll past every
 * paragraph. The strip is a real tablist: arrow keys move between segments.
 */
describe('the note is panels under one strip', () => {
  const withRecording: StoredNote = { ...ROOF, captures: [FILED] };

  async function loaded() {
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');
    });
  }

  it('opens on the text, counts the recordings on their tab, and switches on a tap', async () => {
    const user = userEvent.setup();
    const api = server([withRecording]);
    const { router } = mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    const tablist = screen.getByRole('tablist', { name: 'Note views' });
    const tabs = within(tablist).getAllByRole('tab');
    expect(tabs.map((tab) => tab.textContent)).toEqual(['Text', 'Recordings (1)']);
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('textbox', { name: 'Note body' })).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Recordings' })).toBeNull();
    // One panel, named by its tab.
    const panel = screen.getByRole('tabpanel');
    expect(panel).toHaveAccessibleName('Text');

    await user.click(screen.getByRole('tab', { name: /^Recordings/ }));

    expect(screen.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
    expect(await screen.findByRole('region', { name: 'Recordings' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /more for recording from/i })).toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Note body' })).toBeNull();
    // The bar is on every tab.
    expect(screen.getByRole('toolbar', { name: 'Note actions' })).toBeInTheDocument();
    // The URL says which tab, replacing the entry rather than stacking one.
    expect(router.state.location.search).toBe('?tab=recordings');
    expect(sessionStorage.getItem(noteTabStorageKey('roof-repair'))).toBe('recordings');

    await user.click(screen.getByRole('tab', { name: 'Text' }));
    expect(screen.getByRole('textbox', { name: 'Note body' })).toBeInTheDocument();
    expect(router.state.location.search).toBe('');
  });

  it('moves between segments with the arrow keys, Home and End', async () => {
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    const text = screen.getByRole('tab', { name: 'Text' });
    const recordings = screen.getByRole('tab', { name: /^Recordings/ });
    // Only the selected tab is in the Tab order.
    expect(text).toHaveAttribute('tabindex', '0');
    expect(recordings).toHaveAttribute('tabindex', '-1');

    text.focus();
    await user.keyboard('{ArrowRight}');
    expect(recordings).toHaveAttribute('aria-selected', 'true');
    expect(recordings).toHaveFocus();
    expect(await screen.findByRole('region', { name: 'Recordings' })).toBeInTheDocument();

    await user.keyboard('{ArrowRight}');
    // Wraps.
    expect(text).toHaveAttribute('aria-selected', 'true');
    expect(text).toHaveFocus();

    await user.keyboard('{End}');
    expect(recordings).toHaveAttribute('aria-selected', 'true');
    await user.keyboard('{Home}');
    expect(text).toHaveAttribute('aria-selected', 'true');
  });

  it('remembers the tab per note for the session, and a deep link outranks the memory', async () => {
    sessionStorage.setItem(noteTabStorageKey('roof-repair'), 'recordings');
    const api = server([
      withRecording,
      { ...ROOF, id: 'reading-list', title: 'Reading list', captures: [] },
    ]);

    // The remembered tab.
    let view = mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();
    expect(screen.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
    expect(await screen.findByRole('region', { name: 'Recordings' })).toBeInTheDocument();
    view.queryClient.clear();
    cleanup();

    // Another note has its own memory, which is empty: Text.
    view = mount(api.fetchImpl, '/notes/reading-list');
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('Reading list');
    });
    expect(screen.getByRole('tab', { name: 'Text' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tab', { name: 'Recordings (0)' })).toBeInTheDocument();
    view.queryClient.clear();
    cleanup();

    // A link that names a tab wins over what the session remembers.
    sessionStorage.setItem(noteTabStorageKey('roof-repair'), 'text');
    mount(api.fetchImpl, '/notes/roof-repair?tab=recordings');
    await loaded();
    expect(screen.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
  });

  it('brings the action bar back when the recordings panel is left mid-selection', async () => {
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair?tab=recordings');
    await loaded();

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));
    expect(await screen.findByRole('toolbar', { name: 'Recording actions' })).toBeInTheDocument();
    expect(screen.queryByRole('toolbar', { name: 'Note actions' })).toBeNull();

    await user.click(screen.getByRole('tab', { name: 'Text' }));
    expect(screen.queryByRole('toolbar', { name: 'Recording actions' })).toBeNull();
    expect(screen.getByRole('toolbar', { name: 'Note actions' })).toBeInTheDocument();
  });
});

/**
 * Send returns to the note's Recordings tab. The upload this device is making
 * is the first row there, counted on the tab, until the server's row takes
 * over — the hand-over the library's filing row used to be the only one to do.
 */
describe('a recording sent into this note shows up on its recordings', () => {
  afterEach(() => {
    useCaptureStore.setState({ model: INITIAL_CAPTURE });
  });

  it('counts the upload on the tab, shows it first, and hands over to the server row', async () => {
    const api = server([{ ...ROOF, captures: [FILED] }]);
    act(() => {
      useCaptureStore.setState({
        model: {
          ...INITIAL_CAPTURE,
          state: 'uploading',
          localId: 'cap-local',
          noteId: 'roof-repair',
          elapsedMs: 6_000,
          uploadProgress: 0.4,
        },
      });
    });
    mount(api.fetchImpl, '/notes/roof-repair?tab=recordings');

    expect(await screen.findByRole('tab', { name: 'Recordings (2)' })).toBeInTheDocument();
    const region = await screen.findByRole('region', { name: 'Recordings' });
    const rows = () =>
      within(region)
        .getAllByRole('listitem')
        .filter((item) => item.matches('.recording, .recordings__filing'));
    expect(rows()[0]).toHaveTextContent('Uploading… 40%');

    // The PUT lands and the server mints the row; the note is asked again.
    const getsBefore = api.gets;
    act(() => {
      api.notes.set('roof-repair', {
        ...(api.notes.get('roof-repair') as StoredNote),
        captures: [
          { ...FILED, id: 'cap-new', status: 'transcribing', created_at: new Date().toISOString() },
          FILED,
        ],
      });
      useCaptureStore.setState({
        model: {
          ...useCaptureStore.getState().model,
          state: 'uploaded',
          uploadProgress: 1,
          serverCaptureId: 'cap-new',
        },
      });
    });

    // The server's row replaces the local one and the machine is released.
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('idle');
    });
    expect(api.gets).toBeGreaterThan(getsBefore);
    await waitFor(() => {
      expect(screen.queryByText(/uploading…/i)).toBeNull();
    });
    expect(screen.getByRole('tab', { name: 'Recordings (2)' })).toBeInTheDocument();
    expect(rows()[0]).toHaveTextContent('Filing…');
    expect(within(rows()[0]!).getByRole('list', { name: 'Filing progress' })).toBeInTheDocument();
  });

  it('a recording aimed at another note stays off this one', async () => {
    const api = server([{ ...ROOF, captures: [FILED] }]);
    act(() => {
      useCaptureStore.setState({
        model: {
          ...INITIAL_CAPTURE,
          state: 'uploading',
          localId: 'cap-local',
          noteId: 'reading-list',
          uploadProgress: 0.2,
        },
      });
    });
    mount(api.fetchImpl, '/notes/roof-repair?tab=recordings');

    expect(await screen.findByRole('tab', { name: 'Recordings (1)' })).toBeInTheDocument();
    await screen.findByRole('button', { name: /more for recording from/i });
    expect(screen.queryByText(/uploading…/i)).toBeNull();
  });
});
