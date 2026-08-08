import type { RouteObject } from 'react-router';

import { AppShell } from '@/components/AppShell.tsx';
import { CaptureScreen } from '@/features/capture/CaptureScreen.tsx';
import { HomeScreen } from '@/screens/HomeScreen.tsx';
import { NoteDetailScreen } from '@/features/notes/NoteDetailScreen.tsx';
import { SearchScreen } from '@/features/search/SearchScreen.tsx';
import { SettingsScreen } from '@/features/settings/SettingsScreen.tsx';
import { NotFoundScreen } from '@/screens/NotFoundScreen.tsx';
import { NotesScreen } from '@/screens/NotesScreen.tsx';

import { RouteError } from './RouteError.tsx';
import { ROUTES } from './routes.ts';

/**
 * Every state in §5.2 is one of these. Nothing is a hidden DOM toggle.
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
      { index: true, Component: HomeScreen, ErrorBoundary: RouteError },
      { path: ROUTES.notes.slice(1), Component: NotesScreen, ErrorBoundary: RouteError },
      {
        path: ROUTES.notePattern.slice(1),
        Component: NoteDetailScreen,
        ErrorBoundary: RouteError,
      },
      { path: ROUTES.search.slice(1), Component: SearchScreen, ErrorBoundary: RouteError },
      {
        path: ROUTES.settings.slice(1),
        Component: SettingsScreen,
        ErrorBoundary: RouteError,
      },
      { path: ROUTES.capture.slice(1), Component: CaptureScreen, ErrorBoundary: RouteError },
      { path: '*', Component: NotFoundScreen, ErrorBoundary: RouteError },
    ],
  },
];
