import { QueryClient } from '@tanstack/react-query';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { CAPTURE_POLL_FAST_MS, queryKeys } from '@/api/queries.ts';
import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';
import { routes } from '@/app/router.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

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
    await screen.findByRole('heading', { name: 'Notes' });
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
  });
});
