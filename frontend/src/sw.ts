/// <reference lib="webworker" />

/**
 * The service worker.
 *
 * Built with `vite-plugin-pwa` in `injectManifest` mode rather than
 * `generateSW`. The generated recipes are fine for cache-first static sites and
 * fight both things this app actually needs: a network-first shell and an
 * update strategy that does *not* call `skipWaiting`. Owning ~130 lines is
 * cheaper than working around a generator for each of them.
 *
 * Two rules, each fixing something v1 got wrong:
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
 *
 * There is deliberately no Background Sync here. The worker has no session and
 * no API client, so all a `sync` event could ever do was post a message to an
 * open tab asking it to flush — a tab that was already flushing on reconnect
 * through `useOfflineQueue`. The two flushes ran concurrently, replayed the
 * same PATCH, and the loser marked a delivered edit dead. It also does not
 * exist on WebKit, the platform this app is for. Deleting it removed a race
 * and gained nothing anyone could notice.
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
 *
 * A non-OK response falls back to the shell exactly like a network failure
 * does. GitHub Pages (and any static host) has no server-side route for a
 * deep-linked SPA path — `fetch` for `/notes/{id}` resolves with a real,
 * non-throwing `404` response, not an error, so treating only a thrown
 * exception as "the network didn't have it" left every hard refresh, deep
 * link and bookmark to anything but the site root showing the host's 404
 * page instead of the app. The router only gets a chance to make sense of
 * the path once the shell it lives in has actually loaded.
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
      return response;
    }

    const fallback = (await cache.match(request)) ?? (await matchShell());
    return fallback ?? response;
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
