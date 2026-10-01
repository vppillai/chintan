import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { describe, expect, it } from 'vitest';

import { queryKeys } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { TEST_NOTES, TestProviders, testQueryClient } from '@/test/providers.tsx';

import { BannerRecord } from './BannerRecord.tsx';

function Where() {
  const { pathname, search } = useLocation();
  return <output data-testid="where">{`${pathname}${search}`}</output>;
}

function mount(note: NoteDetailWire) {
  const queryClient = testQueryClient();
  queryClient.setQueryData(queryKeys.note(note.id), note);
  render(
    <TestProviders queryClient={queryClient}>
      <MemoryRouter initialEntries={[`/notes/${note.id}`]}>
        <BannerRecord />
        <Routes>
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </TestProviders>,
  );
}

const name = 'Record into this note';
const ROOF: NoteDetailWire = { ...TEST_NOTES[0]!, body: '', captures: [] };

/*
 * Whether it shows is CSS's (`shell.css`: the keyboard up and a note field
 * focused), and jsdom loads no CSS, so `e2e/note-tabs.spec.ts` checks that.
 * Here: when it is rendered at all, and where a tap goes.
 */
describe('the banner mic', () => {
  it('opens recording into this note, and keeps the field focused on the press', async () => {
    mount(ROOF);
    const mic = screen.getByRole('button', { name });
    // The press must not take focus from the editor, or the keyboard falls
    // and the button goes with it before the click lands.
    // `fireEvent` answers false when the default was prevented.
    expect(fireEvent.pointerDown(mic)).toBe(false);
    await userEvent.setup().click(mic);
    expect(screen.getByTestId('where')).toHaveTextContent('/capture?note=roof-repair');
  });

  it('is not there on an archived note, which takes no recordings', () => {
    mount({ ...ROOF, id: 'old-fence', archived: true });
    expect(screen.queryByRole('button', { name })).toBeNull();
  });
});
