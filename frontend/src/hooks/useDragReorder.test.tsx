import { fireEvent, render, screen } from '@testing-library/react';
import { useRef } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { useDragReorder } from './useDragReorder.ts';

const IDS = ['a', 'b'] as const;

/** Two rows with a grip each; the row's button is what a lift must not click. */
function List({ onOpen }: { onOpen: (id: string) => void }) {
  const listRef = useRef<HTMLUListElement>(null);
  const drag = useDragReorder({ listRef, ids: IDS, onCommit: vi.fn() });
  return (
    <ul ref={listRef} {...drag.listHandlers}>
      {IDS.map((id) => (
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
