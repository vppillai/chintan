/**
 * Passkeys, as far as this app can take them.
 *
 * The app cannot register a passkey itself, and this was verified rather than
 * assumed (2026-09-04, against the prod pool with the test user): Cognito's
 * `StartWebAuthnRegistration` answers with `rp.id` set to the pool's managed
 * login domain (`chintan-dev-prod-….auth.us-west-2.amazoncognito.com`), and a
 * page on `vppillai.github.io` calling `navigator.credentials.create` with that
 * relying party is refused by the browser before any authenticator is asked:
 *
 *   SecurityError: The relying party ID is not a registrable domain suffix of,
 *   nor equal to the current domain.
 *
 * Cognito requires the relying party to be the managed login's domain while
 * that page is the sign-in surface, so the RP cannot be moved to the app's
 * origin either. A second, smaller wall: the access token the hosted UI issues
 * carries only `openid email profile`, and the WebAuthn operations demand
 * `aws.cognito.signin.user.admin` ("Access Token does not have required
 * scopes"), so even listing credentials from here is refused.
 *
 * What does exist is a page on the managed login itself:
 *
 *   GET <cognitoDomain>/passkeys/add?client_id=…&redirect_uri=…
 *
 * With a live managed-login session it renders "Set up sign-in with a passkey
 * — Add passkey / Not now", runs the ceremony on its own origin (where the RP
 * matches), and returns to `redirect_uri?result=success`. Without a session it
 * bounces straight back with `?result=invalid_session`. So the feature is a
 * hand-off: the app sends the user there, and reads the answer on the way back.
 * `redirect_uri` must be one the client registers, which is the app's base
 * URL — the same value the sign-in flow uses, for the same reason.
 */

import { config, type AppConfig } from '@/config/env.ts';

import { redirectUri } from './oauth.ts';

/** The query parameter the managed login answers on. */
export const PASSKEY_RESULT_PARAM = 'result';

/** The parameter the You screen reads the answer from, once the shell has moved it there. */
export const PASSKEY_STATUS_PARAM = 'passkey';

export type PasskeyResult = 'success' | 'invalid_session' | 'other';

export function passkeyAddUrl(
  redirect: string = redirectUri(),
  settings: AppConfig = config,
): string {
  const url = new URL(`${settings.cognitoDomain}/passkeys/add`);
  url.search = new URLSearchParams({
    client_id: settings.clientId,
    redirect_uri: redirect,
  }).toString();
  return url.href;
}

/**
 * What the managed login said, or null when the URL carries no answer.
 *
 * Anything that is neither `success` nor `invalid_session` is reported as
 * `other` rather than dropped: a value this code does not recognise is still a
 * return from that page, and "nothing happened" is the wrong thing to show.
 */
export function readPasskeyResult(search: string): PasskeyResult | null {
  const raw = new URLSearchParams(search).get(PASSKEY_RESULT_PARAM);
  if (raw === null) return null;
  if (raw === 'success' || raw === 'invalid_session') return raw;
  return 'other';
}

/**
 * Whether this browser can take part in a passkey ceremony at all.
 *
 * The ceremony happens on the Cognito page, but in this same browser, so a
 * browser with no WebAuthn is one where the hand-off would end in an error the
 * user cannot act on. Better to say so before sending them.
 */
export function passkeysSupported(win: Window | undefined = globalThis.window): boolean {
  if (typeof win === 'undefined') return false;
  // Declared as a global variable by lib.dom, not as a Window member.
  const ctor = (win as unknown as Record<string, unknown>)['PublicKeyCredential'];
  return typeof ctor === 'function';
}

/* ---------------------------------------------------------------------------
   The nudge: shown on the library until dismissed or a passkey is added.

   Remembered per device in localStorage — the passkey itself is per device,
   so a dismissal on the phone should not silence the laptop, and vice versa.
   The app cannot ask Cognito whether a passkey exists (see above), so the
   record of "added" is this device's own memory of a `success` return.
   --------------------------------------------------------------------------- */

export const PASSKEY_NUDGE_KEY = 'chintan.passkey.nudge.v1';

function storage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage;
  } catch {
    return null;
  }
}

export function nudgeDismissed(store: Storage | null = storage()): boolean {
  try {
    return store?.getItem(PASSKEY_NUDGE_KEY) !== null;
  } catch {
    // Storage denied reads as dismissed: a nudge that reappears on every
    // visit because it cannot be remembered is worse than none.
    return true;
  }
}

export function dismissNudge(
  reason: 'not-now' | 'added',
  store: Storage | null = storage(),
): void {
  try {
    store?.setItem(PASSKEY_NUDGE_KEY, reason);
  } catch {
    /* Nothing to do; the in-memory state hides it for this session. */
  }
}
