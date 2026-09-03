import { beforeEach, describe, expect, it } from 'vitest';

import {
  PENDING_AUTH_KEY,
  PENDING_TTL_MS,
  clearPending,
  rememberPending,
  takePending,
} from './pending.ts';

const PENDING = {
  state: 'st-1',
  verifier: 'ver-1',
  returnTo: '/notes',
  startedAt: 1_000_000,
};

beforeEach(() => {
  localStorage.clear();
});

describe('the verifier survives the redirect and nothing longer', () => {
  it('round-trips across what would be a full page navigation', () => {
    rememberPending(PENDING);
    expect(takePending(PENDING.startedAt + 1_000)).toEqual(PENDING);
  });

  it('survives the process being killed between leaving and coming back', () => {
    /*
     * An iOS home-screen app switched away from — to read an MFA code — is
     * routinely killed, and `sessionStorage` goes with it. The entry has to be
     * where a fresh process can find it, which is `localStorage`.
     */
    rememberPending(PENDING);
    expect(localStorage.getItem(PENDING_AUTH_KEY)).not.toBeNull();
    expect(sessionStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
  });

  it('is single-use, like the code it will be exchanged with', () => {
    rememberPending(PENDING);
    takePending(PENDING.startedAt);
    expect(takePending(PENDING.startedAt)).toBeNull();
  });

  it('leaves nothing behind in storage once taken', () => {
    rememberPending(PENDING);
    takePending(PENDING.startedAt);
    expect(localStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
  });

  it('refuses a flow older than the TTL', () => {
    // Bounds how long a secret sits in storage, and stops a week-old tab
    // accepting a `state` it has no business accepting.
    rememberPending(PENDING);
    expect(takePending(PENDING.startedAt + PENDING_TTL_MS + 1)).toBeNull();
  });

  it('is null when no flow was ever started', () => {
    expect(takePending()).toBeNull();
  });

  it('discards a malformed or half-written entry rather than coercing it', () => {
    localStorage.setItem(PENDING_AUTH_KEY, '{"state":"only-a-state"}');
    expect(takePending()).toBeNull();

    localStorage.setItem(PENDING_AUTH_KEY, 'not json at all');
    expect(takePending()).toBeNull();
  });

  it('can be cleared without being consumed', () => {
    rememberPending(PENDING);
    clearPending();
    expect(localStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
  });
});
