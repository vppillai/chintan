import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';
import { setCanHover } from '@/test/setup.ts';

import { NoteRow } from './NoteRow.tsx';
import { StatusRegion } from './StatusRegion.tsx';
import { Toast, dismissToast } from './Toast.tsx';

/**
 * The swipe tray behind a note row: the right actions for the view the row
 * is in — pin on the tap, Delete (the archive, with Undo in the toast) behind
 * "Delete “<title>”?" (owner, 2026-09-27: "ask are you sure"), Delete forever
 * behind its own plain confirm in the archive. The gesture itself is
 * SwipeRow's test. And the ⋮ at the row's right, which offers the same
 * actions to a pointer that cannot swipe (2026-09-24, C).
 */

const ACTIVE: NoteWire = TEST_NOTES[0] as NoteWire;
const ARCHIVED: NoteWire = {
  ...ACTIVE,
  archived: true,
  purge_after: new Date(Date.now() + 10 * 86_400_000).toISOString(),
};

function mount(note: NoteWire) {
  const calls: string[] = [];
  const bodies: unknown[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    calls.push(`${init?.method ?? 'GET'} ${url.pathname}`);
    if (init?.body) bodies.push(JSON.parse(String(init.body)));
    if (init?.method === 'DELETE') return new Response(null, { status: 204 });
    return new Response(JSON.stringify({ ...note, archived: false }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  });
  const { unmount } = render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter>
        <NoteRow note={note} />
        {/* The shell's, in the app; here so the Undo the row offers can be pressed. */}
        <Toast />
      </MemoryRouter>
    </TestProviders>,
  );
  return { calls, bodies, unmount };
}

const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

afterEach(() => {
  Object.defineProperty(navigator, 'onLine', { value: true, configurable: true });
  dismissToast();
});

function swipeOpen(): HTMLElement {
  const row = screen.getByRole('button', { name: /roof repair/i }).closest('.swipe') as HTMLElement;
  fireEvent.pointerDown(row, { ...touch, clientX: 300, clientY: 10 });
  fireEvent.pointerMove(row, { ...touch, clientX: 280, clientY: 10 });
  fireEvent.pointerMove(row, { ...touch, clientX: 160, clientY: 10 });
  fireEvent.pointerUp(row, { ...touch, clientX: 160, clientY: 10 });
  // The browser's click for the lifted finger (`detail` 1, as a pointer's), which the row swallows.
  fireEvent.click(screen.getByRole('button', { name: /roof repair/i }), { detail: 1 });
  return screen.getByRole('group', { name: 'Actions for Roof repair' });
}

describe('NoteRow swipe actions', () => {
  it('offers Pin and Delete in the library', () => {
    mount(ACTIVE);
    const tray = screen.getByRole('group', { hidden: true });
    expect(tray).toHaveAttribute('aria-label', 'Actions for Roof repair');
    expect(
      within(tray)
        .getAllByRole('button', { hidden: true })
        .map((button) => button.textContent),
    ).toEqual(['Pin', 'Delete']);
  });

  it('pins on the tap, and a pinned row offers Unpin and wears the glyph', async () => {
    const { calls, bodies, unmount } = mount(ACTIVE);
    fireEvent.click(within(swipeOpen()).getByRole('button', { name: 'Pin' }));
    await waitFor(() => {
      expect(calls).toContain('PATCH /v1/notes/roof-repair');
    });
    expect(bodies).toEqual([{ version: ACTIVE.version, pinned: true }]);
    expect(screen.getByRole('button', { name: /^roof repair/i })).not.toHaveAttribute('data-pinned');
    unmount();

    mount({ ...ACTIVE, pinned: true, pin_rank: 0 });
    expect(screen.getByRole('button', { name: /^roof repair/i })).toHaveAttribute('data-pinned');
    expect(document.querySelector('.note-row__pin')).not.toBeNull();
    const tray = screen.getByRole('group', { hidden: true });
    expect(within(tray).getByRole('button', { name: 'Unpin', hidden: true })).toBeInTheDocument();
  });

  it('offers Restore and Delete forever in the archive', () => {
    mount(ARCHIVED);
    const tray = screen.getByRole('group', { hidden: true });
    expect(
      within(tray)
        .getAllByRole('button', { hidden: true })
        .map((button) => button.textContent),
    ).toEqual(['Restore', 'Delete forever']);
  });

  it('offers the same actions behind the ⋮, for a pointer that cannot swipe', async () => {
    const user = userEvent.setup();
    const { calls, unmount } = mount(ACTIVE);
    expect(screen.queryByRole('checkbox')).toBeNull();

    const more = screen.getByRole('button', { name: 'More' });
    // Named "More" so the title answers to the row alone; the title describes it.
    expect(more).toHaveAccessibleDescription(/^Roof repair/);
    await user.click(more);
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Pin',
      'Colour…',
      'Delete',
    ]);
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    // The menu's Delete asks the same question the tray's does.
    const dialog = await screen.findByRole('dialog', { name: 'Delete “Roof repair”?' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => {
      expect(calls).toContain('DELETE /v1/notes/roof-repair');
    });
    expect(screen.queryByRole('dialog')).toBeNull();
    unmount();

    mount(ARCHIVED);
    await user.click(screen.getByRole('button', { name: 'More' }));
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Restore',
      'Delete forever',
    ]);
  });

  it('leaves Pin out of the tray while offline, where the PATCH would only queue', () => {
    // The tray has no disabled state; a paused pin would fire later with a
    // version the cache may no longer hold (review 2026-09-24, R4-11).
    Object.defineProperty(navigator, 'onLine', { value: false, configurable: true });
    mount(ACTIVE);
    const tray = screen.getByRole('group', { hidden: true });
    expect(
      within(tray)
        .getAllByRole('button', { hidden: true })
        .map((button) => button.textContent),
    ).toEqual(['Delete']);
  });

  it('has no tray for a pointer that can hover', () => {
    setCanHover(true);
    mount(ACTIVE);
    expect(screen.queryByRole('group', { hidden: true })).toBeNull();
  });

  it('Delete asks first — the title, where the note goes, focus on Cancel — then archives and offers Undo, which restores', async () => {
    const user = userEvent.setup();
    const { calls } = mount(ACTIVE);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Delete' }));

    // Nothing has gone yet: the question names the note and says where it
    // would go, with nothing to type and the safe answer under Enter.
    const dialog = await screen.findByRole('dialog', { name: 'Delete “Roof repair”?' });
    expect(dialog).toHaveTextContent('It is kept in the Archive for 30 days, then gone for good.');
    expect(within(dialog).queryByRole('textbox')).toBeNull();
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
    expect(calls.filter((call) => call.startsWith('DELETE'))).toEqual([]);

    await user.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => {
      expect(calls).toContain('DELETE /v1/notes/roof-repair');
    });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(calls).not.toContain('DELETE /v1/notes/roof-repair/permanent');

    // The toast says where the note went and for how long, and Undo brings it back.
    const toast = screen.getByRole('status');
    await waitFor(() => {
      expect(toast).toHaveTextContent('Deleted · kept in Archive for 30 days');
    });
    await user.click(within(toast).getByRole('button', { name: 'Undo' }));
    await waitFor(() => {
      expect(calls).toContain('POST /v1/notes/roof-repair/restore');
    });
    expect(toast).toBeEmptyDOMElement();
  });

  it('Escape, or Enter on the focused Cancel, closes the delete confirm and archives nothing', async () => {
    const user = userEvent.setup();
    const { calls } = mount(ACTIVE);

    fireEvent.click(within(swipeOpen()).getByRole('button', { name: 'Delete' }));
    await screen.findByRole('dialog', { name: 'Delete “Roof repair”?' });
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();

    fireEvent.click(within(swipeOpen()).getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete “Roof repair”?' });
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(screen.queryByRole('dialog')).toBeNull();

    // From the ⋮, the everyday keyboard route: the menuitem that opened the
    // dialog is gone with the menu, so Cancel has to land focus back on the
    // ⋮ itself, not at the top of the document (WCAG 2.4.3).
    await user.click(screen.getByRole('button', { name: 'More' }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    await screen.findByRole('dialog', { name: 'Delete “Roof repair”?' });
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByRole('button', { name: 'More' })).toHaveFocus();

    expect(calls.filter((call) => call.startsWith('DELETE'))).toEqual([]);
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('restores on the tap in the archive', async () => {
    const { calls } = mount(ARCHIVED);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Restore' }));

    await waitFor(() => {
      expect(calls).toContain('POST /v1/notes/roof-repair/restore');
    });
  });

  it('Delete forever in the archive asks once, plainly, and purges with the one call', async () => {
    const user = userEvent.setup();
    const { calls } = mount(ARCHIVED);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Delete forever' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete this note forever?' });
    // It names what goes, and there is nothing to type; Cancel is under Enter.
    expect(within(dialog).getByText(/“Roof repair”/)).toBeInTheDocument();
    expect(within(dialog).queryByRole('textbox')).toBeNull();
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toHaveFocus();
    await user.click(within(dialog).getByRole('button', { name: 'Delete forever' }));

    await waitFor(() => {
      expect(calls).toContain('DELETE /v1/notes/roof-repair/permanent');
    });
    expect(calls).not.toContain('DELETE /v1/notes/roof-repair');
  });

  it('cancelling the dialog deletes nothing', async () => {
    const user = userEvent.setup();
    const { calls } = mount(ARCHIVED);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Delete forever' }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }));

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(calls.filter((call) => call.startsWith('DELETE'))).toEqual([]);
  });
});

describe('NoteRow colour', () => {
  it('wears its tint, and Colour… sends the colour alone, tints at once and says so', async () => {
    const user = userEvent.setup();
    const { calls, bodies } = mount({ ...ACTIVE, color: 'sky' });
    render(<StatusRegion />);
    const row = screen.getByRole('button', { name: /roof repair/i });
    expect(row).toHaveAttribute('data-color', 'sky');

    await user.click(screen.getByRole('button', { name: 'More' }));
    await user.click(screen.getByRole('menuitem', { name: 'Colour…' }));
    const dialog = await screen.findByRole('dialog', { name: 'Colour for “Roof repair”' });
    const swatches = within(dialog).getAllByRole('radio');
    expect(swatches.map((swatch) => swatch.getAttribute('aria-label'))).toEqual([
      'No colour', 'Rose', 'Peach', 'Sand', 'Lime', 'Mint', 'Sky', 'Lavender', 'Slate',
    ]);
    expect(within(dialog).getByRole('radio', { name: 'Sky' })).toBeChecked();

    await user.click(within(dialog).getByRole('radio', { name: 'Rose' }));
    await waitFor(() => {
      expect(calls).toContain('PATCH /v1/notes/roof-repair');
    });
    expect(bodies).toContainEqual({ version: ACTIVE.version, color: 'rose' });
    expect(await screen.findByText('Colour: Rose')).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('wears no tint without a colour, and offers no Colour… offline', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'onLine', { value: false, configurable: true });
    mount(ACTIVE);
    expect(screen.getByRole('button', { name: /roof repair/i })).not.toHaveAttribute('data-color');
    await user.click(screen.getByRole('button', { name: 'More' }));
    expect(screen.getByRole('menuitem', { name: 'Colour…' })).toBeDisabled();

describe('a search hit', () => {
  it('marks every word of the search, apart, as the search matched them', () => {
    render(
      <TestProviders api={testApiContext(vi.fn<typeof fetch>())}>
        <MemoryRouter>
          <NoteRow note={ACTIVE} excerpt="Ridge tiles on the south slope have slipped." highlight="SLIPPED ridge" />
        </MemoryRouter>
      </TestProviders>,
    );
    const marks = Array.from(document.querySelectorAll('mark.search-hit')).map((mark) => mark.textContent);
    expect(marks).toEqual(['Ridge', 'slipped']);
  });

  it('draws words inside another word once, the text unchanged', () => {
    // "kit" and "hen" both lie inside "kitchen"; comparing with the previous
    // match rather than the last kept one drew "kitchenhen".
    render(
      <TestProviders api={testApiContext(vi.fn<typeof fetch>())}>
        <MemoryRouter>
          <NoteRow note={ACTIVE} excerpt="Fix the kitchen tap" highlight="kitchen kit hen" />
        </MemoryRouter>
      </TestProviders>,
    );
    const snippet = document.querySelector('.note-row__snippet');
    expect(snippet?.textContent).toBe('Fix the kitchen tap');
    expect(Array.from(document.querySelectorAll('mark.search-hit')).map((mark) => mark.textContent)).toEqual([
      'kitchen',
    ]);
  });

  it('opens the note with Find on the first word searched for', async () => {
    const user = userEvent.setup();
    let arrived: unknown = null;
    function Arrival() {
      arrived = useLocation().state;
      return null;
    }
    render(
      <TestProviders api={testApiContext(vi.fn<typeof fetch>())}>
        <MemoryRouter>
          <Routes>
            <Route path="/" element={<NoteRow note={ACTIVE} excerpt="Ridge tiles" highlight=" tiles ridge" />} />
            <Route path="/notes/:id" element={<Arrival />} />
          </Routes>
        </MemoryRouter>
      </TestProviders>,
    );
    await user.click(screen.getByRole('button', { name: /roof repair/i }));
    expect(arrived).toEqual({ find: 'tiles' });
  });
});

describe('a hit only the server found', () => {
  it('offers no Pin or Colour…, which would be sent on a version the row never had', async () => {
    const user = userEvent.setup();
    mount({ ...ACTIVE, version: 0, updated_at: '' });
    await user.click(screen.getByRole('button', { name: 'More' }));
    expect(screen.getByRole('menuitem', { name: 'Pin' })).toBeDisabled();
    expect(screen.getByRole('menuitem', { name: 'Colour…' })).toBeDisabled();
  });
});
