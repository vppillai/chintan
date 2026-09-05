import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
} from 'react';

import { LONG_PRESS_MS } from './useLongPress.ts';

/**
 * Drag a row sideways to uncover the actions behind it — the gesture every
 * phone list teaches for "get rid of this".
 *
 * Pointer events, one implementation for a finger and a stylus. The gesture
 * has three phases. Until the pointer has travelled `SWIPE_SLOP_PX` nothing
 * happens, so a tap is still a tap and the row's own click fires. At the slop
 * the gesture commits to an axis: more sideways than down, and it is a swipe
 * from here on; otherwise it is the scroll it looks like, and this hook stays
 * out of the way for the rest of the touch (`touch-action: pan-y` on the row
 * lets the browser take the scroll, which arrives here as `pointercancel`).
 * On release the row settles — open if it is past half the tray, closed if
 * not — and the settle is a CSS transition on the motion tokens, so reduced
 * motion collapses it to a frame like every other animation in the app.
 *
 * Coordination with the long press. `useLongPress` cancels itself once the
 * pointer moves past its own slop, so a swipe never selects a row. The
 * reverse is enforced here: a pointer that has held still for the long-press
 * duration has become a long press (or is about to), and a drift after that
 * is not a swipe. The hook reads the clock rather than asking the other hook,
 * so neither needs to know the other exists.
 *
 * One row open at a time, app-wide. A module-level registry holds the closer
 * of whichever row is open; opening another closes it. Tapping the open row's
 * content closes it rather than opening the note, and a tap anywhere else, or
 * a scroll, closes it too. Past the tray's full width the row rubber-bands:
 * the extra travel is damped and capped, which is what says "this is as far
 * as it goes" under the finger.
 *
 * The click the browser fires when the finger lifts after a drag is not a tap
 * and must not open the note; `onContentClickCapture` swallows exactly that
 * one click.
 */

export const SWIPE_SLOP_PX = 10;
/** How far past the tray's width the row can be pulled. */
export const SWIPE_RUBBER_PX = 24;
const RUBBER_FACTOR = 0.35;

/**
 * The one open row, so that opening another can close it. Keyed by the row's
 * container ref, which is the one object stable for the life of the row.
 */
let openRow: { key: object; close: () => void } | null = null;

interface Gesture {
  pointerId: number;
  startX: number;
  startY: number;
  startedAt: number;
  /** Where the row was when the finger landed: 0, or minus the tray's width. */
  startOffset: number;
  /** Where the row is now, for the release to judge. */
  current: number;
  axis: 'undecided' | 'x' | 'y';
  width: number;
}

export interface SwipeHandlers {
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerUp: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => void;
}

export interface SwipeActionsState {
  /** The row's horizontal offset in CSS pixels, never positive. */
  offset: number;
  open: boolean;
  /** The finger is on the row and has committed to a sideways drag. */
  dragging: boolean;
  handlers: SwipeHandlers;
  /** For the row's content: swallows the click that follows a drag, closes an open row. */
  onContentClickCapture: (event: ReactMouseEvent<HTMLElement>) => void;
  close: () => void;
}

export function useSwipeActions({
  enabled,
  measureWidth,
  containerRef,
}: {
  enabled: boolean;
  /** The tray's full width in CSS pixels, read when a drag commits. */
  measureWidth: () => number;
  /** The row's outermost element, for telling a tap inside from one outside. */
  containerRef: React.RefObject<HTMLElement | null>;
}): SwipeActionsState {
  const [offset, setOffset] = useState(0);
  const [open, setOpen] = useState(false);
  const [dragging, setDragging] = useState(false);
  const gesture = useRef<Gesture | null>(null);
  /** A drag just ended: the click the browser fires next is the finger lifting. */
  const swallowClick = useRef(false);

  // No ref is read here, deliberately: `close` is also called while rendering
  // (below), where a ref may not be.
  const close = useCallback(() => {
    if (openRow?.key === containerRef) openRow = null;
    setOpen(false);
    setOffset(0);
  }, [containerRef]);

  const settle = useCallback(
    (shouldOpen: boolean, width: number) => {
      if (!shouldOpen) {
        close();
        return;
      }
      if (openRow && openRow.key !== containerRef) openRow.close();
      openRow = { key: containerRef, close };
      setOpen(true);
      setOffset(-width);
    },
    [close, containerRef],
  );

  // Disabled while open — bulk-select started, the pointer changed — means
  // closed: a tray left uncovered under a checkbox row is two controls in one
  // place. Derived while rendering from the previous value, React's own
  // pattern for state that follows a prop, rather than from an effect that
  // would paint the open row once more first. And a row that unmounts open
  // must let go of the registry.
  const [wasEnabled, setWasEnabled] = useState(enabled);
  if (enabled !== wasEnabled) {
    setWasEnabled(enabled);
    if (!enabled) close();
  }
  useEffect(
    () => () => {
      if (openRow?.key === containerRef) openRow = null;
    },
    [containerRef],
  );

  // A tap anywhere but this row, or any scroll, closes it. Capture phase, so a
  // control that stops propagation still counts as "elsewhere".
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent): void => {
      const root = containerRef.current;
      if (root && !root.contains(event.target as Node)) close();
    };
    document.addEventListener('pointerdown', onPointerDown, true);
    document.addEventListener('scroll', close, { capture: true, passive: true });
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true);
      document.removeEventListener('scroll', close, { capture: true });
    };
  }, [open, close, containerRef]);

  const onPointerDown = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      swallowClick.current = false;
      // Only the primary button; a right-click or a second finger is not a drag.
      if (!enabled || event.button !== 0 || gesture.current) return;
      // Measured now rather than when the drag commits, so an open row knows
      // where it is standing before the finger has moved.
      const width = Math.max(1, measureWidth());
      const startOffset = open ? -width : 0;
      gesture.current = {
        pointerId: event.pointerId,
        startX: event.clientX,
        startY: event.clientY,
        startedAt: Date.now(),
        startOffset,
        current: startOffset,
        axis: 'undecided',
        width,
      };
    },
    [enabled, open, measureWidth],
  );

  const onPointerMove = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const g = gesture.current;
      if (!g || g.pointerId !== event.pointerId) return;
      const dx = event.clientX - g.startX;
      const dy = event.clientY - g.startY;

      if (g.axis === 'undecided') {
        if (Math.hypot(dx, dy) <= SWIPE_SLOP_PX) return;
        // Held still long enough to be a long press: whatever this drift is,
        // it is not a swipe. Nor is a movement that is more down than across.
        if (Date.now() - g.startedAt >= LONG_PRESS_MS || Math.abs(dy) >= Math.abs(dx)) {
          g.axis = 'y';
          return;
        }
        g.axis = 'x';
        // Re-anchor so the row does not jump by the slop the moment it commits.
        g.startX = event.clientX;
        // Keep receiving moves after the finger leaves the row's box. Absent
        // in jsdom, hence the guard.
        const target = event.currentTarget;
        if (typeof target.setPointerCapture === 'function') {
          try {
            target.setPointerCapture(event.pointerId);
          } catch {
            /* A pointer the browser has already released; the drag still works. */
          }
        }
        setDragging(true);
      }
      if (g.axis !== 'x') return;

      g.current = rubberBand(g.startOffset + (event.clientX - g.startX), g.width);
      setOffset(g.current);
    },
    [],
  );

  const end = useCallback(
    (event: ReactPointerEvent<HTMLElement>, cancelled: boolean) => {
      const g = gesture.current;
      if (!g || g.pointerId !== event.pointerId) return;
      gesture.current = null;
      if (g.axis !== 'x') return;
      setDragging(false);
      if (cancelled) {
        // The browser took the pointer (a scroll won, the app lost focus):
        // back to wherever the row was, and no click will follow.
        settle(g.startOffset !== 0, g.width);
        return;
      }
      swallowClick.current = true;
      settle(g.current < -g.width / 2, g.width);
    },
    [settle],
  );

  const onPointerUp = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      end(event, false);
    },
    [end],
  );
  const onPointerCancel = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      end(event, true);
    },
    [end],
  );

  const onContentClickCapture = useCallback(
    (event: ReactMouseEvent<HTMLElement>) => {
      if (swallowClick.current) {
        swallowClick.current = false;
        event.preventDefault();
        event.stopPropagation();
        return;
      }
      // A tap on the uncovered row puts it back; it does not also open the note.
      if (open) {
        event.preventDefault();
        event.stopPropagation();
        close();
      }
    },
    [open, close],
  );

  return {
    offset,
    open,
    dragging,
    handlers: { onPointerDown, onPointerMove, onPointerUp, onPointerCancel },
    onContentClickCapture,
    close,
  };
}

/**
 * The row's offset for a raw drag distance: pinned at the closed edge, damped
 * and capped past the open one.
 */
export function rubberBand(raw: number, width: number): number {
  if (raw >= 0) return 0;
  if (raw >= -width) return raw;
  const over = -width - raw;
  return -(width + Math.min(SWIPE_RUBBER_PX, over * RUBBER_FACTOR));
}
