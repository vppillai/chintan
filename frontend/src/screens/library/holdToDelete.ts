/**
 * When a bulk Delete forever is a hold rather than a tap, and for how long.
 * On their own so the end-to-end specs can hold for `HOLD_TO_DELETE_MS +
 * 100` rather than a literal: `useLibrarySelection.tsx` reaches the query
 * layer, which the Playwright runner cannot load.
 */

/** A bulk Delete forever of more than this many notes is a hold, not a tap. */
export const HOLD_TO_DELETE_ABOVE = 10;
export const HOLD_TO_DELETE_MS = 1000;
