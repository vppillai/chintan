import { useEffect, useRef } from 'react';
import { useLocation, useNavigate } from 'react-router';

import { ROUTES, legacyRedirect } from './routes.ts';
import { seedHome } from './useBackGuard.ts';

/**
 * Sends a pre-tab-bar URL to its new home.
 *
 * `/notes`, `/archive` and `/search` were screens; they are now the library
 * with a query. The redirect replaces the stale entry rather than pushing, so
 * Back does not land on the alias and bounce straight forward again. When the
 * destination is not the bare library — `/archive` → `/?view=archived` — home
 * is seeded beneath it first (`seedHome`, the same seed `useBackGuard` puts
 * under any other deep link): a cold start must have somewhere for Back to go.
 */
export function Redirect() {
  const location = useLocation();
  const navigate = useNavigate();
  const done = useRef(false);

  useEffect(() => {
    if (done.current) return;
    done.current = true;
    seedHome(navigate, legacyRedirect(location.pathname, location.search) ?? ROUTES.home);
    // First mount only; the alias is gone from the address bar after this.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return null;
}
