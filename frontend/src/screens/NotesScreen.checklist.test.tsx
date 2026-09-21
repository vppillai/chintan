import { onlineManager } from '@tanstack/react-query';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { noteRowChecklist } from '@/api/__fixtures__/pending.ts';
import type { NoteWire } from '@/api/schema.ts';
import { cacheNoteList } from '@/offline/notesCache.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';

import { NotesScreen } from './NotesScreen.tsx';

/**
 * The Checklists chip: right after All, carrying the count, filtering the
 * list through `?kind=checklist` on the server and through `note.kind` on the
 * device's copy when the server cannot be asked.
 */

const NOTES: NoteWire[] = [...(TEST_NOTES as NoteWire[]), noteRowChecklist];

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

function library() {
  const requests: string[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input) => {
    const url = new URL(String(input));
    requests.push(`${url.pathname}${url.search}`);
    if (url.pathname.endsWith('/v1/tags')) return json({ items: [{ name: 'house', count: 2 }] });
    if (url.pathname.endsWith('/v1/notes')) {
      const state = url.searchParams.get('state') ?? 'active';
      const kind = url.searchParams.get('kind');
      const items = NOTES.filter(
        (note) => note.archived === (state === 'archived') && (!kind || note.kind === kind),
      );
      return json({ items });
    }
    return json({ items: [] });
  });
  return { fetchImpl, requests };
}

function Location() {
  return <output data-testid="location">{useLocation().search}</output>;
}

function mount(fetchImpl: typeof fetch, path = '/') {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter initialEntries={[path]}>
        <NotesScreen />
        <Location />
      </MemoryRouter>
    </TestProviders>,
  );
}

afterEach(() => {
  Object.defineProperty(navigator, 'onLine', { value: true, configurable: true });
  onlineManager.setOnline(true);
});

describe('the Checklists chip', () => {
  it('sits after All with the count, and filters the list through the URL', async () => {
    const user = userEvent.setup();
    const { fetchImpl, requests } = library();
    mount(fetchImpl);
    await screen.findByRole('button', { name: /roof repair/i });

    const chips = within(screen.getByRole('group', { name: 'Filter notes' }));
    await waitFor(() => {
      expect(chips.getByRole('button', { name: 'Checklists · 1' })).toBeInTheDocument();
    });
    expect(chips.getAllByRole('button').map((chip) => chip.getAttribute('aria-label') ?? chip.textContent)).toEqual([
      'All',
      'Checklists · 1',
      'house',
      'Archived · 0',
    ]);

    await user.click(chips.getByRole('button', { name: 'Checklists · 1' }));
    expect(screen.getByTestId('location')).toHaveTextContent('?kind=checklist');
    expect(chips.getByRole('button', { name: 'Checklists · 1' })).toHaveAttribute('aria-pressed', 'true');
    expect(chips.getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'false');
    expect(requests.some((url) => url.includes('/v1/notes?') && url.includes('kind=checklist'))).toBe(true);
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /roof repair/i })).toBeNull();
    });
    expect(screen.getByRole('button', { name: /shopping/i })).toBeInTheDocument();

    // All clears it.
    await user.click(chips.getByRole('button', { name: 'All' }));
    expect(screen.getByTestId('location')).toHaveTextContent('');
    expect(await screen.findByRole('button', { name: /roof repair/i })).toBeInTheDocument();
  });

  it('filters the device’s copy by kind when the server cannot be asked', async () => {
    await cacheNoteList(NOTES);
    Object.defineProperty(navigator, 'onLine', { value: false, configurable: true });
    onlineManager.setOnline(false);
    const { fetchImpl } = library();
    mount(fetchImpl, '/?kind=checklist');

    expect(await screen.findByRole('button', { name: /shopping/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /roof repair/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /reading list/i })).toBeNull();
  });

  it('says what an empty checklist filter means', async () => {
    const { fetchImpl } = library();
    vi.mocked(fetchImpl).mockImplementation(async (input) => {
      const url = new URL(String(input));
      return json({ items: url.pathname.endsWith('/v1/notes') && !url.searchParams.get('kind') ? TEST_NOTES : [] });
    });
    mount(fetchImpl, '/?kind=checklist');
    expect(await screen.findByText(/No checklists yet/)).toBeInTheDocument();
  });
});
