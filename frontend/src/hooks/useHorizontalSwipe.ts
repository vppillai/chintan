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
 * - a LEFT drag that begins on a closed `.swipe` row (a recording's head) is
 *   the row's tray, which opens leftwards only (`rubberBand` pins the other
 *   way at zero), and a RIGHT drag on a closed row is ours; an open row
 *   (`.swipe[data-open]`) owns both directions, because a right drag on it
 *   is its own close gesture — taking it stole the row's capture, left the
 *   tray open and flipped the tab (review 2026-09-29);
 * - the axis is decided at 12 px of travel, and |dy| ≥ |dx| is a scroll.
 *
 * Once committed the region takes pointer capture and prevents the default of
 * every `touchmove`, so a later vertical wander cannot start a pan and
 * `usePullToRefresh` stands down by the `defaultPrevented` protocol. The panel
 * follows the finger up to 40 % of the region's width, at a quarter of the
 * distance where there is no neighbour to go to; release steps at 30 % of the
 * width or a 0.4 px/ms flick in the same direction, otherwise the panel snaps
 * back. The follow is written straight to the region's `--tab-swipe-x`, as
 * `usePullToRefresh` writes `--pull-offset`, not through state: a textarea
 * and a forty-row checklist sit under this region, and a move at input rate
 * must re-render none of it; only `dragging` is state. The property applies
 * only under `data-swiping` (`notes.css`), so it is left where the finger
 * let go and the attribute's removal is the snap back.
 *
 * The click that follows a committed drag is swallowed once, so lifting over
 * the new panel neither focuses the textarea nor toggles a recording.
 * Chromium synthesises no click after a touch that moved, so the flag can
 * outlive the gesture until the next pointer; a keyboard activation's click
 * carries `detail` 0 and is let through — Enter on a tab after a swipe did
 * nothing (review 2026-09-29) — and a cancelled pointer sets no flag.
 *
 * Known trades: a single-line input inside the region — the Find field —
 * loses the horizontal drag-scroll of an overflowing value (its caret keys
 * still reach it); and a pen is a finger here, so a sideways S Pen drag
 * across the body — Android's pen text-selection gesture — steps the tab
 * rather than selecting.
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
/** The region's own property the panel follows while swiping (`notes.css`). */
const OFFSET_PROPERTY = '--tab-swipe-x';

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
  /** The row was open when the finger landed: a drag either way is its own. */
  onOpenRow: boolean;
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
      onOpenRow: Boolean(target.closest('.swipe[data-open]')),
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
      const region = ref.current;
      if (g.axis === 'undecided') {
        if (Math.hypot(dx, dy) < SWIPE_SLOP_PX) return;
        if (Math.abs(dy) >= Math.abs(dx) || (g.onSwipeRow && dx < 0) || g.onOpenRow) {
          // A scroll, or the row's own gesture (its tray opening, or an open
          // row closing): not ours for the rest of this touch.
          g.axis = 'y';
          return;
        }
        g.axis = 'x';
        // Re-anchored where the swipe was recognised, so the panel starts
        // under the finger rather than jumping the slop.
        g.x0 = event.clientX;
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
      const max = widthOf(region) * FOLLOW_MAX_FRACTION;
      region?.style.setProperty(OFFSET_PROPERTY, `${String(Math.max(-max, Math.min(max, raw)))}px`);
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
      // No click follows a cancelled pointer; a flag set here would wait for
      // the next one (as `useSwipeActions.end`).
      if (cancelled) return;
      swallowClick.current = true;
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
        // A keyboard or script activation carries `detail` 0; a tap's carries 1.
        if (!swallowClick.current || event.detail === 0) return;
        swallowClick.current = false;
        event.preventDefault();
        event.stopPropagation();
      },
    },
  };
}
