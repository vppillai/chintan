import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { notesPageChecklists } from '@/api/__fixtures__/responses.ts';
import type { NoteWire } from '@/api/schema.ts';
import { TestProviders } from '@/test/providers.tsx';

import { NoteRow } from './NoteRow.tsx';

/**
 * A checklist's row: the glyph, the open items as the snippet, the progress
 * in the meta line — counted from the snippet, which is the body's first 500
 * runes and so only a floor when it was cut.
 */
/** The generated checklist row: "Packing", one item done of two. */
const CHECKLIST = notesPageChecklists.items[0] as NoteWire;

function mount(note: NoteWire) {
  render(
    <TestProviders>
      <MemoryRouter>
        <NoteRow note={note} />
      </MemoryRouter>
    </TestProviders>,
  );
  return screen.getByRole('button', { name: /packing/i });
}

describe('NoteRow for a checklist', () => {
  it('wears the glyph, shows the open items plainly, and says how far along it is', () => {
    const row = mount(CHECKLIST);
    expect(row.querySelector('.note-row__kind')).not.toBeNull();
    expect(within(row).getByText('charger')).toBeInTheDocument();
    expect(within(row).queryByText(/\[ \]/)).toBeNull();
    expect(within(row).getByText('1 of 2 done')).toHaveClass('numeric');
  });

  it('marks the total as a floor when the snippet was cut', () => {
    const items = Array.from({ length: 40 }, (_, index) => `- [${index % 2 ? 'x' : ' '}] Item ${String(index + 1)} of a long list`);
    const body = items.join('\n');
    const snippet = `${Array.from(body).slice(0, 500).join('')}...`;
    const row = mount({ ...CHECKLIST, snippet });
    expect(within(row).getByText(/^\d+ of \d+\+ done$/)).toBeInTheDocument();
  });

  it('a plain note is untouched', () => {
    // Unpinned as well: a pin glyph would share the checklist glyph's slot (`.note-row__kind`).
    const row = mount({ ...CHECKLIST, kind: 'note', pinned: false, snippet: 'Just a sentence.' });
    expect(row.querySelector('.note-row__kind')).toBeNull();
    expect(within(row).getByText('Just a sentence.')).toBeInTheDocument();
    expect(within(row).queryByText(/done$/)).toBeNull();
  });
});
