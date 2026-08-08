import type { RouteObject } from 'react-router';

import { AppShell } from '@/components/AppShell.tsx';
import { CaptureScreen } from '@/features/capture/CaptureScreen.tsx';
import { HomeScreen } from '@/screens/HomeScreen.tsx';
import { NotFoundScreen } from '@/screens/NotFoundScreen.tsx';
import { NoteDetailScreen } from '@/screens/NoteDetailScreen.tsx';
import { NotesScreen } from '@/screens/NotesScreen.tsx';
import { SearchScreen } from '@/screens/SearchScreen.tsx';
import { SettingsScreen } from '@/screens/SettingsScreen.tsx';

import { ROUTES } from './routes.ts';

/** Every state in §5.2 is one of these. Nothing is a hidden DOM toggle. */
export const routes: RouteObject[] = [
  {
    path: '/',
    Component: AppShell,
    children: [
      { index: true, Component: HomeScreen },
      { path: ROUTES.notes.slice(1), Component: NotesScreen },
      { path: ROUTES.notePattern.slice(1), Component: NoteDetailScreen },
      { path: ROUTES.search.slice(1), Component: SearchScreen },
      { path: ROUTES.settings.slice(1), Component: SettingsScreen },
      { path: ROUTES.capture.slice(1), Component: CaptureScreen },
      { path: '*', Component: NotFoundScreen },
    ],
  },
];
