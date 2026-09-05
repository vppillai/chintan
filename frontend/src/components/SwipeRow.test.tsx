import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { LONG_PRESS_MS } from '@/hooks/useLongPress.ts';
import { SWIPE_RUBBER_PX, rubberBand } from '@/hooks/useSwipeActions.ts';
import { setCanHover } from '@/test/setup.ts';

import { SWIPE_ACTION_FALLBACK_PX, SwipeRow, type SwipeAction } from './SwipeRow.tsx';

/**
 * The gesture, driven with pointer events the way a finger drives it. jsdom
 * measures nothing, so the tray is the fallback width — two actions, 144 px —
 * and the open threshold is half of that.
 */

const WIDTH = 2 * SWIPE_ACTION_FALLBACK_PX;

function actions(onArchive = vi.fn(), onDelete = vi.fn()): SwipeAction[] {
  return [
    { id: 'archive', label: 'Archive', icon: 'archive', onSelect: onArchive },
    { id: 'delete', label: 'Delete', icon: 'trash', destructive: true, onSelect: onDelete },
  ];
}

function Row({
  list = actions(),
  onTap = vi.fn(),
  disabled = false,
  name = 'row',
}: {
  list?: SwipeAction[];
  onTap?: () => void;
  disabled?: boolean;
  name?: string;
}) {
  return (
    <SwipeRow actions={list} disabled={disabled} label={`Actions for ${name}`}>
      <button type="button" onClick={onTap}>
        {name}
      </button>
    </SwipeRow>
  );
}

const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

/** Finger down, past the slop, then on to `dx`, then up. */
function drag(el: Element, dx: number, { lift = true } = {}): void {
  fireEvent.pointerDown(el, { ...touch, clientX: 300, clientY: 10 });
  // The first move past the slop commits the axis and re-anchors at 280.
  fireEvent.pointerMove(el, { ...touch, clientX: 280, clientY: 12 });
  fireEvent.pointerMove(el, { ...touch, clientX: 280 + dx, clientY: 12 });
  if (lift) fireEvent.pointerUp(el, { ...touch, clientX: 280 + dx, clientY: 12 });
}

function swipe(name: string): HTMLElement {
  return screen.getByRole('button', { name }).closest('.swipe') as HTMLElement;
}

afterEach(() => {
  vi.useRealTimers();
});

describe('SwipeRow', () => {
  it('opens past half the tray and reports the tray to assistive technology only then', () => {
    render(<Row />);
    const row = swipe('row');
    // Closed, the tray has no accessible name to query by: hidden things do not.
    const tray = screen.getByRole('group', { hidden: true });
    expect(tray).toHaveAttribute('aria-hidden', 'true');
    expect(tray).toHaveAttribute('inert');

    drag(row, -100);

    expect(screen.getByRole('group', { name: 'Actions for row' })).toBe(tray);
    expect(row).toHaveAttribute('data-open');
    expect(row.style.getPropertyValue('--swipe-x')).toBe(`${String(-WIDTH)}px`);
    expect(tray).toHaveAttribute('aria-hidden', 'false');
    expect(tray).not.toHaveAttribute('inert');
    expect(screen.getByRole('button', { name: 'Archive' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument();
  });

  it('snaps back under the threshold, and the drag was not a tap', () => {
    const onTap = vi.fn();
    render(<Row onTap={onTap} />);
    const row = swipe('row');

    drag(row, -40);
    // The finger lifting fires the browser's click on the button underneath.
    fireEvent.click(screen.getByRole('button', { name: 'row' }));

    expect(row).not.toHaveAttribute('data-open');
    expect(row.style.getPropertyValue('--swipe-x')).toBe('0px');
    expect(onTap).not.toHaveBeenCalled();

    // The next tap is a tap again.
    fireEvent.click(screen.getByRole('button', { name: 'row' }));
    expect(onTap).toHaveBeenCalledTimes(1);
  });

  it('follows the finger while dragging and rubber-bands past the tray', () => {
    render(<Row />);
    const row = swipe('row');

    drag(row, -60, { lift: false });
    expect(row).toHaveAttribute('data-dragging');
    expect(row.style.getPropertyValue('--swipe-x')).toBe('-60px');

    fireEvent.pointerMove(row, { ...touch, clientX: 280 - WIDTH - 200, clientY: 12 });
    const pulled = Number.parseFloat(row.style.getPropertyValue('--swipe-x'));
    expect(pulled).toBeLessThan(-WIDTH);
    expect(pulled).toBeGreaterThanOrEqual(-(WIDTH + SWIPE_RUBBER_PX));

    fireEvent.pointerUp(row, { ...touch, clientX: 0, clientY: 12 });
    expect(row).not.toHaveAttribute('data-dragging');
    expect(row.style.getPropertyValue('--swipe-x')).toBe(`${String(-WIDTH)}px`);
  });

  it('is a scroll, not a swipe, when the finger moves more down than across', () => {
    render(<Row />);
    const row = swipe('row');

    fireEvent.pointerDown(row, { ...touch, clientX: 300, clientY: 10 });
    fireEvent.pointerMove(row, { ...touch, clientX: 290, clientY: 40 });
    fireEvent.pointerMove(row, { ...touch, clientX: 100, clientY: 60 });
    fireEvent.pointerUp(row, { ...touch, clientX: 100, clientY: 60 });

    expect(row).not.toHaveAttribute('data-open');
    expect(row.style.getPropertyValue('--swipe-x')).toBe('0px');
  });

  it('does not start from a finger that has held still for a long press', () => {
    vi.useFakeTimers();
    render(<Row />);
    const row = swipe('row');

    fireEvent.pointerDown(row, { ...touch, clientX: 300, clientY: 10 });
    act(() => {
      vi.advanceTimersByTime(LONG_PRESS_MS + 20);
    });
    fireEvent.pointerMove(row, { ...touch, clientX: 200, clientY: 10 });
    fireEvent.pointerMove(row, { ...touch, clientX: 100, clientY: 10 });
    fireEvent.pointerUp(row, { ...touch, clientX: 100, clientY: 10 });

    expect(row).not.toHaveAttribute('data-open');
  });

  it('is not wedged by a pen that drifts onto the next row and lifts there', () => {
    render(
      <>
        <Row name="first" />
        <Row name="second" />
      </>,
    );
    const first = swipe('first');
    const pen = { pointerId: 7, pointerType: 'pen', button: 0 };

    // Down on the first row, a nudge under the slop, out over the second row,
    // and up there: no pointerup ever reaches the first row.
    fireEvent.pointerDown(first, { ...pen, clientX: 300, clientY: 10 });
    fireEvent.pointerMove(first, { ...pen, clientX: 298, clientY: 15 });
    fireEvent.pointerLeave(first, { ...pen, clientX: 298, clientY: 60 });
    fireEvent.pointerUp(swipe('second'), { ...pen, clientX: 298, clientY: 60 });

    // The next finger on the first row is a swipe as usual.
    drag(first, -100);
    expect(first).toHaveAttribute('data-open');
  });

  it('keeps following a committed drag after the pointer leaves the row, and lets go when capture is lost', () => {
    render(<Row />);
    const row = swipe('row');

    drag(row, -60, { lift: false });
    // Captured, so the pointer wandering off the row's box is not a leave that counts.
    fireEvent.pointerLeave(row, { ...touch, clientX: 220, clientY: 200 });
    fireEvent.pointerMove(row, { ...touch, clientX: 280 - 100, clientY: 200 });
    expect(row.style.getPropertyValue('--swipe-x')).toBe('-100px');

    // The browser takes the pointer away without an up or a cancel.
    fireEvent.lostPointerCapture(row, { ...touch });
    expect(row).not.toHaveAttribute('data-dragging');
    expect(row).not.toHaveAttribute('data-open');
    expect(row.style.getPropertyValue('--swipe-x')).toBe('0px');
  });

  it('keeps one row open at a time', () => {
    render(
      <>
        <Row name="first" />
        <Row name="second" />
      </>,
    );
    const first = swipe('first');
    const second = swipe('second');

    drag(first, -100);
    expect(first).toHaveAttribute('data-open');

    drag(second, -100);
    expect(second).toHaveAttribute('data-open');
    expect(first).not.toHaveAttribute('data-open');
    expect(first.style.getPropertyValue('--swipe-x')).toBe('0px');
  });

  it('fires the action and closes; a tap on the open row only closes it', () => {
    const onArchive = vi.fn();
    const onTap = vi.fn();
    render(<Row list={actions(onArchive)} onTap={onTap} />);
    const row = swipe('row');

    drag(row, -100);
    fireEvent.click(screen.getByRole('button', { name: 'row' })); // the lift
    fireEvent.click(screen.getByRole('button', { name: 'Archive' }));
    expect(onArchive).toHaveBeenCalledTimes(1);
    expect(row).not.toHaveAttribute('data-open');

    drag(row, -100);
    fireEvent.click(screen.getByRole('button', { name: 'row' })); // the lift
    fireEvent.click(screen.getByRole('button', { name: 'row' })); // a tap on the open row
    expect(row).not.toHaveAttribute('data-open');
    expect(onTap).not.toHaveBeenCalled();
  });

  it('closes on a tap anywhere else and on a scroll', () => {
    render(
      <>
        <Row />
        <p>elsewhere</p>
      </>,
    );
    const row = swipe('row');

    drag(row, -100);
    fireEvent.pointerDown(screen.getByText('elsewhere'), { ...touch, clientX: 0, clientY: 0 });
    expect(row).not.toHaveAttribute('data-open');

    drag(row, -100);
    fireEvent.scroll(document.body);
    expect(row).not.toHaveAttribute('data-open');
  });

  it('is off for a pointer that can hover, and while disabled', () => {
    setCanHover(true);
    const { unmount } = render(<Row />);
    expect(screen.queryByRole('group', { hidden: true })).toBeNull();
    drag(swipe('row'), -100);
    expect(swipe('row')).not.toHaveAttribute('data-open');
    unmount();

    setCanHover(false);
    render(<Row disabled />);
    expect(screen.queryByRole('group', { hidden: true })).toBeNull();
    drag(swipe('row'), -100);
    expect(swipe('row')).not.toHaveAttribute('data-open');
  });

  it('pins the closed edge and damps the open one', () => {
    expect(rubberBand(30, WIDTH)).toBe(0);
    expect(rubberBand(-50, WIDTH)).toBe(-50);
    expect(rubberBand(-WIDTH, WIDTH)).toBe(-WIDTH);
    expect(rubberBand(-WIDTH - 40, WIDTH)).toBeCloseTo(-WIDTH - 14);
    expect(rubberBand(-WIDTH - 1000, WIDTH)).toBe(-(WIDTH + SWIPE_RUBBER_PX));
  });
});
