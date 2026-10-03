import { afterEach, describe, expect, it, vi } from 'vitest';

import { capturePollInterval, CAPTURE_POLL_FAST_MS } from '@/api/queries/captures.ts';
import type { CaptureWire } from '@/api/schema.ts';
import { isStuck, retryAccepted } from '@/features/capture/filing/model.ts';

import { ApiClient } from './client.ts';
import { observeServerDate, resetServerClock, serverNow } from './clock.ts';
import { retryAfterMs } from './problem.ts';
import { Session } from './session.ts';
import { createMemoryTokenStore } from './tokens.ts';

/** A server answer stamped with the server's own clock. */
function dated(serverMs: number): Response {
  return new Response('{}', {
    status: 200,
    headers: { 'content-type': 'application/json', date: new Date(serverMs).toUTCString() },
  });
}

/** A capture the server wrote at `serverMs`, still moving, Retry allowed from `retryAfterMs`. */
function capture(serverMs: number, retryAfterMs: number): CaptureWire {
  return {
    id: 'c1',
    status: 'transcribing',
    created_at: new Date(serverMs).toISOString(),
    last_progress_at: new Date(serverMs).toISOString(),
    retry_after: new Date(retryAfterMs).toISOString(),
  } as CaptureWire;
}

const MINUTE = 60_000;
const SERVER = Date.parse('2026-10-03T12:00:00Z');

afterEach(() => {
  resetServerClock();
  vi.useRealTimers();
});

/**
 * The filing rules read the server's clock, not the phone's: a device
 * minutes off must neither call a fresh capture stuck nor hold Retry back
 * past the instant the server named.
 */
describe('serverNow', () => {
  it('is the device clock until a response dates itself', () => {
    vi.useFakeTimers({ now: SERVER + 12 * MINUTE });
    expect(serverNow()).toBe(SERVER + 12 * MINUTE);
  });

  it('a phone twelve minutes fast does not call a capture the server just wrote stuck', () => {
    vi.useFakeTimers({ now: SERVER + 12 * MINUTE });
    const fresh = capture(SERVER, SERVER + 15 * MINUTE);
    // Before the header is seen the device clock says twelve minutes have passed.
    expect(isStuck(fresh)).toBe(true);
    observeServerDate(dated(SERVER));
    expect(serverNow()).toBe(SERVER);
    expect(isStuck(fresh)).toBe(false);
    expect(retryAccepted(fresh)).toBe(false);
    // The poll stays brisk for a capture that is in fact seconds old.
    expect(capturePollInterval([fresh])).toBe(CAPTURE_POLL_FAST_MS);
  });

  it('a phone ten minutes slow does not withhold Retry past retry_after', () => {
    vi.useFakeTimers({ now: SERVER - 10 * MINUTE });
    const stalled = capture(SERVER - 16 * MINUTE, SERVER - MINUTE);
    expect(retryAccepted(stalled)).toBe(false);
    observeServerDate(dated(SERVER));
    expect(retryAccepted(stalled)).toBe(true);
    expect(isStuck(stalled)).toBe(true);
  });

  it('treats an offset under two seconds as none, and ignores a missing or unreadable header', () => {
    vi.useFakeTimers({ now: SERVER });
    observeServerDate(dated(SERVER + 1_500));
    expect(serverNow()).toBe(SERVER);
    observeServerDate(dated(SERVER + 5 * MINUTE));
    expect(serverNow()).toBe(SERVER + 5 * MINUTE);
    observeServerDate(new Response('{}', { headers: { date: 'yesterday-ish' } }));
    expect(serverNow()).toBe(SERVER + 5 * MINUTE);
    observeServerDate(new Response('{}'));
    expect(serverNow()).toBe(SERVER + 5 * MINUTE);
  });

  it('a Retry-After date is measured against the server clock too', () => {
    vi.useFakeTimers({ now: SERVER + 10 * MINUTE });
    observeServerDate(dated(SERVER));
    const response = new Response(null, {
      status: 429,
      headers: { 'retry-after': new Date(SERVER + 30_000).toUTCString() },
    });
    expect(retryAfterMs(response)).toBe(30_000);
  });

  it('the API client reads the header once per response, on errors too', async () => {
    vi.useFakeTimers({ now: SERVER + 12 * MINUTE });
    const session = new Session(createMemoryTokenStore(null), { refresh: async (t) => t });
    const answers = [dated(SERVER), new Response('{}', { status: 500, headers: { date: new Date(SERVER + 1_000).toUTCString() } })];
    const client = new ApiClient(session, 'https://api.test', async () => answers.shift() ?? dated(SERVER));
    await client.request('/v1/health', { anonymous: true });
    expect(serverNow()).toBe(SERVER);
    await expect(client.request('/v1/health', { anonymous: true, retry: { maxRetries: 0, baseDelayMs: 0, maxDelayMs: 0 } })).rejects.toBeDefined();
    expect(serverNow()).toBe(SERVER + 1_000);
  });
});
