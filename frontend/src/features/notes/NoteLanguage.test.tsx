import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { NoteDetailWire, SettingsWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NoteDetailScreen } from './NoteDetailScreen.tsx';

const NOTE: NoteDetailWire = {
  id: 'roof-repair',
  title: 'Roof repair',
  body: 'Ridge tiles on the south slope have slipped.',
  aliases: [],
  tags: ['house'],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
  captures: [],
};

const SETTINGS: SettingsWire = {
  cleanup_mode: 'faithful',
  retention_days: 0,
  theme: 'ink',
  default_language: 'ml',
};

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

/**
 * The note screen over a stub API that records every PATCH, so the assertion
 * is on what would reach the server rather than on component state.
 */
function mount(note: NoteDetailWire = NOTE) {
  const patches: Record<string, unknown>[] = [];
  let current = note;
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    if (url.pathname.endsWith('/v1/settings')) return json(SETTINGS);
    if (method === 'PATCH') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      patches.push(body);
      current = {
        ...current,
        version: current.version + 1,
        ...(typeof body['language'] === 'string' && body['language'] !== ''
          ? { language: body['language'] }
          : {}),
      };
      if (body['language'] === '') delete current.language;
      return json(current);
    }
    return json(current);
  });

  const router = createMemoryRouter([{ path: '/notes/:id', Component: NoteDetailScreen }], {
    initialEntries: [`/notes/${note.id}`],
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
  return { patches };
}

async function openLanguage(): Promise<HTMLSelectElement> {
  await screen.findByDisplayValue('Roof repair');
  await userEvent.click(screen.getByRole('button', { name: 'Tags' }));
  return (await screen.findByRole('combobox', { name: 'Language' })) as HTMLSelectElement;
}

describe('a note’s transcription language', () => {
  it('lives in the Tags disclosure and is sent with the note’s own PATCH and version', async () => {
    const { patches } = mount();
    const select = await openLanguage();

    // Inherits by default, and says what it inherits.
    expect(select.value).toBe('');
    expect(screen.getByRole('option', { name: 'Default (Malayalam)' })).toBeInTheDocument();

    await userEvent.selectOptions(select, 'hi');

    await waitFor(() => {
      expect(patches).toHaveLength(1);
    });
    expect(patches[0]?.['language']).toBe('hi');
    // The same serialised save as the text, carrying the version the note was
    // loaded at — a separate request would bump the version under a dirty
    // draft and turn the user's own change into a conflict prompt.
    expect(patches[0]?.['version']).toBe(NOTE.version);
    expect(patches[0]?.['title']).toBe('Roof repair');
  });

  it('offers auto-detect and the curated languages, in words', async () => {
    mount();
    await openLanguage();
    for (const name of ['Auto-detect', 'Malayalam', 'Hindi', 'Tamil', 'Spanish', 'Japanese']) {
      expect(screen.getByRole('option', { name })).toBeInTheDocument();
    }
  });

  it('shows the language the note already has, and can hand it back to the default', async () => {
    const { patches } = mount({ ...NOTE, language: 'ta' });
    const select = await openLanguage();
    expect(select.value).toBe('ta');

    await userEvent.selectOptions(select, '');

    await waitFor(() => {
      expect(patches).toHaveLength(1);
    });
    // The empty string is how the contract spells "inherit again".
    expect(patches[0]?.['language']).toBe('');
  });

  it('says what the setting reaches', async () => {
    mount();
    await openLanguage();
    expect(screen.getByText(/recordings made into this note/i)).toBeInTheDocument();
    expect(screen.getByText(/filed automatically is transcribed in your default/i)).toBeInTheDocument();
  });
});
