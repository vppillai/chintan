import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { CleanedWire, NoteDetailWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NoteDetailScreen } from './NoteDetailScreen.tsx';
import { CLEAN_POLL_MS } from './cleaned.ts';

/**
 * The Cleaned tab against a small server that behaves as the contract says:
 * `POST …/clean` answers 202 and writes the view a beat later, so the only
 * way the screen can show it is by asking for the note again. PATCH records
 * what it was sent, so the toggle and the mode are asserted as requests.
 */

const NOTE: NoteDetailWire = {
  id: 'roof-repair',
  title: 'Roof repair',
  body: 'Ridge tiles on the south slope have slipped. Ellis quoted nine hundred.',
  aliases: [],
  tags: [],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
  captures: [],
  cleaned: null,
  auto_clean: false,
};

const VIEW: CleanedWire = {
  body: '# Roof repair\n\n## Summary\n\n- Ridge tiles have slipped\n- Ellis quoted **nine hundred**',
  mode: 'structured',
  generated_at: new Date(Date.now() - 3 * 60_000).toISOString(),
  stale: false,
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

interface Server {
  patches: Record<string, unknown>[];
  cleans: (Record<string, unknown> | null)[];
  note: NoteDetailWire;
  /** What the "worker" writes after a clean is queued; the test decides. */
  worker: (mode: string | undefined) => CleanedWire;
  gets: number;
}

function server(initial: NoteDetailWire = NOTE): Server {
  const state: Server = {
    patches: [],
    cleans: [],
    note: structuredClone(initial),
    worker: (mode) => ({
      ...VIEW,
      mode: (mode as CleanedWire['mode'] | undefined) ?? 'structured',
      generated_at: new Date().toISOString(),
      stale: false,
    }),
    gets: 0,
  };
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    if (url.pathname.endsWith('/clean') && method === 'POST') {
      const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null;
      state.cleans.push(body);
      const mode = typeof body?.['mode'] === 'string' ? body['mode'] : undefined;
      setTimeout(() => {
        state.note = { ...state.note, cleaned: state.worker(mode) };
      }, 50);
      return json({ status: 'queued' }, 202);
    }
    if (method === 'PATCH') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      state.patches.push(body);
      state.note = {
        ...state.note,
        version: state.note.version + 1,
        ...(typeof body['auto_clean'] === 'boolean' ? { auto_clean: body['auto_clean'] } : {}),
      };
      const { body: _body, captures: _captures, ...row } = state.note;
      return json(row);
    }
    if (url.pathname.endsWith('/v1/settings')) {
      return json({ cleanup_mode: 'faithful', retention_days: 0, theme: 'ink' });
    }
    if (url.pathname.endsWith(`/v1/notes/${state.note.id}`)) {
      state.gets += 1;
      return json(state.note);
    }
    return json({ items: [] });
  });
  const router = createMemoryRouter([{ path: '/notes/:id', Component: NoteDetailScreen }], {
    initialEntries: [`/notes/${initial.id}?tab=cleaned`],
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return state;
}

/** Queries scoped to the panel: the screen has a pull-to-refresh status of its own. */
function panel() {
  return within(screen.getByRole('region', { name: 'Cleaned view' }));
}

/** The rendered view, once the poll has found it. */
async function view(): Promise<HTMLElement> {
  return waitFor(
    () => {
      const body = document.querySelector<HTMLElement>('.cleaned__body');
      if (!body) throw new Error('no cleaned view yet');
      return body;
    },
    { timeout: CLEAN_POLL_MS * 3 },
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('the Cleaned tab', () => {
  it('is empty until asked, then polls for the worker’s answer and renders it read-only', async () => {
    const user = userEvent.setup();
    const api = server();
    await screen.findByRole('tab', { name: 'Cleaned', selected: true });
    expect(screen.getByText('No cleaned view yet')).toBeInTheDocument();
    expect(screen.getByText(/rewritten into headings and lists/i)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Generate' }));
    expect(await panel().findByRole('status')).toHaveTextContent(/rewriting the note/i);
    // The mode the switch shows is the one asked for.
    expect(api.cleans).toEqual([{ mode: 'structured' }]);
    const getsAtQueue = api.gets;

    const rendered = await view();
    // The poll asked again; the 202 carried nothing to render.
    expect(api.gets).toBeGreaterThan(getsAtQueue);
    expect(within(rendered).getByRole('heading', { level: 2, name: 'Roof repair' })).toBeInTheDocument();
    expect(within(rendered).getByRole('heading', { level: 3, name: 'Summary' })).toBeInTheDocument();
    expect(within(rendered).getAllByRole('listitem').map((item) => item.textContent)).toEqual([
      'Ridge tiles have slipped',
      'Ellis quoted nine hundred',
    ]);
    expect(within(rendered).getByText('nine hundred').tagName).toBe('STRONG');
    expect(within(rendered).queryByRole('textbox')).toBeNull();
    expect(screen.getByText(/generated just now · structured/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Regenerate' })).toBeEnabled();
    expect(panel().queryByRole('status')).toBeNull();
  }, 10_000);

  it('says when the note has changed since, and Regenerate now brings it up to date', async () => {
    const user = userEvent.setup();
    const api = server({ ...NOTE, cleaned: { ...VIEW, stale: true } });

    expect(await screen.findByText(/generated 3 minutes ago · structured/i)).toBeInTheDocument();
    const notice = panel().getByRole('status');
    expect(notice).toHaveTextContent(/the note changed since this was generated/i);

    await user.click(within(notice).getByRole('button', { name: 'Regenerate now' }));
    expect(api.cleans).toEqual([{ mode: 'structured' }]);
    expect(screen.getByRole('button', { name: 'Regenerating…' })).toBeDisabled();

    await waitFor(
      () => {
        expect(screen.queryByText(/the note changed since/i)).toBeNull();
        expect(screen.getByText(/generated just now/i)).toBeInTheDocument();
      },
      { timeout: CLEAN_POLL_MS * 3 },
    );
    expect(screen.getByRole('button', { name: 'Regenerate' })).toBeEnabled();
  }, 10_000);

  it('switches mode by regenerating in it, and records the choice on the note', async () => {
    const user = userEvent.setup();
    const api = server({ ...NOTE, cleaned: VIEW });
    await screen.findByText(/generated 3 minutes ago · structured/i);
    expect(screen.getByRole('button', { name: 'Structured' })).toHaveAttribute('aria-pressed', 'true');

    await user.click(screen.getByRole('button', { name: 'Polished' }));

    expect(api.cleans).toEqual([{ mode: 'polished' }]);
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    // The note's own serialised PATCH, with its version and only the fields
    // that changed among the new ones.
    expect(api.patches[0]).toEqual(
      expect.objectContaining({ version: NOTE.version, cleaned_mode: 'polished' }),
    );
    expect(api.patches[0]).not.toHaveProperty('auto_clean');

    expect(await screen.findByText(/generated just now · polished/i, {}, { timeout: CLEAN_POLL_MS * 3 })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Polished' })).toHaveAttribute('aria-pressed', 'true');
  }, 10_000);

  it('the toggle writes auto_clean through the note’s PATCH, once, and stays as set', async () => {
    const user = userEvent.setup();
    const api = server({ ...NOTE, cleaned: VIEW });
    const toggle = await screen.findByRole('checkbox', { name: /keep it updated after each recording/i });
    expect(toggle).not.toBeChecked();

    await user.click(toggle);
    expect(toggle).toBeChecked();
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(expect.objectContaining({ version: NOTE.version, auto_clean: true }));
    expect(api.patches[0]).not.toHaveProperty('cleaned_mode');
    expect(toggle).toBeChecked();

    // An ordinary edit afterwards does not carry the cleaned-view fields: a
    // backend that has not learnt them yet refuses a PATCH that names them.
    await user.click(screen.getByRole('tab', { name: 'Text' }));
    const body = await screen.findByRole('textbox', { name: 'Note body' });
    await user.type(body, ' More.');
    await user.tab();
    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches[1]).not.toHaveProperty('auto_clean');
    expect(api.patches[1]).not.toHaveProperty('cleaned_mode');
    expect(api.patches[1]?.['version']).toBe(NOTE.version + 1);
  });

  it('stops early and says so when the backend reports an error on the view', async () => {
    const user = userEvent.setup();
    const api = server({ ...NOTE, cleaned: VIEW });
    api.worker = () => ({ ...VIEW, error: 'The provider refused the request.' });
    await screen.findByText(/generated 3 minutes ago/i);

    await user.click(screen.getByRole('button', { name: 'Regenerate' }));
    expect(await screen.findByRole('alert', {}, { timeout: CLEAN_POLL_MS * 3 })).toHaveTextContent(
      'The provider refused the request.',
    );
    expect(screen.getByRole('button', { name: 'Regenerate' })).toBeEnabled();
    const gets = api.gets;
    await act(() => new Promise((resolve) => setTimeout(resolve, CLEAN_POLL_MS + 200)));
    expect(api.gets).toBe(gets);
  }, 10_000);

  it('offers the view in Share only when there is one', async () => {
    const user = userEvent.setup();
    server({ ...NOTE, cleaned: VIEW });
    await screen.findByText(/generated 3 minutes ago/i);

    await user.click(screen.getByRole('button', { name: 'Share' }));
    expect(screen.getByRole('button', { name: 'Copy note' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy cleaned view' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Download cleaned view' })).toBeInTheDocument();
  });

  it('offers no cleaned copy when there is nothing to copy', async () => {
    const user = userEvent.setup();
    server();
    await screen.findByText('No cleaned view yet');
    await user.click(screen.getByRole('button', { name: 'Share' }));
    expect(screen.getByRole('button', { name: 'Copy note' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy cleaned view' })).toBeNull();
  });
});
