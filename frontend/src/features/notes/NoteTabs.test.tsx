import { render } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { NoteTabList, type NoteTabDescriptor } from './NoteTabs.tsx';

/**
 * The strip's one pill (R8 F4): where it sits is `--tab-index` of
 * `--tab-count` on the list, and `notes.css` turns that into a translate.
 */

const THREE: NoteTabDescriptor[] = [
  { id: 'text', label: 'Text' },
  { id: 'cleaned', label: 'Cleaned' },
  { id: 'recordings', label: 'Recordings', count: 2 },
];

function pill(container: HTMLElement) {
  const list = container.querySelector<HTMLElement>('[role="tablist"]')!;
  const indicator = list.querySelector('[data-swipe-indicator]');
  return {
    indicator,
    index: list.style.getPropertyValue('--tab-index'),
    count: list.style.getPropertyValue('--tab-count'),
  };
}

describe('NoteTabList', () => {
  it('draws one hidden pill that follows the selected tab', () => {
    const { container, rerender } = render(
      <NoteTabList noteId="n" tabs={THREE} value="text" onChange={vi.fn()} />,
    );
    let at = pill(container);
    expect(at.indicator).toHaveAttribute('aria-hidden', 'true');
    expect(at).toMatchObject({ index: '0', count: '3' });

    rerender(<NoteTabList noteId="n" tabs={THREE} value="recordings" onChange={vi.fn()} />);
    at = pill(container);
    expect(at).toMatchObject({ index: '2', count: '3' });
    // Decoration, not a tab.
    expect(container.querySelectorAll('[role="tab"]')).toHaveLength(3);
  });

  it('fits a strip of two', () => {
    const two = THREE.filter((tab) => tab.id !== 'cleaned');
    const { container } = render(
      <NoteTabList noteId="n" tabs={two} value="recordings" onChange={vi.fn()} />,
    );
    expect(pill(container)).toMatchObject({ index: '1', count: '2' });
  });
});
