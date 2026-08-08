import { describe, expect, it, vi } from 'vitest';

import { ApiError } from './problem.ts';
import { Session, type TokenRefresher } from './session.ts';
import { createMemoryTokenStore, type TokenSet } from './tokens.ts';

function tokens(overrides: Partial<TokenSet> = {}): TokenSet {
  return {
    idToken: 'id-1',
    accessToken: 'access-1',
    refreshToken: 'refresh-1',
    expiresAt: Date.now() + 3_600_000,
    tokenType: 'Bearer',
    ...overrides,
  };
}

/** A refresher whose completion the test decides. */
function deferredRefresher(): TokenRefresher & { resolve: (next: TokenSet) => void } {
  let release: (next: TokenSet) => void = () => {};
  const refresher = {
    refresh: () =>
      new Promise<TokenSet>((resolve) => {
        release = resolve;
      }),
    resolve: (next: TokenSet) => {
      release(next);
    },
  };
  return refresher;
}

describe('signing out beats a refresh that is already in flight', () => {
  it('does not resurrect the session when the refresh lands after clear()', async () => {
    /*
     * Reachable from the sign-out button. The app polls in the background, a
     * poll takes a 401 and starts a refresh, and the user taps Sign out while
     * it is in the air. The refresh then resolved into `set()`, which wrote a
     * fresh token set straight back into storage — so the user was signed out
     * and signed back in, with no way to tell.
     */
    const store = createMemoryTokenStore(tokens());
    const refresher = deferredRefresher();
    const session = new Session(store, refresher);

    const inFlight = session.refresh().catch(() => null);
    session.clear();

    refresher.resolve(tokens({ idToken: 'id-2', accessToken: 'access-2' }));
    await inFlight;

    expect(session.current(), 'the session came back after sign-out').toBeNull();
    expect(session.isAuthenticated()).toBe(false);
    expect(store.read(), 'a token was written back to storage after sign-out').toBeNull();
  });

  it('tells the caller its refresh was abandoned rather than resolving quietly', async () => {
    const refresher = deferredRefresher();
    const session = new Session(createMemoryTokenStore(tokens()), refresher);

    const inFlight = session.refresh();
    session.clear();
    refresher.resolve(tokens({ idToken: 'id-2' }));

    await expect(inFlight).rejects.toBeInstanceOf(ApiError);
  });

  it('still refreshes normally when nothing interrupts it', async () => {
    const store = createMemoryTokenStore(tokens());
    const next = tokens({ idToken: 'id-2' });
    const session = new Session(store, { refresh: () => Promise.resolve(next) });

    await expect(session.refresh()).resolves.toEqual(next);
    expect(session.current()?.idToken).toBe('id-2');
    expect(store.read()?.idToken).toBe('id-2');
  });

  it('coalesces concurrent refreshes onto one call', async () => {
    // The property the class exists for; asserted here so the generation guard
    // cannot quietly break it.
    const refresh = vi.fn(() => Promise.resolve(tokens({ idToken: 'id-2' })));
    const session = new Session(createMemoryTokenStore(tokens()), { refresh });

    await Promise.all([session.refresh(), session.refresh(), session.refresh()]);

    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('signs out cleanly when a later refresh is attempted', async () => {
    const session = new Session(createMemoryTokenStore(tokens()), deferredRefresher());
    session.clear();
    await expect(session.refresh()).rejects.toBeInstanceOf(ApiError);
  });
});
