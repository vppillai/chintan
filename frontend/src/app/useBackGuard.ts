import { useEffect, useRef } from 'react';
import { useLocation, useNavigate } from 'react-router';

import { ROUTES } from './routes.ts';

/**
 * Guarantees that Back always has somewhere to go inside the app.
 *
 * The v1 build's worst bug: opening the library and pressing Android Back left
 * the app, because the library was a DOM state with no history entry. v2 makes
 * every state a real URL, which fixes Back *within* a session — but not a cold
 * start on a deep link, where the app's first history entry is also the tab's
 * first history entry, so Back exits.
 *
 * So on first mount at any non-home URL we seed the stack: replace the entry
 * with home, then push the requested route. Back from a deep-linked library
 * now collapses the sheet to the record surface — the one screen that always
 * works — instead of leaving the app.
 *
 * Deliberately does nothing at home. Back from the record surface exiting is
 * correct platform behaviour, and trapping the user there would be hostile.
 */
export function useBackGuard(): void {
  const location = useLocation();
  const navigate = useNavigate();
  const seeded = useRef(false);

  useEffect(() => {
    if (seeded.current) return;
    seeded.current = true;
    if (location.pathname === ROUTES.home) return;

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
