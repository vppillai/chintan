import { vi } from 'vitest';

/**
 * Runs a gesture on a fake clock that still moves with real time, as
 * `NotesScreen.pins.test` holds a row: the recorder's promises and `waitFor`
 * run as before, but a stall on a shared runner moves the clock one tick,
 * not the length of the stall. Whether a press was a tap or a hold, and a
 * hold a slip or a message, is read from `Date.now()` against `HOLD_DELAY_MS`
 * and `MIN_TALK_MS`; on the real clock a long enough pause between two lines
 * of a test flipped the answer.
 */
export async function onAFakeClock(gesture: () => void | Promise<void>): Promise<void> {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    await gesture();
  } finally {
    vi.useRealTimers();
  }
}
