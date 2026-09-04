/**
 * The one and only place token field names exist.
 *
 * Spreading them across files with two spellings is how a client ends up
 * checking `id_token` for validity, sending `id_token` as the bearer, and then
 * decoding `access_token` to decide whether a refresh is due — so a login path
 * that populates only some of those fields leaves `access_token` undefined,
 * the decode throws, refresh is skipped, and the user is silently logged out
 * on the next foreground.
 *
 * The fix is not "be careful". It is that the wire shape is parsed exactly
 * once, at the boundary, into a single internal type with camelCase names that
 * cannot be confused with the snake_case wire names. Nothing outside this
 * module reads `id_token` or `access_token`.
 */

import type { TokenSetWire } from './schema.ts';

export interface TokenSet {
  /** Identity claims. This is the bearer the API expects. */
  idToken: string;
  /** Cognito access token. Kept for the refresh grant and OAuth flows. */
  accessToken: string;
  /**
   * Long-lived refresh grant. Absent from a refresh response, which returns
   * everything *except* this — carrying the previous one forward is required.
   */
  refreshToken: string | null;
  /** Absolute expiry, epoch milliseconds. Derived once from `expires_in`. */
  expiresAt: number;
  tokenType: string;
}

export const TOKEN_STORAGE_KEY = 'chintan.tokens.v2';

/**
 * Refresh this far before the token actually expires. A token that expires
 * mid-flight produces a 401 the user sees; one refreshed early does not.
 */
export const REFRESH_SKEW_MS = 120_000;

/**
 * Parses the wire shape into the internal one. The only function permitted to
 * name a snake_case token field.
 */
export function tokenSetFromWire(
  wire: TokenSetWire,
  now: number = Date.now(),
  previous?: TokenSet | null,
): TokenSet {
  return {
    idToken: wire.id_token,
    accessToken: wire.access_token,
    // A refresh grant response omits `refresh_token`. Dropping it here would
    // log the user out at the end of the *next* window.
    refreshToken: wire.refresh_token ?? previous?.refreshToken ?? null,
    expiresAt: now + wire.expires_in * 1000,
    tokenType: wire.token_type || 'Bearer',
  };
}

export function isExpired(tokens: TokenSet, now: number = Date.now()): boolean {
  return now >= tokens.expiresAt;
}

export function needsRefresh(tokens: TokenSet, now: number = Date.now()): boolean {
  return now >= tokens.expiresAt - REFRESH_SKEW_MS;
}

export function canRefresh(tokens: TokenSet | null): tokens is TokenSet {
  return Boolean(tokens?.refreshToken);
}

/** The value for the `Authorization` header, or null when unauthenticated. */
export function bearerHeader(tokens: TokenSet | null): string | null {
  if (!tokens) return null;
  return `Bearer ${tokens.idToken}`;
}

function isTokenSet(value: unknown): value is TokenSet {
  if (typeof value !== 'object' || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate['idToken'] === 'string' &&
    typeof candidate['accessToken'] === 'string' &&
    typeof candidate['expiresAt'] === 'number' &&
    (candidate['refreshToken'] === null || typeof candidate['refreshToken'] === 'string')
  );
}

/**
 * Token persistence.
 *
 * An interface rather than direct `localStorage` calls so tests do not depend
 * on a global, and so a future move to a more guarded store is one
 * implementation rather than a search-and-replace.
 */
export interface TokenStore {
  read(): TokenSet | null;
  write(tokens: TokenSet): void;
  clear(): void;
}

export function createTokenStore(storage: Storage | undefined): TokenStore {
  return {
    read() {
      if (!storage) return null;
      try {
        const raw = storage.getItem(TOKEN_STORAGE_KEY);
        if (!raw) return null;
        const parsed: unknown = JSON.parse(raw);
        // A shape written by an older build is discarded, not coerced. Half a
        // token set is worse than none: it authenticates and cannot refresh.
        return isTokenSet(parsed) ? parsed : null;
      } catch {
        return null;
      }
    },
    write(tokens) {
      if (!storage) return;
      try {
        storage.setItem(TOKEN_STORAGE_KEY, JSON.stringify(tokens));
      } catch {
        /* Storage denied. The in-memory set still works for this session. */
      }
    },
    clear() {
      if (!storage) return;
      try {
        storage.removeItem(TOKEN_STORAGE_KEY);
      } catch {
        /* Nothing to do. */
      }
    },
  };
}

/** An in-memory store, for tests and for private-mode fallback. */
export function createMemoryTokenStore(initial: TokenSet | null = null): TokenStore {
  let current = initial;
  return {
    read: () => current,
    write: (tokens) => {
      current = tokens;
    },
    clear: () => {
      current = null;
    },
  };
}
