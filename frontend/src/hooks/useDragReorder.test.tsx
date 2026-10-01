import { fireEvent, render, screen } from '@testing-library/react';
import { useRef } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { useDragReorder } from './useDragReorder.ts';

const IDS = ['a', 'b'] as const;

/** Two rows with a grip each, in the draft's order while one is lifted; the row's button is what a lift must not click. */
function List({ onOpen, onCommit = vi.fn() }: { onOpen: (id: string) => void; onCommit?: (next: string[], moved: string) => void }) {
  const listRef = useRef<HTMLUListElement>(null);
  const drag = useDragReorder({ listRef, ids: IDS, onCommit });
  return (
    <ul ref={listRef} {...drag.listHandlers}>
      {(drag.draft ?? IDS).map((id) => (
        <li key={id} data-drag-id={id}>
          <button type="button" onPointerDown={(event) => drag.start(event.pointerId, id)}>
            grip {id}
          </button>
          <button type="button" onClick={() => onOpen(id)}>
            open {id}
          </button>
        </li>
      ))}
    </ul>
  );
}

const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

/** The rows as shown. */
const order = (): string[] =>
  screen.getAllByRole('listitem').map((row) => row.getAttribute('data-drag-id') ?? '');

/** Rows 40 px tall from the top, so a pointer can stop on one slot (jsdom lays nothing out). */
function layOut(): void {
  screen.getAllByRole('listitem').forEach((row, i) => {
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue({ top: i * 40, height: 40 } as DOMRect);
  });
}

describe('useDragReorder, the click after a lift', () => {
  it('swallows the finger’s click once, and lets a keyboard activation through', () => {
    // Chromium fires no click after a touch that moved or was taken, so the
    // swallow armed at the lift waits for the next click. Enter on a row's
    // button carries `detail` 0 and is the person's own (review 2026-10-01,
    // FE-15: before the shared helper this list ate it).
    const onOpen = vi.fn();
    render(<List onOpen={onOpen} />);
    const list = screen.getByRole('list');
    const open = screen.getByRole('button', { name: 'open a' });

    fireEvent.pointerDown(screen.getByRole('button', { name: 'grip a' }), { ...touch, clientX: 0, clientY: 0 });
    fireEvent.pointerUp(list, { ...touch, clientX: 0, clientY: 0 });
    fireEvent.click(open, { detail: 0 });
    expect(onOpen).toHaveBeenCalledTimes(1);

    fireEvent.pointerDown(screen.getByRole('button', { name: 'grip a' }), { ...touch, clientX: 0, clientY: 0 });
    fireEvent.pointerUp(list, { ...touch, clientX: 0, clientY: 0 });
    fireEvent.click(open, { detail: 1 });
    expect(onOpen).toHaveBeenCalledTimes(1);
    fireEvent.click(open, { detail: 1 });
    expect(onOpen).toHaveBeenCalledTimes(2);
  });
});

/*
 * By input mode (review 2026-10-01, FE-21): the suite drove this hook with a
 * mouse and a finger only, and never took the pointer away mid-drag.
 */
describe('useDragReorder, by input mode', () => {
  it('a pen lifts, re-sorts and commits as a finger does', () => {
    const onCommit = vi.fn();
    render(<List onOpen={vi.fn()} onCommit={onCommit} />);
    const list = screen.getByRole('list');
    layOut();
    const pen = { pointerId: 7, pointerType: 'pen', button: 0 };

    fireEvent.pointerDown(screen.getByRole('button', { name: 'grip a' }), { ...pen, clientX: 10, clientY: 20 });
    fireEvent.pointerMove(list, { ...pen, clientX: 10, clientY: 61 });
    expect(order()).toEqual(['b', 'a']);
    fireEvent.pointerUp(list, { ...pen, clientX: 10, clientY: 61 });
    expect(onCommit).toHaveBeenCalledWith(['b', 'a'], 'a');
  });

  it('pointercancel drops the row where it was and writes nothing', () => {
    // The browser takes a touch for a scroll or a gesture of its own; the
    // draft under the finger must not become the list's order.
    const onCommit = vi.fn();
    render(<List onOpen={vi.fn()} onCommit={onCommit} />);
    const list = screen.getByRole('list');
    layOut();

    fireEvent.pointerDown(screen.getByRole('button', { name: 'grip a' }), { ...touch, clientX: 10, clientY: 20 });
    fireEvent.pointerMove(list, { ...touch, clientX: 10, clientY: 61 });
    expect(order()).toEqual(['b', 'a']);
    fireEvent.pointerCancel(list, { ...touch, clientX: 10, clientY: 61 });
    expect(order()).toEqual(['a', 'b']);
    expect(onCommit).not.toHaveBeenCalled();

    // Nothing is left lifted: the next finger starts a drag of its own.
    layOut();
    fireEvent.pointerDown(screen.getByRole('button', { name: 'grip b' }), { ...touch, clientX: 10, clientY: 60 });
    fireEvent.pointerMove(list, { ...touch, clientX: 10, clientY: 19 });
    expect(order()).toEqual(['b', 'a']);
    fireEvent.pointerUp(list, { ...touch, clientX: 10, clientY: 19 });
    expect(onCommit).toHaveBeenCalledWith(['b', 'a'], 'b');
  });
});
