/**
 * Every state in the app is a real URL. These are those URLs.
 *
 * Three tabs, one stack: the library is home, a note is stacked on it, and the
 * capture screen is full-screen. Back always means "back" — there is no sheet
 * state to collapse, so nothing here needs a reducer.
 */
export const ROUTES = {
  /** The library. Also where Notes is: they are the same screen. */
  home: '/',
  notes: '/',
  note: (id: string) => `/notes/${encodeURIComponent(id)}`,
  notePattern: '/notes/:id',
  /** Archived notes, awaiting restore or purge. */
  archive: '/archive',
  settings: '/settings',
  capture: '/capture',
} as const;

/** The `view` query parameter that switches the library to the archive. */
export const ARCHIVED_VIEW = 'archived';

/**
 * URLs from before the library was home. Kept so bookmarks, the OS share
 * sheet and any old notification link land somewhere rather than on Not Found.
 * Each maps onto the library with the equivalent query.
 */
export const LEGACY_ROUTES: Record<string, string> = {
  '/notes': '/',
};

/**
 * Where a legacy path goes now, carrying its query string across (a search
 * link keeps its `q`). `null` when the path is not a legacy one.
 */
export function legacyRedirect(pathname: string, search: string): string | null {
  const target = LEGACY_ROUTES[pathname.length > 1 ? pathname.replace(/\/$/, '') : pathname];
  if (!target) return null;
  const merged = new URLSearchParams(target.split('?')[1] ?? '');
  for (const [key, value] of new URLSearchParams(search)) merged.set(key, value);
  const query = merged.toString();
  return query ? `/?${query}` : '/';
}
