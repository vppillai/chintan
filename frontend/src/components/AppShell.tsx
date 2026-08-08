import { useRef } from 'react';
import { Outlet, useLocation } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { sheetForPath } from '@/app/sheet.ts';
import { ProgressCard } from '@/features/capture/ProgressCard.tsx';
import { OfflineBanner } from '@/offline/OfflineBanner.tsx';
import { useBackGuard } from '@/app/useBackGuard.ts';
import { useRouteFocus } from '@/app/useRouteFocus.ts';

import { BottomBar } from './BottomBar.tsx';
import { LibraryStrip } from './LibraryStrip.tsx';
import { StatusRegion } from './StatusRegion.tsx';

const SCREEN_TITLES: Record<string, string> = {
  [ROUTES.home]: 'Record',
  [ROUTES.notes]: 'Notes',
  [ROUTES.search]: 'Search',
  [ROUTES.settings]: 'You',
  [ROUTES.capture]: 'Recording',
};

function announcementFor(pathname: string): string {
  if (SCREEN_TITLES[pathname]) return `${SCREEN_TITLES[pathname]} screen`;
  if (pathname.startsWith(`${ROUTES.notes}/`)) return 'Note screen';
  return 'Screen changed';
}

/**
 * The app shell.
 *
 * There is exactly one <main>. The sheet is not a second document region — it
 * is <main> itself, restyled: at `collapsed` it is the record surface, at
 * `expanded` it is the library pulled up over it, at `locked` it is the
 * capture screen. That keeps the landmark structure honest (one banner, one
 * main, one navigation) and lets one CSS transition do the pull-up.
 *
 * `data-sheet-state` on the wrapper is the single switch every layout rule
 * reads, so the sheet's appearance can never disagree with the URL.
 */
export function AppShell() {
  const location = useLocation();
  const mainRef = useRef<HTMLElement>(null);

  useBackGuard();
  useRouteFocus(mainRef);

  const sheet = sheetForPath(location.pathname);

  return (
    <>
      <a className="skip-link" href="#main">
        Skip to content
      </a>

      <div className="app" data-sheet-state={sheet.state} data-sheet-tab={sheet.tab}>
        <header className="app__banner">
          <span className="app__wordmark">Chintan</span>
          <OfflineBanner />
        </header>

        <main
          id="main"
          ref={mainRef}
          tabIndex={-1}
          className="app__main"
          aria-label={SCREEN_TITLES[location.pathname] ?? 'Note'}
        >
          {sheet.state === 'expanded' && (
            <span className="app__sheet-grip" aria-hidden="true" />
          )}
          <Outlet />
        </main>

        {/*
          The progress card lives in the shell, not in a screen, because a
          capture must remain visible after the user navigates away from the
          screen that started it.
        */}
        {sheet.state !== 'locked' && <ProgressCard />}

        {sheet.state === 'collapsed' && <LibraryStrip />}
        {sheet.state === 'expanded' && <BottomBar />}

        <StatusRegion message={announcementFor(location.pathname)} />
      </div>
    </>
  );
}
