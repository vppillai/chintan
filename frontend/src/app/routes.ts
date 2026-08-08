/** Every state in the app is a real URL (spec §5.2). These are those URLs. */
export const ROUTES = {
  home: '/',
  notes: '/notes',
  note: (id: string) => `/notes/${encodeURIComponent(id)}`,
  notePattern: '/notes/:id',
  search: '/search',
  settings: '/settings',
  capture: '/capture',
} as const;
