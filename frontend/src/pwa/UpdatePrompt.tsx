import { useEffect, useRef, useState } from 'react';

import { config } from '@/config/env.ts';

/**
 * How often a foregrounded app asks whether a new build is out.
 *
 * The browser re-checks `sw.js` only on a real navigation. An installed app
 * is resumed from the background, not navigated to, so one left open for
 * days kept serving the build it was opened with and never saw a deploy. A
 * return to the foreground now asks, at most this often — the check is one
 * conditional GET, but a phone flicked in and out of view a dozen times a
 * minute should not make a dozen.
 */
export const UPDATE_CHECK_INTERVAL_MS = 30 * 60 * 1000;

/**
 * The single update strategy.
 *
 * A new worker installs and *waits*. This prompt is the only thing that lets it
 * through, by messaging `SKIP_WAITING`; the page reloads once on
 * `controllerchange`. Calling `skipWaiting()` at install while also showing a
 * refresh prompt would let the two race, and a session could be served half of
 * each build.
 */
export function UpdatePrompt() {
  const [waiting, setWaiting] = useState<ServiceWorker | null>(null);
  /**
   * Only an update the user asked for may reload the page.
   *
   * `controllerchange` also fires on the very first visit, when the freshly
   * installed worker calls `clients.claim()`. Reloading on that reloads the app
   * out from under someone who has just opened it — mid-recording, if they were
   * quick. Gating on this flag makes the reload a consequence of pressing
   * Update and nothing else.
   */
  const requested = useRef(false);

  useEffect(() => {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
    // The one container this effect subscribes to and unsubscribes from.
    const container = navigator.serviceWorker;

    let reloading = false;
    const onControllerChange = (): void => {
      if (!requested.current) return;
      // Also guarded against firing twice, which would be a refresh loop.
      if (reloading) return;
      reloading = true;
      window.location.reload();
    };
    container.addEventListener('controllerchange', onControllerChange);

    let disposed = false;
    let removeVisibility = (): void => {};
    void container.ready.then((registration) => {
      if (disposed) return;

      // The load itself was a check, so the clock starts now.
      let lastCheck = Date.now();
      const onVisibility = (): void => {
        if (document.visibilityState !== 'visible') return;
        if (Date.now() - lastCheck < UPDATE_CHECK_INTERVAL_MS) return;
        lastCheck = Date.now();
        // Offline, or the server between deploys: the next return asks again.
        registration.update().catch(() => {});
      };
      document.addEventListener('visibilitychange', onVisibility);
      removeVisibility = () => {
        document.removeEventListener('visibilitychange', onVisibility);
      };
      if (registration.waiting) setWaiting(registration.waiting);

      const watch = (installing: ServiceWorker): void => {
        installing.addEventListener('statechange', () => {
          // `installed` with an existing controller means an update is ready;
          // without one it is the first install, which needs no prompt.
          if (installing.state === 'installed' && container.controller) {
            setWaiting(installing);
          }
        });
      };

      /*
       * An update whose install began before this mounted — the browser's
       * own check on navigation, or a reload during one — is
       * `registration.installing` by now and its `updatefound` has already
       * fired. Watching the event alone missed it, and the prompt waited for
       * the next check to find a worker that was already waiting.
       */
      if (registration.installing) watch(registration.installing);
      registration.addEventListener('updatefound', () => {
        if (registration.installing) watch(registration.installing);
      });
    });

    return () => {
      disposed = true;
      removeVisibility();
      container.removeEventListener('controllerchange', onControllerChange);
    };
  }, []);

  if (!waiting) return null;

  return (
    <div className="update-prompt" role="status" aria-live="polite">
      <span>A new version of {config.appName} is ready.</span>
      <button
        type="button"
        className="update-prompt__action"
        onClick={() => {
          requested.current = true;
          waiting.postMessage({ type: 'SKIP_WAITING' });
        }}
      >
        Update
      </button>
    </div>
  );
}
