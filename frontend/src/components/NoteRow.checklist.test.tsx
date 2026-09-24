import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { noteRowChecklist } from '@/api/__fixtures__/pending.ts';
import type { NoteWire } from '@/api/schema.ts';
import { TestProviders } from '@/test/providers.tsx';

import { NoteRow } from './NoteRow.tsx';

/**
 * A checklist's row: the glyph, the open items as the snippet, the progress
 * in the meta line — counted from the snippet, which is the body's first 500
 * runes and so only a floor when it was cut.
 */
function mount(note: NoteWire) {
  render(
    <TestProviders>
      <MemoryRouter>
        <NoteRow note={note} />
      </MemoryRouter>
    </TestProviders>,
  );
  return screen.getByRole('button', { name: /shopping/i });
}

describe('NoteRow for a checklist', () => {
  it('wears the glyph, shows the open items plainly, and says how far along it is', () => {
    const row = mount(noteRowChecklist);
    expect(row.querySelector('.note-row__kind')).not.toBeNull();
    expect(within(row).getByText('Milk · Bread and butter for the weekend')).toBeInTheDocument();
    expect(within(row).queryByText(/\[ \]/)).toBeNull();
    expect(within(row).getByText('1 of 3 done')).toHaveClass('numeric');
  });

  it('marks the total as a floor when the snippet was cut', () => {
    const items = Array.from({ length: 40 }, (_, index) => `- [${index % 2 ? 'x' : ' '}] Item ${String(index + 1)} of a long list`);
    const body = items.join('\n');
    const snippet = `${Array.from(body).slice(0, 500).join('')}...`;
    const row = mount({ ...noteRowChecklist, snippet });
    expect(within(row).getByText(/^\d+ of \d+\+ done$/)).toBeInTheDocument();
  });

  it('a plain note is untouched', () => {
    // Unpinned as well: the generated checklist fixture is pinned, and the
    // pin glyph shares the checklist glyph's slot (`.note-row__kind`).
    const row = mount({ ...noteRowChecklist, kind: 'note', pinned: false, snippet: 'Just a sentence.' });
    expect(row.querySelector('.note-row__kind')).toBeNull();
    expect(within(row).getByText('Just a sentence.')).toBeInTheDocument();
    expect(within(row).queryByText(/done$/)).toBeNull();
  });
});
