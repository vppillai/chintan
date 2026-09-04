/**
 * Token lifecycle: who holds the current set, and how a refresh happens.
 *
 * Split out from the HTTP client so the client depends on an interface and the
 * tests can drive refresh deterministically without a fetch mock pretending to
 * be Cognito.
 */

import { config } from '@/config/env.ts';

import { ApiError, networkError } from './problem.ts';
import type { TokenSetWire } from './schema.ts';
import {
  canRefresh,
  createTokenStore,
  tokenSetFromWire,
  type TokenSet,
  type TokenStore,
} from './tokens.ts';

/** Exchanges a refresh token for a new set. */
export interface TokenRefresher {
  refresh(tokens: TokenSet): Promise<TokenSet>;
}

export type SessionListener = (tokens: TokenSet | null) => void;

/**
 * Owns the current token set.
 *
 * The important property is **single-flight refresh**: five requests failing
 * 401 at once produce one refresh, not five. Cognito invalidates the old
 * refresh token when rotation is enabled, so concurrent refreshes race and all
 * but one lose — which presents as a random logout.
 */
export class Session {
  private tokens: TokenSet | null;
  private inFlight: Promise<TokenSet> | null = null;
  private readonly listeners = new Set<SessionListener>();
  /**
   * Bumped by every `clear()`.
   *
   * A refresh captures the value it started under and refuses to apply its
   * result if it no longer matches. Without it, a refresh in the air when the
   * user signs out resolved into `set()` afterwards and wrote a fresh token set
   * straight back — the session was cleared and then silently restored, which
   * is a sign-out that did not sign anybody out.
   */
  private generation = 0;

  constructor(
    private readonly store: TokenStore,
    private readonly refresher: TokenRefresher,
  ) {
    this.tokens = store.read();
  }

  current(): TokenSet | null {
    return this.tokens;
  }

  isAuthenticated(): boolean {
    return this.tokens !== null;
  }

  set(tokens: TokenSet): void {
    this.tokens = tokens;
    this.store.write(tokens);
    this.emit();
  }

  /**
   * Drops the session. Deliberately does *not* navigate or reload: a
   * `window.location.reload()` after a 401 destroys unsaved note edits and
   * kills in-flight recordings. Sign-out is the UI's decision to make, from a
   * rendered state, not a side effect of a fetch.
   */
  clear(): void {
    this.generation += 1;
    this.tokens = null;
    this.inFlight = null;
    this.store.clear();
    this.emit();
  }

  subscribe(listener: SessionListener): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  private emit(): void {
    for (const listener of this.listeners) listener(this.tokens);
  }

  /**
   * Refreshes, coalescing concurrent callers onto one network call.
   *
   * Throws if there is nothing to refresh with, so the caller surfaces the
   * original 401 rather than looping.
   */
  async refresh(): Promise<TokenSet> {
    if (this.inFlight) return this.inFlight;

    const current = this.tokens;
    if (!canRefresh(current)) {
      throw new ApiError({
        kind: 'http',
        status: 401,
        title: 'Your session has expired',
        detail: 'Sign in again to continue.',
      });
    }

    const generation = this.generation;

    this.inFlight = this.refresher
      .refresh(current)
      .then((next) => {
        // Signed out while this was in the air. Applying it now would undo the
        // sign-out, so the result is dropped and the caller told.
        if (generation !== this.generation) {
          throw new ApiError({
            kind: 'http',
            status: 401,
            title: 'Your session has ended',
            detail: 'Sign in again to continue.',
          });
        }
        this.set(next);
        return next;
      })
      .catch((error: unknown) => {
        // A refresh that fails because the network is down must NOT sign the
        // user out — they are offline, not unauthenticated, and clearing the
        // session here would lose the queued mutations with it.
        if (error instanceof ApiError && error.isOffline) throw error;
        // Already cleared — by sign-out, or by an earlier failure. Clearing
        // again would bump the generation under a newer refresh.
        if (generation !== this.generation) throw error;
        this.clear();
        throw error;
      })
      .finally(() => {
        // Only if this is still the current attempt: a sign-out may already
        // have started a new one.
        if (generation === this.generation) this.inFlight = null;
      });

    return this.inFlight;
  }
}

/**
 * The real refresher: Cognito's hosted-UI token endpoint, `refresh_token` grant.
 *
 * The response omits `refresh_token`; `tokenSetFromWire` carries the previous
 * one forward. Dropping it logs the user out one window later.
 */
export class CognitoRefresher implements TokenRefresher {
  constructor(
    private readonly domain: string = config.cognitoDomain,
    private readonly clientId: string = config.clientId,
    private readonly fetchImpl: typeof fetch = globalThis.fetch.bind(globalThis),
  ) {}

  async refresh(tokens: TokenSet): Promise<TokenSet> {
    if (!tokens.refreshToken) {
      throw new ApiError({ kind: 'http', status: 401, title: 'Your session has expired' });
    }

    const body = new URLSearchParams({
      grant_type: 'refresh_token',
      client_id: this.clientId,
      refresh_token: tokens.refreshToken,
    });

    let response: Response;
    try {
      response = await this.fetchImpl(`${this.domain}/oauth2/token`, {
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
        title: 'Your session has expired',
        detail: 'Sign in again to continue.',
      });
    }

    const wire = (await response.json()) as TokenSetWire;
    return tokenSetFromWire(wire, Date.now(), tokens);
  }
}

export function createSession(
  refresher: TokenRefresher = new CognitoRefresher(),
  store: TokenStore = createTokenStore(
    typeof window === 'undefined' ? undefined : window.localStorage,
  ),
): Session {
  return new Session(store, refresher);
}
