import { useEffect, useState } from 'react';

/**
 * The single update strategy.
 *
 * A new worker installs and *waits*. This prompt is the only thing that lets it
 * through, by messaging `SKIP_WAITING`; the page reloads once on
 * `controllerchange`. v1 called `skipWaiting()` at install while also showing a
 * refresh toast, so the two raced and a session could be served half of each
 * build.
 */
export function UpdatePrompt() {
  const [waiting, setWaiting] = useState<ServiceWorker | null>(null);

  useEffect(() => {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;

    let reloading = false;
    const onControllerChange = (): void => {
      // Guarded: `controllerchange` can fire more than once, and an unguarded
      // reload here is a refresh loop.
      if (reloading) return;
      reloading = true;
      window.location.reload();
    };
    navigator.serviceWorker.addEventListener('controllerchange', onControllerChange);

    let disposed = false;
    void navigator.serviceWorker.ready.then((registration) => {
      if (disposed) return;
      if (registration.waiting) setWaiting(registration.waiting);

      registration.addEventListener('updatefound', () => {
        const installing = registration.installing;
        if (!installing) return;
        installing.addEventListener('statechange', () => {
          // `installed` with an existing controller means an update is ready;
          // without one it is the first install, which needs no prompt.
          if (installing.state === 'installed' && navigator.serviceWorker.controller) {
            setWaiting(installing);
          }
        });
      });
    });

    return () => {
      disposed = true;
      navigator.serviceWorker.removeEventListener('controllerchange', onControllerChange);
    };
  }, []);

  if (!waiting) return null;

  return (
    <div className="update-prompt" role="status" aria-live="polite">
      <span>A new version of Chintan is ready.</span>
      <button
        type="button"
        className="update-prompt__action"
        onClick={() => {
          waiting.postMessage({ type: 'SKIP_WAITING' });
        }}
      >
        Update
      </button>
    </div>
  );
}
