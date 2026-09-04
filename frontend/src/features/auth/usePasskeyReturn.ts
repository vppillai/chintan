import { useEffect, useRef } from 'react';
import { useLocation, useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';

import {
  PASSKEY_RESULT_PARAM,
  PASSKEY_STATUS_PARAM,
  dismissNudge,
  readPasskeyResult,
} from './passkeys.ts';

/**
 * Catches the managed login's answer on the way back in.
 *
 * `/passkeys/add` can only return to a registered callback URL, which is the
 * app's base URL — the library — and it reports the outcome as `?result=…`
 * there. The person left from You, so that is where they are taken: the
 * library is replaced with a clean home entry and You is pushed on top with the
 * outcome as `?passkey=…`, the same two-step `useBackGuard` uses so that Back
 * still lands on the library rather than leaving the app. A `success` also
 * settles the library's nudge for this device.
 *
 * First mount only, like the back guard: the parameter arrives exactly once,
 * with the document.
 */
export function usePasskeyReturn(): void {
  const location = useLocation();
  const navigate = useNavigate();
  const handled = useRef(false);

  useEffect(() => {
    if (handled.current) return;
    handled.current = true;

    const result = readPasskeyResult(location.search);
    if (!result) return;
    if (result === 'success') dismissNudge('added');

    const rest = new URLSearchParams(location.search);
    rest.delete(PASSKEY_RESULT_PARAM);
    const home = rest.toString() ? `${ROUTES.home}?${rest.toString()}` : ROUTES.home;

    void (async () => {
      await navigate(home, { replace: true });
      await navigate(`${ROUTES.settings}?${PASSKEY_STATUS_PARAM}=${result}`);
    })();
    // Intentionally first-mount only: the parameter arrives once, with the
    // document, and re-running on every navigation would loop through You.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
}
