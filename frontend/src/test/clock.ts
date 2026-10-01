import { vi } from 'vitest';

/**
 * Runs a gesture on a fake clock that still moves with real time, as
 * `NotesScreen.pins.test` holds a row: the recorder's promises and `waitFor`
 * run as before, but a stall on a shared runner moves the clock one tick,
 * not the length of the stall. Whether a hold was a slip or a message is
 * read from `Date.now()` against `MIN_TALK_MS`; on the real clock a long
 * enough pause between two lines of a test flipped the answer.
 */
/**
 * One turn of the task queue, after every microtask already queued. What a
 * negative ("nothing was asked for", "no second request left") waits on: the
 * work that would have asked is in the tasks queued before this one, so it has
 * had its turn when the promise settles. In place of a sleep of some tens of
 * milliseconds chosen by feel (review 2026-10-01, FE-11). Holds only for work with no I/O
 * hop: a chain that reads IndexedDB first is not back by then, and the test
 * must queue a read of its own behind it (as `useNotesCache.test` does).
 */
export const flushTasks = (): Promise<void> =>
  new Promise((resolve) => {
    setTimeout(resolve, 0);
  });

export async function onAFakeClock(gesture: () => void | Promise<void>): Promise<void> {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    await gesture();
  } finally {
    vi.useRealTimers();
  }
}
