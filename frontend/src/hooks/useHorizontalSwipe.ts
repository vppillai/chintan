import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react';

import {
  GESTURE_SLOP_PX,
  SWIPE_COMMIT_FRACTION,
  SWIPE_FLICK_MAX_AGE_MS,
  SWIPE_FLICK_PX_PER_MS,
} from './gesture.ts';
import { useSwallowNextClick } from './swallowNextClick.ts';

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
 * `usePullToRefresh` stands down by the `defaultPrevented` protocol. Only the
 * horizontal travel is followed; the vertical component of a committed swipe
 * is ignored. Release steps at 30 % of the width or a 0.4 px/ms flick in the
 * same direction — a flick the finger was still making as it lifted, not one
 * an earlier move left behind — otherwise the panel snaps back.
 *
 * The motion (R8 F4). The panel (`[data-swipe-panel]`) follows the finger
 * 1:1 out to the full width and fades by up to 40 % as it goes, so the
 * vacated ground reads as "something is coming". Where there is no
 * neighbour it rubber-bands toward an asymptote of 15 % of the width. The
 * strip's pill (`[data-swipe-indicator]`) tracks the drag through
 * `--tab-progress`. On a step the old panel is left where the finger let go
 * until the new tab is committed — `at` changes — and only then, in a layout
 * effect before that commit paints, is the new panel posed where a
 * neighbour would have been, on the side the finger pulled away from, under
 * `data-tab-enter`, which turns its transition off; two frames later the
 * attribute and the pose are cleared and the panel settles to rest over
 * `--motion-duration-base`, or `--motion-duration-fast` after a flick. Keyed
 * on the commit, not on pointerup: a tab named in the URL changes through
 * the router's `setParams`, which commits in a transition a frame late, and
 * posing at pointerup showed the old panel at the enter pose for that frame
 * (review of #196).
 * Reduced motion keeps the follow (it moves only with the finger) and makes
 * every settle instant through the tokens.
 *
 * Every write is `translate` and `opacity` on those two leaves, never a
 * property on the region: an inherited property there restyled the whole
 * subtree — a textarea and a forty-row checklist — on every move. The width
 * and the two leaves are read once, when the axis is decided, and nothing in
 * the move path reads layout or touches React; only `dragging` is state.
 *
 * The click that follows a committed drag is swallowed once
 * (`useSwallowNextClick`), so lifting over the new panel neither focuses the
 * textarea nor toggles a recording; a cancelled pointer arms no swallow.
 *
 * Known trades: a single-line input inside the region — the Find field —
 * loses the horizontal drag-scroll of an overflowing value (its caret keys
 * still reach it); and a pen is a finger here, so a sideways S Pen drag
 * across the body — Android's pen text-selection gesture — steps the tab
 * rather than selecting.
 */

/** A start this close to either screen edge is the system's. */
export const SWIPE_EDGE_PX = 24;
/* Letting go this far across the region steps, or a flick this fast however
   short: the drawer's drag-to-close shares both numbers (`gesture.ts`). */
export { SWIPE_COMMIT_FRACTION, SWIPE_FLICK_PX_PER_MS };
/** The rubber band's asymptote, as a fraction of the width. */
export const SWIPE_RUBBER_FRACTION = 0.15;
/** How much the panel fades at a full width of travel. */
export const SWIPE_FADE = 0.4;
/** Controls whose own gesture is horizontal: the swipe never starts on them. */
const OWN_GESTURE =
  '.checklist__grip, [role="slider"], .scrubber__track, .overflow-menu, select, input[type="range"]';
/** The pill's offset from its tab, in tab widths (`notes.css`). */
const PROGRESS_PROPERTY = '--tab-progress';

export type SwipeDirection = 'left' | 'right';

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
  /** Read once, when the axis is decided. */
  width: number;
  panel: HTMLElement | null;
  indicator: HTMLElement | null;
}

/**
 * Where the panel sits for a finger `dx` across: 1:1 toward a neighbour,
 * clamped to the width, and rubber-banded where there is none —
 * `R·(1 − 1/(1 + |dx|/R))`, which never reaches `R`.
 */
export function followOf(dx: number, width: number, neighbour: boolean): number {
  const clamped = Math.max(-width, Math.min(width, dx));
  if (neighbour) return clamped;
  const r = width * SWIPE_RUBBER_FRACTION;
  return Math.sign(clamped) * r * (1 - 1 / (1 + Math.abs(clamped) / r));
}

function pose(panel: HTMLElement | null, x: number, width: number): void {
  if (!panel) return;
  panel.style.translate = `${String(x)}px`;
  panel.style.opacity = String(1 - (SWIPE_FADE * Math.abs(x)) / width);
}

function unpose(panel: HTMLElement | null): void {
  panel?.style.removeProperty('translate');
  panel?.style.removeProperty('opacity');
}

/** Past the longest settle: a backstop for a `transitionend` that never comes. */
const FAST_CLEAR_MS = 500;

/**
 * The settle after a flick runs at the fast token. Set inline rather than by
 * an attribute because the attribute that poses the panel is gone by the time
 * the settle starts; cleared when it ends, on a backstop timer (a cancelled
 * transition fires no `transitionend`), or when the next finger lands.
 */
function settleFast(element: HTMLElement | null): void {
  if (!element) return;
  element.style.transitionDuration = 'var(--motion-duration-fast)';
  const clear = (): void => {
    element.style.removeProperty('transition-duration');
    element.removeEventListener('transitionend', done);
    window.clearTimeout(timer);
  };
  // Its own transition only: `transitionend` bubbles from every row inside.
  const done = (event: TransitionEvent): void => {
    if (event.target === element) clear();
  };
  element.addEventListener('transitionend', done);
  const timer = window.setTimeout(clear, FAST_CLEAR_MS);
}

/** A step on its way: posed when the new tab commits, then let go two frames later. */
interface Enter {
  /** Not yet posed: the new tab has not committed. */
  waiting: boolean;
  frame: number;
  x: number;
  width: number;
  fast: boolean;
  panel: HTMLElement | null;
  indicator: HTMLElement | null;
}

export function useHorizontalSwipe({
  ref,
  at,
  canGo,
  onSwipe,
}: {
  ref: RefObject<HTMLElement | null>;
  /** The segment shown; its change is the commit a step's enter waits for. */
  at: number;
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
  const swallow = useSwallowNextClick();
  const enter = useRef<Enter | null>(null);
  // The latest callbacks, so the handlers never go stale without changing
  // (the same shape as `usePullToRefresh`'s `refresh`).
  const latest = useRef({ canGo, onSwipe });
  useEffect(() => {
    latest.current = { canGo, onSwipe };
  }, [canGo, onSwipe]);

  /** Lands an entering panel at rest at once: a new finger does not wait for it. */
  const cutEnter = useCallback(() => {
    const pending = enter.current;
    if (!pending) return;
    enter.current = null;
    cancelAnimationFrame(pending.frame);
    ref.current?.removeAttribute('data-tab-enter');
    unpose(pending.panel);
    pending.indicator?.style.removeProperty(PROGRESS_PROPERTY);
  }, [ref]);

  // The new tab is in the DOM and not yet painted: pose it, slide the pill on
  // from where the finger left it, and let both go two frames later.
  useLayoutEffect(() => {
    const pending = enter.current;
    if (!pending?.waiting) return;
    pending.waiting = false;
    ref.current?.setAttribute('data-tab-enter', '');
    pose(pending.panel, pending.x, pending.width);
    if (pending.fast) {
      settleFast(pending.panel);
      settleFast(pending.indicator);
    }
    pending.indicator?.style.removeProperty(PROGRESS_PROPERTY);
    pending.frame = requestAnimationFrame(() => {
      if (enter.current === pending) pending.frame = requestAnimationFrame(cutEnter);
    });
  }, [at, ref, cutEnter]);

  const onPointerDown = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      swallow.reset();
      cutEnter();
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
        width: 0,
        panel: null,
        indicator: null,
      };
    },
    [cutEnter, swallow],
  );

  const onPointerMove = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const g = gesture.current;
      if (!g || g.pointerId !== event.pointerId) return;
      const dx = event.clientX - g.x0;
      const dy = event.clientY - g.y0;
      const region = ref.current;
      if (g.axis === 'undecided') {
        if (Math.hypot(dx, dy) < GESTURE_SLOP_PX) return;
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
        // jsdom lays nothing out, so the window's width stands in for none.
        g.width = region?.clientWidth || window.innerWidth;
        g.panel = region?.querySelector<HTMLElement>('[data-swipe-panel]') ?? null;
        g.indicator = region?.querySelector<HTMLElement>('[data-swipe-indicator]') ?? null;
        g.panel?.style.removeProperty('transition-duration');
        g.indicator?.style.removeProperty('transition-duration');
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
      const x = followOf(g.dx, g.width, latest.current.canGo(g.dx < 0 ? 'left' : 'right'));
      pose(g.panel, x, g.width);
      g.indicator?.style.setProperty(PROGRESS_PROPERTY, String(-x / g.width));
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
      // A snap back clears the pose in the same task as `data-swiping`, so the
      // browser sees one change with the transition back on.
      const snapBack = (): void => {
        unpose(g.panel);
        g.indicator?.style.removeProperty(PROGRESS_PROPERTY);
      };
      // No click follows a cancelled pointer; a flag set here would wait for
      // the next one (as `useSwipeActions.end`).
      if (cancelled) {
        snapBack();
        return;
      }
      swallow.arm();
      const direction: SwipeDirection = g.dx < 0 ? 'left' : 'right';
      const far = Math.abs(g.dx) >= g.width * SWIPE_COMMIT_FRACTION;
      // A still finger fires no move, so the last fast move's velocity would
      // outlive the pause and a drag that stopped short would step on the
      // lift (review 2026-09-29, DB6-23); a flick is what the finger was
      // doing as it left.
      const fresh = event.timeStamp - g.lastT <= SWIPE_FLICK_MAX_AGE_MS;
      const flick =
        fresh && Math.abs(g.vx) >= SWIPE_FLICK_PX_PER_MS && Math.sign(g.vx) === Math.sign(g.dx);
      if (!(far || flick) || !latest.current.canGo(direction)) {
        snapBack();
        return;
      }
      // The new panel starts where a neighbour would have been: a left drag
      // of −156 at 390 poses it at +234, to the right, so it arrives moving
      // the way the finger went. Until the tab commits the old panel and the
      // pill hold where the finger left them: the values do not change, so
      // nothing transitions in that frame.
      const dx = Math.max(-g.width, Math.min(g.width, g.dx));
      enter.current = {
        waiting: true,
        frame: 0,
        x: dx < 0 ? g.width + dx : dx - g.width,
        width: g.width,
        fast: flick,
        panel: g.panel,
        indicator: g.indicator,
      };
      latest.current.onSwipe(direction);
    },
    [swallow],
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
      if (enter.current) cancelAnimationFrame(enter.current.frame);
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
        if (!swallow.take(event)) return;
        event.preventDefault();
        event.stopPropagation();
      },
    },
  };
}
