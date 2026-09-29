import { act, fireEvent, render, screen } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  SWIPE_COMMIT_FRACTION,
  SWIPE_FLICK_PX_PER_MS,
  SWIPE_RUBBER,
  SWIPE_SLOP_PX,
  useHorizontalSwipe,
  type SwipeDirection,
} from './useHorizontalSwipe.ts';

/**
 * The gesture, driven with pointer events the way a finger drives it, as
 * `SwipeRow.test.tsx` does. jsdom measures nothing, so the region is as wide
 * as the window (1024 px): a step is 30 % of that. Velocity comes from the
 * events' timestamps, which jsdom takes from the clock, so the clock is fake
 * and each move is a deliberate number of milliseconds after the last. The
 * hook writes `--tab-swipe-x` on the region itself; `offsetOf` reads it back.
 */

const WIDTH = 1024;
const COMMIT_PX = WIDTH * SWIPE_COMMIT_FRACTION;

function Region({
  canGo = () => true,
  onSwipe,
  onTap = vi.fn(),
  rowOpen = false,
}: {
  canGo?: (direction: SwipeDirection) => boolean;
  onSwipe: (direction: SwipeDirection) => void;
  onTap?: () => void;
  /** The `.swipe` row starts open, its tray uncovered by an earlier drag. */
  rowOpen?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const swipe = useHorizontalSwipe({ ref, canGo, onSwipe });
  return (
    <div ref={ref} data-testid="region" data-swiping={swipe.dragging || undefined} {...swipe.handlers}>
      <div className="swipe" data-open={rowOpen || undefined}>
        <button type="button" onClick={onTap}>
          row
        </button>
      </div>
      <button type="button" onClick={onTap}>
        panel
      </button>
    </div>
  );
}

const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

/** A finger down at `x`, then moves `ms` apart, then up unless told otherwise. */
function drag(
  el: Element,
  moves: number[],
  { x = 300, y = 10, ms = 100, lift = true, pointerType = 'touch' } = {},
): void {
  const pointer = { ...touch, pointerType };
  fireEvent.pointerDown(el, { ...pointer, clientX: x, clientY: y });
  let at = x;
  for (const dx of moves) {
    act(() => {
      vi.advanceTimersByTime(ms);
    });
    at += dx;
    fireEvent.pointerMove(el, { ...pointer, clientX: at, clientY: y + 2 });
  }
  if (lift) fireEvent.pointerUp(el, { ...pointer, clientX: at, clientY: y + 2 });
}

const region = () => screen.getByTestId('region');
const offsetOf = () => region().style.getPropertyValue('--tab-swipe-x');

beforeEach(() => {
  vi.useFakeTimers();
  Object.defineProperty(window, 'innerWidth', { value: WIDTH, configurable: true });
});

afterEach(() => {
  vi.useRealTimers();
});

describe('useHorizontalSwipe', () => {
  it('steps once let go past 30 % of the width, taking the pointer for the region on the way', () => {
    const onSwipe = vi.fn();
    const capture = vi.fn();
    HTMLElement.prototype.setPointerCapture = capture;
    render(<Region onSwipe={onSwipe} />);
    // The first move past the slop decides the axis and re-anchors there.
    drag(screen.getByText('panel'), [-SWIPE_SLOP_PX - 8, -COMMIT_PX]);
    expect(onSwipe).toHaveBeenCalledTimes(1);
    expect(onSwipe).toHaveBeenCalledWith('left');
    expect(capture).toHaveBeenCalledWith(1);
    expect(region()).not.toHaveAttribute('data-swiping');
  });

  it('steps on a flick short of 30 %', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    // 40 px every 10 ms: 4 px/ms, ten times the flick.
    const step = -Math.ceil(SWIPE_FLICK_PX_PER_MS * 10 * 10);
    drag(screen.getByText('panel'), [-SWIPE_SLOP_PX - 8, step, step], { ms: 10 });
    expect(onSwipe).toHaveBeenCalledWith('left');
  });

  it('snaps back from a short, slow drag', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    // 60 px in 400 ms: well under the flick.
    drag(screen.getByText('panel'), [-SWIPE_SLOP_PX - 8, -60], { ms: 400 });
    expect(onSwipe).not.toHaveBeenCalled();
    // The snap back is the attribute's removal; the property is left where
    // the finger let go and applies only under it.
    expect(region()).not.toHaveAttribute('data-swiping');
  });

  it('follows at a quarter of the distance where there is no neighbour, and never steps there', () => {
    const onSwipe = vi.fn();
    render(<Region canGo={() => false} onSwipe={onSwipe} />);
    drag(screen.getByText('panel'), [-SWIPE_SLOP_PX - 8, -COMMIT_PX], { lift: false });
    expect(region()).toHaveAttribute('data-swiping');
    expect(offsetOf()).toBe(`${String(-COMMIT_PX * SWIPE_RUBBER)}px`);
    fireEvent.pointerUp(region(), { ...touch, clientX: 0, clientY: 12 });
    expect(onSwipe).not.toHaveBeenCalled();
  });

  it('is not for a mouse', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    drag(screen.getByText('panel'), [-SWIPE_SLOP_PX - 8, -COMMIT_PX], { pointerType: 'mouse' });
    expect(onSwipe).not.toHaveBeenCalled();
    expect(region()).not.toHaveAttribute('data-swiping');
  });

  it('leaves a drag from the screen edge to the system', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    drag(screen.getByText('panel'), [SWIPE_SLOP_PX + 8, COMMIT_PX], { x: 10 });
    expect(onSwipe).not.toHaveBeenCalled();
  });

  it('leaves a left drag on a closed swipe row to its tray, and takes a right one', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    drag(screen.getByText('row'), [-SWIPE_SLOP_PX - 8, -COMMIT_PX]);
    expect(onSwipe).not.toHaveBeenCalled();
    drag(screen.getByText('row'), [SWIPE_SLOP_PX + 8, COMMIT_PX]);
    expect(onSwipe).toHaveBeenCalledWith('right');
  });

  it('leaves both directions on an open swipe row to its close gesture', () => {
    const onSwipe = vi.fn();
    const capture = vi.fn();
    HTMLElement.prototype.setPointerCapture = capture;
    render(<Region rowOpen onSwipe={onSwipe} />);
    drag(screen.getByText('row'), [SWIPE_SLOP_PX + 8, COMMIT_PX]);
    drag(screen.getByText('row'), [-SWIPE_SLOP_PX - 8, -COMMIT_PX]);
    expect(onSwipe).not.toHaveBeenCalled();
    // Never taking the pointer either: doing so fired the row's own
    // `lostpointercapture`, which settled its tray back open.
    expect(capture).not.toHaveBeenCalled();
    expect(region()).not.toHaveAttribute('data-swiping');
  });

  it('snaps back without stepping when the pointer is cancelled mid-swipe, and swallows no click', () => {
    const onSwipe = vi.fn();
    const onTap = vi.fn();
    render(<Region onSwipe={onSwipe} onTap={onTap} />);
    const panel = screen.getByText('panel');
    drag(panel, [-SWIPE_SLOP_PX - 8, -COMMIT_PX], { lift: false });
    expect(region()).toHaveAttribute('data-swiping');
    fireEvent.pointerCancel(region(), { ...touch });
    expect(onSwipe).not.toHaveBeenCalled();
    expect(region()).not.toHaveAttribute('data-swiping');
    fireEvent.click(panel, { detail: 1 });
    expect(onTap).toHaveBeenCalledTimes(1);
  });

  it('swallows the click that follows a swipe, once', () => {
    const onTap = vi.fn();
    render(<Region onSwipe={vi.fn()} onTap={onTap} />);
    const panel = screen.getByText('panel');
    drag(panel, [-SWIPE_SLOP_PX - 8, -COMMIT_PX]);
    // A tap's click carries `detail` 1; jsdom's default is 0, a keyboard's.
    fireEvent.click(panel, { detail: 1 });
    expect(onTap).not.toHaveBeenCalled();
    fireEvent.click(panel, { detail: 1 });
    expect(onTap).toHaveBeenCalledTimes(1);
  });

  it('lets a keyboard activation through after a swipe', () => {
    const onTap = vi.fn();
    render(<Region onSwipe={vi.fn()} onTap={onTap} />);
    const panel = screen.getByText('panel');
    drag(panel, [-SWIPE_SLOP_PX - 8, -COMMIT_PX]);
    // Enter on a focused button: a click with no pointer before it and
    // `detail` 0. Chromium fires no click after a moved touch, so without
    // this the flag ate the next activation.
    fireEvent.click(panel, { detail: 0 });
    expect(onTap).toHaveBeenCalledTimes(1);
  });
});
