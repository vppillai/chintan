/**
 * The hold-to-talk gesture's timings, on their own so the end-to-end specs
 * can hold for `constant + 100` rather than a literal: `useHoldToTalk.ts`
 * reaches the API provider and the capture store, which the Playwright
 * runner cannot load, and a literal there drifted from the constant here.
 */

/** How long the tab-bar mic must be held before a press is a hold rather than a tap. */
export const HOLD_DELAY_MS = 350;
/** Fewer milliseconds of audio than this is a slip, not a message. */
export const MIN_TALK_MS = 600;
/** How far off the button the pointer may be before release means cancel. */
export const SLIDE_AWAY_PX = 80;
/** How long the too-short hint and the "Sent" confirmation stay up. */
export const HOLD_NOTICE_MS = 1_500;
