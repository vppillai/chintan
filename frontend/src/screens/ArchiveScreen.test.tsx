import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';

import { ArchiveScreen } from './ArchiveScreen.tsx';

const ARCHIVED_NOTES = TEST_NOTES.map((note) => ({
  ...note,
  archived: true,
  purge_after: '2026-09-01T00:00:00.000Z',
}));

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function mount(fetchImpl: typeof fetch) {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter>
        <ArchiveScreen />
      </MemoryRouter>
    </TestProviders>,
  );
}

describe('doing something to several archived notes at once', () => {
  it('restores every selected note', async () => {
    const user = userEvent.setup();
    const restored: string[] = [];
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      if (method === 'POST' && url.endsWith('/restore')) {
        const id = url.split('/v1/notes/')[1]?.split('/restore')[0] ?? '';
        restored.push(decodeURIComponent(id));
        return json({});
      }
      return json({ items: ARCHIVED_NOTES });
    });
    mount(fetchImpl);

    await user.click(await screen.findByRole('button', { name: 'Select' }));
    const checkboxes = await screen.findAllByRole('checkbox');
    await user.click(checkboxes[0] as HTMLElement);
    await user.click(checkboxes[1] as HTMLElement);

    await user.click(screen.getByRole('button', { name: 'Restore' }));
    await user.click(await screen.findByRole('button', { name: 'Restore them' }));

    await waitFor(() => {
      expect(restored.sort()).toEqual(ARCHIVED_NOTES.map((n) => n.id).sort());
    });
  });

  it('empties the archive: select all, then delete forever, in one batch call', async () => {
    // The feature request this closes: no way to clear the whole archive at
    // once. "Select all" plus this is that, using the real batch endpoint —
    // one POST /v1/notes/purge naming every id, not N individual calls.
    const user = userEvent.setup();
    let purgeBody: unknown = null;
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      if (method === 'POST' && url.endsWith('/v1/notes/purge')) {
        purgeBody = JSON.parse(String(init?.body));
        return json({
          results: ARCHIVED_NOTES.map((n) => ({ note_id: n.id, status: 'purged' })),
        });
      }
      return json({ items: ARCHIVED_NOTES });
    });
    mount(fetchImpl);

    await user.click(await screen.findByRole('button', { name: 'Select' }));
    await user.click(await screen.findByRole('button', { name: 'Select all' }));
    await user.click(screen.getByRole('button', { name: 'Delete forever' }));

    const dialog = await screen.findByRole('dialog');
    await user.type(
      await screen.findByLabelText(/type "delete" to confirm/i),
      'delete',
    );
    await user.click(
      await screen.findByRole('button', { name: 'Delete them forever' }),
    );

    await waitFor(() => {
      expect(purgeBody).toEqual({ note_ids: ARCHIVED_NOTES.map((n) => n.id) });
    });
    expect(dialog).not.toBeInTheDocument();
  });

  it('keeps the destructive confirm locked until "delete" is typed', async () => {
    const user = userEvent.setup();
    mount(vi.fn<typeof fetch>(async () => json({ items: ARCHIVED_NOTES })));

    await user.click(await screen.findByRole('button', { name: 'Select' }));
    await user.click((await screen.findAllByRole('checkbox'))[0] as HTMLElement);
    await user.click(screen.getByRole('button', { name: 'Delete forever' }));

    expect(await screen.findByRole('button', { name: 'Delete them forever' })).toBeDisabled();
  });
});
