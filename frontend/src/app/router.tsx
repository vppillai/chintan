import { lazy } from 'react';
import type { RouteObject } from 'react-router';

import { AppShell } from '@/components/AppShell.tsx';
import { NoteDetailScreen } from '@/features/notes/NoteDetailScreen.tsx';
import { NotFoundScreen } from '@/screens/NotFoundScreen.tsx';
import { NotesScreen } from '@/screens/NotesScreen.tsx';

import { Redirect } from './Redirect.tsx';
import { RouteError } from './RouteError.tsx';
import { LEGACY_ROUTES, ROUTES } from './routes.ts';

/*
 * Loaded when first visited, not with Home (round-3 T47): the bundle was one
 * 548 KB chunk, and someone opening the app to record paid for the usage
 * chart and the About page on the way. Each is its own chunk; the shell's
 * <Suspense> shows nothing in the outlet for the moment it takes, and the
 * service worker precaches every chunk, so it is a first-visit cost only.
 * The library and the note screen stay in the main chunk: they are where
 * every launch lands.
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
const CaptureScreen = lazy(() =>
  import('@/features/capture/CaptureScreen.tsx').then((m) => ({ default: m.CaptureScreen })),
);
const TalkScreen = lazy(() =>
  import('@/features/talk/TalkScreen.tsx').then((m) => ({ default: m.TalkScreen })),
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
      { path: ROUTES.talk.slice(1), Component: TalkScreen, ErrorBoundary: RouteError },
      ...Object.keys(LEGACY_ROUTES).map((path) => ({
        path: path.slice(1),
        Component: Redirect,
        ErrorBoundary: RouteError,
      })),
      { path: '*', Component: NotFoundScreen, ErrorBoundary: RouteError },
    ],
  },
];
