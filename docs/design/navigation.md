# Navigation

Every state in the app is a real URL, and the history behind those URLs is
a short stack with Home at the bottom, as a phone app keeps it, not a
browser's trail of every screen visited. Code: the URLs (`frontend/src/app/
routes.ts`), the route table (`router.tsx`), the base path (`basePath.ts`),
the stack rules (`useTabNavigation.ts`, `useBackGuard.ts`, `Redirect.tsx`),
focus and scroll on a route change (`useRouteFocus.ts`,
`useScrollRestore.ts`) and the render-fault screen (`RouteError.tsx`). The
shell that mounts them is `shell.md`'s; the library screen itself is
`home.md`'s.

## Routes

`ROUTES` names every destination; nothing else spells a path.

| Route | Screen |
|---|---|
| `/` | the library; Home and Notes are the same screen |
| `/?view=archived` | the archive: the library with a filter, not a destination of its own (`ARCHIVED_VIEW`) |
| `/?mode=ask` | the library's search field in Ask mode (`ASK_MODE`); the mode is in the URL so it survives a reload and Back from a cited note, the question never is |
| `/notes/:id` | a note (`ROUTES.note(id)`, `notePattern`) |
| `/capture`, `/capture?note=<id>` | the recording screen, full screen; `captureInto` records straight into an open note |
| `/settings` | You |
| `/about`, `/usage` | reached from You |

`LEGACY_ROUTES` maps the paths from before the library became home —
`/notes`, `/archive`, `/search`, and `/talk`, the manifest shortcut an
installed app keeps for days after a deploy drops it — onto the library with
the equivalent query; `legacyRedirect` carries the query string across, so
`/search?q=` keeps its `q`. `Redirect` replaces the alias entry rather than
pushing (Back must not land on it and bounce forward) and seeds Home beneath
a destination that is not the bare library. Unknown paths render
`NotFoundScreen`. Settings, Usage, About and Capture are lazy chunks; the
library and the note stay in the main chunk, where every launch lands.

The router's `basename` is Vite's `BASE_URL` with its trailing slash
(`routerBasename`), because that slash is the manifest's `scope` and
`start_url` and the service worker's registration scope; a document loaded at
the scope path without it is moved inside the scope before the router reads
the address bar (`scopedEntryUrl` in `basePath.ts`, called from `App.tsx`). Tests: `basePath.test.ts`,
`routing.test.tsx` "every in-app URL stays inside the deploy scope",
`e2e/scope.spec.ts`.

## The stack

Home `/` — any search or Ask mode, but not the archive (`isHome`) — is
always entry 0. You and the archive, the top-level screens, only ever sit at
entry 1. A note, About and Usage push above them. So Back from anywhere walks
a short fixed path that ends at Home, and Back from Home leaves the app: in
the installed PWA Android's Back closes it, in a tab it goes wherever the tab
came from. There is no "press again to exit".

The index is React Router's private `history.state.idx`, read only through
`historyIndex()` and only inside `useTabNavigation` and `useBackGuard`, so a
router upgrade that moves it breaks one helper and its test. Tabs and
back-links stay real `<Link>`s with real `href`s; a plain primary click
(`isPlainClick`) is the app's move, and a modified or middle click is left to
the browser to open a new tab.

`useTabNavigation` offers three moves:

- `goHome` (the Home tab): from above entry 0, `navigate(-index)`, a POP, so
  the list comes back at its own place under its own key. Already on Home
  it clears a filter and scrolls `#main` to the top. Deleting a note goes
  Home this way (`NoteActions`).
- `goTab(to)` (the You tab; the Archived chip in `LibraryList` and
  `LibraryField`; delete-forever from the archive in `NoteActions`): from
  entry 0 push; from entry 1 replace; from deeper, go back to entry 1 and
  then replace it. Arriving where it already is only scrolls to the top.
- `goBackTo(to)` (a note's "‹ Notes", "‹ You" on About and Usage via
  `YouBackLink`, the Archived chip turning off): `navigate(-1)` when the
  screen beneath is there — anything above entry 0 for Home, above entry 1
  for You — and otherwise, on a cold deep link that seeded only Home, `to`
  in this entry's place, so the stack still ends `[Home]` or `[Home, You]`.

### Seeding Home

A cold start on a deep link has the app's first entry as the tab's first, so
Back would exit. `useBackGuard`, first mount only, replaces that entry with
Home and pushes the requested route (`seedHome`), the archive included. It
does nothing on Home itself (trapping the user there would be hostile), when
`historyIndex() > 0` (a reload keeps the tab's history, and seeding again
would put a second Home under the reloaded screen), or on a legacy path,
whose `Redirect` seeds for itself.

### The pending tab move

A tab move from deep in the stack is two halves: a Back, then a replace once
the POP lands, because `history.go(-n)` settles in a later `popstate` and
returns nothing to wait on. The second half is parked by `setPending` in
`sessionStorage` under `chintan.nav.pending` (memory when storage is
denied), stamped with the `location.key` of the entry that set it and the time, and
taken by `usePendingTab` — mounted once in the shell — on the next POP. A
`history.go(-n)` is a silent no-op when the router's index overstates the
real stack, and a pending that never landed would otherwise be acted on by
the next reload's initial POP; so one taken at the entry that set it, or
older than `PENDING_TTL_MS = 10_000` ms, is dropped (`takePending`). The
same parking carries a hash a Back cannot: About's link to the Devices card
lands on You and then gets `#devices` from the pending. Tests:
`useTabNavigation.test.ts` "the pending tab move".

### What a sideways swipe does

A horizontal swipe moves between a note's segments under the strip's pill,
not between tabs; `note-screen.md` owns it, and `scroll-restore.spec.ts`
checks that Back after a swipe still returns Home to its place.

Tests for the stack: `routing.test.tsx` ("Back always means back", "an app's
stack, not a browser's", "old URLs still land somewhere"),
`e2e/back-nav.spec.ts` and the two Back cases at the end of `e2e/a11y.spec.ts`.

## Focus and scroll on a route change

`useRouteFocus(mainRef)` moves focus to `<main tabindex="-1">` when
`pathname` changes, skipping the first render: without it a screen reader
stays parked where the user tapped and a keyboard user's next Tab resumes
at the top of the document. Test: `routing.test.tsx` "moves focus to the
routed region on navigation".

`useScrollRestore(mainRef)` keeps `.app__main`'s offset — the list scrolls
inside it, not the window, so the browser's own restoration never sees it —
per history entry (`location.key`) and per path-with-query in
`sessionStorage` under `chintan.scroll.`, at most `KEEP = 50` places, oldest
dropped. A POP restores the entry's offset; a push whose state is
`RESTORE_SCROLL` restores the path's (the capture screen replaces its own
entry with the note it recorded into, so the note returns under a key it has
never had); anything else starts at 0. Because the rows are not there on the
first frame, the restore is retried each frame, up to `RESTORE_FRAMES =
60`, while the region is too short to hold the offset, and stops the moment the person
scrolls — any wheel, touch, pointer or key on the region, or an offset the
restore did not set — so it never drags the page back under a finger. Both
prefixes are under `chintan.`, which sign-out sweeps. Tests:
`e2e/scroll-restore.spec.ts`.

## A screen that fails to render

`RouteError` is the `ErrorBoundary` on the root and on every child route,
so a render fault replaces the outlet, never the document: a heading, a
sentence saying nothing is lost (notes live on the server, unsent audio in
IndexedDB), "Back to your notes" and "Reload the app", and one quotable line
of detail rather than a stack trace. Test: `routing.test.tsx` "a render fault
never leaves the user with no controls".

## History

`docs/backlog.md` and `docs/reviews/` hold the decisions behind these rules.
