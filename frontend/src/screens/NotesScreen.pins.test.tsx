import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';
import { setCanHover } from '@/test/setup.ts';

import { NotesScreen } from './NotesScreen.tsx';

/**
 * Pinned notes on Home (2026-09-24 contract, B): the group above the days,
 * Pin and Unpin on the row, and the drag that orders the group — shown at
 * once, sent once, and put back when the server refuses.
 */

const THREE: NoteWire[] = ['One', 'Two', 'Three'].map((title, index) => ({
  ...TEST_NOTES[0]!,
  id: title.toLowerCase(),
  title,
  updated_at: new Date(Date.UTC(2026, 7, 10 - index)).toISOString(),
  pinned: index < 2,
  pin_rank: index < 2 ? index * 1000 : null,
}));

/**
 * A stateful stub of the notes API: the list sorts as the server does, a
 * PATCH pins or unpins, and `POST /v1/notes/pins` re-ranks — or refuses,
 * when told to.
 */
function server(notes: NoteWire[], { refuseReorder = false } = {}) {
  const state = new Map(notes.map((note) => [note.id, { ...note }]));
  const posts: string[][] = [];
  const patches: { id: string; body: Record<string, unknown> }[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), {
        status,
        headers: { 'content-type': status === 200 ? 'application/json' : 'application/problem+json' },
      });
    if (url.pathname.endsWith('/v1/notes/pins') && method === 'POST') {
      const { ids } = JSON.parse(String(init?.body)) as { ids: string[] };
      posts.push(ids);
      // The contract's refusal, a 400: a 5xx would be retried by the client first.
      if (refuseReorder) {
        return json({ title: 'That request was not valid', status: 400, detail: 'every id must be one of your pinned notes' }, 400);
      }
      ids.forEach((id, index) => {
        const note = state.get(id);
        if (note) note.pin_rank = index * 1000;
      });
      return json({ items: ids.map((id) => state.get(id)) });
    }
    const one = /\/v1\/notes\/([^/]+)$/.exec(url.pathname);
    if (one && method === 'PATCH') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const note = state.get(one[1]!)!;
      patches.push({ id: note.id, body });
      if (typeof body['pinned'] === 'boolean') {
        note.pinned = body['pinned'];
        note.pin_rank = body['pinned'] ? 5000 : null;
      }
      note.version += 1;
      return json(note);
    }
    if (url.pathname.endsWith('/v1/notes')) {
      const items = [...state.values()]
        .filter((note) => note.archived === (url.searchParams.get('state') === 'archived'))
        .sort(
          (a, b) =>
            Number(Boolean(b.pinned)) - Number(Boolean(a.pinned)) ||
            (a.pin_rank ?? 0) - (b.pin_rank ?? 0) ||
            b.updated_at.localeCompare(a.updated_at),
        );
      return json({ items });
    }
    return json({ items: [] });
  });
  return { fetchImpl, posts, patches, state };
}

function mount(fetchImpl: typeof fetch) {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter>
        <NotesScreen />
      </MemoryRouter>
    </TestProviders>,
  );
}

function pinnedTitles(): string[] {
  const group = screen.getByRole('region', { name: 'Pinned' });
  return Array.from(group.querySelectorAll('.note-row__title')).map((el) => el.textContent ?? '');
}

/** The row's ⋮, found by the title that describes it. */
function moreFor(title: string): HTMLElement {
  return screen.getByRole('button', { name: 'More', description: title });
}

/*
 * The row's accessible name runs the title, the time and the snippet together
 * ("OneAug 9Ridge tiles…"), so it is matched by its start alone; no title here
 * is a prefix of another.
 */
function row(title: string): HTMLElement {
  return screen.getByRole('button', { name: new RegExp(`^${title}`) });
}

const mouse = { pointerType: 'mouse', button: 0, pointerId: 1 };

describe('pinned notes on Home', () => {
  it('lists the pinned notes first, in rank order, in a group of their own', async () => {
    mount(server(THREE).fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });

    const labels = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent);
    expect(labels[0]).toBe('Pinned');
    expect(pinnedTitles()).toEqual(['One', 'Two']);
    // Not in the day groups as well.
    expect(screen.getAllByRole('button', { name: /^One/ })).toHaveLength(1);
    // The glyph says which rows are pinned.
    expect(row('One')).toHaveAttribute('data-pinned');
    expect(row('One').querySelector('.note-row__pin')).not.toBeNull();
    expect(row('Three').querySelector('.note-row__pin')).toBeNull();
    expect(row('Three')).not.toHaveAttribute('data-pinned');
  });

  it('Pin on the row’s ⋮ sends the PATCH and moves the row into the group; Unpin moves it out', async () => {
    const user = userEvent.setup();
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });

    await user.click(moreFor('Three'));
    await user.click(screen.getByRole('menuitem', { name: 'Pin' }));

    await waitFor(() => {
      expect(pinnedTitles()).toEqual(['One', 'Two', 'Three']);
    });
    expect(api.patches).toEqual([{ id: 'three', body: { version: 3, pinned: true } }]);

    await user.click(moreFor('One'));
    await user.click(screen.getByRole('menuitem', { name: 'Unpin' }));
    await waitFor(() => {
      expect(pinnedTitles()).toEqual(['Two', 'Three']);
    });
  });

  it('a mouse drag on the grip reorders the group and posts the order once', async () => {
    setCanHover(true);
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });
    const list = screen.getByRole('region', { name: 'Pinned' }).querySelector('.pin-list')!;

    // jsdom lays nothing out — every row's midpoint is 0 — so a move to a
    // positive y is "below every row" and a negative one "above every row".
    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move One' }), {
      ...mouse,
      clientX: 10,
      clientY: 0,
    });
    fireEvent.pointerMove(list, { ...mouse, clientX: 10, clientY: 40 });
    expect(pinnedTitles()).toEqual(['Two', 'One']);
    fireEvent.pointerUp(list, { ...mouse, clientX: 10, clientY: 40 });

    await waitFor(() => {
      expect(api.posts).toEqual([['two', 'one']]);
    });
    // Still in the dragged order once the server has answered and the list refetched.
    await waitFor(() => {
      expect(api.state.get('two')?.pin_rank).toBe(0);
    });
    expect(pinnedTitles()).toEqual(['Two', 'One']);
    // The click the browser fires as the mouse lifts does not open the note.
    fireEvent.click(row('One'));
    expect(screen.getByRole('region', { name: 'Pinned' })).toBeInTheDocument();
  });

  it('puts the order back when the server refuses the reorder', async () => {
    setCanHover(true);
    const api = server(THREE, { refuseReorder: true });
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });
    const list = screen.getByRole('region', { name: 'Pinned' }).querySelector('.pin-list')!;

    fireEvent.pointerDown(screen.getByRole('button', { name: 'Move One' }), {
      ...mouse,
      clientX: 10,
      clientY: 0,
    });
    fireEvent.pointerMove(list, { ...mouse, clientX: 10, clientY: 40 });
    fireEvent.pointerUp(list, { ...mouse, clientX: 10, clientY: 40 });

    await waitFor(() => {
      expect(api.posts).toHaveLength(1);
    });
    await waitFor(() => {
      expect(pinnedTitles()).toEqual(['One', 'Two']);
    });
  });

  it('the arrow keys on a grip move the row one step', async () => {
    setCanHover(true);
    const user = userEvent.setup();
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });

    const grip = screen.getByRole('button', { name: 'Move One' });
    expect(grip).toHaveAccessibleDescription(/arrow keys/);
    grip.focus();
    await user.keyboard('{ArrowDown}');
    await waitFor(() => {
      expect(api.posts).toEqual([['two', 'one']]);
    });
    expect(pinnedTitles()).toEqual(['Two', 'One']);
  });

  it('on a phone, holding a pinned row lifts it rather than selecting it, and dragging reorders', async () => {
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });
    const list = screen.getByRole('region', { name: 'Pinned' }).querySelector('.pin-list')!;
    const touch = { pointerType: 'touch', button: 0, pointerId: 2 };

    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.pointerDown(row('One'), {
        ...touch,
        clientX: 10,
        clientY: 0,
      });
      vi.advanceTimersByTime(600);
    } finally {
      vi.useRealTimers();
    }
    // Lifted, not selected.
    expect(screen.queryByRole('toolbar')).toBeNull();
    fireEvent.pointerMove(list, { ...touch, clientX: 10, clientY: 40 });
    expect(pinnedTitles()).toEqual(['Two', 'One']);
    fireEvent.pointerUp(list, { ...touch, clientX: 10, clientY: 40 });

    await waitFor(() => {
      expect(api.posts).toEqual([['two', 'one']]);
    });
  });
});
