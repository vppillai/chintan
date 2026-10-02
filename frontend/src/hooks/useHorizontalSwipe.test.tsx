import { act, createEvent, fireEvent, render, screen } from '@testing-library/react';
import { useRef, useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { GESTURE_SLOP_PX } from './gesture.ts';
import {
  SWIPE_COMMIT_FRACTION,
  SWIPE_FLICK_PX_PER_MS,
  SWIPE_RUBBER_FRACTION,
  useHorizontalSwipe,
  type SwipeDirection,
} from './useHorizontalSwipe.ts';

/**
 * The gesture, driven with pointer events the way a finger drives it, as
 * `SwipeRow.test.tsx` does. jsdom measures nothing, so the region is as wide
 * as the window (1024 px): a step is 30 % of that. Velocity comes from the
 * events' timestamps, which jsdom takes from the clock, so the clock is fake
 * and each move is a deliberate number of milliseconds after the last (the
 * fake clock drives `requestAnimationFrame` too). The hook writes `translate`
 * and `opacity` on the panel and `--tab-progress` on the pill.
 */

const WIDTH = 1024;
const COMMIT_PX = WIDTH * SWIPE_COMMIT_FRACTION;

function Region({
  canGo = () => true,
  onSwipe,
  onTap = vi.fn(),
  rowOpen = false,
  steps = true,
}: {
  canGo?: (direction: SwipeDirection) => boolean;
  onSwipe: (direction: SwipeDirection) => void;
  onTap?: () => void;
  /** The `.swipe` row starts open, its tray uncovered by an earlier drag. */
  rowOpen?: boolean;
  /** A step commits a new segment; false holds it back, as a late router commit does. */
  steps?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState(0);
  const swipe = useHorizontalSwipe({
    ref,
    at,
    canGo,
    onSwipe: (direction) => {
      onSwipe(direction);
      if (steps) setAt((was) => was + 1);
    },
  });
  return (
    <div ref={ref} data-testid="region" data-swiping={swipe.dragging || undefined} {...swipe.handlers}>
      <span data-testid="pill" data-swipe-indicator />
      <div data-testid="panel" data-swipe-panel>
        <div className="swipe" data-open={rowOpen || undefined}>
          <button type="button" onClick={onTap}>
            row
          </button>
        </div>
        <button type="button" onClick={onTap}>
          panel
        </button>
      </div>
    </div>
  );
}

const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

/**
 * A pointer event stamped with the fake clock. The hook reads
 * `event.timeStamp`, which jsdom takes from its own `Date.now()`; under the
 * threads pool that is the faked one, in a vm realm it is the real clock, so
 * the stamp is set on the event itself and the test means the same in both.
 */
function pointer(
  kind: 'pointerDown' | 'pointerMove' | 'pointerUp' | 'pointerCancel',
  el: Element,
  init: Record<string, unknown>,
): void {
  const event = createEvent[kind](el, init);
  Object.defineProperty(event, 'timeStamp', { value: Date.now() });
  fireEvent(el, event);
}

/** A finger down at `x`, then moves `ms` apart, then up unless told otherwise. */
function drag(
  el: Element,
  moves: number[],
  { x = 300, y = 10, ms = 100, lift = true, pointerType = 'touch' } = {},
): void {
  const pointerInit = { ...touch, pointerType };
  pointer('pointerDown', el, { ...pointerInit, clientX: x, clientY: y });
  let at = x;
  for (const dx of moves) {
    act(() => {
      vi.advanceTimersByTime(ms);
    });
    at += dx;
    pointer('pointerMove', el, { ...pointerInit, clientX: at, clientY: y + 2 });
  }
  if (lift) pointer('pointerUp', el, { ...pointerInit, clientX: at, clientY: y + 2 });
}

const region = () => screen.getByTestId('region');
const panelOf = () => screen.getByTestId('panel');
const offsetOf = () => panelOf().style.getPropertyValue('translate');
const progressOf = () => screen.getByTestId('pill').style.getPropertyValue('--tab-progress');
/** The two frames after a step. */
const twoFrames = () => {
  act(() => {
    vi.advanceTimersByTime(40);
  });
};

beforeEach(() => {
  // The frames too: jsdom's own `requestAnimationFrame` runs on whichever
  // realm's timers it was built in, which a vm pool does not fake.
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date', 'requestAnimationFrame', 'cancelAnimationFrame'] });
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
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -COMMIT_PX]);
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
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, step, step], { ms: 10 });
    expect(onSwipe).toHaveBeenCalledWith('left');
  });

  it('reads no flick from a fast move that a still finger outlived', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    // Four fast moves to 16 % of the width — short of the commit line — then
    // the finger holds still, which fires no move, and lifts: the velocity
    // in hand is the last fast move's, and it is not the lift's.
    const step = -Math.ceil(SWIPE_FLICK_PX_PER_MS * 10 * 10);
    const moves = [-GESTURE_SLOP_PX - 8, step, step, step, step];
    drag(screen.getByText('panel'), moves, { ms: 10, lift: false });
    act(() => {
      vi.advanceTimersByTime(300);
    });
    const at = 300 + moves.reduce((sum, dx) => sum + dx, 0);
    pointer('pointerUp', region(), { ...touch, clientX: at, clientY: 12 });
    expect(onSwipe).not.toHaveBeenCalled();
    expect(region()).not.toHaveAttribute('data-swiping');
  });

  it('snaps back from a short, slow drag', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    // 60 px in 400 ms: well under the flick.
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -60], { ms: 400 });
    expect(onSwipe).not.toHaveBeenCalled();
    // The snap back is the attribute's removal with the inline pose cleared.
    expect(region()).not.toHaveAttribute('data-swiping');
  });

  it('follows the finger 1:1 past 40 % of the width, fading as it goes', () => {
    render(<Region onSwipe={vi.fn()} />);
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -500], { lift: false });
    expect(offsetOf()).toBe('-500px');
    expect(Number(panelOf().style.opacity)).toBeCloseTo(1 - (0.4 * 500) / WIDTH);
    // The pill moves toward the neighbour by the same share of a tab.
    expect(Number(progressOf())).toBeCloseTo(500 / WIDTH);
  });

  it('rubber-bands where there is no neighbour, never reaching 15 % of the width, and never steps there', () => {
    const onSwipe = vi.fn();
    render(<Region canGo={() => false} onSwipe={onSwipe} />);
    const r = WIDTH * SWIPE_RUBBER_FRACTION;
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -100], { lift: false });
    expect(parseFloat(offsetOf())).toBeCloseTo(-r * (1 - 1 / (1 + 100 / r)));
    act(() => {
      vi.advanceTimersByTime(100);
    });
    fireEvent.pointerMove(region(), { ...touch, clientX: -5000, clientY: 12 });
    expect(Math.abs(parseFloat(offsetOf()))).toBeLessThan(r);
    fireEvent.pointerUp(region(), { ...touch, clientX: -5000, clientY: 12 });
    expect(onSwipe).not.toHaveBeenCalled();
    expect(region()).not.toHaveAttribute('data-tab-enter');
  });

  it('poses the new panel on the far side when the step commits, then lets it settle two frames later', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    // 400 px over two seconds: far enough, and no flick.
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -400], { ms: 2000 });
    expect(onSwipe).toHaveBeenCalledWith('left');
    expect(region()).toHaveAttribute('data-tab-enter');
    expect(offsetOf()).toBe(`${String(WIDTH - 400)}px`);
    expect(progressOf()).toBe('');
    // A drag, not a flick: the settle runs at the base token.
    expect(panelOf().style.transitionDuration).toBe('');
    twoFrames();
    expect(region()).not.toHaveAttribute('data-tab-enter');
    expect(offsetOf()).toBe('');
    expect(panelOf().style.opacity).toBe('');
  });

  it('holds the old panel where the finger left it until the new tab commits', () => {
    // A tab in the URL changes through the router in a transition, a frame
    // late; posing at pointerup showed the old panel at the enter pose.
    render(<Region steps={false} onSwipe={vi.fn()} />);
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -400], { ms: 2000 });
    expect(region()).not.toHaveAttribute('data-tab-enter');
    expect(offsetOf()).toBe('-400px');
    expect(Number(progressOf())).toBeCloseTo(400 / WIDTH);
  });

  it('enters from the left on a right step', () => {
    render(<Region onSwipe={vi.fn()} />);
    drag(screen.getByText('panel'), [GESTURE_SLOP_PX + 8, 400], { ms: 2000 });
    expect(offsetOf()).toBe(`${String(400 - WIDTH)}px`);
  });

  it('settles at the fast token after a flick', () => {
    render(<Region onSwipe={vi.fn()} />);
    const step = -Math.ceil(SWIPE_FLICK_PX_PER_MS * 10 * 10);
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, step, step], { ms: 10 });
    expect(region()).toHaveAttribute('data-tab-enter');
    expect(panelOf().style.transitionDuration).toBe('var(--motion-duration-fast)');
    expect(screen.getByTestId('pill').style.transitionDuration).toBe('var(--motion-duration-fast)');
    // Cleared by the backstop when no `transitionend` comes (jsdom runs none).
    act(() => {
      vi.advanceTimersByTime(600);
    });
    expect(panelOf().style.transitionDuration).toBe('');
  });

  it('snaps back with no enter pose and clears what it wrote', () => {
    render(<Region onSwipe={vi.fn()} />);
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -60], { ms: 400 });
    expect(region()).not.toHaveAttribute('data-tab-enter');
    expect(offsetOf()).toBe('');
    expect(progressOf()).toBe('');
  });

  it('lands an entering panel at rest when a new finger comes down', () => {
    render(<Region onSwipe={vi.fn()} />);
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -400], { ms: 2000 });
    expect(region()).toHaveAttribute('data-tab-enter');
    fireEvent.pointerDown(screen.getByText('panel'), { ...touch, clientX: 300, clientY: 10 });
    expect(region()).not.toHaveAttribute('data-tab-enter');
    expect(offsetOf()).toBe('');
  });

  it('reads the width once per gesture', () => {
    const width = vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(390);
    render(<Region onSwipe={vi.fn()} />);
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -10, -10, -10, -10], { lift: false });
    // Once, by the region, at the axis.
    expect(width).toHaveBeenCalledTimes(1);
    width.mockRestore();
  });

  it('is not for a mouse', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    drag(screen.getByText('panel'), [-GESTURE_SLOP_PX - 8, -COMMIT_PX], { pointerType: 'mouse' });
    expect(onSwipe).not.toHaveBeenCalled();
    expect(region()).not.toHaveAttribute('data-swiping');
  });

  it('leaves a drag from the screen edge to the system', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    drag(screen.getByText('panel'), [GESTURE_SLOP_PX + 8, COMMIT_PX], { x: 10 });
    expect(onSwipe).not.toHaveBeenCalled();
  });

  it('leaves a left drag on a closed swipe row to its tray, and takes a right one', () => {
    const onSwipe = vi.fn();
    render(<Region onSwipe={onSwipe} />);
    drag(screen.getByText('row'), [-GESTURE_SLOP_PX - 8, -COMMIT_PX]);
    expect(onSwipe).not.toHaveBeenCalled();
    drag(screen.getByText('row'), [GESTURE_SLOP_PX + 8, COMMIT_PX]);
    expect(onSwipe).toHaveBeenCalledWith('right');
  });

  it('leaves both directions on an open swipe row to its close gesture', () => {
    const onSwipe = vi.fn();
    const capture = vi.fn();
    HTMLElement.prototype.setPointerCapture = capture;
    render(<Region rowOpen onSwipe={onSwipe} />);
    drag(screen.getByText('row'), [GESTURE_SLOP_PX + 8, COMMIT_PX]);
    drag(screen.getByText('row'), [-GESTURE_SLOP_PX - 8, -COMMIT_PX]);
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
    drag(panel, [-GESTURE_SLOP_PX - 8, -COMMIT_PX], { lift: false });
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
    drag(panel, [-GESTURE_SLOP_PX - 8, -COMMIT_PX]);
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
    drag(panel, [-GESTURE_SLOP_PX - 8, -COMMIT_PX]);
    // Enter on a focused button: a click with no pointer before it and
    // `detail` 0. Chromium fires no click after a moved touch, so without
    // this the flag ate the next activation.
    fireEvent.click(panel, { detail: 0 });
    expect(onTap).toHaveBeenCalledTimes(1);
  });
});
