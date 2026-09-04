import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { NoteDetailWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NoteDetailScreen } from './NoteDetailScreen.tsx';
import { countWords, describeWords } from './words.ts';

describe('counting words', () => {
  it('counts runs of non-whitespace, whatever the whitespace', () => {
    expect(countWords('Ridge tiles on the south slope have slipped.')).toBe(8);
    expect(countWords('one\ttwo\nthree\r\n\nfour')).toBe(4);
    expect(countWords('  padded   with    spaces  ')).toBe(3);
  });

  it('is zero for nothing, and not one for a blank line', () => {
    expect(countWords('')).toBe(0);
    expect(countWords('   \n\n  ')).toBe(0);
  });

  it('treats every script’s spaces as spaces', () => {
    // Malayalam, spaced as it is written; the no-break space between the
    // French words counts too, which the Unicode flag is for.
    expect(countWords('മേൽക്കൂര നന്നാക്കണം രണ്ട് ക്വട്ടേഷൻ')).toBe(4);
    expect(countWords('un deux trois')).toBe(3);
  });

  it('says it in words', () => {
    expect(describeWords(0)).toBe('0 words');
    expect(describeWords(1)).toBe('1 word');
    expect(describeWords(412)).toBe('412 words');
  });
});

const NOTE: NoteDetailWire = {
  id: 'roof-repair',
  title: 'Roof repair',
  body: 'Ridge tiles on the south slope have slipped.',
  aliases: [],
  tags: ['house'],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
  captures: [{ id: 'cap-1', status: 'appended', created_at: '2026-08-06T09:10:00.000Z', version: 1, duration_ms: 12_000 }],
};

function mount() {
  const fetchImpl = vi.fn<typeof fetch>(async () =>
    new Response(JSON.stringify(NOTE), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }),
  );
  const router = createMemoryRouter([{ path: '/notes/:id', Component: NoteDetailScreen }], {
    initialEntries: ['/notes/roof-repair'],
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <RouterProvider router={router} />
    </TestProviders>,
  );
}

describe('the word count on the note', () => {
  it('sits in the meta line after the recordings, and follows the typing', async () => {
    mount();
    const body = await screen.findByRole('textbox', { name: 'Note body' });

    const meta = screen.getByText(/8 words/);
    expect(meta).toHaveClass('note-meta');
    // Recordings before words, as the backlog asked.
    expect(meta.textContent?.indexOf('1 recording')).toBeLessThan(meta.textContent?.indexOf('8 words') ?? -1);

    // Live: the count is of the draft, not of the saved note.
    await userEvent.type(body, ' Two quotes before the rain.');
    expect(screen.getByText(/13 words/)).toBeInTheDocument();

    await userEvent.clear(body);
    expect(screen.getByText(/0 words/)).toBeInTheDocument();
  });
});
