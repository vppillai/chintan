import type { RouteObject } from 'react-router';

import { AppShell } from '@/components/AppShell.tsx';
import { CaptureScreen } from '@/features/capture/CaptureScreen.tsx';
import { NoteDetailScreen } from '@/features/notes/NoteDetailScreen.tsx';
import { SettingsScreen } from '@/features/settings/SettingsScreen.tsx';
import { AboutScreen } from '@/screens/AboutScreen.tsx';
import { NotFoundScreen } from '@/screens/NotFoundScreen.tsx';
import { NotesScreen } from '@/screens/NotesScreen.tsx';

import { Redirect } from './Redirect.tsx';
import { RouteError } from './RouteError.tsx';
import { LEGACY_ROUTES, ROUTES } from './routes.ts';

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
