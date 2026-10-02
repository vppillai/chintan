# The app shell

The frame every screen sits in: one banner, one `<main>`, one navigation,
the rows the shell stacks beneath the content, the service worker that
serves it offline, and the tokens every colour and size comes from. Code:
`frontend/src/components/AppShell.tsx` and its parts (`StatusRegion.tsx`,
`Toast.tsx`, `TabBar.tsx`, `RecordingIndicator.tsx`, `offline/OfflineBanner.
tsx`, `pwa/UpdatePrompt.tsx`), the worker (`src/sw.ts`), the manifest
(`frontend/manifest.config.ts`), the tokens (`src/styles/tokens.css`,
`src/theme/`) and the layout (`src/styles/shell.css`, `base.css`).

## Layout

`.app` is a grid of `auto minmax(0, 1fr)` plus auto rows: the banner, the
scrolling `.app__main`, and then whatever the shell stacks below — the
recording indicator, the toast, the update prompt and the tab bar — each a
row in normal flow, so nothing can overlay the content or the record button.
`data-screen` on the wrapper (`screenForPath`: `library`, `note`, `you`,
`usage`, `about`, `capture`, `other`) is the one switch the layout rules
read, so the shell cannot disagree with the URL. `<main id="main"
tabindex="-1">` is labelled with the screen's title and is where the skip
link (`base.css` `.skip-link`, `--z-skip-link`) and `useRouteFocus` land
(`navigation.md`). The on-screen keyboard's inset on `.app__main` is
`note-screen.md`'s. The shell mounts the app-wide hooks once: `useBackGuard`,
`usePendingTab`, `useScrollRestore` (`navigation.md`), `useKeyboardInset`
(`note-screen.md`), `useResendOnReconnect` (`offline.md`), `usePushWakeup`
(`push.md`) and `usePasskeyReturn` (`auth.md`). Below the gate — the shell
renders `SignedOutScreen` alone while `useAuthGate` is not `signed-in` — so
no authenticated query can mount without a token.

### The banner

`.app__banner` holds the `Wordmark` (not on the library, whose own heading
row carries it; `home.css` collapses the empty banner), `OfflineBanner`
(`offline.md`) and, on a note, `BannerRecord`, the mic shown only with the
keyboard up (`note-screen.md`).

### The tab bar

`TabBar` is `<nav aria-label="Main">`, a `1fr auto 1fr` grid of Home, the
record disc and You, `min-block-size: var(--layout-bottom-bar-height)` (the
disc's `--layout-record-button-size`, 4.75 rem, plus a `--space-2` either
side) with `--safe-bottom` under it. The tab is "Home", not "Notes", because
the library's heading already says Notes and two controls with one word read
as two places; it stays lit on a note (`matches`) because the tab names the
section. Tabs are `<Link>`s whose plain clicks become `goHome` and `goTab`
(`navigation.md`). The disc, push-to-talk and what the slots show while a
hold is in progress are `capture-ux.md`'s; the tabs keep their boxes, hidden
and `inert`, so the bar never changes height. The bar is not rendered on
`/capture`: a live microphone is the one state where an accidental tap on
Record or Home is worse than pressing Stop first. Tests: `TabBar.test.tsx`,
`routing.test.tsx` "notes first" and "the capture screen is full screen".

## Saying things

**One live region.** `StatusRegion` is the single always-mounted polite,
atomic region, and `announce(message)` the one way to speak through it: each
call replaces the last and is read whole. A screen with several regions of
its own loses announcements when two fire inside a second, and a region
added to the DOM together with its text is often not read at all. The shell
announces `<title> screen` as it lands (`SCREEN_TITLES`), or "Recording,
hands-free" when a hold locked onto the capture screen; the checklist
editor says its moves ("Made a sub-item of …", "Marked done", refusals) and
the note screen its outcomes through the same call. Test: `routing.test.tsx`
"exposes a polite live region for route announcements".

**One toast.** `Toast` is the transient notice with the one action it can
carry — "Deleted · kept in Archive for 30 days — Undo" (`showDeleted`, from
`NoteRow` and `NoteActions`), Tidy up list's Undo and "Try again"
(`useTidyList`, `ChecklistEditor`). It is a shell row above the tab bar, in
flow, so it can cover nothing and survives the route change a delete makes;
the region is always mounted and empty when quiet, and the visible card
exists only while there is a notice. `showToast` replaces whatever is
showing, except that a `weak` notice (a tick's Undo) never displaces a
standing notice that carries an action and is not weak — a tick right after
Delete done must not take away the Undo that brings the items back. The card
dismisses itself after `TOAST_MS = 6000` ms (or the notice's own `ms`), and
the clock stops while a pointer rests on it or focus is in it (WCAG 2.2.1).
Undo is a real button. Tests: `Toast.test.tsx`.

**The microphone is never invisible.** `RecordingIndicator` is a button row
above the bar on every screen but `/capture`, shown while `isCaptureBusy`
(requesting, recording, paused, stopping, uploading) and reading
"Recording — tap to return" (or the state's own label); tapping it returns
to the capture screen rather than stopping, because a recording surviving
navigation is the design. It stands aside where something else already says
it: on the library during an upload (the filing row) and on the note an
upload is going into (its filing banner). Tests: `RecordingIndicator.test.tsx`.

## The service worker and install

`vite-plugin-pwa` in `injectManifest` mode builds `src/sw.ts`, with
`registerType: 'prompt'` and `injectRegister: 'auto'`; the dev server runs
none. Two rules:

1. **The precached shell answers every navigation.** Every same-origin
   `navigate` request — Home, a deep link, the manifest's Record shortcut —
   is answered from the precache with this build's `index.html`
   (`matchPrecache(SHELL_URL)`, the URL derived from the registration scope
   so a sub-path deploy precaches the right file), so an offline cold start
   at an unvisited URL renders the app. A client with no precached shell
   falls to `networkFirst`: `NETWORK_TIMEOUT_MS = 4_000` ms, then the
   `chintan-runtime-v1` cache, then the shell; a non-OK response falls back
   the same way, because a static host answers a deep link with a real 404.
   `/v1/` traffic and cross-origin requests (presigned audio, Cognito) are
   never touched: IndexedDB is the offline store and knows what is stale.
2. **One update strategy.** `install` does not call `skipWaiting`; a new
   worker waits. `UpdatePrompt` shows "A new version is ready — Update"
   for a worker that is waiting, installing when the prompt mounted, or
   found later; Update posts `{ type: 'SKIP_WAITING' }`, the only message the
   worker acts on, and the page reloads once on `controllerchange` only
   when the user asked (the first install's `clients.claim()` fires it too).
   A foregrounded app asks for a new build on `visibilitychange`, at most
   every `UPDATE_CHECK_INTERVAL_MS` (30 min), because the browser re-checks
   `sw.js` only on a real navigation and an installed app is resumed, not
   navigated to. There is no Background Sync: the worker has no session.

The `push` and `notificationclick` handlers are `push.md`'s. Tests:
`UpdatePrompt.test.tsx`; `e2e/offline.spec.ts` "opening … offline serves
the app shell" and "a deep link is served from the precached shell";
`e2e/scope.spec.ts`.

**The manifest** (`chintanManifest(base, identity)`) states every URL from
the deploy base outright — `start_url`, `scope` and `id` are the base
itself, so one bundle installs per instance — with the name, short name and
description from the instance's YAML through `VITE_APP_*`, `display:
standalone`, `orientation: any` (a car mount is landscape), the SVG icon
first and PNG renders of it (`scripts/make-icons.mjs`) including maskable
ones, two JPEG screenshots for the install sheets (taken by
`e2e/screenshots.spec.ts`) and one shortcut, "Record a thought", to
`/capture`. `GROUND = '#fbf9f4'` and the two `theme-color` metas in
`index.html` are the only colours outside `tokens.css`: neither a manifest
nor a meta can read a custom property, and the splash and window chrome
must paint the ground the first frame paints. Tests: `pwa/manifest.test.ts`,
`e2e/manifest.spec.ts`. The app relies on the browser's own install
affordance; it has no install prompt of its own.

## Tokens and themes

`src/styles/tokens.css` is the only file allowed a literal colour or font
size; `bun run lint` runs `scripts/check-tokens.mjs`, which fails on a
literal elsewhere, on a class selector nothing renders and on a token
nothing reads. Colour tokens are semantic (`--color-ground`, `--color-ink`,
`--color-raised`, `--color-line` …); nothing outside the file names a hue.
The file also holds the type scale, spacing, radii, safe-area insets, the
layout constants (`--layout-content-max` 42 rem, the gutter, the record
button's size) and the z-order (`--z-content` 1 … `--z-skip-link` 60).

Two themes: Ink & Paper (`ink`, light, the default) and Nocturne
(`nocturne`, dark), selected by `data-theme` on `<html>`. The third
preference, `system`, inherits Ink & Paper and swaps to Nocturne under
`@media (prefers-color-scheme: dark)`, the block declared twice so an
explicit choice always outranks the media query. `ThemeProvider` puts the
*preference* on `<html data-theme>` (and the answer on
`data-resolved-theme`), persists it in `localStorage` under `chintan.theme`
(`THEME_STORAGE_KEY`; an unreadable store falls back to the default rather
than failing), and copies the resolved `--color-ground` into both
`theme-color` metas so the address bar follows an explicit choice;
`index.html` carries one meta per colour scheme for the moment before the
stylesheet loads. You's theme segments offer the three (`THEME_LABELS`).
Tests: `theme/useTheme.test.tsx`; `e2e/a11y.spec.ts` runs axe on every route
in both themes.

**Reduced motion** is honoured in one place: every transition reads
`--motion-duration-{fast,base,slow}` (140, 220, 340 ms), and `@media
(prefers-reduced-motion: reduce)` sets all three to 1 ms and, as a
backstop, forces every animation and transition to 1 ms and one iteration.
`useReducedMotion` exists for what CSS cannot reach: canvas loops.

## History

`docs/backlog.md` and `docs/reviews/` hold the decisions behind these rules.
