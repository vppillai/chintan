import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { NoteDetailWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NoteDetailScreen } from './NoteDetailScreen.tsx';

/**
 * The note screen for a checklist, against a small server that stores what
 * PATCH sends. What these prove is the contract's frontend half: the tabs are
 * Items and Recordings — a checklist has no Cleaned tab since 2026-09-27, its
 * recordings became items as they were filed — the meta line counts items,
 * and the Details switch converts the body and sends `kind` with it.
 */

const SHOPPING: NoteDetailWire = {
  id: 'shopping',
  kind: 'checklist',
  title: 'Shopping',
  body: '- [ ] Milk\n- [x] Eggs\n- [ ] Bread',
  aliases: [],
  tags: [],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
  captures: [],
  cleaned: null,
  auto_clean: false,
};

const ROOF: NoteDetailWire = {
  id: 'roof-repair',
  kind: 'note',
  title: 'Roof repair',
  body: 'Ridge tiles have slipped.\n\nGet two\nquotes.',
  aliases: [],
  tags: [],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 1,
  archived: false,
  captures: [],
  cleaned: null,
  auto_clean: false,
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function server(initial: NoteDetailWire, path = `/notes/${initial.id}`) {
  const state = {
    note: structuredClone(initial),
    patches: [] as Record<string, unknown>[],
  };
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    if (method === 'PATCH') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      state.patches.push(body);
      state.note = {
        ...state.note,
        version: state.note.version + 1,
        ...(typeof body['body'] === 'string' ? { body: body['body'] } : {}),
        ...(body['kind'] === 'note' || body['kind'] === 'checklist' ? { kind: body['kind'] } : {}),
      };
      const { body: _body, captures: _captures, ...row } = state.note;
      return json(row);
    }
    if (url.pathname.endsWith('/v1/settings')) {
      return json({ retention_days: 0, theme: 'ink' });
    }
    if (url.pathname.endsWith(`/v1/notes/${state.note.id}`)) return json(state.note);
    return json({ items: [] });
  });
  const router = createMemoryRouter([{ path: '/notes/:id', Component: NoteDetailScreen }], {
    initialEntries: [path],
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return { ...state, router, get patches() { return state.patches; } };
}

function tabNames(): string[] {
  return within(screen.getByRole('tablist', { name: 'Note views' }))
    .getAllByRole('tab')
    .map((tab) => tab.textContent ?? '');
}

describe('a checklist note', () => {
  it('has Items and Recordings for tabs, the editor for a body, and the count in the meta line', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING);
    await screen.findByRole('textbox', { name: 'Item 1' });
    expect(tabNames()).toEqual(['Items', 'Recordings (0)']);
    expect(screen.queryByRole('textbox', { name: 'Note body' })).toBeNull();
    expect(screen.getByText(/1 of 3 done/)).toBeInTheDocument();
    expect(screen.queryByText(/\d+ words/)).toBeNull();

    await user.click(screen.getByRole('checkbox', { name: 'Milk' }));
    expect(screen.getByText(/2 of 3 done/)).toBeInTheDocument();
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    // The item's own line flipped, the order kept; the kind did not change,
    // so it is not sent.
    expect(api.patches[0]).toEqual(
      expect.objectContaining({ version: 3, body: '- [x] Milk\n- [x] Eggs\n- [ ] Bread' }),
    );
    expect(api.patches[0]).not.toHaveProperty('kind');
  });

  it('the Details switch converts prose to items and back, sending kind with the body', async () => {
    const user = userEvent.setup();
    const api = server(ROOF);
    await screen.findByRole('textbox', { name: 'Note body' });
    expect(tabNames()[0]).toBe('Text');

    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Details' }));
    const toggle = screen.getByRole('checkbox', { name: 'This note is a checklist' });
    expect(toggle).not.toBeChecked();
    await user.click(toggle);

    // The screen is a checklist's at once, before the save lands, and the
    // Cleaned tab is gone with the prose.
    expect(tabNames()).toEqual(['Items', 'Recordings (0)']);
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toHaveValue('Ridge tiles have slipped.');
    expect(screen.getByRole('textbox', { name: 'Item 2' })).toHaveValue('Get two quotes.');
    expect(screen.getByText(/0 of 2 done/)).toBeInTheDocument();
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(
      expect.objectContaining({
        kind: 'checklist',
        body: '- [ ] Ridge tiles have slipped.\n- [ ] Get two quotes.',
      }),
    );

    await user.click(screen.getByRole('checkbox', { name: 'This note is a checklist' }));
    expect(tabNames()).toEqual(['Text', 'Cleaned', 'Recordings (0)']);
    expect(await screen.findByRole('textbox', { name: 'Note body' })).toHaveValue(
      'Ridge tiles have slipped.\n\nGet two quotes.',
    );
    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches[1]).toEqual(
      expect.objectContaining({ kind: 'note', body: 'Ridge tiles have slipped.\n\nGet two quotes.' }),
    );
  });

  // A link or a remembered tab that names Cleaned opens a checklist on its
  // items: the tab does not exist for one, and a view stored by the deleted
  // tasks mode is not the note's.
  it('opens a checklist linked to ?tab=cleaned on its items', async () => {
    server(SHOPPING, '/notes/shopping?tab=cleaned');
    await screen.findByRole('textbox', { name: 'Item 1' });
    expect(screen.getByRole('tab', { name: 'Items' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByRole('tab', { name: /split up|cleaned/i })).toBeNull();
  });
});
