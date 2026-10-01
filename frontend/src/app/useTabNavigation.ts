import { useEffect, type MouseEvent } from 'react';
import { useLocation, useNavigate, useNavigationType } from 'react-router';

import { ARCHIVED_VIEW, ROUTES } from './routes.ts';
import { RESTORE_SCROLL } from './useScrollRestore.ts';

/*
 * The app's history is a stack with Home at the bottom (R8, F2).
 *
 * Every screen is a real URL, and the tabs used to push like any link, so
 * You → About → Home → You → About and then Back walked every screen in turn:
 * a browser's history, not an app's. The rule now is the one a phone app
 * keeps. Home `/` (any search, but not the archive) is always entry 0. You
 * and the archive, the top-level screens, only ever sit at entry 1: reached
 * from Home they push, reached from anywhere else they take entry 1's place.
 * A note, About and Usage push above them. So Back from anywhere walks down a
 * short, fixed path that ends at Home, and Back from Home leaves the app,
 * which is the platform's own behaviour (owner decision D6).
 */

/**
 * The current entry's place in the app's history, counted by React Router in
 * `history.state.idx`. A private field of the router's, read only here so
 * that a router upgrade that moves it breaks one helper and its test.
 */
export function historyIndex(): number {
  return (window.history.state as { idx?: number } | null)?.idx ?? 0;
}

/** Home: the library, whatever its search or Ask mode, but not the archive. */
export function isHome(pathname: string, search: string): boolean {
  return pathname === ROUTES.home && new URLSearchParams(search).get('view') !== ARCHIVED_VIEW;
}

/**
 * A plain primary click, which the app handles itself. A modified or middle
 * click is the browser's (a new tab or window), so the link's `href` stays
 * real and is left to do it.
 */
export function isPlainClick(event: MouseEvent): boolean {
  return (
    event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey
  );
}

/*
 * The screen a multi-entry Back is on its way to. `navigate(-n)` is a plain
 * `history.go`, which settles in a later `popstate` and returns nothing to
 * wait on, so the second half of the move waits here for the POP to land
 * (`usePendingTab`). Module-level because there is one history.
 */
let pending: string | null = null;

/** Whether the location is already the target, so a landing needs no replace. */
function arrived(target: string, pathname: string, search: string): boolean {
  if (target === ROUTES.home) return isHome(pathname, search);
  const url = new URL(target, window.location.origin);
  const here = new URLSearchParams(search);
  return (
    url.pathname === pathname && [...url.searchParams].every(([key, value]) => here.get(key) === value)
  );
}

function scrollToTop(): void {
  const main = document.getElementById('main');
  if (main) main.scrollTop = 0;
}

/**
 * The moves the tab bar and the back-links make. Each keeps the stack's
 * shape; see the table in `docs/design/home.md` §Back.
 */
export function useTabNavigation() {
  const navigate = useNavigate();
  const { pathname, search } = useLocation();

  /** The Home tab: back down to entry 0, which is a POP, so the list's place comes back with it. */
  const goHome = (): void => {
    const index = historyIndex();
    if (index > 0 && !isHome(pathname, search)) {
      pending = ROUTES.home;
      void navigate(-index);
      return;
    }
    // Already Home (or a cold archive with nothing beneath it): the tab
    // clears a filter and goes to the top, as it always has.
    scrollToTop();
    if (`${pathname}${search}` !== ROUTES.home) void navigate(ROUTES.home, { replace: true });
  };

  /** You or the archive: entry 1, pushed from Home and swapped in from anywhere else. */
  const goTab = (to: string): void => {
    if (arrived(to, pathname, search)) {
      scrollToTop();
      return;
    }
    const index = historyIndex();
    if (index === 0) void navigate(to, { state: RESTORE_SCROLL });
    else if (index === 1) void navigate(to, { replace: true, state: RESTORE_SCROLL });
    else {
      pending = to;
      void navigate(-(index - 1));
    }
  };

  /**
   * A screen's "‹ You": Back when You is beneath it, and otherwise (a cold
   * deep link, which seeds only Home) You in this entry's place, so the stack
   * still ends [Home, You].
   */
  const goBackTo = (to: string): void => {
    if (historyIndex() >= 2) void navigate(-1);
    else void navigate(to, { replace: true, state: RESTORE_SCROLL });
  };

  return { goHome, goTab, goBackTo };
}

/**
 * The second half of a tab move from deep in the stack: once the Back has
 * landed on entry 1 (or 0), the tab replaces it. Mounted once, in the shell.
 * The landed screen may paint for a frame first; accepted, since it only
 * happens when switching tabs from three or more levels deep.
 */
export function usePendingTab(): void {
  const location = useLocation();
  const navigationType = useNavigationType();
  const navigate = useNavigate();

  useEffect(() => {
    const target = pending;
    if (target === null || navigationType !== 'POP') return;
    pending = null;
    if (arrived(target, location.pathname, location.search)) return;
    void navigate(target, { replace: true, state: RESTORE_SCROLL });
    // The landing is one location change; nothing else should re-run this.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.key]);
}
