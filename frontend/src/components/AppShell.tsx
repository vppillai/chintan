import { useQueryClient } from '@tanstack/react-query';
import { Suspense, useEffect, useRef } from 'react';
import { Outlet, useLocation } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { queryKeys } from '@/api/queries.ts';
import { ROUTES } from '@/app/routes.ts';
import { useBackGuard } from '@/app/useBackGuard.ts';
import { useRouteFocus } from '@/app/useRouteFocus.ts';
import { useScrollRestore } from '@/app/useScrollRestore.ts';
import { usePendingTab } from '@/app/useTabNavigation.ts';
import { SignedOutScreen } from '@/features/auth/SignedOutScreen.tsx';
import { useAuthGate } from '@/features/auth/useAuth.ts';
import { usePasskeyReturn } from '@/features/auth/usePasskeyReturn.ts';
import { useResendOnReconnect } from '@/features/capture/useResendOnReconnect.ts';
import { useKeyboardInset } from '@/hooks/useKeyboardInset.ts';
import { OfflineBanner } from '@/offline/OfflineBanner.tsx';
import { UpdatePrompt } from '@/pwa/UpdatePrompt.tsx';
import { usePushWakeup } from '@/pwa/usePushWakeup.ts';

import { BannerRecord } from './BannerRecord.tsx';
import { RecordingIndicator } from './RecordingIndicator.tsx';
import { StatusRegion } from './StatusRegion.tsx';
import { TabBar } from './TabBar.tsx';
import { Toast } from './Toast.tsx';
import { Wordmark } from './Wordmark.tsx';

/** Which of the app's surfaces a URL is. Drives layout and announcements. */
export type Screen = 'library' | 'note' | 'you' | 'usage' | 'about' | 'capture' | 'other';

export function screenForPath(pathname: string): Screen {
  const path = pathname.length > 1 ? pathname.replace(/\/$/, '') : pathname;
  if (path === ROUTES.home) return 'library';
  if (path === ROUTES.capture) return 'capture';
  if (path === ROUTES.settings) return 'you';
  if (path === ROUTES.usage) return 'usage';
  if (path === ROUTES.about) return 'about';
  if (path.startsWith('/notes/')) return 'note';
  return 'other';
}

const SCREEN_TITLES: Record<Screen, string> = {
  library: 'Notes',
  note: 'Note',
  you: 'You',
  usage: 'Usage',
  about: 'About',
  capture: 'Recording',
  other: 'Screen',
};

/**
 * The app shell.
 *
 * There is exactly one <main>, one banner and one navigation. The tab bar is a
 * grid row in normal flow beneath <main>, so content can never be overlaid by
 * it; on the capture screen the bar is not rendered at all, because a live
 * microphone is the one state where an accidental tap on Record or Notes is
 * worse than having to press Stop first.
 *
 * `data-screen` on the wrapper is the single switch the layout rules read, so
 * the shell's appearance can never disagree with the URL.
 */
export function AppShell() {
  const location = useLocation();
  const mainRef = useRef<HTMLElement>(null);
  const auth = useAuthGate();
  const api = useApi();
  const queryClient = useQueryClient();

  useBackGuard();
  // Where the on-screen keyboard is, for the scroll container's padding
  // (`shell.css` `.app__main`) and the note sheet's cap (`notes.css`), and
  // whether it is up at all, for the banner's mic (`BannerRecord`).
  useKeyboardInset();
  usePasskeyReturn();
  useRouteFocus(mainRef);
  // Back returns to the place in the list (or the note) the user left.
  useScrollRestore(mainRef);
  // A tab chosen from deep in the stack lands after its Back (R8, F2).
  usePendingTab();
  // A recording sent while offline goes out when the connection returns,
  // whichever screen the user is on by then.
  useResendOnReconnect();
  // The service worker's word that a recording filed while a window was
  // open, so Home shows the receipt rather than the OS a notification.
  usePushWakeup();

  const screen = screenForPath(location.pathname);

  /*
   * The settings, read once for the session (round-3 T48). The note screen
   * fetched them after the note — a second waterfall step for one language
   * label — and You and the capture chooser read them too. Never stale, here
   * and in `useSettings` (the observer decides its own staleness, so the
   * prefetch's setting alone was not enough): `useSaveSettings` writes what
   * the server stored into this same cache, so the device's own changes are
   * always reflected and a refetch would only repeat them. Not on the capture
   * screen, where the Record shortcut's one request must stay the recording's
   * own.
   */
  useEffect(() => {
    if (auth.phase !== 'signed-in' || screen === 'capture') return;
    void queryClient.prefetchQuery({
      queryKey: queryKeys.settings(),
      queryFn: () => api.getSettings(),
      staleTime: Infinity,
    });
  }, [auth.phase, screen, api, queryClient]);

  /*
   * The gate is here, above the outlet, rather than per screen.
   *
   * Every screen's first act is an authenticated query, and the library itself
   * runs two more — the filing row polls `GET /v1/captures` and the resume
   * prompt offers a Send. Rendering any of that without a token is what
   * produced a shell of 401s with nothing on screen explaining itself. One
   * decision point means none of it can mount.
   */
  if (auth.phase !== 'signed-in') {
    return (
      <div className="app" data-screen="signed-out">
        <header className="app__banner">
          <Wordmark />
        </header>
        <main id="main" ref={mainRef} tabIndex={-1} className="app__main" aria-label="Sign in">
          <SignedOutScreen {...auth} />
        </main>
      </div>
    );
  }

  return (
    <>
      <a className="skip-link" href="#main">
        Skip to content
      </a>

      <div className="app" data-screen={screen}>
        {/*
          On the library the wordmark moves into the screen's own heading row
          (round-3 T17: the banner, the h1 and the tab named the same place
          three times in the top 45 percent of a phone), and the banner keeps
          only the offline pill. It stays in the DOM because it is the shell
          grid's first row; `home.css` collapses it while it is empty.
        */}
        <header className="app__banner">
          {screen !== 'library' && <Wordmark />}
          <OfflineBanner />
          {/* Record while typing, shown by CSS only with the keyboard up (R8, F3). */}
          {screen === 'note' && <BannerRecord />}
        </header>

        <main
          id="main"
          ref={mainRef}
          tabIndex={-1}
          className="app__main"
          aria-label={SCREEN_TITLES[screen]}
        >
          {/* The lazy screens (`router.tsx`) resolve here; nothing else suspends. */}
          <Suspense fallback={null}>
            <Outlet />
          </Suspense>
        </main>

        {/*
          The microphone being open is the one fact that outranks everything
          else on the screen, so it is stated above the bar on every screen but
          the capture screen, which is its own indicator.
        */}
        {screen !== 'capture' && <RecordingIndicator />}

        {/*
          Above the bar, not over it. The update prompt and the Undo toast are
          shell rows, so they can never cover the record button — which is
          what the prompt did while it was a fixed-position toast.
        */}
        <Toast />
        <UpdatePrompt />

        {screen !== 'capture' && <TabBar />}

        {/*
          A hold locked on the tab bar arrives here mid-take: the news is that
          it carries on without the finger, not which screen this is.
        */}
        <StatusRegion
          message={
            screen === 'capture' &&
            (location.state as { handsFree?: boolean } | null)?.handsFree === true
              ? 'Recording, hands-free'
              : `${SCREEN_TITLES[screen]} screen`
          }
        />
      </div>
    </>
  );
}
