import { useCallback, useEffect, useRef, type PointerEvent as ReactPointerEvent } from 'react';

/**
 * Press and hold to select, the gesture every phone list already teaches.
 *
 * Pointer events rather than touch events, so one implementation serves a
 * finger, a stylus and — since the hover checkbox went (2026-09-24, C) — a
 * mouse: press-and-hold is the one way into selection on every pointer, so
 * nobody has to learn two. Only the primary button: a right-click is the
 * context menu's, and arming on it would suppress that menu below. The press
 * is cancelled the moment the pointer travels more than
 * `LONG_PRESS_TOLERANCE_PX`, which is what keeps a scroll that happens to
 * start on a row from selecting it, and on release, leave or cancel.
 *
 * A long press that fires is followed by the browser's own `click` when the
 * finger lifts; the row would open the note it had just selected. `consumeClick`
 * reports and clears that, so the row's click handler can return early once.
 * The context menu that Android raises for the same hold is suppressed while
 * a press is armed — that event is also where the platform's text selection
 * begins, so cancelling it is what keeps a held row from turning into a
 * highlighted paragraph.
 *
 * The movement cancel is also the hand-off to `useSwipeActions`: a finger
 * that travels sideways past the slop has stopped being a press before the
 * swipe commits, so the two gestures share a row without either knowing the
 * other exists (the swipe, for its part, ignores a drift after a hold).
 *
 * A haptic tick where the platform offers one: the row changes state under a
 * finger that is covering it, and the vibration is what says "got it" without
 * the eyes.
 */

export const LONG_PRESS_MS = 500;
export const LONG_PRESS_TOLERANCE_PX = 10;

export interface LongPressHandlers {
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerUp: () => void;
  onPointerCancel: () => void;
  onPointerLeave: () => void;
  onContextMenu: (event: ReactPointerEvent<HTMLElement> | { preventDefault: () => void }) => void;
}

export interface LongPress {
  handlers: LongPressHandlers;
  /** True exactly once after a press fired: the click that follows is not a tap. */
  consumeClick: () => boolean;
}

export function useLongPress(onLongPress: (() => void) | null, ms: number = LONG_PRESS_MS): LongPress {
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const origin = useRef<{ x: number; y: number } | null>(null);
  const fired = useRef(false);
  // The latest handler, read when the timer fires rather than when the press
  // began, so a row whose props changed mid-hold acts on the current ones.
  const callback = useRef(onLongPress);
  useEffect(() => {
    callback.current = onLongPress;
  }, [onLongPress]);

  const clear = useCallback(() => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
    origin.current = null;
  }, []);

  useEffect(() => clear, [clear]);

  const onPointerDown = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (!callback.current || event.button !== 0) return;
      clear();
      fired.current = false;
      origin.current = { x: event.clientX, y: event.clientY };
      timer.current = setTimeout(() => {
        timer.current = null;
        origin.current = null;
        fired.current = true;
        if (typeof navigator !== 'undefined' && typeof navigator.vibrate === 'function') {
          navigator.vibrate(10);
        }
        callback.current?.();
      }, ms);
    },
    [clear, ms],
  );

  const onPointerMove = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const start = origin.current;
      if (!start) return;
      const travelled = Math.hypot(event.clientX - start.x, event.clientY - start.y);
      if (travelled > LONG_PRESS_TOLERANCE_PX) clear();
    },
    [clear],
  );

  const onContextMenu = useCallback((event: { preventDefault: () => void }) => {
    if (origin.current || fired.current) event.preventDefault();
  }, []);

  const consumeClick = useCallback(() => {
    const was = fired.current;
    fired.current = false;
    return was;
  }, []);

  return {
    handlers: {
      onPointerDown,
      onPointerMove,
      onPointerUp: clear,
      onPointerCancel: clear,
      onPointerLeave: clear,
      onContextMenu,
    },
    consumeClick,
  };
}
