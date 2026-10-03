import { lazy } from 'react';
import type { RouteObject } from 'react-router';

import { AppShell } from '@/components/AppShell.tsx';
import { CaptureScreen } from '@/features/capture/CaptureScreen.tsx';
import { NoteDetailScreen } from '@/features/notes/NoteDetailScreen.tsx';
import { NotFoundScreen } from '@/screens/NotFoundScreen.tsx';
import { NotesScreen } from '@/screens/NotesScreen.tsx';

import { Redirect } from './Redirect.tsx';
import { RouteError } from './RouteError.tsx';
import { LEGACY_ROUTES, ROUTES } from './routes.ts';

/*
 * Loaded when first visited, not with Home: someone opening the app to
 * record must not pay for the usage chart and the About page on the way.
 * Each is its own chunk; the shell's <Suspense> shows nothing in the outlet
 * for the moment it takes, and the service worker precaches every chunk, so
 * it is a first-visit cost only. The library, the note screen and the
 * capture screen stay in the main chunk: they are where a launch lands. The
 * capture screen was a lazy chunk once, and the manifest's "Record a
 * thought" shortcut paid one extra round trip for it on every cold launch —
 * measured at 650 ms of the 3.1 s to a live microphone on Fast 3G
 * (`e2e/launch-latency.spec.ts`); the note screen's drawer and the zip
 * writer left the main chunk to make room for it.
 */
const SettingsScreen = lazy(() =>
  import('@/features/settings/SettingsScreen.tsx').then((m) => ({ default: m.SettingsScreen })),
);
const UsageScreen = lazy(() =>
  import('@/screens/UsageScreen.tsx').then((m) => ({ default: m.UsageScreen })),
);
const AboutScreen = lazy(() =>
  import('@/screens/AboutScreen.tsx').then((m) => ({ default: m.AboutScreen })),
);

/**
 * Three tabs plus the note and capture screens. Nothing is a hidden DOM toggle.
 *
 * `ErrorBoundary` is on every entry, and that is not belt-and-braces. Without
 * one, React Router replaces the whole document with its raw error page on any
 * render fault — no banner, no navigation, no buttons — and the only escape on
 * a phone is a reload the page never mentions. On the children it renders in
 * the outlet, so the shell and its navigation survive a broken screen; on the
 * root it is the backstop for the shell itself.
 */
export const routes: RouteObject[] = [
  {
    path: '/',
    Component: AppShell,
    ErrorBoundary: RouteError,
    children: [
      { index: true, Component: NotesScreen, ErrorBoundary: RouteError },
      {
        path: ROUTES.notePattern.slice(1),
        Component: NoteDetailScreen,
        ErrorBoundary: RouteError,
      },
      {
        path: ROUTES.settings.slice(1),
        Component: SettingsScreen,
        ErrorBoundary: RouteError,
      },
      { path: ROUTES.usage.slice(1), Component: UsageScreen, ErrorBoundary: RouteError },
      { path: ROUTES.about.slice(1), Component: AboutScreen, ErrorBoundary: RouteError },
      { path: ROUTES.capture.slice(1), Component: CaptureScreen, ErrorBoundary: RouteError },
      ...Object.keys(LEGACY_ROUTES).map((path) => ({
        path: path.slice(1),
        Component: Redirect,
        ErrorBoundary: RouteError,
      })),
      { path: '*', Component: NotFoundScreen, ErrorBoundary: RouteError },
    ],
  },
];
