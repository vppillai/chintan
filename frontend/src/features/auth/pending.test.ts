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
  sessionStorage.clear();
});

describe('the verifier survives the redirect and nothing longer', () => {
  it('round-trips across what would be a full page navigation', () => {
    rememberPending(PENDING);
    expect(takePending(PENDING.startedAt + 1_000)).toEqual(PENDING);
  });

  it('is single-use, like the code it will be exchanged with', () => {
    rememberPending(PENDING);
    takePending(PENDING.startedAt);
    expect(takePending(PENDING.startedAt)).toBeNull();
  });

  it('leaves nothing behind in storage once taken', () => {
    rememberPending(PENDING);
    takePending(PENDING.startedAt);
    expect(sessionStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
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
    sessionStorage.setItem(PENDING_AUTH_KEY, '{"state":"only-a-state"}');
    expect(takePending()).toBeNull();

    sessionStorage.setItem(PENDING_AUTH_KEY, 'not json at all');
    expect(takePending()).toBeNull();
  });

  it('can be cleared without being consumed', () => {
    rememberPending(PENDING);
    clearPending();
    expect(sessionStorage.getItem(PENDING_AUTH_KEY)).toBeNull();
  });
});
