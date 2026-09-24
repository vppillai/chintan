import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { CleanedWire, NoteDetailWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NoteDetailScreen } from './NoteDetailScreen.tsx';
import { CLEAN_POLL_MS } from './cleaned.ts';

/**
 * The note screen for a checklist, against a small server that stores what
 * PATCH sends and answers `POST …/clean` with 202 and a `tasks` view a beat
 * later. What these prove is the 2026-09-21 contract's frontend half: the
 * tabs are Items and Split up, the meta line counts items, the Details switch
 * converts the body and sends `kind` with it, and the Split up tab has no
 * mode to pick.
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

/** What the cleanup model writes for the shopping list in `tasks` mode. */
const SPLIT: CleanedWire = {
  body: '- [ ] Milk\n- [x] Eggs\n- [ ] Bread\n- [ ] Butter',
  mode: 'tasks',
  generated_at: '2026-08-06T09:20:00.000Z',
  stale: false,
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function server(initial: NoteDetailWire) {
  const state = {
    note: structuredClone(initial),
    patches: [] as Record<string, unknown>[],
    cleans: [] as (Record<string, unknown> | null)[],
  };
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    if (url.pathname.endsWith('/clean') && method === 'POST') {
      state.cleans.push(init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null);
      setTimeout(() => {
        state.note = { ...state.note, cleaned: { ...SPLIT, generated_at: new Date().toISOString() } };
      }, 50);
      return json({ status: 'queued', mode: 'tasks' }, 202);
    }
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
      return json({ cleanup_mode: 'faithful', retention_days: 0, theme: 'ink' });
    }
    if (url.pathname.endsWith(`/v1/notes/${state.note.id}`)) return json(state.note);
    return json({ items: [] });
  });
  const router = createMemoryRouter([{ path: '/notes/:id', Component: NoteDetailScreen }], {
    initialEntries: [`/notes/${initial.id}`],
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return { ...state, router, get patches() { return state.patches; }, get cleans() { return state.cleans; } };
}

function tabNames(): string[] {
  return within(screen.getByRole('tablist', { name: 'Note views' }))
    .getAllByRole('tab')
    .map((tab) => tab.textContent ?? '');
}

describe('a checklist note', () => {
  it('has Items and Split up for tabs, the editor for a body, and the count in the meta line', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING);
    await screen.findByRole('textbox', { name: 'Item 1' });
    expect(tabNames()).toEqual(['Items', 'Split up', 'Recordings (0)']);
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

    // The screen is a checklist's at once, before the save lands.
    expect(tabNames().slice(0, 2)).toEqual(['Items', 'Split up']);
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
    expect(tabNames()[0]).toBe('Text');
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

  it('Split up has no mode to pick, and its list is live: the first tick adopts the split list, the next edits it', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING);
    await screen.findByRole('textbox', { name: 'Item 1' });
    await user.click(screen.getByRole('tab', { name: 'Split up' }));

    const panel = () => within(screen.getByRole('region', { name: 'Split up' }));
    expect(panel().queryByRole('group', { name: 'Cleaned view mode' })).toBeNull();
    expect(panel().getByText('Not split up yet')).toBeInTheDocument();
    expect(panel().getByText(/one task per action/i)).toBeInTheDocument();
    // The auto-refresh switch stays.
    expect(panel().getByRole('checkbox', { name: /keep it updated/i })).toBeInTheDocument();

    await user.click(panel().getByRole('button', { name: 'Generate' }));
    // No mode named: the server applies `tasks` itself.
    expect(api.cleans).toEqual([null]);

    const preview = () => within(panel().getByRole('list', { name: 'Split up items' }));
    await waitFor(() => panel().getByRole('list', { name: 'Split up items' }), {
      timeout: CLEAN_POLL_MS * 3,
    });
    const boxes = preview().getAllByRole('checkbox');
    expect(boxes.map((box) => (box as HTMLInputElement).checked)).toEqual([false, true, false, false]);
    for (const box of boxes) expect(box).toBeEnabled();
    expect(preview().queryByRole('textbox')).toBeNull();
    expect(panel().getByText(/^Generated .* · Split up$/)).toBeInTheDocument();
    const caption = 'Ticking here replaces your list with the split version.';
    expect(panel().getByText(caption)).toBeInTheDocument();
    expect(panel().getByRole('button', { name: 'Use this list' })).toBeInTheDocument();

    // The first tick: the body becomes the split list with Butter done, in
    // one save; the caption has done its job.
    await user.click(preview().getByRole('checkbox', { name: 'Butter' }));
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(
      expect.objectContaining({ body: '- [ ] Milk\n- [x] Eggs\n- [ ] Bread\n- [x] Butter' }),
    );
    expect(api.patches[0]).not.toHaveProperty('cleaned_mode');
    expect(preview().getByRole('checkbox', { name: 'Butter' })).toBeChecked();
    expect(panel().queryByText(caption)).toBeNull();

    // The next act edits the body it made — Eggs reopened, Butter kept done —
    // rather than replacing it with the proposal again.
    await user.click(preview().getByRole('checkbox', { name: 'Eggs' }));
    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches[1]).toEqual(
      expect.objectContaining({ body: '- [ ] Milk\n- [ ] Eggs\n- [ ] Bread\n- [x] Butter' }),
    );

    // A done item's × deletes it, as under Done.
    await user.click(preview().getByRole('button', { name: 'Delete Butter' }));
    await waitFor(() => {
      expect(api.patches).toHaveLength(3);
    });
    expect(api.patches[2]).toEqual(expect.objectContaining({ body: '- [ ] Milk\n- [ ] Eggs\n- [ ] Bread' }));

    // Away and back — the panel is remounted — the tab still shows the body,
    // not the proposal, and Use this list and Regenerate are still there.
    await user.click(screen.getByRole('tab', { name: 'Items' }));
    expect(screen.getByRole('textbox', { name: 'Item 3' })).toHaveValue('Bread');
    expect(screen.queryByRole('checkbox', { name: 'Butter' })).toBeNull();
    await user.click(screen.getByRole('tab', { name: 'Split up' }));
    expect(preview().getAllByRole('checkbox')).toHaveLength(3);
    expect(panel().queryByText(caption)).toBeNull();
    expect(panel().getByRole('button', { name: 'Use this list' })).toBeInTheDocument();
    expect(panel().getByRole('button', { name: 'Regenerate' })).toBeInTheDocument();
  });
});
