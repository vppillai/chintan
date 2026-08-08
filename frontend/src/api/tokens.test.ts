import { describe, expect, it } from 'vitest';

import {
  TOKEN_STORAGE_KEY,
  bearerHeader,
  canRefresh,
  createTokenStore,
  isExpired,
  needsRefresh,
  tokenSetFromWire,
  type TokenSet,
} from './tokens.ts';

const NOW = 1_700_000_000_000;

const WIRE = {
  id_token: 'id-1',
  access_token: 'access-1',
  refresh_token: 'refresh-1',
  expires_in: 3600,
  token_type: 'Bearer',
};

describe('tokenSetFromWire — the one place snake_case is read', () => {
  it('maps every field and derives an absolute expiry', () => {
    expect(tokenSetFromWire(WIRE, NOW)).toEqual({
      idToken: 'id-1',
      accessToken: 'access-1',
      refreshToken: 'refresh-1',
      expiresAt: NOW + 3_600_000,
      tokenType: 'Bearer',
    });
  });

  it('carries the previous refresh token forward when the response omits it', () => {
    // This is the v1 defect in miniature: Cognito's refresh grant does not
    // return a refresh_token, and dropping it logs the user out one window
    // later, apparently at random.
    const previous = tokenSetFromWire(WIRE, NOW);
    const refreshed = tokenSetFromWire(
      { id_token: 'id-2', access_token: 'access-2', expires_in: 3600, token_type: 'Bearer' },
      NOW + 3_000_000,
      previous,
    );

    expect(refreshed.refreshToken).toBe('refresh-1');
    expect(refreshed.idToken).toBe('id-2');
  });

  it('yields a null refresh token when there is none and no previous set', () => {
    const parsed = tokenSetFromWire(
      { id_token: 'id-1', access_token: 'a', expires_in: 60, token_type: 'Bearer' },
      NOW,
    );
    expect(parsed.refreshToken).toBeNull();
    expect(canRefresh(parsed)).toBe(false);
  });

  it('defaults an empty token type to Bearer', () => {
    const parsed = tokenSetFromWire({ ...WIRE, token_type: '' }, NOW);
    expect(parsed.tokenType).toBe('Bearer');
  });
});

describe('expiry', () => {
  const tokens = tokenSetFromWire(WIRE, NOW);

  it('is not expired before its expiry', () => {
    expect(isExpired(tokens, NOW)).toBe(false);
    expect(isExpired(tokens, NOW + 3_600_001)).toBe(true);
  });

  it('asks for a refresh inside the skew window, before the user sees a 401', () => {
    expect(needsRefresh(tokens, NOW)).toBe(false);
    expect(needsRefresh(tokens, NOW + 3_600_000 - 130_000)).toBe(false);
    expect(needsRefresh(tokens, NOW + 3_600_000 - 60_000)).toBe(true);
  });
});

describe('bearerHeader', () => {
  it('uses the id token, which is what the API verifies', () => {
    expect(bearerHeader(tokenSetFromWire(WIRE, NOW))).toBe('Bearer id-1');
  });

  it('is null when unauthenticated', () => {
    expect(bearerHeader(null)).toBeNull();
  });
});

describe('createTokenStore', () => {
  function freshStorage(): Storage {
    window.localStorage.clear();
    return window.localStorage;
  }

  it('round-trips a token set', () => {
    const store = createTokenStore(freshStorage());
    const tokens = tokenSetFromWire(WIRE, NOW);

    store.write(tokens);
    expect(store.read()).toEqual(tokens);

    store.clear();
    expect(store.read()).toBeNull();
  });

  it('discards a shape written by an older build rather than coercing it', () => {
    // Half a token set authenticates and cannot refresh, which is worse than
    // none: the user gets one working session and then a hard logout.
    const storage = freshStorage();
    storage.setItem(TOKEN_STORAGE_KEY, JSON.stringify({ id_token: 'legacy' }));

    expect(createTokenStore(storage).read()).toBeNull();
  });

  it('survives unparseable stored data', () => {
    const storage = freshStorage();
    storage.setItem(TOKEN_STORAGE_KEY, 'not json');
    expect(createTokenStore(storage).read()).toBeNull();
  });

  it('degrades to null when storage is unavailable', () => {
    const store = createTokenStore(undefined);
    expect(store.read()).toBeNull();
    expect(() => store.write({} as TokenSet)).not.toThrow();
    expect(() => store.clear()).not.toThrow();
  });
});
