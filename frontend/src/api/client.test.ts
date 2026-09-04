import { beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiClient, NO_RETRY, type RetryPolicy } from './client.ts';
import { ApiError } from './problem.ts';
import { Session, type TokenRefresher } from './session.ts';
import { createMemoryTokenStore, tokenSetFromWire, type TokenSet } from './tokens.ts';

const FAST_RETRY: RetryPolicy = { maxRetries: 2, baseDelayMs: 0, maxDelayMs: 0 };

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

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
    ...init,
  });
}

function problemResponse(status: number, body: Record<string, unknown> = {}): Response {
  return new Response(
    JSON.stringify({ type: 'about:blank', title: 'Failed', status, ...body }),
    {
      status,
      headers: {
        'content-type': 'application/problem+json',
        'X-Correlation-Id': 'corr-abc',
      },
    },
  );
}

/** The headers actually sent on the nth fetch attempt. */
function headersOf(fetchImpl: ReturnType<typeof vi.fn<typeof fetch>>, index: number): Headers {
  const init = fetchImpl.mock.calls[index]?.[1];
  return new Headers(init?.headers);
}

/** A refresher that records calls and can be made to fail. */
function stubRefresher(
  impl: (current: TokenSet) => Promise<TokenSet>,
): TokenRefresher & { calls: number } {
  const refresher = {
    calls: 0,
    async refresh(current: TokenSet) {
      refresher.calls += 1;
      return impl(current);
    },
  };
  return refresher;
}

function build(
  fetchImpl: typeof fetch,
  refresher: TokenRefresher,
  initial: TokenSet | null = tokens(),
) {
  const store = createMemoryTokenStore(initial);
  const session = new Session(store, refresher);
  const client = new ApiClient(session, 'https://api.test', fetchImpl);
  return { client, session, store };
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe('bearer and idempotency', () => {
  it('injects the id token as the bearer', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => jsonResponse({ ok: true }));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await client.request('/v1/notes');

    expect(headersOf(fetchImpl, 0).get('Authorization')).toBe('Bearer id-1');
  });

  it('sends an Idempotency-Key on POST and not on GET', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => jsonResponse({ ok: true }));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await client.request('/v1/notes', { method: 'POST', body: { title: 'x' } });
    await client.request('/v1/notes');

    const post = headersOf(fetchImpl, 0);
    const get = headersOf(fetchImpl, 1);

    const key = post.get('Idempotency-Key');
    expect(key).toBeTruthy();
    expect(key!.length).toBeGreaterThanOrEqual(8);
    expect(get.get('Idempotency-Key')).toBeNull();
  });

  it('holds one Idempotency-Key across the internal retries of a POST', async () => {
    // The whole point: a retried POST must replay, not act twice.
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(problemResponse(503))
      .mockResolvedValueOnce(problemResponse(503))
      .mockResolvedValueOnce(jsonResponse({ id: 'n1' }));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await client.request('/v1/notes', {
      method: 'POST',
      body: { title: 'x' },
      retry: FAST_RETRY,
    });

    const keys = fetchImpl.mock.calls.map((call) =>
      new Headers(call[1]?.headers).get('Idempotency-Key'),
    );
    expect(keys).toHaveLength(3);
    expect(new Set(keys).size).toBe(1);
  });

  it('reuses a caller-supplied key so a resumed capture replays its original create', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => jsonResponse({ ok: true }));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await client.request('/v1/captures', {
      method: 'POST',
      body: {},
      idempotencyKey: 'capture-local-123',
    });

    expect(headersOf(fetchImpl, 0).get('Idempotency-Key')).toBe('capture-local-123');
  });
});

describe('a 401 refreshes before anything reaches the user', () => {
  it('refreshes and replays the request, so the caller never sees the 401', async () => {
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(problemResponse(401))
      .mockResolvedValueOnce(jsonResponse({ items: [] }));
    const refresher = stubRefresher(async (current) =>
      tokenSetFromWire(
        {
          id_token: 'id-2',
          access_token: 'access-2',
          expires_in: 3600,
          token_type: 'Bearer',
        },
        Date.now(),
        current,
      ),
    );
    const { client } = build(fetchImpl, refresher);

    const result = await client.request<{ items: unknown[] }>('/v1/notes');

    expect(result).toEqual({ items: [] });
    expect(refresher.calls).toBe(1);
    expect(headersOf(fetchImpl, 1).get('Authorization')).toBe('Bearer id-2');
  });

  it('replays with the same idempotency key after refreshing', async () => {
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(problemResponse(401))
      .mockResolvedValueOnce(jsonResponse({ id: 'n1' }));
    const { client } = build(
      fetchImpl,
      stubRefresher(async (current) => ({ ...current, idToken: 'id-2' })),
    );

    await client.request('/v1/notes', { method: 'POST', body: { title: 'x' } });

    const keys = fetchImpl.mock.calls.map((call) =>
      new Headers(call[1]?.headers).get('Idempotency-Key'),
    );
    expect(new Set(keys).size).toBe(1);
  });

  it('refreshes at most once per request, then surfaces the 401', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => problemResponse(401));
    const refresher = stubRefresher(async (current) => ({ ...current, idToken: 'id-2' }));
    const { client } = build(fetchImpl, refresher);

    await expect(client.request('/v1/notes')).rejects.toMatchObject({
      status: 401,
      correlationId: 'corr-abc',
    });
    expect(refresher.calls).toBe(1);
  });

  it('surfaces the 401 and clears the session when the refresh is rejected', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => problemResponse(401));
    const refresher = stubRefresher(async () => {
      throw new ApiError({ kind: 'http', status: 401, title: 'Your session has expired' });
    });
    const { client, session } = build(fetchImpl, refresher);

    await expect(client.request('/v1/notes')).rejects.toBeInstanceOf(ApiError);
    expect(session.current()).toBeNull();
  });

  it('does not sign the user out when the refresh fails because they are offline', async () => {
    // Offline is not unauthenticated. Clearing here would take the queued
    // mutations and the in-flight recording's credentials with it.
    const fetchImpl = vi.fn<typeof fetch>(async () => problemResponse(401));
    const refresher = stubRefresher(async () => {
      throw new ApiError({ kind: 'network', status: 0, title: 'No connection' });
    });
    const { client, session } = build(fetchImpl, refresher);

    await expect(client.request('/v1/notes')).rejects.toMatchObject({ kind: 'network' });
    expect(session.current()).not.toBeNull();
  });

  it('never touches window.location', async () => {
    // A `window.location.reload()` after a 401 would destroy unsaved edits and
    // in-flight recordings.
    const reload = vi.fn();
    const assign = vi.fn();
    vi.stubGlobal('location', { ...window.location, reload, assign });

    const fetchImpl = vi.fn<typeof fetch>(async () => problemResponse(401));
    const { client } = build(
      fetchImpl,
      stubRefresher(async () => {
        throw new ApiError({ kind: 'http', status: 401, title: 'Expired' });
      }),
    );

    await expect(client.request('/v1/notes')).rejects.toBeInstanceOf(ApiError);
    expect(reload).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });

  it('coalesces concurrent 401s onto a single refresh', async () => {
    // Cognito rotates refresh tokens; five parallel refreshes means four
    // losers and a random logout.
    let served = 0;
    const fetchImpl = vi.fn<typeof fetch>(async () => {
      served += 1;
      return served <= 5 ? problemResponse(401) : jsonResponse({ ok: true });
    });
    const refresher = stubRefresher(async (current) => {
      await new Promise((resolve) => setTimeout(resolve, 5));
      return { ...current, idToken: 'id-2' };
    });
    const { client } = build(fetchImpl, refresher);

    await Promise.all([
      client.request('/v1/notes'),
      client.request('/v1/tags'),
      client.request('/v1/settings'),
      client.request('/v1/captures'),
      client.request('/v1/search?q=a'),
    ]);

    expect(refresher.calls).toBe(1);
  });

  it('does not attempt a refresh on an anonymous route', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => problemResponse(401));
    const refresher = stubRefresher(async (t) => t);
    const { client } = build(fetchImpl, refresher);

    await expect(
      client.request('/v1/health', { anonymous: true, retry: NO_RETRY }),
    ).rejects.toBeInstanceOf(ApiError);
    expect(refresher.calls).toBe(0);
  });
});

describe('proactive refresh', () => {
  it('refreshes before the request when the token is inside the skew window', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => jsonResponse({ ok: true }));
    const refresher = stubRefresher(async (current) => ({
      ...current,
      idToken: 'id-fresh',
      expiresAt: Date.now() + 3_600_000,
    }));
    const { client } = build(fetchImpl, refresher, tokens({ expiresAt: Date.now() + 1_000 }));

    await client.request('/v1/notes');

    expect(refresher.calls).toBe(1);
    expect(headersOf(fetchImpl, 0).get('Authorization')).toBe('Bearer id-fresh');
  });
});

describe('retry and errors', () => {
  it('retries 5xx within the budget and then succeeds', async () => {
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(problemResponse(500))
      .mockResolvedValueOnce(jsonResponse({ ok: true }));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await expect(client.request('/v1/notes', { retry: FAST_RETRY })).resolves.toEqual({
      ok: true,
    });
    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });

  it('gives up after the retry budget and throws the last error', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => problemResponse(503));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await expect(client.request('/v1/notes', { retry: FAST_RETRY })).rejects.toMatchObject({
      status: 503,
    });
    expect(fetchImpl).toHaveBeenCalledTimes(3);
  });

  it('does not retry a 400', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => problemResponse(400));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await expect(client.request('/v1/notes', { retry: FAST_RETRY })).rejects.toMatchObject({
      status: 400,
    });
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it('retries a network failure', async () => {
    const fetchImpl = vi
      .fn<typeof fetch>()
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(jsonResponse({ ok: true }));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await expect(client.request('/v1/notes', { retry: FAST_RETRY })).resolves.toEqual({
      ok: true,
    });
  });

  it('reports a network failure as offline, not as a server error', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    const error = await client.request('/v1/notes', { retry: NO_RETRY }).catch((e) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).isOffline).toBe(true);
    expect((error as ApiError).userMessage).toMatch(/no connection/i);
  });

  it('parses problem+json into a typed error carrying the correlation id', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () =>
      problemResponse(409, {
        title: 'Someone else changed this first',
        detail: 'Reload and try again.',
        current_version: 7,
        correlation_id: 'corr-xyz',
      }),
    );
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    const error = (await client
      .request('/v1/notes/n1', { method: 'PATCH', body: {}, retry: NO_RETRY })
      .catch((e) => e)) as ApiError;

    expect(error.isConflict).toBe(true);
    expect(error.currentVersion).toBe(7);
    expect(error.correlationId).toBe('corr-xyz');
    expect(error.userMessage).toBe('Reload and try again.');
  });

  it('does not surface an upstream HTML body as an error message', async () => {
    // Rendering raw upstream bodies is how DynamoDB table names reach the
    // screen.
    const fetchImpl = vi.fn<typeof fetch>(
      async () =>
        new Response('<html>ResourceNotFoundException: table chintan-dev-prod</html>', {
          status: 502,
          headers: { 'content-type': 'text/html' },
        }),
    );
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    const error = (await client
      .request('/v1/notes', { retry: NO_RETRY })
      .catch((e) => e)) as ApiError;

    expect(error.userMessage).not.toMatch(/chintan-dev-prod/);
    expect(error.userMessage).toBe('The service is unreachable');
  });

  it('treats the spend cap as its own case, not a generic 429', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () =>
      problemResponse(429, {
        type: 'https://chintan.dev/problems/spend-cap',
        title: 'Daily spend cap reached',
      }),
    );
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    const error = (await client
      .request('/v1/captures', { method: 'POST', body: {}, retry: FAST_RETRY })
      .catch((e) => e)) as ApiError;

    expect(error.isSpendCapped).toBe(true);
    expect(error.isRetryable).toBe(false);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });
});

describe('timeout and cancellation', () => {
  it('aborts a request that outlasts its timeout', async () => {
    const fetchImpl = vi.fn<typeof fetch>(
      (_input, init) =>
        new Promise((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => {
            reject(new DOMException('Aborted', 'AbortError'));
          });
        }),
    );
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    const error = (await client
      .request('/v1/notes', { timeoutMs: 10, retry: NO_RETRY })
      .catch((e) => e)) as ApiError;

    expect(error.kind).toBe('timeout');
  });

  it('surfaces caller cancellation as cancelled, not as a failure to retry', async () => {
    const controller = new AbortController();
    const fetchImpl = vi.fn<typeof fetch>(
      (_input, init) =>
        new Promise((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => {
            reject(new DOMException('Aborted', 'AbortError'));
          });
        }),
    );
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    const promise = client.request('/v1/notes', { signal: controller.signal });
    controller.abort();

    const error = (await promise.catch((e) => e)) as ApiError;
    expect(error.kind).toBe('cancelled');
    expect(error.isRetryable).toBe(false);
  });
});

describe('204 and empty bodies', () => {
  it('resolves a 204 without parsing a body', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    const { client } = build(fetchImpl, stubRefresher(async (t) => t));

    await expect(client.request('/v1/notes/n1', { method: 'DELETE' })).resolves.toBeUndefined();
  });
});
