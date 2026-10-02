/**
 * Travel before a press becomes a gesture: a swipe commits to an axis, a drag
 * decides between reorder and indent, a long press is cancelled, a hold arms
 * at once. One number for all of them, so a finger's wobble means the same
 * thing on every control; the four hooks had four names for it, three at 10
 * and one at 12 (review 2026-10-01, FE-15).
 */
export const GESTURE_SLOP_PX = 10;

/**
 * How far a drag must travel before a lift commits it, as a fraction of the
 * distance it can go: the tab swipe's width, the drawer's height.
 */
export const SWIPE_COMMIT_FRACTION = 0.3;

/** Or how fast: a flick at this speed commits from any distance. */
export const SWIPE_FLICK_PX_PER_MS = 0.4;

/** A flick counts only when the finger was still moving as it lifted. */
export const SWIPE_FLICK_MAX_AGE_MS = 100;
