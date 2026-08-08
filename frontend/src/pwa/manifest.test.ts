import { describe, expect, it } from 'vitest';

import { basedPath, chintanManifest } from '../../manifest.config.ts';

/**
 * The manifest's URLs, at a sub-path deploy.
 *
 * Reported from the live app: `GET /chintan/icon-192.png → 404` on a deep link,
 * while `/chintan/dev/icon-192.png` returned 200. Every URL-bearing field was a
 * bare relative path, and relative resolution depends on which URL the manifest
 * was fetched from and on trailing slashes — fine at the root, wrong for every
 * instance of this app, which is served from `/<repo>/<instance>/`.
 *
 * A test asserting only that icons are *declared* passes against that bug, so
 * every assertion here is about the shape of the path.
 */

const BASE = '/chintan/dev/';

function urls(manifest: ReturnType<typeof chintanManifest>): string[] {
  return [
    manifest.start_url,
    manifest.scope,
    ...manifest.icons.map((icon) => icon.src),
    ...manifest.shortcuts.map((shortcut) => shortcut.url),
    ...manifest.shortcuts.flatMap((shortcut) => shortcut.icons.map((icon) => icon.src)),
  ];
}

describe('the manifest at a sub-path deploy', () => {
  it('states every URL from the base, leaving nothing to resolve', () => {
    for (const url of urls(chintanManifest(BASE))) {
      expect(url, `${url} is not base-absolute`).toMatch(/^\/chintan\/dev\//);
    }
  });

  it('never emits a bare relative path, which is what broke', () => {
    for (const url of urls(chintanManifest(BASE))) {
      expect(url.startsWith('/')).toBe(true);
      expect(url).not.toBe('.');
    }
  });

  it('scopes the installed app to its own instance', () => {
    const manifest = chintanManifest(BASE);
    // `scope` decides which navigations stay inside the installed app. A "."
    // that resolved a directory too high would swallow a sibling instance.
    expect(manifest.scope).toBe('/chintan/dev/');
    expect(manifest.start_url).toBe('/chintan/dev/');
  });

  it('still works at the root, where the bug was invisible', () => {
    const manifest = chintanManifest('/');
    expect(manifest.start_url).toBe('/');
    expect(manifest.icons[0]?.src).toBe('/icon-192.png');
  });

  it('does not depend on the base carrying a trailing slash', () => {
    expect(basedPath('/chintan/dev', 'icon-192.png')).toBe('/chintan/dev/icon-192.png');
    expect(basedPath('/chintan/dev/', 'icon-192.png')).toBe('/chintan/dev/icon-192.png');
    expect(basedPath('/chintan/dev/', '/icon-192.png')).toBe('/chintan/dev/icon-192.png');
  });

  it('names icons the build actually ships', () => {
    // The four in `public/`. A manifest may declare only what exists: an icon
    // that 404s is how an installed app ends up with a blank home-screen tile.
    expect(chintanManifest(BASE).icons.map((icon) => icon.src)).toEqual([
      '/chintan/dev/icon-192.png',
      '/chintan/dev/icon-512.png',
      '/chintan/dev/icon-maskable-192.png',
      '/chintan/dev/icon-maskable-512.png',
    ]);
  });
});
