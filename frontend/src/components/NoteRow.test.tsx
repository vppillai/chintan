import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';
import { setCanHover } from '@/test/setup.ts';

import { NoteRow } from './NoteRow.tsx';

/**
 * The swipe tray behind a note row: the right actions for the view the row
 * is in, pin and archive on the tap, the delete behind the same typed gate
 * the note's own action bar uses. The gesture itself is SwipeRow's test. And
 * the ⋮ at the row's right, which offers the same actions plus Select to a
 * pointer that cannot swipe (2026-09-24, C).
 */

const ACTIVE: NoteWire = TEST_NOTES[0] as NoteWire;
const ARCHIVED: NoteWire = {
  ...ACTIVE,
  archived: true,
  purge_after: new Date(Date.now() + 10 * 86_400_000).toISOString(),
};

function mount(note: NoteWire, { selectable = false } = {}) {
  const calls: string[] = [];
  const bodies: unknown[] = [];
  const onToggleSelect = vi.fn();
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
        <NoteRow note={note} selectable={selectable} onToggleSelect={onToggleSelect} />
      </MemoryRouter>
    </TestProviders>,
  );
  return { calls, bodies, onToggleSelect, unmount };
}

const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

afterEach(() => {
  Object.defineProperty(navigator, 'onLine', { value: true, configurable: true });
});

function swipeOpen(): HTMLElement {
  const row = screen.getByRole('button', { name: /roof repair/i }).closest('.swipe') as HTMLElement;
  fireEvent.pointerDown(row, { ...touch, clientX: 300, clientY: 10 });
  fireEvent.pointerMove(row, { ...touch, clientX: 280, clientY: 10 });
  fireEvent.pointerMove(row, { ...touch, clientX: 160, clientY: 10 });
  fireEvent.pointerUp(row, { ...touch, clientX: 160, clientY: 10 });
  // The browser's click for the lifted finger, which the row swallows.
  fireEvent.click(screen.getByRole('button', { name: /roof repair/i }));
  return screen.getByRole('group', { name: 'Actions for Roof repair' });
}

describe('NoteRow swipe actions', () => {
  it('offers Pin, Archive and Delete in the library', () => {
    mount(ACTIVE);
    const tray = screen.getByRole('group', { hidden: true });
    expect(tray).toHaveAttribute('aria-label', 'Actions for Roof repair');
    expect(
      within(tray)
        .getAllByRole('button', { hidden: true })
        .map((button) => button.textContent),
    ).toEqual(['Pin', 'Archive', 'Delete']);
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

  it('offers Restore and Delete in the archive', () => {
    mount(ARCHIVED);
    const tray = screen.getByRole('group', { hidden: true });
    expect(
      within(tray)
        .getAllByRole('button', { hidden: true })
        .map((button) => button.textContent),
    ).toEqual(['Restore', 'Delete']);
  });

  it('offers the same actions and Select behind the ⋮, for a pointer that cannot swipe', async () => {
    const user = userEvent.setup();
    const { calls, onToggleSelect, unmount } = mount(ACTIVE);
    expect(screen.queryByRole('checkbox')).toBeNull();

    const more = screen.getByRole('button', { name: 'More' });
    // Named "More" so the title answers to the row alone; the title describes it.
    expect(more).toHaveAccessibleDescription(/^Roof repair/);
    await user.click(more);
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Pin',
      'Archive',
      'Delete',
      'Select',
    ]);
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));
    expect(onToggleSelect).toHaveBeenCalledWith('roof-repair', { range: false });

    await user.click(screen.getByRole('button', { name: 'More' }));
    await user.click(screen.getByRole('menuitem', { name: 'Archive' }));
    await waitFor(() => {
      expect(calls).toContain('DELETE /v1/notes/roof-repair');
    });
    unmount();

    mount(ARCHIVED);
    await user.click(screen.getByRole('button', { name: 'More' }));
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Restore',
      'Delete',
      'Select',
    ]);
  });

  it('Select from the ⋮ hands focus to the checkbox that replaces the row', async () => {
    // The menu's trigger unmounts with the row it sat on, so the checkbox is
    // what a keyboard user should land on, not the body.
    const user = userEvent.setup();
    const api = testApiContext(vi.fn<typeof fetch>(async () => new Response('{}', { status: 200 })));
    const tree = (selectable: boolean) => (
      <TestProviders api={api}>
        <MemoryRouter>
          <NoteRow note={ACTIVE} selectable={selectable} onToggleSelect={() => {}} />
        </MemoryRouter>
      </TestProviders>
    );
    const { rerender } = render(tree(false));
    await user.click(screen.getByRole('button', { name: 'More' }));
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));
    rerender(tree(true));
    expect(screen.getByRole('checkbox')).toHaveFocus();
  });

  it('draws the app’s own box while bulk-select is on, over the real checkbox', () => {
    // Every other box in the app is drawn (F1); the browser's square tick
    // beside them was the odd one out (review 2026-09-24, R4-12).
    mount(ACTIVE, { selectable: true });
    const box = screen.getByRole('checkbox');
    expect(box).toHaveClass('checklist__box');
    // Adjacent, as `.checklist__box:checked + .checklist__mark` needs.
    expect(box.nextElementSibling).toHaveClass('checklist__mark');
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
    ).toEqual(['Archive', 'Delete']);
  });

  it('has no tray while bulk-select is on, nor for a pointer that can hover', () => {
    const { unmount } = mount(ACTIVE, { selectable: true });
    expect(screen.queryByRole('group', { hidden: true })).toBeNull();
    unmount();

    setCanHover(true);
    mount(ACTIVE);
    expect(screen.queryByRole('group', { hidden: true })).toBeNull();
  });

  it('archives on the tap — it is reversible', async () => {
    const { calls } = mount(ACTIVE);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Archive' }));

    await waitFor(() => {
      expect(calls).toContain('DELETE /v1/notes/roof-repair');
    });
    expect(calls).not.toContain('DELETE /v1/notes/roof-repair/permanent');
  });

  it('restores on the tap in the archive', async () => {
    const { calls } = mount(ARCHIVED);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Restore' }));

    await waitFor(() => {
      expect(calls).toContain('POST /v1/notes/roof-repair/restore');
    });
  });

  it('deletes only behind the typed word, archiving first from the library', async () => {
    const user = userEvent.setup();
    const { calls } = mount(ACTIVE);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Delete' }));

    const dialog = await screen.findByRole('dialog', { name: 'Delete this note forever?' });
    const confirm = within(dialog).getByRole('button', { name: 'Delete forever' });
    expect(confirm).toBeDisabled();
    // The word, not the title — a dictated title is a chore on a phone keyboard.
    expect(within(dialog).getByText(/“Roof repair”/)).toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Type "delete" to confirm'), 'delete');
    await user.click(confirm);

    // The server refuses to purge an active note, so the row archives it first.
    await waitFor(() => {
      expect(calls).toContain('DELETE /v1/notes/roof-repair/permanent');
    });
    expect(calls.indexOf('DELETE /v1/notes/roof-repair')).toBeLessThan(
      calls.indexOf('DELETE /v1/notes/roof-repair/permanent'),
    );
  });

  it('deletes an archived note with the one call', async () => {
    const user = userEvent.setup();
    const { calls } = mount(ARCHIVED);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    await user.type(within(dialog).getByRole('textbox'), 'delete');
    await user.click(within(dialog).getByRole('button', { name: 'Delete forever' }));

    await waitFor(() => {
      expect(calls).toContain('DELETE /v1/notes/roof-repair/permanent');
    });
    expect(calls).not.toContain('DELETE /v1/notes/roof-repair');
  });

  it('cancelling the dialog deletes nothing', async () => {
    const user = userEvent.setup();
    const { calls } = mount(ACTIVE);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Delete' }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }));

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(calls.filter((call) => call.startsWith('DELETE'))).toEqual([]);
  });
});
