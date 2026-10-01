import { useState } from 'react';

/**
 * The click a browser fires when the finger lifts after a gesture (a drag, a
 * swipe, a long press, a hold) is not a tap on whatever ends up under it. A
 * gesture hook arms this as the gesture ends and asks `take` from its click
 * handler; a new press resets it.
 *
 * A keyboard or script activation carries `detail` 0 and is always let
 * through. Chromium fires no click after a touch that moved or was cancelled,
 * so an armed swallow can outlive its gesture until the next press, and two
 * hooks that kept their own flag ate the Enter that came next (review
 * 2026-10-01, FE-15). `windowMs` bounds the swallow in time for the one
 * gesture whose next press may be a long way off: the record disc.
 */
export interface ClickSwallow {
  /** The gesture ended: the next pointer click is its lift, not a tap. */
  arm: () => void;
  /** A new press: a swallow no click consumed is dropped. */
  reset: () => void;
  /** Still waiting for the lift's click. */
  armed: () => boolean;
  /** True once, for the click to swallow; the caller stops it. */
  take: (event: { detail: number }) => boolean;
}

export function useSwallowNextClick(windowMs = Infinity): ClickSwallow {
  return useState<ClickSwallow>(() => {
    let until = 0;
    const armed = (): boolean => Date.now() < until;
    return {
      arm: () => {
        until = Date.now() + windowMs;
      },
      reset: () => {
        until = 0;
      },
      armed,
      take: (event) => {
        if (event.detail === 0 || !armed()) return false;
        until = 0;
        return true;
      },
    };
  })[0];
}
