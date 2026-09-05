import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { NoteWire } from '@/api/schema.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';
import { setCanHover } from '@/test/setup.ts';

import { NoteRow } from './NoteRow.tsx';

/**
 * The swipe tray behind a note row: the right two actions for the view the
 * row is in, the archive on the tap, the delete behind the same typed gate
 * the note's own action bar uses. The gesture itself is SwipeRow's test.
 */

const ACTIVE: NoteWire = TEST_NOTES[0] as NoteWire;
const ARCHIVED: NoteWire = {
  ...ACTIVE,
  archived: true,
  purge_after: new Date(Date.now() + 10 * 86_400_000).toISOString(),
};

function mount(note: NoteWire, { selectable = false } = {}) {
  const calls: string[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    calls.push(`${init?.method ?? 'GET'} ${url.pathname}`);
    if (init?.method === 'DELETE') return new Response(null, { status: 204 });
    return new Response(JSON.stringify({ ...note, archived: false }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  });
  const { unmount } = render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter>
        <NoteRow note={note} selectable={selectable} onToggleSelect={() => {}} />
      </MemoryRouter>
    </TestProviders>,
  );
  return { calls, unmount };
}

const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

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
  it('offers Archive and Delete in the library', () => {
    mount(ACTIVE);
    const tray = screen.getByRole('group', { hidden: true });
    expect(tray).toHaveAttribute('aria-label', 'Actions for Roof repair');
    expect(
      within(tray)
        .getAllByRole('button', { hidden: true })
        .map((button) => button.textContent),
    ).toEqual(['Archive', 'Delete']);
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

  it('deletes only behind the typed title, archiving first from the library', async () => {
    const user = userEvent.setup();
    const { calls } = mount(ACTIVE);
    const tray = swipeOpen();

    fireEvent.click(within(tray).getByRole('button', { name: 'Delete' }));

    const dialog = await screen.findByRole('dialog', { name: 'Delete this note forever?' });
    const confirm = within(dialog).getByRole('button', { name: 'Delete forever' });
    expect(confirm).toBeDisabled();
    await user.type(
      within(dialog).getByLabelText("Type the note's title to confirm: Roof repair"),
      'Roof repair',
    );
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
    await user.type(within(dialog).getByRole('textbox'), 'roof repair');
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
