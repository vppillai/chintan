/**
 * The Cognito hosted-UI authorization-code flow.
 *
 * URL construction and the token exchange, kept free of React and of the DOM
 * beyond one `window.location.origin` read, so the whole flow is testable
 * without a browser redirect.
 *
 * **The redirect URI is the app's base URL, not a `/auth/callback` path.**
 * That is not a shortcut. Cognito matches `redirect_uri` exactly against the
 * client's registered `CallbackURLs`, and the deployed client registers exactly
 * one — `https://<host>/<repo>/<instance>/` (`infrastructure/template.yaml`).
 * Any other value fails the authorize request with `redirect_mismatch` before
 * the user ever sees a login form. So the code comes back on the base URL's
 * query string and is consumed there.
 */

import { config, type AppConfig } from '@/config/env.ts';
import { ApiError, networkError } from '@/api/problem.ts';
import type { TokenSetWire } from '@/api/schema.ts';
import { tokenSetFromWire, type TokenSet } from '@/api/tokens.ts';

import { CODE_CHALLENGE_METHOD } from './pkce.ts';

/** What the hosted UI is asked for. `openid` is what mints the id_token. */
const SCOPES = ['openid', 'email', 'profile'] as const;

/**
 * Where Cognito sends the browser back to.
 *
 * `BASE_URL` always carries a trailing slash (Vite normalises it, and
 * `vite.config.ts` re-normalises `VITE_BASE` on top), so this renders the
 * registered callback byte for byte.
 */
export function redirectUri(origin: string = window.location.origin): string {
  return new URL(import.meta.env.BASE_URL, origin).href;
}

export interface AuthorizeInput {
  state: string;
  challenge: string;
  redirectUri: string;
  settings?: AppConfig;
}

export function authorizeUrl({
  state,
  challenge,
  redirectUri: redirect,
  settings = config,
}: AuthorizeInput): string {
  const url = new URL(`${settings.cognitoDomain}/oauth2/authorize`);
  url.search = new URLSearchParams({
    response_type: 'code',
    client_id: settings.clientId,
    redirect_uri: redirect,
    scope: SCOPES.join(' '),
    state,
    code_challenge: challenge,
    code_challenge_method: CODE_CHALLENGE_METHOD,
  }).toString();
  return url.href;
}

/**
 * Ends the session at Cognito, not just on this device.
 *
 * Without it the hosted UI still holds its own session cookie, so the next
 * "Sign in" tap redirects straight back through with no prompt — which looks
 * exactly like the sign-out having done nothing.
 */
export function logoutUrl(
  redirect: string = redirectUri(),
  settings: AppConfig = config,
): string {
  const url = new URL(`${settings.cognitoDomain}/logout`);
  url.search = new URLSearchParams({
    client_id: settings.clientId,
    logout_uri: redirect,
  }).toString();
  return url.href;
}

export type CallbackParams =
  | { kind: 'code'; code: string; state: string }
  | { kind: 'error'; error: string; description: string | null }
  | null;

/**
 * Reads what Cognito put on the query string.
 *
 * `error` is a real outcome, not an edge case: it is what a cancelled login or
 * a disabled account produces, and swallowing it leaves the user on a sign-in
 * button that appears to do nothing.
 */
export function readCallbackParams(search: string): CallbackParams {
  const params = new URLSearchParams(search);

  const error = params.get('error');
  if (error) {
    return { kind: 'error', error, description: params.get('error_description') };
  }

  const code = params.get('code');
  const state = params.get('state');
  if (code && state) return { kind: 'code', code, state };

  return null;
}

export interface ExchangeInput {
  code: string;
  verifier: string;
  redirectUri: string;
  settings?: AppConfig;
  fetchImpl?: typeof fetch;
  now?: number;
}

/**
 * Redeems the authorization code.
 *
 * Deliberately shaped like `CognitoRefresher.refresh` — same endpoint, same
 * form encoding, same `tokenSetFromWire` boundary — so there is exactly one
 * place in the app that knows what a Cognito token response looks like.
 */
export async function exchangeCode({
  code,
  verifier,
  redirectUri: redirect,
  settings = config,
  fetchImpl = globalThis.fetch.bind(globalThis),
  now = Date.now(),
}: ExchangeInput): Promise<TokenSet> {
  const body = new URLSearchParams({
    grant_type: 'authorization_code',
    client_id: settings.clientId,
    code,
    redirect_uri: redirect,
    code_verifier: verifier,
  });

  let response: Response;
  try {
    response = await fetchImpl(`${settings.cognitoDomain}/oauth2/token`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body,
    });
  } catch (cause) {
    throw networkError(cause);
  }

  if (!response.ok) {
    throw new ApiError({
      kind: 'http',
      status: response.status === 400 ? 401 : response.status,
      title: 'That sign-in could not be completed',
      detail: 'Try signing in again.',
    });
  }

  const wire = (await response.json()) as TokenSetWire;
  return tokenSetFromWire(wire, now);
}
