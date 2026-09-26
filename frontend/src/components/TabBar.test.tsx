import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { describe, expect, it } from 'vitest';

import { queryKeys } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { TEST_NOTES, TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import { PATHS } from './Icon.tsx';
import { TabBar } from './TabBar.tsx';

function Where() {
  const { pathname, search } = useLocation();
  return <output>{pathname + search}</output>;
}

/** Where the router is: the probe's `<output>`, read by tag because the hold overlay's live region is a status too. */
const where = () => document.querySelector('output');

/** With `note`, the open note is in the cache as the screen would have it, and answered the same. */
function mount(path: string, note?: NoteDetailWire) {
  const queryClient = testQueryClient();
  if (note) queryClient.setQueryData(queryKeys.note(note.id), note);
  const api = testApiContext(
    note
      ? async () =>
          new Response(JSON.stringify(note), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          })
      : undefined,
  );
  return render(
    <TestProviders api={api} queryClient={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <TabBar />
        <Routes>
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </TestProviders>,
  );
}

/** The glyph a tab link is drawn with, as the path data on its SVG. */
function glyphOf(link: HTMLElement): string | null {
  return link.querySelector('svg path')?.getAttribute('d') ?? null;
}

describe('the tab bar', () => {
  it('draws Home with the house glyph, not the document glyph the tab wore as Notes', () => {
    mount('/');
    const home = screen.getByRole('link', { name: 'Home' });
    expect(glyphOf(home)).toBe(PATHS.home);
    // The person for You, and nothing on the bar drawn as a page of lines.
    expect(glyphOf(screen.getByRole('link', { name: 'You' }))).toBe(PATHS.you);
    expect('notes' in PATHS).toBe(false);
  });

  it('keeps Home lit while reading a note, because the tab names the section', () => {
    mount('/notes/roof-repair');
    expect(screen.getByRole('link', { name: 'Home' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'You' })).not.toHaveAttribute('aria-current');
  });
});

/**
 * The mic is contextual: on a note it records into that note, and says so.
 * The note's own "Record into this" sat 30 px above a mic that recorded into
 * a new note (review 2026-09-21, T6); one control now, one destination.
 */
describe('the record button', () => {
  it('records into a new note from the library', async () => {
    mount('/');
    const record = screen.getByRole('button', { name: /^PTT: tap to record/ });
    expect(screen.queryByText('Into this note')).toBeNull();
    await userEvent.click(record);
    expect(where()).toHaveTextContent('/capture');
  });

  it('records into the open note, and wears its caption, while a note is on screen', async () => {
    mount('/notes/roof-repair?tab=recordings');
    const record = screen.getByRole('button', { name: /^PTT into this note/ });
    expect(screen.getByText('Into this note')).toBeInTheDocument();
    await userEvent.click(record);
    expect(where()).toHaveTextContent('/capture?note=roof-repair');
  });

  it('records into a new note from an archived one, which the server would refuse', async () => {
    const fence: NoteDetailWire = {
      ...TEST_NOTES[0]!,
      id: 'old-fence',
      title: 'Old fence',
      archived: true,
      body: '',
      captures: [],
    };
    mount('/notes/old-fence', fence);
    const record = screen.getByRole('button', { name: /^PTT: tap to record/ });
    expect(screen.queryByText('Into this note')).toBeNull();
    await userEvent.click(record);
    expect(where()).toHaveTextContent(/^\/capture$/);
  });
});
