import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it } from 'vitest';

import { PATHS } from './Icon.tsx';
import { TabBar } from './TabBar.tsx';

function mount(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <TabBar />
    </MemoryRouter>,
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
