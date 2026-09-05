import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createElement, useReducer } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { FindBar, markTree } from './FindBar.tsx';
import { FIND_CLOSED, findReducer } from './find.ts';
import { renderMarkdown } from './markdown.ts';

/**
 * The bar over its reducer, with a panel of a fixed twelve matches standing in
 * for the text: what the count says, how Enter and the arrows move it, and
 * what Escape does.
 */
function Harness({ total, onClose }: { total: number; onClose: () => void }) {
  const [state, dispatch] = useReducer(findReducer, { ...FIND_CLOSED, open: true, total });
  return createElement(FindBar, {
    id: 'find',
    query: state.query,
    active: state.active,
    total: state.total,
    onQueryChange: (query: string) => {
      dispatch({ type: 'query', query });
      // The panel would report its count; here it is always the same.
      dispatch({ type: 'total', total: query === '' ? 0 : total });
    },
    onNext: () => {
      dispatch({ type: 'next' });
    },
    onPrevious: () => {
      dispatch({ type: 'previous' });
    },
    onClose,
  });
}

describe('the find bar', () => {
  it('counts "n of total", steps with Enter and the arrows, and wraps both ways', async () => {
    const user = userEvent.setup();
    render(<Harness total={12} onClose={vi.fn()} />);

    const input = screen.getByRole('searchbox', { name: 'Find in note' });
    expect(input).toHaveFocus();
    expect(screen.getByRole('status')).toHaveTextContent('');

    await user.type(input, 'tile');
    expect(screen.getByRole('status')).toHaveTextContent('1 of 12');

    await user.keyboard('{Enter}{Enter}');
    expect(screen.getByRole('status')).toHaveTextContent('3 of 12');

    await user.keyboard('{Shift>}{Enter}{/Shift}');
    expect(screen.getByRole('status')).toHaveTextContent('2 of 12');

    await user.click(screen.getByRole('button', { name: 'Previous match' }));
    await user.click(screen.getByRole('button', { name: 'Previous match' }));
    // Wrapped from the first to the last.
    expect(screen.getByRole('status')).toHaveTextContent('12 of 12');

    await user.click(screen.getByRole('button', { name: 'Next match' }));
    expect(screen.getByRole('status')).toHaveTextContent('1 of 12');
  });

  it('says so when there is nothing, and the arrows are inert', async () => {
    const user = userEvent.setup();
    render(<Harness total={0} onClose={vi.fn()} />);

    await user.type(screen.getByRole('searchbox'), 'zzz');
    expect(screen.getByRole('status')).toHaveTextContent('No matches');
    expect(screen.getByRole('button', { name: 'Next match' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Previous match' })).toBeDisabled();
  });

  it('closes on Escape and on the close control', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<Harness total={3} onClose={onClose} />);

    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', { name: 'Close find' }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it('stands greyed with the hint where the panel has nothing to search', () => {
    render(
      <FindBar
        id="find"
        query="tile"
        active={0}
        total={0}
        disabled
        hint="Search works in Text and Cleaned."
        onQueryChange={vi.fn()}
        onNext={vi.fn()}
        onPrevious={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByRole('searchbox')).toBeDisabled();
    expect(screen.getByText('Search works in Text and Cleaned.')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('');
  });
});

/**
 * The cleaned view is a tree of elements; matches are marked where they stand
 * and numbered in document order, so "2 of 3" in the bar is the second mark
 * on the page.
 */
describe('marking a rendered document', () => {
  const source = '# Roof repair\n\n- Ridge **tiles** have slipped\n- Tiles again, and tiles';

  it('marks every match in place, numbers them in order, and flags the active one', () => {
    const { nodes, total } = markTree(renderMarkdown(source), 'tiles', 1);
    expect(total).toBe(3);
    const { container } = render(<div>{nodes}</div>);

    const marks = Array.from(container.querySelectorAll('mark'));
    expect(marks.map((mark) => mark.textContent)).toEqual(['tiles', 'Tiles', 'tiles']);
    expect(marks.map((mark) => mark.hasAttribute('data-active'))).toEqual([false, true, false]);
    // In place: the first is still inside its <strong>, the shape is intact.
    expect(marks[0]?.parentElement?.tagName).toBe('STRONG');
    expect(container.querySelector('h2')).toHaveTextContent('Roof repair');
    expect(container.querySelectorAll('li')).toHaveLength(2);
    expect(container.textContent).toBe('Roof repairRidge tiles have slippedTiles again, and tiles');
  });

  it('marks in Malayalam as readily as in Latin script', () => {
    const { nodes, total } = markTree(renderMarkdown('## കുറിപ്പ്\n\nമലയാളം ഒരു ഭാഷ. **മലയാളം**.'), 'മലയാളം', 0);
    expect(total).toBe(2);
    const { container } = render(<div>{nodes}</div>);
    expect(container.querySelectorAll('mark')).toHaveLength(2);
  });

  it('leaves the tree untouched for an empty query or no match', () => {
    const rendered = renderMarkdown(source);
    expect(markTree(rendered, '', 0)).toEqual({ nodes: rendered, total: 0 });
    const { nodes, total } = markTree(rendered, 'gutter', 0);
    expect(total).toBe(0);
    const { container } = render(<div>{nodes}</div>);
    expect(container.querySelector('mark')).toBeNull();
  });
});
