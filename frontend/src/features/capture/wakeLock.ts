/**
 * Screen wake lock for the duration of a recording.
 *
 * Without it the phone sleeps mid-dictation. On most platforms sleeping does
 * not stop the recorder, but it does suspend timers and can let the OS reclaim
 * a backgrounded tab, which is exactly when a twenty-minute recording is lost.
 *
 * The lock is released by the browser on visibility change, so it has to be
 * reacquired when the page comes back — a subtlety that makes "call
 * `request()` once" quietly wrong.
 */

export interface WakeLockHandle {
  release(): Promise<void>;
}

export function isWakeLockSupported(): boolean {
  return typeof navigator !== 'undefined' && 'wakeLock' in navigator;
}

export async function acquireWakeLock(): Promise<WakeLockHandle | null> {
  if (!isWakeLockSupported()) return null;

  let sentinel: WakeLockSentinel | null = null;
  let released = false;

  const request = async (): Promise<void> => {
    if (released) return;
    try {
      sentinel = await navigator.wakeLock.request('screen');
    } catch {
      // Denied (a non-visible document, or a battery-saver policy). Recording
      // still proceeds; it is a robustness measure, not a precondition.
      sentinel = null;
    }
  };

  const onVisibilityChange = (): void => {
    if (document.visibilityState === 'visible') void request();
  };

  await request();
  document.addEventListener('visibilitychange', onVisibilityChange);

  return {
    async release() {
      released = true;
      document.removeEventListener('visibilitychange', onVisibilityChange);
      try {
        await sentinel?.release();
      } catch {
        /* Already released by the browser. */
      }
      sentinel = null;
    },
  };
}
