/**
 * The signed-in / signed-out state of the app, and the two transitions.
 *
 * `Session` already owned the token set and published changes; nothing was
 * subscribed to it and nothing ever called `set()` from a login or `clear()`
 * from a UI action. This is that missing half.
 */

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';

import { useSession } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import type { Session } from '@/api/session.ts';
import { config } from '@/config/env.ts';

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
  | 'signed-in';

/** The part of the flow React owns. Whether a token exists is the session's. */
type Flow = 'idle' | 'redirecting' | 'exchanging';

export interface AuthGateState {
  phase: AuthPhase;
  /** Set when the hosted UI refused or the exchange failed. */
  error: string | null;
  signIn: () => void;
  /** True when the build has no Cognito configuration to sign in against. */
  configured: boolean;
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
  const authenticated = useAuthenticated();

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
          : 'signed-out';

  return {
    phase,
    error,
    signIn,
    configured: config.cognitoDomain.length > 0 && config.clientId.length > 0,
  };
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
