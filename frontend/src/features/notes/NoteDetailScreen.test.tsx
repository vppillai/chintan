import { QueryClient } from '@tanstack/react-query';
import { act, cleanup, createEvent, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { onlineManager } from '@tanstack/react-query';

import {
  CAPTURE_POLL_FAST_MS,
  CAPTURE_POLL_INTERVAL_MS,
  CAPTURE_POLL_SLOW_MS,
  queryKeys,
} from '@/api/queries.ts';
import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';
import { settings } from '@/api/__fixtures__/responses.ts';
import { routes } from '@/app/router.tsx';
import { INITIAL_CAPTURE } from '@/features/capture/machine.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { LOADING_PATIENCE_MS, NOTE_RETRY_GIVE_UP_MS, noteRetryInterval } from './NoteDetailScreen.tsx';
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

/** Details and Share live in the header's ⋮ menu, and open a drawer at the foot. */
async function openPanel(user: ReturnType<typeof userEvent.setup>, item: 'Details' | 'Share') {
  await user.click(screen.getByRole('button', { name: 'Note actions' }));
  await user.click(screen.getByRole('menuitem', { name: item }));
}

function server(initial: StoredNote[]) {
  const notes = new Map(initial.map((note) => [note.id, note]));
  const patches: { version: number; status: number; body: Record<string, unknown> }[] = [];
  const calls: string[] = [];
  let gets = 0;
  let captureGets = 0;

  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    calls.push(`${method} ${url.pathname}`);
    const detail = /\/v1\/notes\/([^/]+)$/.exec(url.pathname);

    // The three ways a note leaves: the archive (204), the way back from it,
    // and the purge, which the header's ⋮ reaches through a dialog.
    if (detail && method === 'DELETE') {
      const note = notes.get(detail[1] ?? '');
      if (note) Object.assign(note, { archived: true, version: note.version + 1 });
      return new Response(null, { status: 204 });
    }
    const restore = /\/v1\/notes\/([^/]+)\/restore$/.exec(url.pathname);
    if (restore && method === 'POST') {
      const note = notes.get(restore[1] ?? '');
      if (!note) return json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
      Object.assign(note, { archived: false, version: note.version + 1 });
      const { body: _body, captures: _captures, ...row } = note;
      return json(row);
    }
    const permanent = /\/v1\/notes\/([^/]+)\/permanent$/.exec(url.pathname);
    if (permanent && method === 'DELETE') {
      notes.delete(permanent[1] ?? '');
      return new Response(null, { status: 204 });
    }
    // Regenerate: the landed recordings go back to transcribed, as the
    // server resets them, and the 202 says how many.
    const regenerate = /\/v1\/notes\/([^/]+)\/regenerate$/.exec(url.pathname);
    if (regenerate && method === 'POST') {
      const note = notes.get(regenerate[1] ?? '');
      if (!note) return json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
      const landed = (note.captures ?? []).filter((capture) => capture.status === 'appended');
      note.captures = (note.captures ?? []).map((capture) =>
        capture.status === 'appended'
          ? { ...capture, status: 'transcribed' as const, last_progress_at: new Date().toISOString() }
          : capture,
      );
      return json({ status: 'queued', captures: landed.length }, 202);
    }

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
    // The open note's filing poll asks after each moving capture on its own.
    const capture = /\/v1\/captures\/([^/]+)$/.exec(url.pathname);
    if (capture && method === 'GET') {
      captureGets += 1;
      const held = [...notes.values()]
        .flatMap((note) => note.captures ?? [])
        .find((item) => item.id === capture[1]);
      return held ? json(held) : json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
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
    calls,
    notes,
    get captureGets() {
      return captureGets;
    },
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

// The provider's own `staleTime`, not the test default of zero: the defect
// lives in the thirty seconds during which a re-open is answered from cache.
function providerLikeClient(): QueryClient {
  return new QueryClient({
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
}

function mount(fetchImpl: typeof fetch, path: string, queryClient = providerLikeClient()) {
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(
    <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return { router, queryClient };
}

describe('the settings come from the shell, not after the note', () => {
  it('reads settings cached by the shell however long ago, rather than fetching them again (round-3 T48)', async () => {
    /*
     * The shell prefetches settings once per session; this screen reads them
     * for one language label. With the client's thirty-second default the
     * observer refetched on every open after the first half-minute — the
     * waterfall T48 removed, back under another name.
     */
    const api = server([ROOF]);
    const queryClient = providerLikeClient();
    // As the shell left them: cached well past the client's default thirty seconds.
    queryClient.setQueryData(queryKeys.settings(), settings, { updatedAt: Date.now() - 5 * 60_000 });
    mount(api.fetchImpl, '/notes/roof-repair', queryClient);

    await screen.findByRole('textbox', { name: 'Note body' });
    const urls = api.fetchImpl.mock.calls.map((call) => new URL(String(call[0])).pathname);
    expect(urls.filter((url) => url.endsWith('/v1/settings'))).toEqual([]);
  });
});

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
    expect(await screen.findByText('Saved', { selector: '.save-indicator' })).toBeInTheDocument();
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

    await openPanel(user, 'Details');
    await user.type(screen.getByRole('textbox', { name: 'Add a tag' }), 'vtag{Enter}');

    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches[1]).toEqual(expect.objectContaining({ version: 2, status: 200 }));
    // The tag alone: the body the cache handed back is the server's, and an
    // unchanged body is not an edit (QA 2026-09-21, finding 1).
    expect(api.patches[1]?.body).toEqual({ version: 2, tags: ['vtag'] });
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
    expect(await screen.findByText('Saved', { selector: '.save-indicator' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /back to\s*notes/i }));
    expect(await screen.findByRole('button', { name: /^renamed/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /roof repair/i })).toBeNull();
  });
});

describe('a note whose recording is still filing keeps asking', () => {
  // The poll on a fake clock that still moves with real time: the test
  // advances CAPTURE_POLL_FAST_MS and the refetch lands, rather than waiting
  // through two real ticks (five seconds, and a flake on the CI runner).
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

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
    const { queryClient } = mount(api.fetchImpl, '/notes/roof-repair');
    const invalidated = vi.spyOn(queryClient, 'invalidateQueries');

    const body = await screen.findByRole('textbox', { name: 'Note body' });
    await waitFor(() => {
      expect(body).toHaveValue('Only paragraph.');
    });
    expect(invalidated).not.toHaveBeenCalledWith({ queryKey: ['notes'] });

    // The worker finishes between two polls.
    act(() => {
      api.notes.set('roof-repair', {
        ...(api.notes.get('roof-repair') as StoredNote),
        body: 'Only paragraph.\n\nThe gutter is leaking again.',
        version: 2,
        captures: [{ ...moving, status: 'appended' }],
      });
    });

    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS);
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note body' })).toHaveValue(
        'Only paragraph.\n\nThe gutter is leaking again.',
      );
    });
    // The capture was asked after; the note itself once more, for the body.
    expect(api.captureGets).toBeGreaterThan(0);
    expect(api.gets).toBe(2);
    // And the library's lists, whose snippet just grew: the same reconciliation
    // the library's poll does, from the note's own copy crossing into appended.
    expect(invalidated).toHaveBeenCalledWith({ queryKey: ['notes'] });
    // Settled: nothing left to ask about, so the polling stops.
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS + 200);
    const after = api.gets + api.captureGets;
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS + 200);
    expect(api.gets + api.captureGets).toBe(after);
  });

  it('moves the banner through each stage from the capture alone, and reads the note only when it lands', async () => {
    /*
     * The detail GET — body and every capture — used to be the poll, every
     * 1.5 s and then every 4 s for as long as anything filed. The stages
     * now come from GET /v1/captures/{id}.
     */
    const moving: CaptureWire = {
      id: 'cap-new',
      status: 'uploaded',
      created_at: new Date().toISOString(),
      version: 1,
      note_id: 'roof-repair',
      duration_ms: 5_000,
    };
    const api = server([{ ...ROOF, body: 'Only paragraph.', captures: [moving] }]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await screen.findByRole('textbox', { name: 'Note body' });
    const readsAtOpen = api.gets;
    const banner = () => screen.getByRole('region', { name: 'Filing a recording' }).textContent;
    const atUpload = banner();

    const setStatus = (status: CaptureWire['status']): void => {
      const note = api.notes.get('roof-repair') as StoredNote;
      note.captures = [{ ...moving, status }];
    };
    for (const status of ['transcribing', 'transcribed', 'routing'] as const) {
      setStatus(status);
      await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS);
    }
    expect(api.captureGets).toBeGreaterThanOrEqual(3);
    expect(api.gets).toBe(readsAtOpen);
    // The banner followed the capture, not the note.
    await waitFor(() => {
      expect(screen.getByText('Filing in progress')).toBeInTheDocument();
    });
    // The stages still reach the banner: the capture's answer is written into the note.
    await waitFor(() => {
      expect(banner()).not.toBe(atUpload);
    });

    setStatus('appended');
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS);
    await waitFor(() => {
      expect(api.gets).toBe(readsAtOpen + 1);
    });
  });

  it('reads the note once when a capture stops short, shows Retry and Dismiss, and stops asking', async () => {
    const moving: CaptureWire = {
      id: 'cap-new',
      status: 'routing',
      created_at: new Date().toISOString(),
      version: 1,
      note_id: 'roof-repair',
      duration_ms: 5_000,
    };
    const api = server([{ ...ROOF, body: 'Only paragraph.', captures: [moving] }]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await screen.findByRole('textbox', { name: 'Note body' });
    const readsAtOpen = api.gets;

    (api.notes.get('roof-repair') as StoredNote).captures = [
      { ...moving, status: 'failed', error: 'The provider refused the request.' },
    ];
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS);
    const banner = await screen.findByRole('region', { name: 'Filing a recording' });
    await waitFor(() => {
      expect(within(banner).getByRole('button', { name: 'Retry' })).toBeInTheDocument();
    });
    expect(within(banner).getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
    expect(api.gets).toBe(readsAtOpen + 1);

    const asked = api.gets + api.captureGets;
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_INTERVAL_MS * 3);
    expect(api.gets + api.captureGets).toBe(asked);
  });

  it('stops asking after a capture the server no longer has, and reads the note again', async () => {
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
    await screen.findByRole('textbox', { name: 'Note body' });
    const readsAtOpen = api.gets;

    // Deleted on another device: gone from the server and from the note.
    (api.notes.get('roof-repair') as StoredNote).captures = [];
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS);
    await waitFor(() => {
      expect(api.gets).toBe(readsAtOpen + 1);
    });
    const asked = api.gets + api.captureGets;
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_INTERVAL_MS * 3);
    expect(api.gets + api.captureGets).toBe(asked);
  });
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
     * hundred pixels. Where `field-sizing: content` is missing (older WebKit)
     * the hook measures the scroll height and sets the height to it. jsdom 30
     * knows `field-sizing`, so the fallback has to be pinned here.
     */
    vi.spyOn(CSS, 'supports').mockReturnValue(false);
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
 * The note is there, the first request was not: a phone woken by its own
 * notification whose radio is not up yet fails the first GET before it
 * reaches the server. The screen must ask again on its own; it used to stand
 * on "Not on this device" until Try again was pressed.
 */
describe('a note the first request never reached is asked for again', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('recovers from a first read that failed on the way out, with nothing pressed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true, toFake: ['setTimeout', 'clearTimeout'] });
    // No jitter: the client's own retries of a network failure are instant.
    vi.spyOn(Math, 'random').mockReturnValue(0);
    const api = server([ROOF]);
    // The client retries a network failure three times before it gives up,
    // so the first four attempts fail as a dead link fails them.
    let failures = 4;
    const flaky = vi.fn<typeof fetch>(async (input, init) => {
      if (/\/v1\/notes\/roof-repair$/.test(String(input)) && failures > 0) {
        failures -= 1;
        throw new TypeError('Failed to fetch');
      }
      return api.fetchImpl(input, init);
    });
    mount(flaky, '/notes/roof-repair');

    expect(await screen.findByRole('heading', { name: 'Not on this device' })).toBeInTheDocument();
    expect(api.gets).toBe(0);

    // The first rung of the ladder, measured from the mount.
    act(() => {
      vi.advanceTimersByTime(CAPTURE_POLL_FAST_MS + 50);
    });

    expect(
      await screen.findByRole('textbox', { name: 'Note body' }, { timeout: 3_000 }),
    ).toBeInTheDocument();
    expect(api.gets).toBe(1);
  });

  it('asks on the filing ladder: brisk at first, then every few seconds, then every fifteen, then not at all', () => {
    expect(noteRetryInterval(0)).toBe(CAPTURE_POLL_FAST_MS);
    expect(noteRetryInterval(29_000)).toBe(CAPTURE_POLL_FAST_MS);
    expect(noteRetryInterval(31_000)).toBe(CAPTURE_POLL_INTERVAL_MS);
    expect(noteRetryInterval(3 * 60_000)).toBe(CAPTURE_POLL_SLOW_MS);
    expect(noteRetryInterval(NOTE_RETRY_GIVE_UP_MS)).toBe(false);
  });

  it('climbs the ladder on a dead link rather than asking every second or two', async () => {
    // The clock too: the ladder reads `Date.now()` against the mount.
    vi.useFakeTimers({ shouldAdvanceTime: true, toFake: ['setTimeout', 'clearTimeout', 'Date'] });
    vi.spyOn(Math, 'random').mockReturnValue(0);
    // Every attempt fails as a dead link fails it; the client's own retries
    // make four attempts per read, which is what is counted.
    let attempts = 0;
    const dead = vi.fn<typeof fetch>(async (input) => {
      if (/\/v1\/notes\/roof-repair$/.test(String(input))) attempts += 1;
      throw new TypeError('Failed to fetch');
    });
    mount(dead, '/notes/roof-repair');
    expect(await screen.findByRole('heading', { name: 'Not on this device' })).toBeInTheDocument();

    const WINDOW_MS = 5 * 60_000;
    for (let t = 0; t < WINDOW_MS; t += 1_000) {
      // Async, so each read's retries and their zero-delay sleeps settle
      // within the second they belong to.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_000);
      });
    }

    // One read at mount plus one per rung of the ladder over the window; a
    // latch that reset on every tick asked more than twice as often.
    let ticks = 1;
    for (let at = 0; at < WINDOW_MS; ) {
      const next = noteRetryInterval(at);
      if (next === false) break;
      at += next;
      ticks += 1;
    }
    expect(attempts).toBeLessThanOrEqual(ticks * 4);
    expect(attempts).toBeGreaterThan(4);
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
    expect(tabs.map((tab) => tab.textContent)).toEqual(['Text', 'Cleaned', 'Recordings (1)']);
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
    // The note's menu is on every tab.
    expect(screen.getByRole('button', { name: 'Note actions' })).toBeInTheDocument();
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
    const cleaned = screen.getByRole('tab', { name: 'Cleaned' });
    expect(cleaned).toHaveAttribute('aria-selected', 'true');
    expect(cleaned).toHaveFocus();
    expect(await screen.findByRole('region', { name: 'Cleaned view' })).toBeInTheDocument();

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

  /** The region's top this far above (or below) the scroll region's: jsdom lays nothing out. */
  function placeViews(above: number) {
    return vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (
      this: Element,
    ) {
      const top = this.classList.contains('note-views') ? 60 - above : 60;
      return { top, bottom: top, left: 0, right: 0, width: 0, height: 0, x: 0, y: top } as DOMRect;
    });
  }

  it('starts a new tab at its top under a stuck strip', async () => {
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();
    const main = document.querySelector('.app__main')!;
    main.scrollTop = 1_500;
    // Scrolled deep: the strip is stuck and the region's top 1 200 px above it.
    const rects = placeViews(1_200);
    await user.click(screen.getByRole('tab', { name: 'Cleaned' }));
    expect(main.scrollTop).toBe(300);
    rects.mockRestore();
  });

  it('leaves the scroll alone on a tab change with the head on screen', async () => {
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();
    const main = document.querySelector('.app__main')!;
    main.scrollTop = 40;
    const rects = placeViews(-100);
    await user.click(screen.getByRole('tab', { name: 'Cleaned' }));
    expect(main.scrollTop).toBe(40);
    rects.mockRestore();
  });

  it('names each of the three segments, and each panel is what its tab says', async () => {
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    await user.click(screen.getByRole('tab', { name: 'Cleaned' }));
    expect(screen.getByRole('tabpanel')).toHaveAccessibleName('Cleaned');
    expect(screen.getByText('No cleaned view yet')).toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Note body' })).toBeNull();
    // The note's menu is here too.
    expect(screen.getByRole('button', { name: 'Note actions' })).toBeInTheDocument();
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

  it('hides an open drawer while recordings are selected, and brings it back when the panel is left', async () => {
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair?tab=recordings');
    await loaded();
    await openPanel(user, 'Details');
    expect(screen.getByRole('heading', { name: 'Details' })).toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));
    expect(await screen.findByRole('toolbar', { name: 'Recording actions' })).toBeInTheDocument();
    // Hidden, not closed: the selection bar has the foot of the screen.
    expect(screen.queryByRole('heading', { name: 'Details' })).toBeNull();

    await user.click(screen.getByRole('tab', { name: 'Text' }));
    expect(screen.queryByRole('toolbar', { name: 'Recording actions' })).toBeNull();
    expect(screen.getByRole('heading', { name: 'Details' })).toBeInTheDocument();
  });

  it('shows the note id in Details, copyable, with the header it is for', async () => {
    // The id is what a device's `X-Chintan-Note-Id` carries; until now the
    // address bar was the only place to read it (owner, 2026-09-29).
    const user = userEvent.setup();
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    try {
      const api = server([withRecording]);
      mount(api.fetchImpl, '/notes/roof-repair');
      await loaded();
      await openPanel(user, 'Details');

      // A caption, not a heading: the section is named by it (FE-22).
      const section = screen.getByRole('region', { name: 'Note id' });
      expect(within(section).getByText('roof-repair')).toHaveClass('note-id');
      expect(section).toHaveTextContent('X-Chintan-Note-Id');
      await user.click(within(section).getByRole('button', { name: 'Copy note id' }));
      expect(writeText).toHaveBeenCalledWith('roof-repair');
      expect(await within(section).findByRole('button', { name: 'Copied' })).toBeInTheDocument();
    } finally {
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
    }
  });

  it('sends focus into the drawer it opens, and back to the menu when it closes', async () => {
    // The menuitem that opened the drawer unmounts on select, and the drawer
    // is at the far end of the screen: without this a keyboard user is left
    // on <body> with the whole note to Tab through.
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    await openPanel(user, 'Details');
    expect(screen.getByRole('combobox', { name: 'Transcription language' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Close details' }));
    expect(screen.getByRole('button', { name: 'Note actions' })).toHaveFocus();

    await openPanel(user, 'Share');
    expect(screen.getByRole('heading', { name: 'Share' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Close share' }));
    expect(screen.getByRole('button', { name: 'Note actions' })).toHaveFocus();
  });

  it('is a dialog while on screen: Tab stays inside, Escape closes it and hands focus back to the ⋮', async () => {
    // After R8-S1 the sheet reads as a dialog but let Tab walk out into the
    // note and ignored Escape, unlike the Move sheet and the confirm dialogs
    // (review 2026-10-01, FE-3).
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    await openPanel(user, 'Share');
    const dialog = screen.getByRole('dialog', { name: 'Share' });
    expect(dialog).toHaveAttribute('aria-modal', 'true');
    // The heading keeps the focus the screen gives it; Tab from the last
    // control wraps to the first, and Shift+Tab from the first to the last.
    expect(screen.getByRole('heading', { name: 'Share' })).toHaveFocus();
    const controls = within(dialog).getAllByRole('button');
    // From the heading, which is not a control, Shift+Tab must not walk out
    // into the note: it is the sheet's start, so it goes to the last control.
    await user.tab({ shift: true });
    expect(controls.at(-1)).toHaveFocus();
    await user.tab();
    expect(controls[0]).toHaveFocus();
    await user.tab({ shift: true });
    expect(controls.at(-1)).toHaveFocus();
    screen.getByRole('heading', { name: 'Share' }).focus();
    await user.tab();
    expect(controls[0]).toHaveFocus();

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByRole('button', { name: 'Note actions' })).toHaveFocus();
  });

  it('arrives from Home with the placeholder title selected, once', async () => {
    // From Home, as the + sends it: a deep link is re-seeded under the
    // library and would lose the route state on the way.
    const api = server([withRecording]);
    const { router } = mount(api.fetchImpl, '/');
    await screen.findByRole('heading', { name: /notes/i });
    await act(() => router.navigate('/notes/roof-repair', { state: { focusTitle: true } }));
    await loaded();
    const title = screen.getByRole('textbox', { name: 'Note title' }) as HTMLInputElement;
    expect(title).toHaveFocus();
    expect(title.selectionStart).toBe(0);
    expect(title.selectionEnd).toBe('Roof repair'.length);
  });

  it('discards an untouched placeholder when the screen is left, and keeps one that was typed into', async () => {
    const placeholder: StoredNote = { ...ROOF, id: 'typed-1', title: 'New note', body: '', snippet: '' };
    const api = server([ROOF, placeholder]);
    const { router } = mount(api.fetchImpl, '/');
    await screen.findByRole('heading', { name: /notes/i });

    // Opened from the +, left alone, Back: archived and purged, no dialog.
    await act(() => router.navigate('/notes/typed-1', { state: { focusTitle: true } }));
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('New note');
    });
    await act(() => router.navigate(-1));
    await waitFor(() => {
      expect(api.calls).toContain('DELETE /v1/notes/typed-1/permanent');
    });
    expect(api.calls).toContain('DELETE /v1/notes/typed-1');
    expect(screen.queryByRole('dialog')).toBeNull();

    // A title typed over the placeholder, even before it has saved: kept.
    const user = userEvent.setup();
    const kept: StoredNote = { ...ROOF, id: 'typed-2', title: 'New note', body: '', snippet: '' };
    api.notes.set('typed-2', kept);
    await act(() => router.navigate('/notes/typed-2', { state: { focusTitle: true } }));
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('New note');
    });
    await user.keyboard('Hardware');
    const before = api.calls.filter((call) => call.startsWith('DELETE')).length;
    await act(() => router.navigate(-1));
    await screen.findByRole('heading', { name: /notes/i });
    expect(api.calls.filter((call) => call.startsWith('DELETE'))).toHaveLength(before);
  });

  it('closes from a drag down on its head, past the threshold or in a flick, and settles back short of it', async () => {
    // A phone's own sheets close this way; the × and Escape stay (above).
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();
    const height = 400;
    const box = vi
      .spyOn(HTMLElement.prototype, 'getBoundingClientRect')
      .mockReturnValue({ height, width: 360, top: 0, left: 0, right: 360, bottom: height, x: 0, y: 0, toJSON: () => ({}) });
    try {
      const touch = { pointerId: 7, pointerType: 'touch', button: 0, clientX: 100 };
      // `timeStamp` is read-only on an event, so the clock the hook reads is
      // set on the event itself: two synchronous moves would otherwise be a
      // flick of infinite speed.
      const at = (
        kind: 'pointerDown' | 'pointerMove' | 'pointerUp',
        head: Element,
        init: Record<string, unknown>,
        timeStamp: number,
      ) => {
        const event = createEvent[kind](head, init);
        Object.defineProperty(event, 'timeStamp', { value: timeStamp });
        fireEvent(head, event);
      };
      const dragDown = (dy: number, ms: number) => {
        const head = screen.getByRole('dialog').querySelector('.note-panel__head')!;
        const t0 = 1_000;
        at('pointerDown', head, { ...touch, clientY: 500 }, t0);
        at('pointerMove', head, { ...touch, clientY: 500 + dy / 2 }, t0 + ms / 2);
        at('pointerMove', head, { ...touch, clientY: 500 + dy }, t0 + ms);
        at('pointerUp', head, { ...touch, clientY: 500 + dy }, t0 + ms);
      };
      const sheet = () => screen.getByRole('dialog');

      // Short and slow: back where it was.
      await openPanel(user, 'Share');
      dragDown(40, 400);
      expect(sheet()).toBeInTheDocument();
      expect(sheet().style.translate).toBe('');

      // Past 30 % of the height, slowly (0.2 px/ms, half the flick speed):
      // the fraction alone closes it, sliding away until the slide ends.
      dragDown(height * 0.4, 800);
      expect(sheet().style.translate).toBe(`0 ${height}px`);
      // The sheet's own slide ending; a child's colour settling would not do.
      fireEvent.transitionEnd(sheet(), { propertyName: 'translate' });
      expect(screen.queryByRole('dialog')).toBeNull();
      expect(screen.getByRole('button', { name: 'Note actions' })).toHaveFocus();

      // A flick: short, but fast and still moving at the lift.
      await openPanel(user, 'Share');
      dragDown(60, 40);
      fireEvent.transitionEnd(sheet(), { propertyName: 'translate' });
      expect(screen.queryByRole('dialog')).toBeNull();

      // A mouse has the ×: its drag is nothing.
      await openPanel(user, 'Share');
      const head = sheet().querySelector('.note-panel__head')!;
      fireEvent.pointerDown(head, { ...touch, pointerType: 'mouse', clientY: 500 });
      fireEvent.pointerMove(head, { ...touch, pointerType: 'mouse', clientY: 800 });
      fireEvent.pointerUp(head, { ...touch, pointerType: 'mouse', clientY: 800 });
      expect(sheet()).toBeInTheDocument();
    } finally {
      box.mockRestore();
    }
  });

  it('stops being a dialog while hidden behind the selection bar, so Escape cancels the selection', async () => {
    const user = userEvent.setup();
    const api = server([withRecording]);
    mount(api.fetchImpl, '/notes/roof-repair?tab=recordings');
    await loaded();
    await openPanel(user, 'Details');
    expect(screen.getByRole('dialog', { name: 'Details' })).toBeInTheDocument();

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));
    expect(await screen.findByRole('toolbar', { name: 'Recording actions' })).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { hidden: true })).toBeNull();

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('toolbar', { name: 'Recording actions' })).toBeNull();
    // Back, and a dialog again.
    expect(screen.getByRole('dialog', { name: 'Details' })).toBeInTheDocument();
  });
});

/**
 * Getting rid of the note from its own screen, through the header's ⋮
 * (`NoteMenu`). Delete is the archive and asks first (owner, 2026-09-27:
 * "ask are you sure"); Delete forever asks in its own words. There is no
 * typed word in either (owner, 2026-09-26).
 */
describe('deleting from the note screen', () => {
  async function loaded() {
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');
    });
  }

  it('Delete asks first, then archives, lands on the library and offers Undo, which restores', async () => {
    const user = userEvent.setup();
    const api = server([ROOF]);
    const { router } = mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));

    // The question, with nothing gone yet and the safe answer under Enter.
    const dialog = screen.getByRole('dialog', { name: 'Delete “Roof repair”?' });
    expect(dialog).toHaveTextContent('It is kept in the Archive for 30 days, then gone for good.');
    expect(within(dialog).queryByRole('textbox')).toBeNull();
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
    expect(api.calls.filter((call) => call.startsWith('DELETE'))).toEqual([]);

    // Cancel hands focus back to the ⋮ the menuitem came from, not the body.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByRole('button', { name: 'Note actions' })).toHaveFocus();
    expect(api.calls.filter((call) => call.startsWith('DELETE'))).toEqual([]);

    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    await user.click(
      within(screen.getByRole('dialog', { name: 'Delete “Roof repair”?' })).getByRole('button', {
        name: 'Delete',
      }),
    );
    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() => {
      expect(api.calls).toContain('DELETE /v1/notes/roof-repair');
    });
    expect(api.calls).not.toContain('DELETE /v1/notes/roof-repair/permanent');
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/');
    });
    // The note's URL is not left in the history for Back to 404 into.
    expect(router.state.location.search).toBe('');

    // The toast is the shell's, so it survives the navigation, and says where the note went.
    expect(screen.getByText('Deleted · kept in Archive for 30 days')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Undo' }));
    await waitFor(() => {
      expect(api.calls).toContain('POST /v1/notes/roof-repair/restore');
    });
    expect(api.notes.get('roof-repair')?.archived).toBe(false);
    expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
  });

  it('a note Delete takes a standing Delete done Undo’s place; its Undo restores the note, never the done items (D3)', async () => {
    const user = userEvent.setup();
    const shopping: StoredNote = {
      ...ROOF,
      id: 'shopping',
      kind: 'checklist',
      title: 'Shopping',
      body: '- [ ] Milk\n- [x] Eggs',
      snippet: '- [ ] Milk',
    };
    const api = server([shopping]);
    const { router } = mount(api.fetchImpl, '/notes/shopping');
    await screen.findByRole('textbox', { name: 'Item 1' });

    await user.click(screen.getByRole('button', { name: 'Delete done' }));
    // The toast's copy: the shell's status region says the same words.
    expect(screen.getByText('1 done item deleted', { selector: '.toast__text' })).toBeInTheDocument();
    await waitFor(() => {
      expect(api.notes.get('shopping')?.body).toBe('- [ ] Milk');
    });

    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    await user.click(within(screen.getByRole('dialog', { name: 'Delete “Shopping”?' })).getByRole('button', { name: 'Delete' }));
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/');
    });
    // The toast is the note's now; the done items' Undo went with its toast.
    expect(screen.getByText('Deleted · kept in Archive for 30 days')).toBeInTheDocument();
    expect(screen.queryByText('1 done item deleted', { selector: '.toast__text' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Undo' }));
    await waitFor(() => {
      expect(api.calls).toContain('POST /v1/notes/shopping/restore');
    });
    expect(api.notes.get('shopping')?.archived).toBe(false);
    expect(api.notes.get('shopping')?.body).toBe('- [ ] Milk');
    expect(api.calls.filter((call) => call.startsWith('PATCH'))).toHaveLength(1);
  });

  it('Delete forever on an archived note opens a plain confirm — no textbox, focus on Cancel — whose button purges', async () => {
    const user = userEvent.setup();
    const api = server([
      { ...ROOF, archived: true, purge_after: new Date(Date.now() + 10 * 86_400_000).toISOString() },
    ]);
    const { router } = mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete forever' }));

    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByRole('textbox')).toBeNull();
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
    expect(dialog).toHaveTextContent(/recordings and transcripts/i);

    await user.click(within(dialog).getByRole('button', { name: 'Delete forever' }));
    await waitFor(() => {
      expect(api.calls).toContain('DELETE /v1/notes/roof-repair/permanent');
    });
    expect(api.calls).not.toContain('DELETE /v1/notes/roof-repair');
    expect(api.notes.has('roof-repair')).toBe(false);
    // Back to the archive, which is where this note was.
    await waitFor(() => {
      expect(router.state.location.search).toBe('?view=archived');
    });
  });
});

/**
 * Regenerate from recordings, through the header's ⋮ (`NoteMenu`): a plain
 * confirm that names the count, one POST, and the filing strip while the
 * recordings come back (owner, 2026-09-27).
 */
describe('regenerating a note from its recordings', () => {
  it('opens a plain confirm naming the count, posts on Regenerate, and shows the strip while the recordings land', async () => {
    const user = userEvent.setup();
    const api = server([{ ...ROOF, captures: [FILED, { ...FILED, id: 'cap-older', created_at: '2026-08-05T09:10:00.000Z' }] }]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');
    });

    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Regenerate from recordings…' }));

    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByRole('textbox')).toBeNull();
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
    expect(dialog).toHaveTextContent(
      'Re-does the AI cleanup of 2 recordings with the current settings; edits you made inside those paragraphs are replaced. Ticks are kept.',
    );
    expect(api.calls).not.toContain('POST /v1/notes/roof-repair/regenerate');

    await user.click(within(dialog).getByRole('button', { name: 'Regenerate' }));
    await waitFor(() => {
      expect(api.calls).toContain('POST /v1/notes/roof-repair/regenerate');
    });
    expect(screen.queryByRole('dialog')).toBeNull();
    // The refetch sees the recordings back at transcribed: the strip appears
    // and the menu item stands down until they land.
    expect(await screen.findByRole('region', { name: 'Filing a recording' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    expect(screen.getByRole('menuitem', { name: 'Regenerating…' })).toBeDisabled();
  });

  it('is off when no recording has landed in the note', async () => {
    const user = userEvent.setup();
    const api = server([ROOF]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');
    });
    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    expect(screen.getByRole('menuitem', { name: 'Regenerate from recordings…' })).toBeDisabled();
  });

  it('says so under the menu when the server finds nothing to regenerate', async () => {
    const user = userEvent.setup();
    const api = server([{ ...ROOF, captures: [FILED] }]);
    // The dialog counts the landed recordings; the server also skips a
    // paragraph rewritten by hand, so its count can be zero where the dialog
    // said one.
    const fetchImpl: typeof fetch = async (input, init) =>
      init?.method === 'POST' && String(input).endsWith('/regenerate')
        ? json({ status: 'queued', captures: 0 }, 202)
        : api.fetchImpl(input, init);
    mount(fetchImpl, '/notes/roof-repair');
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');
    });
    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Regenerate from recordings…' }));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Regenerate' }));
    expect(
      await screen.findByText('Nothing to regenerate: every paragraph is in your own words now.'),
    ).toBeInTheDocument();
  });

  it('offers a fresh regeneration when a recording has sat stuck rather than reading it as still moving', async () => {
    const user = userEvent.setup();
    const stuck: CaptureWire = {
      ...FILED,
      id: 'cap-stuck',
      status: 'cleaning',
      last_progress_at: '2026-08-06T09:10:00.000Z',
    };
    const api = server([{ ...ROOF, captures: [FILED, stuck] }]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');
    });
    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    expect(screen.getByRole('menuitem', { name: 'Regenerate from recordings…' })).toBeEnabled();
  });
});

/**
 * Find in this note. The body is a textarea, which cannot show a mark, so
 * while the bar has a query the panel shows a read-only mirror of the same
 * text with every match marked; closing the bar brings the textarea back with
 * the caret on the match that was current.
 */
describe('find in this note', () => {
  const LONG: StoredNote = {
    ...ROOF,
    body: 'Ridge tiles have slipped.\n\nEllis quoted for the tiles. The café roof has tiles too.',
    captures: [FILED],
  };

  async function loaded() {
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Note body' })).toHaveValue(LONG.body);
    });
  }

  it('mirrors the body with the matches marked, and hands back the textarea with the caret on the match', async () => {
    const user = userEvent.setup();
    const api = server([LONG]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();
    expect(screen.queryByRole('search', { name: 'Find in note' })).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Find in note' }));
    const input = screen.getByRole('searchbox', { name: 'Find in note' });
    expect(input).toHaveFocus();
    // Nothing typed yet: the textarea stays.
    expect(screen.getByRole('textbox', { name: 'Note body' })).toBeInTheDocument();

    await user.type(input, 'tiles');
    expect(screen.queryByRole('textbox', { name: 'Note body' })).toBeNull();
    // Looked up each time: clearing the query hands the textarea back for a
    // moment, and the next letter mounts a fresh mirror.
    const mirror = () => screen.getByRole('region', { name: /note body, read-only while finding/i });
    // The same text, newlines and all.
    expect(mirror().textContent).toBe(LONG.body);
    const marks = () => Array.from(mirror().querySelectorAll('mark'));
    expect(marks().map((mark) => mark.textContent)).toEqual(['tiles', 'tiles', 'tiles']);
    expect(marks().map((mark) => mark.hasAttribute('data-active'))).toEqual([true, false, false]);
    expect(screen.getByText('1 of 3')).toBeInTheDocument();

    await user.keyboard('{Enter}');
    expect(marks().map((mark) => mark.hasAttribute('data-active'))).toEqual([false, true, false]);
    expect(screen.getByText('2 of 3')).toBeInTheDocument();

    // Diacritics do not matter: "cafe" finds "café".
    await user.clear(input);
    await user.type(input, 'cafe');
    expect(marks().map((mark) => mark.textContent)).toEqual(['café']);

    await user.clear(input);
    await user.type(input, 'tiles');
    await user.keyboard('{Enter}');
    await user.keyboard('{Escape}');

    const body = screen.getByRole<HTMLTextAreaElement>('textbox', { name: 'Note body' });
    expect(body).toHaveFocus();
    expect(screen.queryByRole('search', { name: 'Find in note' })).toBeNull();
    // The caret is on the second "tiles".
    const second = LONG.body.indexOf('tiles', LONG.body.indexOf('tiles') + 1);
    expect(body.selectionStart).toBe(second);
    expect(body.selectionEnd).toBe(second + 'tiles'.length);
  });

  it('opens on Ctrl/⌘+F instead of the browser’s find, and closes from the mirror on a tap', async () => {
    const user = userEvent.setup();
    const api = server([LONG]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    const intercepted = vi.fn<(event: KeyboardEvent) => void>((event) => {
      // What the browser would look at: was the default (its own find) refused?
      // Registered after the screen's listener, so it sees the answer.
      if (event.key === 'f') expect(event.defaultPrevented).toBe(true);
    });
    window.addEventListener('keydown', intercepted);
    await user.keyboard('{Control>}f{/Control}');
    window.removeEventListener('keydown', intercepted);
    expect(intercepted).toHaveBeenCalled();
    const input = screen.getByRole('searchbox', { name: 'Find in note' });
    expect(input).toHaveFocus();

    await user.type(input, 'Ellis');
    const mirror = screen.getByRole('region', { name: /note body, read-only while finding/i });
    expect(mirror.querySelectorAll('mark')).toHaveLength(1);

    await user.click(mirror);
    const body = screen.getByRole<HTMLTextAreaElement>('textbox', { name: 'Note body' });
    expect(body).toHaveFocus();
    expect(body.selectionStart).toBe(LONG.body.indexOf('Ellis'));
    expect(screen.queryByRole('searchbox')).toBeNull();
  });

  it('is greyed on Recordings with a hint, and searches the cleaned view in place', async () => {
    const user = userEvent.setup();
    const api = server([
      {
        ...LONG,
        cleaned: {
          body: '# Roof\n\n- **Tiles** slipped\n- Ellis quoted for the tiles',
          mode: 'structured',
          generated_at: '2026-08-06T09:20:00.000Z',
          stale: false,
        },
      },
    ]);
    mount(api.fetchImpl, '/notes/roof-repair');
    await loaded();

    await user.click(screen.getByRole('button', { name: 'Find in note' }));
    await user.type(screen.getByRole('searchbox'), 'tiles');
    expect(screen.getByText('1 of 3')).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: /^Recordings/ }));
    expect(screen.getByRole('searchbox')).toBeDisabled();
    expect(screen.getByText('Search works in Text and Cleaned.')).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Cleaned' }));
    expect(screen.getByRole('searchbox')).toBeEnabled();
    const view = await screen.findByRole('region', { name: 'Cleaned view' });
    await waitFor(() => {
      expect(view.querySelectorAll('mark')).toHaveLength(2);
    });
    expect(view.querySelector('mark')?.parentElement?.tagName).toBe('STRONG');
    expect(screen.getByText('1 of 2')).toBeInTheDocument();
    // The query is kept across tabs; the count is the new panel's.
    expect(screen.getByRole('searchbox')).toHaveValue('tiles');
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

describe('a recording filed while the user was typing', () => {
  it('is named in the conflict prompt, and its paragraph can be kept alongside the edit', async () => {
    /*
     * Review S10: "Keep my edits" after a voice append carried the
     * recording's text away without saying so. The prompt now says a
     * recording is at stake, and offers to keep both.
     */
    sessionStorage.removeItem(noteTabStorageKey('roof-repair'));
    const user = userEvent.setup();
    const api = server([ROOF]);
    mount(api.fetchImpl, '/notes/roof-repair');
    const body = await screen.findByRole('textbox', { name: 'Note body' });
    await waitFor(() => {
      expect(body).toHaveValue('v1 body');
    });

    // The worker filed a recording: its paragraph after the body, its row on the note.
    api.notes.set('roof-repair', {
      ...ROOF,
      body: 'v1 body\n\nFrom the recording.',
      snippet: 'v1 body From the recording.',
      version: 2,
      captures: [{ ...FILED, id: 'cap-new', created_at: new Date().toISOString() }],
    });
    await user.type(body, ' mine');
    await user.tab();

    expect(await screen.findByText(/changed elsewhere/i)).toBeInTheDocument();
    expect(screen.getByText(/a recording was filed into this note/i)).toBeInTheDocument();
    expect(screen.getByText(/keeping only your edits removes its text/i)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Keep my edits and add the recording' }));
    await waitFor(() => {
      expect(screen.queryByText(/changed elsewhere/i)).toBeNull();
    });
    expect(body).toHaveValue('v1 body mine\n\nFrom the recording.');
    expect(await screen.findByText('Saved', { selector: '.save-indicator' })).toBeInTheDocument();
    expect(api.notes.get('roof-repair')?.body).toBe('v1 body mine\n\nFrom the recording.');
    expect(api.patches.at(-1)).toEqual(expect.objectContaining({ version: 2, status: 200 }));
  });

  it('asks twice before keeping only the edits when the recording’s paragraph cannot be added back', async () => {
    // Another device also rewrote the words, so what the recording added is
    // not separable; the first tap explains, the second acts.
    sessionStorage.removeItem(noteTabStorageKey('roof-repair'));
    const user = userEvent.setup();
    const api = server([ROOF]);
    mount(api.fetchImpl, '/notes/roof-repair');
    const body = await screen.findByRole('textbox', { name: 'Note body' });
    await waitFor(() => {
      expect(body).toHaveValue('v1 body');
    });

    api.notes.set('roof-repair', {
      ...ROOF,
      body: 'Rewritten, then a recording.',
      snippet: 'Rewritten, then a recording.',
      version: 2,
      captures: [{ ...FILED, id: 'cap-new', created_at: new Date().toISOString() }],
    });
    await user.type(body, ' mine');
    await user.tab();

    expect(await screen.findByText(/a recording was filed into this note/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /and add the recording/ })).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Keep my edits' }));
    expect(screen.getByText(/tap keep my edits again/i)).toBeInTheDocument();
    expect(api.notes.get('roof-repair')?.body).toBe('Rewritten, then a recording.');

    await user.click(screen.getByRole('button', { name: 'Keep my edits' }));
    await waitFor(() => {
      expect(screen.queryByText(/changed elsewhere/i)).toBeNull();
    });
    expect(await screen.findByText('Saved', { selector: '.save-indicator' })).toBeInTheDocument();
    expect(api.notes.get('roof-repair')?.body).toBe('v1 body mine');
  });
});

describe('a conflict takes the foot of the screen', () => {
  it('closes the Details panel while the banner is up, so its buttons are not covered, and brings it back after', async () => {
    /*
     * QA 2026-09-05 (6): at 1280×800 the foot-anchored Details panel sat over
     * "Use the newer version / Keep my edits". A conflict is resolved before
     * anything else, so the panel steps aside for it.
     */
    // An earlier test may have left this note on its Recordings tab for the session.
    sessionStorage.removeItem(noteTabStorageKey('roof-repair'));
    const user = userEvent.setup();
    const api = server([ROOF]);
    mount(api.fetchImpl, '/notes/roof-repair');
    const body = await screen.findByRole('textbox', { name: 'Note body' });
    await waitFor(() => {
      expect(body).toHaveValue('v1 body');
    });

    await openPanel(user, 'Details');
    expect(screen.getByRole('combobox', { name: 'Transcription language' })).toBeInTheDocument();

    // Another device saved first; this save is answered 409.
    api.notes.set('roof-repair', { ...ROOF, body: 'v2 body', snippet: 'v2 body', version: 2 });
    await user.type(body, ' mine');
    await user.tab();

    expect(await screen.findByText(/changed elsewhere/i)).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: 'Transcription language' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Details' })).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Use the newer version' }));
    await waitFor(() => {
      expect(screen.queryByText(/changed elsewhere/i)).toBeNull();
    });
    expect(screen.getByRole('combobox', { name: 'Transcription language' })).toBeInTheDocument();
  });
});
