/// <reference lib="webworker" />

/**
 * The service worker.
 *
 * Built with `vite-plugin-pwa` in `injectManifest` mode rather than
 * `generateSW`. The generated recipes are fine for cache-first static sites and
 * fight all three things this app actually needs: a network-first shell, an
 * update strategy that does *not* call `skipWaiting`, and a Background Sync
 * handler for the mutation queue. Owning ~150 lines is cheaper than working
 * around a generator for each of them.
 *
 * Three rules, each fixing something v1 got wrong:
 *
 * 1. **Network-first for navigations.** v1 was cache-first including its
 *    generated `config.js`, so recreating the stack permanently bricked every
 *    installed client — the app kept serving a config pointing at an API that
 *    no longer existed, with no path to self-heal.
 * 2. **One update strategy.** v1 called `skipWaiting()` and `clients.claim()`
 *    on install *and* showed a "refresh to update" toast, so a session could be
 *    served half the old bundle and half the new one. Here the new worker waits
 *    until the user accepts; `skipWaiting` runs only in response to that
 *    explicit message.
 * 3. **Background Sync that exists.** v1 registered a `sync` listener whose
 *    handler was an empty stub, for a tag nothing ever registered.
 */

import { cleanupOutdatedCaches, precacheAndRoute } from 'workbox-precaching';

declare const self: ServiceWorkerGlobalScope & {
  __WB_MANIFEST: { url: string; revision: string | null }[];
};

const RUNTIME_CACHE = 'chintan-runtime-v1';

/**
 * The precached app shell.
 *
 * Derived from the registration scope rather than hard-coded to `/index.html`:
 * a Pages sub-path deploy (`VITE_BASE=/repo/dev/`) precaches
 * `/repo/dev/index.html`, and the constant was wrong for every one of them.
 */
export const SHELL_URL = new URL('index.html', self.registration.scope).href;

/**
 * Workbox stores the shell as `index.html?__WB_REVISION__=<hash>`, so a plain
 * `caches.match(SHELL_URL)` misses it. Matching without the query string is
 * what makes an offline cold start at an unvisited URL — a car park, a lift, or
 * the manifest's own "Record a thought" shortcut straight to `/capture` —
 * render the app instead of a blank page reading "Offline".
 */
async function matchShell(): Promise<Response | undefined> {
  return caches.match(SHELL_URL, { ignoreSearch: true });
}

/** How long to wait for the network before falling back to cache. */
const NETWORK_TIMEOUT_MS = 4_000;

/** The tag the page registers. Must match `offline/backgroundSync.ts`. */
export const SYNC_TAG = 'chintan-flush-queue';

// Content-hashed build assets. Safe to cache immutably: a new build produces
// new filenames, so a stale asset can never shadow a fresh one.
precacheAndRoute(self.__WB_MANIFEST);
cleanupOutdatedCaches();

self.addEventListener('install', () => {
  // Deliberately NOT skipWaiting(). The new worker waits until the user accepts
  // the update, so a session is never served a mix of two builds.
});

self.addEventListener('activate', (event: ExtendableEvent) => {
  event.waitUntil(
    (async () => {
      const names = await caches.keys();
      await Promise.all(
        names
          .filter((name) => name.startsWith('chintan-runtime-') && name !== RUNTIME_CACHE)
          .map((name) => caches.delete(name)),
      );
      await self.clients.claim();
    })(),
  );
});

self.addEventListener('message', (event: ExtendableMessageEvent) => {
  // The only way skipWaiting happens: the user pressed the update control.
  if ((event.data as { type?: string } | null)?.type === 'SKIP_WAITING') {
    void self.skipWaiting();
  }
});

/**
 * Network-first with a timeout, falling back to the cached shell.
 *
 * The timeout matters on mobile: a connection that hangs rather than failing
 * would otherwise leave the user on a blank screen indefinitely when a
 * perfectly good cached shell is sitting right there.
 */
async function networkFirst(request: Request): Promise<Response> {
  const cache = await caches.open(RUNTIME_CACHE);

  try {
    const controller = new AbortController();
    const timer = setTimeout(() => {
      controller.abort();
    }, NETWORK_TIMEOUT_MS);

    const response = await fetch(request, { signal: controller.signal });
    clearTimeout(timer);

    if (response.ok) {
      await cache.put(request, response.clone());
    }
    return response;
  } catch {
    const cached = (await cache.match(request)) ?? (await matchShell());
    if (cached) return cached;
    return new Response('Offline', { status: 503, statusText: 'Offline' });
  }
}

self.addEventListener('fetch', (event: FetchEvent) => {
  const { request } = event;
  if (request.method !== 'GET') return;

  const url = new URL(request.url);

  // API traffic is never cached by the worker. The app's own IndexedDB is the
  // offline store, and it knows what is stale; an opaque HTTP cache in front of
  // it would serve stale notes with no way to label them as such — which is
  // exactly the X-Offline header v1 set and nothing read.
  if (url.pathname.startsWith('/v1/')) return;

  // Cross-origin (presigned S3 audio, Cognito) is left alone.
  if (url.origin !== self.location.origin) return;

  if (request.mode === 'navigate') {
    event.respondWith(networkFirst(request));
  }
});

/**
 * Background Sync: flush the offline mutation queue even if the tab is gone.
 *
 * The worker cannot import the app's API client — it has no session and no
 * bundle — so it wakes any open client and asks it to flush. When there is no
 * client, the registration is retained by the browser and retried, and the
 * foreground `refetchOnReconnect` path covers the common case anyway.
 */
self.addEventListener('sync', ((event: SyncEvent) => {
  if (event.tag !== SYNC_TAG) return;
  event.waitUntil(
    (async () => {
      const clients = await self.clients.matchAll({
        type: 'window',
        includeUncontrolled: true,
      });
      for (const client of clients) {
        client.postMessage({ type: 'FLUSH_QUEUE' });
      }
      // No client to do the work: reject so the browser keeps the registration
      // and retries later rather than silently dropping the queue.
      if (clients.length === 0) throw new Error('No client available to flush');
    })(),
  );
}) as EventListener);

interface SyncEvent extends ExtendableEvent {
  readonly tag: string;
}
