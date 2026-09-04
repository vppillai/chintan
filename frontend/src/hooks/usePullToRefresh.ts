import { useEffect, useRef, useState, type RefObject } from 'react';

/**
 * Pull down at the top of a list to refresh it.
 *
 * Touch-driven and hand-rolled: no dependency, and no pointer events. Pointer
 * events look like the natural choice, but the browser fires `pointercancel`
 * the moment it decides a touch is a pan — which it does for a downward drag
 * even when the container is already at the top and has nowhere to scroll —
 * and the gesture is lost. Touch events keep firing through a pan, so they are
 * the only way to read one that the scroller is not going to act on.
 *
 * The gesture is armed only when the scroll container is at `scrollTop === 0`
 * when the finger lands. Anywhere else the touch is an ordinary scroll and
 * this never sees it. Once pulling, the default is prevented so the browser's
 * own rubber-band or native pull-to-refresh does not fire underneath
 * (`overscroll-behavior: contain` on `.app__main` is the belt to this brace).
 *
 * The pull distance is written straight to a CSS custom property on the
 * indicator element rather than through React state — a `touchmove` fires at
 * screen refresh rate, and re-rendering a library of a hundred rows on each
 * one is not a price a progress indicator should charge. Only the phase is
 * state, and it changes four times per gesture.
 */

export type PullPhase =
  /** Nothing in progress. */
  | 'idle'
  /** The finger is down and moving, not yet far enough. */
  | 'pulling'
  /** Far enough: letting go now refreshes. */
  | 'armed'
  /** `onRefresh` is running. */
  | 'refreshing';

/** How far to pull before letting go refreshes. */
export const PULL_THRESHOLD_PX = 64;
/** The indicator stops growing here, however far the finger goes. */
export const PULL_MAX_PX = 96;
/** The finger travels further than the indicator grows: it should feel elastic. */
export const PULL_DAMPING = 0.6;

export interface PullToRefreshOptions {
  /** Off entirely — while there is nothing on screen to refresh, say. Defaults to on. */
  enabled?: boolean;
}

/** The scroll container this element lives in: the shell's <main>, or the nearest parent. */
function scrollContainerOf(element: HTMLElement): HTMLElement {
  return element.closest<HTMLElement>('.app__main') ?? element.parentElement ?? element;
}

function clientYOf(event: TouchEvent): number | null {
  const touch = event.touches[0] ?? event.changedTouches[0];
  return touch ? touch.clientY : null;
}

/**
 * Attach `ref` to the indicator element, placed inside the scroll container.
 * The hook finds the container from it, listens there, and writes
 * `--pull-offset` on the indicator as the finger moves.
 */
export function usePullToRefresh(
  onRefresh: () => Promise<unknown>,
  { enabled = true }: PullToRefreshOptions = {},
): { ref: RefObject<HTMLDivElement | null>; phase: PullPhase } {
  const ref = useRef<HTMLDivElement>(null);
  const [phase, setPhase] = useState<PullPhase>('idle');
  // The latest callback, so the listeners below never go stale without being
  // torn down and re-attached on every render.
  const refresh = useRef(onRefresh);
  useEffect(() => {
    refresh.current = onRefresh;
  }, [onRefresh]);

  useEffect(() => {
    const indicator = ref.current;
    if (!indicator || !enabled) return;
    const container = scrollContainerOf(indicator);

    let startY: number | null = null;
    let offset = 0;
    let busy = false;

    const setOffset = (px: number): void => {
      indicator.style.setProperty('--pull-offset', `${Math.round(px)}px`);
    };

    const reset = (): void => {
      startY = null;
      offset = 0;
      setOffset(0);
      setPhase('idle');
    };

    const onTouchStart = (event: TouchEvent): void => {
      if (busy || event.touches.length !== 1 || container.scrollTop > 0) return;
      startY = clientYOf(event);
    };

    const onTouchMove = (event: TouchEvent): void => {
      if (startY === null || busy) return;
      const y = clientYOf(event);
      if (y === null) return;
      const distance = y - startY;
      if (distance <= 0 && offset === 0) {
        // Moving up, or sideways: an ordinary scroll. Stand down for this touch.
        startY = null;
        return;
      }
      if (container.scrollTop > 0) {
        reset();
        return;
      }
      // Ours now. Stop the browser rubber-banding or refreshing the page itself.
      if (event.cancelable) event.preventDefault();
      offset = Math.min(PULL_MAX_PX, Math.max(0, distance * PULL_DAMPING));
      setOffset(offset);
      setPhase(offset >= PULL_THRESHOLD_PX ? 'armed' : 'pulling');
    };

    const onTouchEnd = (): void => {
      if (startY === null || busy) return;
      if (offset < PULL_THRESHOLD_PX) {
        reset();
        return;
      }
      busy = true;
      startY = null;
      setOffset(PULL_THRESHOLD_PX);
      setPhase('refreshing');
      // Called now, not on a later tick: the release is the request.
      let outcome: Promise<unknown>;
      try {
        outcome = Promise.resolve(refresh.current());
      } catch (error) {
        outcome = Promise.reject(error instanceof Error ? error : new Error(String(error)));
      }
      void outcome
        .catch(() => {
          /* The query reports its own failure on screen. */
        })
        .finally(() => {
          busy = false;
          reset();
        });
    };

    container.addEventListener('touchstart', onTouchStart, { passive: true });
    // Not passive: this is the one listener that needs `preventDefault`.
    container.addEventListener('touchmove', onTouchMove, { passive: false });
    container.addEventListener('touchend', onTouchEnd);
    container.addEventListener('touchcancel', reset);

    return () => {
      container.removeEventListener('touchstart', onTouchStart);
      container.removeEventListener('touchmove', onTouchMove);
      container.removeEventListener('touchend', onTouchEnd);
      container.removeEventListener('touchcancel', reset);
    };
  }, [enabled]);

  return { ref, phase };
}

/** What the indicator says in each phase. Empty while idle: the row is zero-height then. */
export function pullLabel(phase: PullPhase): string {
  switch (phase) {
    case 'pulling':
      return 'Pull to refresh';
    case 'armed':
      return 'Release to refresh';
    case 'refreshing':
      return 'Refreshing…';
    default:
      return '';
  }
}
