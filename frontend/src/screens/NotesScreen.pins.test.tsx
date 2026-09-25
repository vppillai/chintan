import { onlineManager } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

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
 * when told to. After a refusal (or, with `holdListsAfterPatch`, after any
 * PATCH) every list GET waits for `release()`, so what the screen shows in
 * the meantime is the mutation's doing, not a refetch's. A refused pin waits
 * for `answerPatch()` before its 409, so the optimistic state can be seen.
 */
function server(
  notes: NoteWire[],
  { refuseReorder = false, refusePin = false, holdListsAfterPatch = false } = {},
) {
  const state = new Map(notes.map((note) => [note.id, { ...note }]));
  const posts: string[][] = [];
  const patches: { id: string; body: Record<string, unknown> }[] = [];
  let held: Promise<void> | null = null;
  let release = (): void => {};
  let answerPatch = (): void => {};
  /** List GETs that arrived while the lists were held, i.e. after the answer that held them. */
  let heldGets = 0;
  const hold = (): void => {
    held = new Promise((resolve) => {
      release = resolve;
    });
  };
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
        hold();
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
      if (refusePin) {
        await new Promise<void>((resolve) => {
          answerPatch = resolve;
        });
        hold();
        return json(
          { title: 'Someone else changed this first', status: 409, current_version: note.version + 1 },
          409,
        );
      }
      if (typeof body['pinned'] === 'boolean') {
        note.pinned = body['pinned'];
        note.pin_rank = body['pinned'] ? 5000 : null;
      }
      note.version += 1;
      if (holdListsAfterPatch) hold();
      return json(note);
    }
    if (url.pathname.endsWith('/v1/notes')) {
      if (held) {
        heldGets += 1;
        await held;
      }
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
  return {
    fetchImpl,
    posts,
    patches,
    state,
    release: () => release(),
    answerPatch: () => answerPatch(),
    heldGets: () => heldGets,
  };
}

function mount(fetchImpl: typeof fetch, path = '/') {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter initialEntries={[path]}>
        <NotesScreen />
      </MemoryRouter>
    </TestProviders>,
  );
}

/** The network goes after mount: `useOnline` hears the event and re-reads `navigator.onLine`. */
function goOffline(): void {
  Object.defineProperty(navigator, 'onLine', { value: false, configurable: true });
  onlineManager.setOnline(false);
  act(() => {
    window.dispatchEvent(new Event('offline'));
  });
}

afterEach(() => {
  Object.defineProperty(navigator, 'onLine', { value: true, configurable: true });
  onlineManager.setOnline(true);
});

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
    // The list GET the refusal triggers is still held, so only the rollback
    // (`restoreNoteLists`) can have put the rows back.
    await waitFor(() => {
      expect(pinnedTitles()).toEqual(['One', 'Two']);
    });
    api.release();
  });

  it('the arrow keys on a grip move the row one step', async () => {
    setCanHover(true);
    const user = userEvent.setup();
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });

    const grip = screen.getByRole('button', { name: 'Move One' });
    expect(grip).toHaveAccessibleDescription(/arrow keys/);
    // Before the row in the DOM, as on the screen, so Tab runs grip → row → ⋮.
    expect(grip.compareDocumentPosition(row('One')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
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

  it('on a touchscreen laptop, holding a pinned row selects it and does not also lift it', async () => {
    // A fine pointer is present, so the row arms its own hold (`holdToSelect`)
    // and the list must not arm a second one for the same finger.
    setCanHover(true);
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });
    const list = screen.getByRole('region', { name: 'Pinned' }).querySelector('.pin-list')!;
    const touch = { pointerType: 'touch', button: 0, pointerId: 2 };

    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.pointerDown(row('One'), { ...touch, clientX: 10, clientY: 0 });
      act(() => {
        vi.advanceTimersByTime(600);
      });
    } finally {
      vi.useRealTimers();
    }

    await screen.findByRole('toolbar');
    expect(list).not.toHaveAttribute('data-dragging');
    expect(pinnedTitles()).toEqual(['One', 'Two']);
    fireEvent.pointerMove(list, { ...touch, clientX: 10, clientY: 40 });
    expect(pinnedTitles()).toEqual(['One', 'Two']);
    fireEvent.pointerUp(list, { ...touch, clientX: 10, clientY: 40 });
    expect(api.posts).toEqual([]);
  });

  it('on a phone, a slow press on the row’s ⋮ opens the menu rather than lifting the row', async () => {
    const user = userEvent.setup();
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });
    const list = screen.getByRole('region', { name: 'Pinned' }).querySelector('.pin-list')!;
    const touch = { pointerType: 'touch', button: 0, pointerId: 2 };

    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.pointerDown(moreFor('One'), { ...touch, clientX: 300, clientY: 0 });
      act(() => {
        vi.advanceTimersByTime(600);
      });
    } finally {
      vi.useRealTimers();
    }
    expect(list).not.toHaveAttribute('data-dragging');
    fireEvent.pointerUp(moreFor('One'), { ...touch, clientX: 300, clientY: 0 });

    // The click the finger's lift fires is the menu's, not one the list swallows.
    await user.click(moreFor('One'));
    expect(screen.getByRole('menuitem', { name: 'Unpin' })).toBeInTheDocument();
    expect(pinnedTitles()).toEqual(['One', 'Two']);
  });

  it('Move up and Move down in a pinned row’s ⋮ step the row without a drag', async () => {
    // The gesture-free path (WCAG 2.5.7): on a phone nothing says a row can be
    // held and dragged, and a switch or a screen reader cannot drag at all.
    const user = userEvent.setup();
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });

    // The top row cannot move up; the bottom one cannot move down.
    await user.click(moreFor('One'));
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Unpin',
      'Archive',
      'Delete',
      'Select',
      'Move down',
    ]);
    await user.keyboard('{Escape}');
    await user.click(moreFor('Two'));
    expect(screen.queryByRole('menuitem', { name: 'Move down' })).toBeNull();
    await user.click(screen.getByRole('menuitem', { name: 'Move up' }));

    await waitFor(() => {
      expect(api.posts).toEqual([['two', 'one']]);
    });
    expect(pinnedTitles()).toEqual(['Two', 'One']);
    // An unpinned row has no order to move in.
    await user.click(moreFor('Three'));
    expect(screen.queryByRole('menuitem', { name: /^Move/ })).toBeNull();
  });

  it('under a tag chip the group is a subset, so no grip and no Move items are offered', async () => {
    // The server ranks exactly the ids it is sent, from 0, so an order made
    // over a subset would hoist the visible rows above every pinned note the
    // filter hid (review 2026-09-24, R4-2).
    setCanHover(true);
    const user = userEvent.setup();
    const api = server(THREE);
    mount(api.fetchImpl, '/?tag=house');
    await screen.findByRole('button', { name: /^Three/ });
    expect(pinnedTitles()).toEqual(['One', 'Two']);

    expect(screen.queryByRole('button', { name: 'Move One' })).toBeNull();
    await user.click(moreFor('Two'));
    expect(screen.queryByRole('menuitem', { name: /^Move/ })).toBeNull();
  });

  it('under a tag chip on a phone, holding a pinned row selects it and lifts nothing', async () => {
    const api = server(THREE);
    mount(api.fetchImpl, '/?tag=house');
    await screen.findByRole('button', { name: /^Three/ });
    const list = screen.getByRole('region', { name: 'Pinned' }).querySelector('.pin-list')!;
    const touch = { pointerType: 'touch', button: 0, pointerId: 2 };

    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.pointerDown(row('One'), { ...touch, clientX: 10, clientY: 0 });
      act(() => {
        vi.advanceTimersByTime(600);
      });
    } finally {
      vi.useRealTimers();
    }

    await screen.findByRole('toolbar');
    expect(list).not.toHaveAttribute('data-dragging');
    fireEvent.pointerMove(list, { ...touch, clientX: 10, clientY: 40 });
    fireEvent.pointerUp(list, { ...touch, clientX: 10, clientY: 40 });
    expect(pinnedTitles()).toEqual(['One', 'Two']);
    expect(api.posts).toEqual([]);
  });

  it('a pin the server refuses is undone: the row leaves the Pinned group', async () => {
    const user = userEvent.setup();
    const api = server(THREE, { refusePin: true });
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });

    await user.click(moreFor('Three'));
    await user.click(screen.getByRole('menuitem', { name: 'Pin' }));
    // Shown at once, while the PATCH is in the air...
    await waitFor(() => {
      expect(pinnedTitles()).toEqual(['One', 'Two', 'Three']);
    });
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    api.answerPatch();
    // ...and put back by the rollback: the list GET the refusal triggers is
    // held, so nothing else can have moved the row.
    await waitFor(() => {
      expect(pinnedTitles()).toEqual(['One', 'Two']);
    });
    api.release();
  });

  it('writes the pin’s answer back, so an Unpin before the refetch carries the new version', async () => {
    // Every pin bumps the version; "Pin, reopen ⋮, Unpin" used to send the old
    // one and be refused 409 with the note left pinned (review 2026-09-24, R4-3).
    const user = userEvent.setup();
    const api = server(THREE, { holdListsAfterPatch: true });
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });

    await user.click(moreFor('Three'));
    await user.click(screen.getByRole('menuitem', { name: 'Pin' }));
    // The refetch the settled PATCH asks for is the sign its answer has been
    // written back; that GET is held, so the version can only be the PATCH's.
    await waitFor(() => {
      expect(api.heldGets()).toBeGreaterThan(0);
    });
    await user.click(moreFor('Three'));
    await user.click(screen.getByRole('menuitem', { name: 'Unpin' }));
    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches.map((patch) => patch.body['version'])).toEqual([3, 4]);
    api.release();
  });

  it('offline, Pin and the grip wait for the network', async () => {
    // A pin made offline sits paused and fires later with whatever version the
    // cache held, while the row does not move, so the tap looks lost (R4-11).
    setCanHover(true);
    const user = userEvent.setup();
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });
    expect(screen.getByRole('button', { name: 'Move One' })).toBeEnabled();

    goOffline();
    expect(screen.getByRole('button', { name: 'Move One' })).toBeDisabled();
    await user.click(moreFor('Three'));
    expect(screen.getByRole('menuitem', { name: 'Pin' })).toBeDisabled();
    expect(screen.getByRole('menuitem', { name: 'Archive' })).toBeEnabled();
    await user.keyboard('{Escape}');
    await user.click(moreFor('One'));
    expect(screen.getByRole('menuitem', { name: 'Unpin' })).toBeDisabled();
    expect(screen.getByRole('menuitem', { name: 'Move down' })).toBeDisabled();
  });

  it('offline on a phone, holding a pinned row selects it instead of lifting it', async () => {
    const api = server(THREE);
    mount(api.fetchImpl);
    await screen.findByRole('button', { name: /^Three/ });
    goOffline();
    const list = screen.getByRole('region', { name: 'Pinned' }).querySelector('.pin-list')!;
    const touch = { pointerType: 'touch', button: 0, pointerId: 2 };

    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.pointerDown(row('One'), { ...touch, clientX: 10, clientY: 0 });
      act(() => {
        vi.advanceTimersByTime(600);
      });
    } finally {
      vi.useRealTimers();
    }

    await screen.findByRole('toolbar');
    expect(list).not.toHaveAttribute('data-dragging');
    fireEvent.pointerMove(list, { ...touch, clientX: 10, clientY: 40 });
    fireEvent.pointerUp(list, { ...touch, clientX: 10, clientY: 40 });
    expect(pinnedTitles()).toEqual(['One', 'Two']);
    expect(api.posts).toEqual([]);
  });
});
