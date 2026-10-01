/**
 * Travel before a press becomes a gesture: a swipe commits to an axis, a drag
 * decides between reorder and indent, a long press is cancelled, a hold arms
 * at once. One number for all of them, so a finger's wobble means the same
 * thing on every control; the four hooks had four names for it, three at 10
 * and one at 12 (review 2026-10-01, FE-15).
 */
export const GESTURE_SLOP_PX = 10;
