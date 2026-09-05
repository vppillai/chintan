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
  /** The same note, opened on its recordings rather than its text. */
  noteRecordings: (id: string) => `/notes/${encodeURIComponent(id)}?tab=recordings`,
  notePattern: '/notes/:id',
  /** Archived notes are the library with a filter chip, not a destination. */
  archive: '/?view=archived',
  /** The library's search field in Ask mode: a question over the notes rather than a filter. */
  ask: '/?mode=ask',
  settings: '/settings',
  /** What Chintan is and where its data lives. Reached from You. */
  about: '/about',
  capture: '/capture',
  /** Record straight into a note the user is already reading. */
  captureInto: (noteId: string) => `/capture?note=${encodeURIComponent(noteId)}`,
} as const;

/** The `view` query parameter that switches the library to the archive. */
export const ARCHIVED_VIEW = 'archived';

/**
 * The `mode` query parameter that turns the search field into Ask. In the URL
 * like `q` and `view`, so it survives a reload and Back from a cited note
 * returns to the answer — but the question itself never is: a question is
 * not a place, and it should not be in anyone's history or a shared link.
 */
export const ASK_MODE = 'ask';

/**
 * URLs from before the library was home. Kept so bookmarks, the OS share
 * sheet and any old notification link land somewhere rather than on Not Found.
 * Each maps onto the library with the equivalent query.
 */
export const LEGACY_ROUTES: Record<string, string> = {
  '/notes': '/',
  '/archive': ROUTES.archive,
  '/search': '/',
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
