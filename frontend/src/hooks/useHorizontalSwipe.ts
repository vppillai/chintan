import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react';

/**
 * A horizontal swipe across a region that steps between its segments — the
 * note's Text · Cleaned · Recordings — for one hand (R6-NAV-1).
 *
 * Touch and pen only; a mouse has the tabs. The region wears `touch-action:
 * pan-y pinch-zoom` (`notes.css` `.note-views`), so the browser keeps the
 * vertical scroll and the pinch — a pan that starts arrives here as
 * `pointercancel` — and never pans sideways. What yields to what:
 *
 * - the first 24 px at either screen edge are the system's back gesture;
 * - a start on a control whose own gesture is horizontal (`OWN_GESTURE`: the
 *   checklist grip, a slider, the waveform scrubber, an open menu) is never a
 *   swipe — the grip's exclusion is the one thing that keeps a sideways grip
 *   drag from being a tab switch, so the checklist adds no attribute of its own;
 * - a LEFT drag that begins on a `.swipe` row (a recording's head) is the
 *   row's tray, which opens leftwards only (`rubberBand` pins the other way at
 *   zero); a RIGHT drag on it is ours;
 * - the axis is decided at 12 px of travel, and |dy| ≥ |dx| is a scroll.
 *
 * Once committed the region takes pointer capture and prevents the default of
 * every `touchmove`, so a later vertical wander cannot start a pan and
 * `usePullToRefresh` stands down by the `defaultPrevented` protocol. The panel
 * follows the finger up to 40 % of the region's width, at a quarter of the
 * distance where there is no neighbour to go to; release steps at 30 % of the
 * width or a 0.4 px/ms flick in the same direction, otherwise the panel snaps
 * back. The click that follows a committed drag is swallowed once, so lifting
 * over the new panel neither focuses the textarea nor toggles a recording.
 *
 * Known trade: a single-line input inside the region — the Find field — loses
 * the horizontal drag-scroll of an overflowing value; its caret keys still
 * reach it.
 */

/** Travel before the axis is decided. */
export const SWIPE_SLOP_PX = 12;
/** A start this close to either screen edge is the system's. */
export const SWIPE_EDGE_PX = 24;
/** Letting go this far across the region steps. */
export const SWIPE_COMMIT_FRACTION = 0.3;
/** A flick this fast steps however short. */
export const SWIPE_FLICK_PX_PER_MS = 0.4;
/** The panel follows no further than this. */
const FOLLOW_MAX_FRACTION = 0.4;
/** How much of the finger's travel the panel follows where there is no neighbour. */
export const SWIPE_RUBBER = 0.25;
/** Controls whose own gesture is horizontal: the swipe never starts on them. */
const OWN_GESTURE =
  '.checklist__grip, [role="slider"], .scrubber__track, .overflow-menu, select, input[type="range"]';

export type SwipeDirection = 'left' | 'right';

/** The region's width; jsdom lays nothing out, so the window's stands in for a region without one. */
function widthOf(region: HTMLElement | null): number {
  return region?.clientWidth || window.innerWidth;
}

interface Gesture {
  pointerId: number;
  x0: number;
  y0: number;
  axis: 'undecided' | 'x' | 'y';
  onSwipeRow: boolean;
  lastX: number;
  lastT: number;
  /** Velocity over the last two moves, px per ms, signed. */
  vx: number;
  dx: number;
}

export function useHorizontalSwipe({
  ref,
  canGo,
  onSwipe,
}: {
  ref: RefObject<HTMLElement | null>;
  /** Whether there is a segment in that direction; the follow is damped where there is not. */
  canGo: (direction: SwipeDirection) => boolean;
  onSwipe: (direction: SwipeDirection) => void;
}): {
  /** How far the panel has followed the finger, in px, signed. */
  offset: number;
  /** A swipe is committed and the finger is down. */
  dragging: boolean;
  handlers: {
    onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
    onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
    onPointerUp: (event: ReactPointerEvent<HTMLElement>) => void;
    onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => void;
    onLostPointerCapture: (event: ReactPointerEvent<HTMLElement>) => void;
    onClickCapture: (event: ReactMouseEvent<HTMLElement>) => void;
  };
} {
  const [offset, setOffset] = useState(0);
  const [dragging, setDragging] = useState(false);
  const gesture = useRef<Gesture | null>(null);
  const swallowClick = useRef(false);
  // The latest callbacks, so the handlers never go stale without changing
  // (the same shape as `usePullToRefresh`'s `refresh`).
  const latest = useRef({ canGo, onSwipe });
  useEffect(() => {
    latest.current = { canGo, onSwipe };
  }, [canGo, onSwipe]);

  const onPointerDown = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    swallowClick.current = false;
    if (event.pointerType === 'mouse' || event.button !== 0 || gesture.current) return;
    const target = event.target as Element;
    if (target.closest(OWN_GESTURE)) return;
    if (event.clientX < SWIPE_EDGE_PX || event.clientX > window.innerWidth - SWIPE_EDGE_PX) return;
    gesture.current = {
      pointerId: event.pointerId,
      x0: event.clientX,
      y0: event.clientY,
      axis: 'undecided',
      onSwipeRow: Boolean(target.closest('.swipe')),
      lastX: event.clientX,
      lastT: event.timeStamp,
      vx: 0,
      dx: 0,
    };
  }, []);

  const onPointerMove = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const g = gesture.current;
      if (!g || g.pointerId !== event.pointerId) return;
      const dx = event.clientX - g.x0;
      const dy = event.clientY - g.y0;
      if (g.axis === 'undecided') {
        if (Math.hypot(dx, dy) < SWIPE_SLOP_PX) return;
        if (Math.abs(dy) >= Math.abs(dx) || (g.onSwipeRow && dx < 0)) {
          // A scroll, or the row's tray: not ours for the rest of this touch.
          g.axis = 'y';
          return;
        }
        g.axis = 'x';
        // Re-anchored where the swipe was recognised, so the panel starts
        // under the finger rather than jumping the slop.
        g.x0 = event.clientX;
        const region = ref.current;
        if (region && typeof region.setPointerCapture === 'function') {
          try {
            region.setPointerCapture(event.pointerId);
          } catch {
            /* The pointer is already gone. */
          }
        }
        setDragging(true);
      }
      if (g.axis !== 'x') return;
      const dt = event.timeStamp - g.lastT;
      if (dt > 0) g.vx = (event.clientX - g.lastX) / dt;
      g.lastX = event.clientX;
      g.lastT = event.timeStamp;
      g.dx = event.clientX - g.x0;
      const direction: SwipeDirection = g.dx < 0 ? 'left' : 'right';
      const raw = latest.current.canGo(direction) ? g.dx : g.dx * SWIPE_RUBBER;
      const max = widthOf(ref.current) * FOLLOW_MAX_FRACTION;
      setOffset(Math.max(-max, Math.min(max, raw)));
    },
    [ref],
  );

  const end = useCallback(
    (event: ReactPointerEvent<HTMLElement>, cancelled: boolean) => {
      const g = gesture.current;
      if (!g || g.pointerId !== event.pointerId) return;
      gesture.current = null;
      if (g.axis !== 'x') return;
      setDragging(false);
      setOffset(0);
      swallowClick.current = true;
      if (cancelled) return;
      const direction: SwipeDirection = g.dx < 0 ? 'left' : 'right';
      const far = Math.abs(g.dx) >= widthOf(ref.current) * SWIPE_COMMIT_FRACTION;
      const flick =
        Math.abs(g.vx) >= SWIPE_FLICK_PX_PER_MS && Math.sign(g.vx) === Math.sign(g.dx);
      if ((far || flick) && latest.current.canGo(direction)) latest.current.onSwipe(direction);
    },
    [ref],
  );

  useEffect(() => {
    const region = ref.current;
    if (!region) return;
    // Not passive: while a swipe is committed the browser gets no pan, and
    // the pull-to-refresh listener above sees the touch as claimed.
    const block = (event: TouchEvent): void => {
      if (gesture.current?.axis === 'x' && event.cancelable) event.preventDefault();
    };
    region.addEventListener('touchmove', block, { passive: false });
    return () => {
      region.removeEventListener('touchmove', block);
    };
  }, [ref]);

  return {
    offset,
    dragging,
    handlers: {
      onPointerDown,
      onPointerMove,
      onPointerUp: (event) => {
        end(event, false);
      },
      onPointerCancel: (event) => {
        end(event, true);
      },
      onLostPointerCapture: (event) => {
        // Only the region's own capture: a child losing its says nothing about ours.
        if (event.target === event.currentTarget) end(event, true);
      },
      onClickCapture: (event) => {
        if (!swallowClick.current) return;
        swallowClick.current = false;
        event.preventDefault();
        event.stopPropagation();
      },
    },
  };
}
