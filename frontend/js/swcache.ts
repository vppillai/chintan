/**
 * Cache naming for the service worker.
 *
 * Two functions and no state, in their own module for one reason: the sweep in the
 * worker's `activate` handler is destructive, it runs on every deploy, and it cannot
 * be exercised by anything else in this repository — there is no headless browser in
 * containers/toolchain (§4A.7 records why) and therefore no way to install a worker
 * and inspect CacheStorage. Kept here, it is a pure function over a list of strings
 * that frontend/test/swcache.test.ts drives directly.
 *
 * The failure this exists to prevent, stated in full because it is invisible from
 * this app's side:
 *
 *   CacheStorage is partitioned by ORIGIN, not by service-worker scope. `allowed_origin`
 *   for this instance is a GitHub Pages user site (config/instances/dev.yaml) that
 *   serves a dozen other project sites, so `caches.keys()` inside this worker returns
 *   the cache names of every one of them. A sweep that deletes "everything that is not
 *   my current cache" therefore deletes a sibling PWA's shell on every deploy of this
 *   app. That app is then broken offline, its next load silently refetches or fails,
 *   and nothing in either application explains it.
 *
 * The scope is the namespace, rather than a literal: it is unique per project site by
 * construction, it cannot drift from where the worker is actually registered, and a
 * hardcoded app name here would be a §7.3 violation that check-brand-strings.sh
 * rejects. Same shared-origin reasoning as G-007, applied to storage instead of URLs.
 */

/**
 * The prefix every cache belonging to this app carries.
 *
 * `scope` is `ServiceWorkerRegistration.scope` — an absolute URL ending in "/".
 */
export function cachePrefix(scope: string): string {
    return `${scope}shell:`;
}

/**
 * The caches this app owns that are not the current deploy's, and nothing else.
 *
 * Both conditions are load-bearing and they fail in opposite directions: without the
 * prefix test this deletes other applications' storage, and without the `!== current`
 * test it deletes the shell this deploy just installed.
 */
export function staleCaches(names: readonly string[], prefix: string, current: string): readonly string[] {
    return names.filter((name) => name.startsWith(prefix) && name !== current);
}
