/**
 * Service worker. The cache token is the one thing on this surface that is most
 * likely to be got wrong, so it is worth stating the failure in full (G-035):
 *
 *   Caches must rotate on every DEPLOY, not every TAG. A deploy without a fresh
 *   tag produces a byte-identical sw.js, so the browser detects no worker update
 *   and install/activate never runs — installed PWAs keep serving previously
 *   cached assets indefinitely. Meanwhile index.html, served
 *   stale-while-revalidate, DOES pick up the new markup. Symptom: new HTML running
 *   against old JavaScript, in installed PWAs only. The user cannot clear it: no
 *   update toast fires, because no update was detected. It never reproduces in a
 *   normal browser tab.
 *
 * The fix is one line and it is the CACHE line below: the cache name ends in
 * `{tag}-{short-sha}`, so the bytes of this file change on every deploy whether or
 * not anyone tagged. scripts/build-frontend.sh substitutes the token HERE AND
 * NOWHERE ELSE and asserts both halves of that — the displayed version stays the
 * clean tag (§0.6), which is why js/build.ts deliberately has no cache token in it.
 *
 * backend/internal/version/version.go implements the identical split:
 * `Display()` returns the tag, `CacheToken()` returns tag + "-" + commit, with
 * "Never substitute Display() here" on the latter.
 */
import { cachePrefix, staleCaches } from "./swcache";

/*
 * The service-worker globals, declared locally.
 *
 * lib.dom does not declare ServiceWorkerGlobalScope or FetchEvent, and adding
 * lib.webworker alongside lib.dom produces hundreds of duplicate-identifier errors
 * — so a shared tsconfig cannot type both this file and app.ts from the standard
 * libs. Declaring the four shapes this worker actually touches keeps the whole
 * frontend under one `tsc --noEmit` (scripts/checks/typecheck-frontend.sh runs
 * exactly one project), and a wrong shape here fails at typecheck rather than at
 * install time in a browser nobody is watching.
 *
 * Declared FIRST, before the cache name, because the cache name is derived from
 * `sw.registration.scope` — a `const` referenced above its own initialisation is a
 * TDZ error that kills the worker at evaluation, which presents as a registration
 * that silently never activates.
 */
interface SwExtendableEvent extends Event {
    waitUntil(promise: Promise<unknown>): void;
}

interface SwFetchEvent extends Event {
    readonly request: Request;
    respondWith(response: Response | Promise<Response>): void;
}

interface SwGlobalScope {
    addEventListener(type: "install" | "activate", listener: (event: SwExtendableEvent) => void): void;
    addEventListener(type: "fetch", listener: (event: SwFetchEvent) => void): void;
    skipWaiting(): Promise<void>;
    readonly clients: { claim(): Promise<void> };
    readonly location: { readonly origin: string };
    /** The URL this worker controls. Used as the cache namespace — see below. */
    readonly registration: { readonly scope: string };
}

const sw = self as unknown as SwGlobalScope;

/*
 * The cache name is NAMESPACED, and the namespace is not decoration.
 *
 * CacheStorage is partitioned by ORIGIN, not by service-worker scope. A worker
 * registered at /{repo}/ sees, and can delete, every cache belonging to every
 * other application served from the same origin. The configured origin here is
 * https://vppillai.github.io (config/instances/dev.yaml `allowed_origin`), which
 * is a GitHub Pages user site serving a dozen other project sites — so an
 * unprefixed name plus a "delete everything that is not mine" sweep in `activate`
 * is a routine that wipes a sibling PWA's shell on every deploy of this one, in
 * both directions, with nothing in either app to explain it.
 *
 * The naming and the sweep predicate live in ./swcache so they can be unit-tested
 * without a browser — read that file for the whole argument. No legacy sweep is
 * needed for the previous unprefixed scheme: it never reached a deployed page, so no
 * browser anywhere holds a cache under it.
 */
const CACHE_PREFIX = cachePrefix(sw.registration.scope);
const CACHE = `${CACHE_PREFIX}{{CACHE_TOKEN}}`;

/**
 * The shell, precached on install. Relative paths: a GitHub Pages project site is
 * served under /{repo}/, so a root-absolute path here caches the wrong origin path
 * and every entry misses (§10.6).
 */
const SHELL: readonly string[] = [
    "./",
    "./index.html",
    "./manifest.webmanifest",
    "./css/tokens.css",
    "./css/app.css",
    "./js/app.js",
    "./assets/icon.svg",
];

sw.addEventListener("install", (event) => {
    event.waitUntil(
        (async () => {
            const cache = await caches.open(CACHE);
            // addAll is atomic: one 404 fails the whole install, which is correct.
            // A partially populated cache under a fresh token is the state that
            // produces "some assets are new, some are old" — the same symptom as
            // G-035 with a different cause.
            await cache.addAll(SHELL.slice());
            // Take over immediately instead of waiting for every tab to close.
            // The tradeoff is real: assets can swap under a page that is already
            // running. It is the right side of the tradeoff here because the
            // alternative IS G-035's symptom — an installed app serving old
            // JavaScript with no way for the user to force an update — and because
            // an update prompt is a Phase 1 concern on a surface that can afford
            // to ask.
            await sw.skipWaiting();
        })(),
    );
});

sw.addEventListener("activate", (event) => {
    event.waitUntil(
        (async () => {
            // Every cache of THIS app but this deploy's. This is the other half of
            // the token doing its work: a new token means a new cache name, and the
            // previous deploy's bytes are deleted rather than accumulating in the
            // origin's storage quota until an eviction picks a victim at random.
            //
            // The prefix test is what confines that to this app. caches.keys()
            // returns every cache in the ORIGIN — CacheStorage is not partitioned
            // by worker scope — so `name !== CACHE` alone deletes the shell of
            // every other application on the same GitHub Pages user site. See the
            // CACHE_PREFIX comment above; this is the line that depends on it.
            //
            // CacheStorage only, and it must stay that way. Phase 1 buffers audio
            // in IndexedDB and prunes it only after the server confirms the upload
            // (I2: audio is never lost to a software bug). A cleanup routine here
            // that ever grew to clear origin storage generally would delete
            // unuploaded audio on every deploy — silently, because a VAD
            // false-negative and a lost recording look identical to the user.
            const names = await caches.keys();
            await Promise.all(staleCaches(names, CACHE_PREFIX, CACHE).map((name) => caches.delete(name)));
            await sw.clients.claim();
        })(),
    );
});

sw.addEventListener("fetch", (event) => {
    const request = event.request;
    if (request.method !== "GET") {
        return;
    }

    // Cross-origin is never intercepted, which is what keeps the API out of any
    // cache. A cached /v1/health would defeat the only purpose the endpoint has
    // (§0.6), and a cached authenticated response would be a data-at-rest question
    // this phase has not answered.
    if (new URL(request.url).origin !== sw.location.origin) {
        return;
    }

    if (request.mode === "navigate") {
        // Network-first for the document, cache as the offline fallback. The
        // reverse — cache-first HTML — is how a stale shell outlives its own
        // deploy, and it is the half of G-035 that a fresh token does not fix.
        event.respondWith(
            (async () => {
                try {
                    return await fetch(request);
                } catch {
                    // cacheName, always. A bare caches.match searches EVERY cache
                    // in the origin, so a sibling app on the same GitHub Pages user
                    // site that happens to have cached an identical URL would have
                    // its response served as this app's shell.
                    const cached = await caches.match("./index.html", { cacheName: CACHE });
                    return cached ?? Response.error();
                }
            })(),
        );
        return;
    }

    // Cache-first for the shell: these are the bytes the token versions, so a hit
    // is by definition this deploy's asset.
    event.respondWith(
        (async () => {
            // cacheName, always — same reason as the navigate branch above.
            const cached = await caches.match(request, { cacheName: CACHE });
            if (cached !== undefined) {
                return cached;
            }
            const response = await fetch(request);
            // Only same-origin successes are stored. An opaque or error response
            // cached here would be served indefinitely as if it were the asset.
            if (response.ok && response.type === "basic") {
                const cache = await caches.open(CACHE);
                await cache.put(request, response.clone());
            }
            return response;
        })(),
    );
});
