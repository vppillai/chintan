import { useEffect, useRef } from 'react';
import { useLocation, useNavigate } from 'react-router';

import { ROUTES, legacyRedirect } from './routes.ts';

/**
 * Guarantees that Back always has somewhere to go inside the app.
 *
 * Android Back from any screen below the library must stay in the app. Every
 * screen is a real URL with its own history entry, which covers Back *within*
 * a session — but not a cold start on a deep link, where the app's first
 * history entry is also the tab's first history entry, so Back exits.
 *
 * So on first mount at any URL other than the library we seed the stack:
 * replace the entry with home, then push the requested route. Back from a
 * deep-linked note now returns to the library instead of leaving the app.
 *
 * Deliberately does nothing at home. Back from the library exiting is correct
 * platform behaviour, and trapping the user there would be hostile. Legacy
 * paths are left alone too: `Redirect` seeds the stack itself, because it knows
 * where the alias is going.
 */
export function useBackGuard(): void {
  const location = useLocation();
  const navigate = useNavigate();
  const seeded = useRef(false);

  useEffect(() => {
    if (seeded.current) return;
    seeded.current = true;
    if (location.pathname === ROUTES.home) return;
    if (legacyRedirect(location.pathname, location.search)) return;

    const target = `${location.pathname}${location.search}${location.hash}`;
    void (async () => {
      await navigate(ROUTES.home, { replace: true });
      await navigate(target);
    })();
    // Intentionally first-mount only: this seeds the history stack once, and
    // re-running it on every navigation would rewrite history under the user.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
}
