import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { NoteDetailWire, SettingsWire } from '@/api/schema.ts';
import { languageLabel } from '@/features/settings/languages.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NoteDetailScreen, effectiveLanguage } from './NoteDetailScreen.tsx';

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
  await userEvent.click(screen.getByRole('button', { name: 'Details' }));
  return (await screen.findByRole('combobox', {
    name: 'Transcription language',
  })) as HTMLSelectElement;
}

describe('a note’s transcription language', () => {
  it('lives in the Details disclosure and is sent with the note’s own PATCH and version', async () => {
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

  it('offers auto-detect and the curated languages, each in its own script and in English', async () => {
    mount();
    await openLanguage();
    expect(screen.getByRole('option', { name: 'Auto-detect' })).toBeInTheDocument();
    for (const code of ['ml', 'hi', 'ta', 'es', 'ja']) {
      expect(screen.getByRole('option', { name: languageLabel(code) })).toBeInTheDocument();
    }
    expect(screen.getByRole('option', { name: 'മലയാളം · Malayalam' })).toBeInTheDocument();
    // Inherit first, then Auto-detect, then the languages.
    const options = within(screen.getByRole('combobox', { name: 'Transcription language' }))
      .getAllByRole('option')
      .map((option) => option.textContent);
    expect(options.slice(0, 3)).toEqual(['Default (Malayalam)', 'Auto-detect', 'English']);
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

/**
 * The owner's trial: "I don't see multilingual support anywhere." The control
 * existed, third in a panel called Tags. It is now the first thing in Details,
 * and the language is said in the meta line under the title, where a tap on
 * it opens the panel.
 */
describe('the language is where the user looks', () => {
  it('is the first field of Details, before the tags and the other names', async () => {
    mount();
    await screen.findByDisplayValue('Roof repair');
    await userEvent.click(screen.getByRole('button', { name: 'Details' }));

    const panel = screen.getByRole('combobox', { name: 'Transcription language' }).closest(
      '.note-panel',
    );
    expect(panel).not.toBeNull();
    const labels = Array.from(panel!.querySelectorAll('.tag-editor__label')).map(
      (label) => label.textContent,
    );
    expect(labels).toEqual(['Transcription language', 'Tags', 'Also called']);
    expect(screen.queryByRole('button', { name: 'Tags' })).toBeNull();
  });

  it('names the effective language in the meta line when it is not plain English', async () => {
    // The default is Malayalam here; the note inherits it.
    mount();
    await screen.findByDisplayValue('Roof repair');
    const fact = await screen.findByRole('button', { name: /transcription language:\s*malayalam/i });
    expect(fact.closest('.note-meta')).toHaveTextContent(/house · .*Malayalam$/);
  });

  it('names the note’s own language when it differs from the default', async () => {
    mount({ ...NOTE, language: 'ta' });
    await screen.findByDisplayValue('Roof repair');
    expect(
      await screen.findByRole('button', { name: /transcription language:\s*tamil/i }),
    ).toBeInTheDocument();
  });

  it('opens Details and lands on the select when the fact is tapped', async () => {
    mount({ ...NOTE, language: 'auto' });
    await screen.findByDisplayValue('Roof repair');
    expect(screen.queryByRole('combobox', { name: 'Transcription language' })).toBeNull();

    await userEvent.click(
      await screen.findByRole('button', { name: /transcription language:\s*auto-detect/i }),
    );

    const select = screen.getByRole('combobox', { name: 'Transcription language' });
    expect(select).toHaveFocus();
    expect(screen.getByRole('button', { name: 'Details' })).toHaveAttribute('aria-expanded', 'true');
  });
});

describe('when the language is worth a word', () => {
  const cases: [note: string, fallback: string | undefined, expected: string | null][] = [
    ['', 'en', null],
    ['', undefined, null],
    ['en', 'en', null],
    ['', 'ml', 'ml'],
    ['ml', 'ml', 'ml'],
    ['en', 'ml', 'en'],
    ['ta', 'en', 'ta'],
    ['auto', 'en', 'auto'],
  ];
  for (const [note, fallback, expected] of cases) {
    it(`note ${JSON.stringify(note)} under default ${JSON.stringify(fallback)} → ${JSON.stringify(expected)}`, () => {
      expect(effectiveLanguage(note, fallback)).toBe(expected);
    });
  }
});
