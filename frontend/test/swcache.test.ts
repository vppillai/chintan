/**
 * Regression tests for the service worker's cache naming and its activate sweep.
 *
 * The defect these exist for: the sweep deleted every cache in the ORIGIN rather than
 * every cache belonging to this app. CacheStorage is partitioned by origin, not by
 * worker scope, and this app's origin is a GitHub Pages user site serving a dozen
 * other project sites — so `caches.keys().filter(name => name !== CACHE)` deleted a
 * sibling PWA's shell on every deploy, in both directions, with nothing in either
 * application to explain it.
 *
 * Driven as pure functions because there is no other way: containers/toolchain pins
 * no headless browser (§4A.7 records the reasoning), so no test here can install a
 * worker or open a real CacheStorage. That constraint is why frontend/js/swcache.ts
 * exists as a module at all.
 */
import { expect, test } from "bun:test";
import { cachePrefix, staleCaches } from "../js/swcache";

// The literal scenario from the review, with a real sibling from the same account.
const OWN_SCOPE = "https://vppillai.github.io/chintan/";
const SIBLING_SCOPE = "https://vppillai.github.io/whiteboard/";

test("two apps on one origin get different cache prefixes", () => {
    expect(cachePrefix(OWN_SCOPE)).not.toBe(cachePrefix(SIBLING_SCOPE));
    expect(cachePrefix(OWN_SCOPE).startsWith(OWN_SCOPE)).toBe(true);
});

test("the sweep leaves another application's caches alone", () => {
    const prefix = cachePrefix(OWN_SCOPE);
    const current = `${prefix}v0.2.0-abc1234`;
    const names = [
        "whiteboard-v4",
        `${cachePrefix(SIBLING_SCOPE)}v1.0.0-deadbee`,
        "passbook-static",
        `${prefix}v0.1.0-83e2ecd`,
        current,
    ];

    // Exactly one entry: this app's previous deploy. Asserted as an equality on the
    // whole list rather than "does not contain whiteboard-v4", so a future predicate
    // that widens by accident fails here.
    expect(staleCaches(names, prefix, current)).toEqual([`${prefix}v0.1.0-83e2ecd`]);
});

test("the sweep never deletes the cache this deploy just installed", () => {
    const prefix = cachePrefix(OWN_SCOPE);
    const current = `${prefix}v0.2.0-abc1234`;
    expect(staleCaches([current], prefix, current)).toEqual([]);
});

test("a name that merely contains the prefix is not swept", () => {
    // A prefix test, not a substring test: `includes` would match another app whose
    // own cache name happened to embed this app's scope, e.g. a proxy or a mirror.
    const prefix = cachePrefix(OWN_SCOPE);
    const current = `${prefix}v0.2.0-abc1234`;
    expect(staleCaches([`mirror-of-${prefix}v0.1.0`], prefix, current)).toEqual([]);
});

test("the cache token, not the tag, is what rotates the cache name (G-035)", () => {
    // Two deploys of ONE tag must produce two different names, or an installed PWA
    // serves the first bundle it ever cached, forever, with no update toast.
    const prefix = cachePrefix(OWN_SCOPE);
    expect(`${prefix}v0.1.0-aaaaaaa`).not.toBe(`${prefix}v0.1.0-bbbbbbb`);
});
