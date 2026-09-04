/**
 * Where the app lives on its host, and the two rules that keep every URL it
 * makes inside that place.
 *
 * Vite's `BASE_URL` is `/chintan/dev/` on a Pages deploy — trailing slash
 * included, and that slash is the manifest's `scope` and `start_url` and the
 * service worker's registration scope. The router used to be given the path
 * with the slash stripped, and React Router builds the home URL as the
 * basename verbatim, so every in-app navigation to the library — after
 * Discard, after Archive, the Home tab, typing in search — landed on
 * `/chintan/dev` and `/chintan/dev?view=archived`: outside the installed
 * app's scope, outside the worker's, and a browser error page when reloaded
 * offline (QA D5). The slash stays on.
 */

/** The router's basename: Vite's `BASE_URL` as it is, slash and all. */
export function routerBasename(base: string): string {
  return base || '/';
}

/**
 * The URL to replace the entry with when the document was loaded at the
 * scope's path without its trailing slash, or `null` when there is nothing
 * to fix.
 *
 * GitHub Pages redirects `/chintan/dev` to `/chintan/dev/` before the app
 * runs, so this is for hosts that serve both spellings, and for the old
 * bookmarks the previous router produced. The query and hash ride along:
 * `/chintan/dev?view=archived` is still the archive.
 */
export function scopedEntryUrl(
  base: string,
  location: { pathname: string; search: string; hash: string },
): string | null {
  if (base === '/' || !base.endsWith('/')) return null;
  const bare = base.slice(0, -1);
  if (location.pathname !== bare) return null;
  return `${base}${location.search}${location.hash}`;
}
