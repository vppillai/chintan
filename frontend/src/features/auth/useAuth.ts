/**
 * The signed-in / signed-out state of the app, and the two transitions.
 *
 * `Session` already owned the token set and published changes; nothing was
 * subscribed to it and nothing ever called `set()` from a login or `clear()`
 * from a UI action. This is that missing half.
 */

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';

import { useApi, useSession } from '@/api/ApiProvider.tsx';
import {
  ApiError,
  BIOMETRIC_NOT_ENROLLED_TYPE,
  BIOMETRIC_RE_ENROLMENT_TYPE,
} from '@/api/problem.ts';
import type { Session } from '@/api/session.ts';
import { tokenSetFromWire } from '@/api/tokens.ts';
import { config } from '@/config/env.ts';
import { canAssertWebAuthn, performAssertion } from '@/features/settings/webauthn.ts';

import { deviceMayBeEnrolled, forgetEnrolment } from './enrolment.ts';

import {
  authorizeUrl,
  exchangeCode,
  readCallbackParams,
  redirectUri,
} from './oauth.ts';
import { createState, createVerifier, challengeFor } from './pkce.ts';
import { rememberPending, takePending } from './pending.ts';

/** True while a token set is held. Re-renders when it is set or cleared. */
export function useAuthenticated(): boolean {
  const session: Session = useSession();
  return useSyncExternalStore(
    useCallback((onChange) => session.subscribe(onChange), [session]),
    () => session.isAuthenticated(),
    () => false,
  );
}

export type AuthPhase =
  /** No token, nothing in flight. The sign-in surface. */
  | 'signed-out'
  /** Sending the browser to the hosted UI. */
  | 'redirecting'
  /** A code came back and is being redeemed. */
  | 'exchanging'
  /** A biometric assertion is in flight. */
  | 'unlocking'
  | 'signed-in';

/** The part of the flow React owns. Whether a token exists is the session's. */
type Flow = 'idle' | 'redirecting' | 'exchanging' | 'unlocking';

export interface AuthGateState {
  phase: AuthPhase;
  /** Set when the hosted UI refused, the exchange failed, or unlock did. */
  error: string | null;
  signIn: () => void;
  /**
   * Signs in with the enrolled authenticator, skipping the hosted UI entirely.
   *
   * Null when this browser cannot assert a credential, which is what stops the
   * signed-out screen offering an unlock it cannot perform.
   */
  unlock: (() => void) | null;
  /**
   * Set when the server accepted the assertion but could not open the vault.
   *
   * A distinct state, not an error: nothing is wrong with the user, their
   * finger or their device, and the only thing that fixes it is enrolling
   * again. Showing "biometric verification failed" here would send them to try
   * the same finger a second time, forever.
   */
  needsReEnrolment: boolean;
  /** True when the build has no Cognito configuration to sign in against. */
  configured: boolean;
}

/**
 * How the server says "the assertion was fine, the vault was not".
 *
 * The problem `type` is the authority now that the backend emits one. The prose
 * match stays as a fallback so a client running against an instance that has
 * not been redeployed still behaves; it can go once nothing old is serving.
 *
 * Either way it fails *safe*: an unrecognised 401 is reported as a failed
 * verification, which is the more conservative of the two answers.
 */
const RE_ENROL_DETAIL = /set up again/i;

function isReEnrolmentRequired(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  if (error.problemType === BIOMETRIC_RE_ENROLMENT_TYPE) return true;
  if (error.status !== 401) return false;
  return RE_ENROL_DETAIL.test(`${error.detail ?? ''} ${error.title}`);
}

/** A flow failure with a sentence already fit to show someone. */
class AuthFlowError extends Error {}

/**
 * Drives the whole flow from one place.
 *
 * The callback is consumed wherever the app happens to be mounted rather than
 * at a dedicated route, because Cognito redirects to the registered callback
 * URL and the deployed client registers only the app's base URL. See
 * `oauth.ts`.
 */
export function useAuthGate(): AuthGateState {
  const session = useSession();
  const api = useApi();
  const authenticated = useAuthenticated();
  const [needsReEnrolment, setNeedsReEnrolment] = useState(false);
  /*
   * Whether to offer an unlock at all.
   *
   * Two conditions, and the second is the one that was missing. The browser
   * must be able to assert a credential, and this device must have seen one
   * enrolled — because signing out revokes the credential on purpose, so
   * offering the button unconditionally meant every sign-out was followed by
   * sign in, tap unlock, `login/options → 503`, every time.
   *
   * State rather than a constant: the hint is only a hint, and the first
   * authoritative "there is nothing enrolled" withdraws the offer for good.
   */
  const [mayUnlock, setMayUnlock] = useState(
    () => canAssertWebAuthn() && deviceMayBeEnrolled(),
  );

  // Read once, synchronously, before the first paint: landing with `?code=`
  // must never flash the signed-out screen on the way in.
  const [callback] = useState(() =>
    typeof window === 'undefined' ? null : readCallbackParams(window.location.search),
  );

  const [flow, setFlow] = useState<Flow>(() =>
    callback?.kind === 'code' ? 'exchanging' : 'idle',
  );
  const [error, setError] = useState<string | null>(() =>
    callback?.kind === 'error' ? describeAuthError(callback.error, callback.description) : null,
  );

  /*
   * An authorization code is single-use at Cognito, and StrictMode runs every
   * effect twice in development. Redeeming twice burns the code and presents as
   * "sign-in works in production and not locally", so the attempt is latched.
   */
  const redeemed = useRef(false);

  useEffect(() => {
    if (callback?.kind !== 'code') return;
    if (redeemed.current) return;
    redeemed.current = true;

    const run = async (): Promise<void> => {
      const pending = takePending();
      // Off the address bar before anything can fail: a spent code must not
      // survive into a reload or a shared link.
      cleanCallbackFromUrl();

      if (!pending) {
        throw new AuthFlowError('That sign-in could not be completed. Please try again.');
      }
      // The state check is the CSRF boundary: a code delivered with someone
      // else's state did not come from a flow this tab started.
      if (pending.state !== callback.state) {
        throw new AuthFlowError('That sign-in did not match this device. Please try again.');
      }

      const tokens = await exchangeCode({
        code: callback.code,
        verifier: pending.verifier,
        redirectUri: redirectUri(),
      });
      session.set(tokens);
    };

    // `.then`/`.catch` rather than an inline `await`: these run in a microtask,
    // so no state is set synchronously inside the effect body.
    run()
      .then(() => {
        setError(null);
        setFlow('idle');
      })
      .catch((cause: unknown) => {
        setError(describeFailure(cause));
        setFlow('idle');
      });
  }, [callback, session]);

  useEffect(() => {
    if (callback?.kind === 'error') cleanCallbackFromUrl();
  }, [callback]);

  const signIn = useCallback(() => {
    setError(null);
    setFlow('redirecting');

    void (async () => {
      try {
        const state = createState();
        const verifier = createVerifier();
        const challenge = await challengeFor(verifier);

        rememberPending({
          state,
          verifier,
          returnTo: `${window.location.pathname}${window.location.search}`,
          startedAt: Date.now(),
        });

        window.location.assign(
          authorizeUrl({ state, challenge, redirectUri: redirectUri() }),
        );
      } catch {
        setError('This browser could not start a secure sign-in.');
        setFlow('idle');
      }
    })();
  }, []);

  /**
   * Unlock: `POST login/options` → `navigator.credentials.get` → `POST login`.
   *
   * This is the path that did not exist. `BiometricSetting` performed a real
   * registration and the backend sealed a real refresh token, and there was no
   * `webauthn/login` wrapper, no assertion helper and no control anywhere in the
   * client — so an enrolled credential could never be used for anything, and
   * `useAuth` always redirected to Cognito regardless.
   */
  const unlock = useCallback(() => {
    setError(null);
    setNeedsReEnrolment(false);
    setFlow('unlocking');

    void (async () => {
      try {
        const options = await api.webauthnLoginOptions();
        const credential = await performAssertion(options.options);
        const tokens = await api.webauthnLogin({
          challenge_id: options.challenge_id,
          credential,
        });
        // Straight into the session: the vault held a Cognito refresh token and
        // the server has already exchanged it, so this is the same token set
        // the hosted UI would have produced.
        session.set(tokenSetFromWire(tokens));
        setFlow('idle');
      } catch (cause) {
        setFlow('idle');
        if (isReEnrolmentRequired(cause)) {
          setNeedsReEnrolment(true);
          return;
        }
        /*
         * The server is the authority and it says there is nothing to unlock.
         * The hint this device was going on is wrong — the account was changed
         * elsewhere, or a revoke was missed — so it is erased and the offer
         * withdrawn rather than left to fail again on the next tap.
         */
        if (isNotEnrolled(cause)) {
          forgetEnrolment();
          setMayUnlock(false);
        }
        setError(describeUnlockFailure(cause));
      }
    })();
  }, [api, session]);

  /*
   * Derived, never mirrored. Whether a token exists is the session's answer and
   * is read through `useSyncExternalStore`; `flow` only says what this
   * component has in flight. Copying the session into state with an effect is
   * what produces a UI that disagrees with the thing it is describing.
   */
  const phase: AuthPhase =
    flow === 'exchanging'
      ? 'exchanging'
      : authenticated
        ? 'signed-in'
        : flow === 'redirecting'
          ? 'redirecting'
          : flow === 'unlocking'
            ? 'unlocking'
            : 'signed-out';

  return {
    phase,
    error,
    signIn,
    unlock: mayUnlock ? unlock : null,
    needsReEnrolment,
    configured: config.cognitoDomain.length > 0 && config.clientId.length > 0,
  };
}

/**
 * An unlock failure, said in words.
 *
 * The DOM throws `NotAllowedError` both when the user dismissed the prompt and
 * when the authenticator timed out, and they are the same thing to the person
 * looking at the screen: nothing happened, try again or use the other button.
 */
/**
 * The server saying this account has no enrolled credential.
 *
 * Now a 404 carrying `#biometric-not-enrolled`: an account fact, not a server
 * fault, and deliberately not the 503 it used to be — 503 means the instance
 * cannot do biometrics at all and reads as transient, so a client retried an
 * answer that could not change until somebody enrolled.
 *
 * The old 503 and the prose are still accepted, so a client that has updated
 * ahead of its backend keeps working. Both fallbacks can go once nothing old is
 * serving.
 */
function isNotEnrolled(cause: unknown): boolean {
  if (!(cause instanceof ApiError)) return false;
  if (cause.problemType === BIOMETRIC_NOT_ENROLLED_TYPE) return true;
  if (cause.status === 503) return true;
  return /not set up on this account/i.test(`${cause.detail ?? ''} ${cause.title}`);
}

function describeUnlockFailure(cause: unknown): string {
  const name = (cause as { name?: string } | null)?.name;
  if (name === 'NotAllowedError' || name === 'AbortError') {
    return 'That unlock was cancelled.';
  }
  if (isNotEnrolled(cause)) {
    return 'Biometric unlock is not set up on this account. Sign in, then turn it on in Settings.';
  }
  if (cause instanceof ApiError) return cause.userMessage;
  return 'That did not unlock. You can sign in instead.';
}

function describeFailure(cause: unknown): string {
  if (cause instanceof AuthFlowError) return cause.message;
  if (cause instanceof ApiError) return cause.userMessage;
  return 'That sign-in could not be completed. Please try again.';
}

/**
 * Takes `code`/`state`/`error` off the address bar without a navigation.
 *
 * Leaving them there means a reload replays a spent code and shows a failure,
 * and it puts the code in the history entry the user can share.
 */
function cleanCallbackFromUrl(): void {
  if (typeof window === 'undefined') return;
  const url = new URL(window.location.href);
  let touched = false;
  for (const key of ['code', 'state', 'error', 'error_description']) {
    if (url.searchParams.has(key)) {
      url.searchParams.delete(key);
      touched = true;
    }
  }
  if (!touched) return;
  window.history.replaceState(
    window.history.state,
    '',
    `${url.pathname}${url.search}${url.hash}`,
  );
}

/** Cognito's error codes, said in words. */
export function describeAuthError(error: string, description: string | null): string {
  if (error === 'access_denied') return 'That sign-in was cancelled.';
  if (description && description.length > 0) return description;
  return 'That sign-in could not be completed. Please try again.';
}
