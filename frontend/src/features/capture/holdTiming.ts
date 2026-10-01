/**
 * The hold-to-talk gesture's numbers, on their own so the end-to-end specs
 * can hold for `constant + 100` rather than a literal: `useHoldToTalk.ts`
 * reaches the API provider and the capture store, which the Playwright
 * runner cannot load, and a literal there drifted from the constant here.
 *
 * Distances are measured from the press point, not the disc's edge. Measured
 * from the edge of the retired full-screen talk disc, which was as wide as
 * the phone, a slide-to-cancel needed 80 px beyond it — off the screen (R8,
 * F6).
 */

/** A press longer than this is a hold; a shorter one is a tap. */
export const HOLD_ARM_MS = 250;
/** Movement beyond this arms the hold at once: a fast slide is not a tap. */
export const TAP_SLOP_PX = 10;
/** Fewer milliseconds of audio than this is a slip, not a message. */
export const MIN_TALK_MS = 600;
/** Leftward from the press point; crossing it cancels. */
export const CANCEL_DX_PX = 110;
/** Upward from the press point; crossing it locks the recording hands-free. */
export const LOCK_DY_PX = 72;
/** How long "Sent", "Too short" and the other notices stay up. */
export const HOLD_NOTICE_MS = 1_500;
/** "Microphone blocked" stays longer: it is the one notice that needs acting on. */
export const BLOCKED_NOTICE_MS = 3_000;
/** The click a browser sends after a long press is swallowed for this long. */
export const CLICK_SUPPRESS_MS = 600;
