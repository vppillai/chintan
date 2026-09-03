import { IDBFactory } from 'fake-indexeddb';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError, SPEND_CAP_PROBLEM_TYPE } from '@/api/problem.ts';

import { resetDatabaseHandle, type QueuedMutation } from './db.ts';
import { MAX_ATTEMPTS, count, enqueue, flush, isDead, pending, remove } from './queue.ts';

beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
  resetDatabaseHandle();
});

afterEach(() => {
  vi.useRealTimers();
});

const offline = () => new ApiError({ kind: 'network', status: 0, title: 'No connection' });
const badRequest = () =>
  new ApiError({ kind: 'http', status: 400, title: 'Invalid', detail: 'Title too long' });
const serverError = () => new ApiError({ kind: 'http', status: 503, title: 'Unavailable' });
const unauthorized = () => new ApiError({ kind: 'http', status: 401, title: 'Expired' });
const rateLimited = () => new ApiError({ kind: 'http', status: 429, title: 'Too Many Requests' });
const spendCapped = () =>
  new ApiError({
    kind: 'http',
    status: 429,
    title: 'Too Many Requests',
    problemType: SPEND_CAP_PROBLEM_TYPE,
  });

describe('enqueue', () => {
  it('stores a mutation with an idempotency key', async () => {
    const mutation = await enqueue({ kind: 'updateNote', payload: { id: 'n1' } });

    expect(mutation.idempotencyKey).toBeTruthy();
    expect(mutation.attempts).toBe(0);
    expect(await count()).toBe(1);
  });

  it('is itself idempotent, so a double-tap queues one mutation', async () => {
    await enqueue({ id: 'fixed', kind: 'updateNote', payload: { id: 'n1' } });
    await enqueue({ id: 'fixed', kind: 'updateNote', payload: { id: 'n1' } });

    expect(await count()).toBe(1);
  });

  it('preserves order of intent, oldest first', async () => {
    // Only Date is faked: faking timers wholesale would stall idb's own
    // promise scheduling.
    vi.useFakeTimers({ toFake: ['Date'] });

    vi.setSystemTime(3_000);
    await enqueue({ kind: 'updateNote', payload: 'third' });
    vi.setSystemTime(1_000);
    await enqueue({ kind: 'updateNote', payload: 'first' });
    vi.setSystemTime(2_000);
    await enqueue({ kind: 'updateNote', payload: 'second' });

    expect((await pending()).map((m) => m.payload)).toEqual(['first', 'second', 'third']);
  });
});

describe('flush', () => {
  it('applies every queued mutation and empties the queue', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });
    await enqueue({ kind: 'updateNote', payload: 'b' });

    const run = vi.fn(async () => {});
    const result = await flush(run);

    expect(result).toEqual({ applied: 2, failed: 0, stoppedOffline: false });
    expect(await count()).toBe(0);
  });

  it('replays each mutation with the key it was created with', async () => {
    // This is the only reason retrying a queued POST is safe.
    const created = await enqueue({ kind: 'updateNote', payload: 'a' });

    const seen: string[] = [];
    await flush(async (mutation) => {
      seen.push(mutation.idempotencyKey);
      throw serverError();
    });
    await flush(async (mutation) => {
      seen.push(mutation.idempotencyKey);
    });

    expect(seen).toEqual([created.idempotencyKey, created.idempotencyKey]);
  });

  it('stops at the first offline failure instead of burning every budget', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });
    await enqueue({ kind: 'updateNote', payload: 'b' });
    await enqueue({ kind: 'updateNote', payload: 'c' });

    const run = vi.fn(async () => {
      throw offline();
    });
    const result = await flush(run);

    expect(run).toHaveBeenCalledTimes(1);
    expect(result.stoppedOffline).toBe(true);
    expect(await count()).toBe(3);
  });

  it('resumes from where it stopped when the connection returns', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });
    await enqueue({ kind: 'updateNote', payload: 'b' });

    await flush(async () => {
      throw offline();
    });
    const result = await flush(async () => {});

    expect(result.applied).toBe(2);
    expect(await count()).toBe(0);
  });

  it('keeps a mutation that failed on a server error, and counts the attempt', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });

    await flush(async () => {
      throw serverError();
    });

    const [queued] = await pending();
    expect(queued?.attempts).toBe(1);
    expect(queued?.lastError).toBe('Unavailable');
  });

  it('stops replaying a mutation the server rejected on its merits', async () => {
    // Replaying a 400 forever would block everything behind it.
    await enqueue({ kind: 'updateNote', payload: 'a' });
    await enqueue({ kind: 'updateNote', payload: 'b' });

    const result = await flush(async (mutation: QueuedMutation) => {
      if (mutation.payload === 'a') throw badRequest();
    });

    expect(result).toEqual({ applied: 1, failed: 1, stoppedOffline: false });

    // Nothing queued behind it was held up: that is what "does not block" means,
    // and it is the property this test was written for.
    expect((await pending()).map((entry) => entry.payload)).toEqual(['a']);

    // It is retained rather than deleted, and marked as finished with.
    // Deleting it was how the note screen came to promise a sync forever: the
    // entry vanished, so nothing could tell "it synced" from "it was thrown
    // away", and "Saved on this device — will sync" stayed on screen for an
    // edit that had been discarded.
    const [dead] = await pending();
    expect(dead && isDead(dead)).toBe(true);
    expect(dead?.lastError).toBe('Title too long');
  });

  it('never attempts a rejected mutation a second time', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });
    await flush(async () => {
      throw badRequest();
    });

    const run = vi.fn(async () => {});
    await flush(run);

    expect(run).not.toHaveBeenCalled();
  });

  it('treats a 409 as settled rather than retrying it to exhaustion', async () => {
    /*
     * A queued mutation carries the version the note was loaded at, and that
     * version never changes — so every replay produces the same conflict. It
     * used to burn the whole eight-attempt budget proving that. Same shape as
     * replaying an expired presigned URL: repeating a request whose outcome
     * cannot change.
     */
    await enqueue({ kind: 'updateNote', payload: 'a' });

    const run = vi.fn(async () => {
      throw new ApiError({ kind: 'http', status: 409, title: 'Conflict' });
    });
    await flush(run);
    await flush(run);

    expect(run).toHaveBeenCalledTimes(1);
    const [entry] = await pending();
    expect(entry && isDead(entry)).toBe(true);
  });

  it('keeps a mutation that failed with 401, because a refresh will fix it', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });

    await flush(async () => {
      throw unauthorized();
    });

    expect(await count()).toBe(1);
  });

  it('keeps a 409 so the caller can reconcile rather than losing the edit', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });

    await flush(async () => {
      throw new ApiError({ kind: 'http', status: 409, title: 'Conflict' });
    });

    expect(await count()).toBe(1);
  });

  it('stops attempting a mutation that has exhausted its budget, without deleting it', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });

    for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt += 1) {
      await flush(async () => {
        throw serverError();
      });
    }

    const run = vi.fn(async () => {});
    const result = await flush(run);

    expect(run).not.toHaveBeenCalled();
    expect(result.failed).toBe(1);
    // Retained so the UI can show it as needing attention rather than
    // vanishing the user's edit.
    expect(await count()).toBe(1);
  });

  it('tolerates a non-ApiError thrown by the runner', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });

    const result = await flush(async () => {
      throw new Error('boom');
    });

    expect(result.failed).toBe(1);
    expect((await pending())[0]?.lastError).toBe('boom');
  });
});

describe('flush runs one pass at a time', () => {
  it('hands a concurrent caller the pass already running, so no PATCH is replayed against itself', async () => {
    /*
     * A reconnect refetch and a focus refetch landing together used to start
     * two loops over the same `pending()` list. Both ran the same mutation
     * under the same idempotency key; the loser's 4xx marked the entry dead
     * and re-inserted it after the winner had removed it — a permanent "did
     * not save" for an edit the server had.
     */
    await enqueue({ kind: 'updateNote', payload: 'a' });
    await enqueue({ kind: 'updateNote', payload: 'b' });

    const seen: unknown[] = [];
    let release = (): void => {};
    let entered = (): void => {};
    const firstEntered = new Promise<void>((resolve) => {
      entered = resolve;
    });
    const runner = async (mutation: QueuedMutation) => {
      seen.push(mutation.payload);
      if (seen.length === 1) {
        entered();
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      }
    };

    const first = flush(runner);
    const second = flush(runner);
    expect(second, 'the second caller must share the first pass').toBe(first);

    await firstEntered;
    release();
    const [a, b] = await Promise.all([first, second]);
    expect(a).toEqual({ applied: 2, failed: 0, stoppedOffline: false });
    expect(b).toBe(a);
    // Each mutation ran exactly once. Order is not asserted: both were
    // enqueued in the same millisecond, and the byCreatedAt index does not
    // order ties, so on a fast runner 'b' can legitimately come first.
    expect([...seen].sort()).toEqual(['a', 'b']);
    expect(await count()).toBe(0);
  });

  it('starts a fresh pass once the previous one has settled', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });
    await flush(async () => {});
    await enqueue({ kind: 'updateNote', payload: 'b' });
    const result = await flush(async () => {});
    expect(result.applied).toBe(1);
  });
});

describe('what counts as terminal', () => {
  it('retries a rate limit rather than marking the edit dead', async () => {
    // 429 was caught by "any 4xx but 401". A busy minute at the API turned an
    // edit that had not saved *yet* into one reported as never saving.
    await enqueue({ kind: 'updateNote', payload: 'a' });

    await flush(async () => {
      throw rateLimited();
    });

    const [queued] = await pending();
    expect(queued?.attempts).toBe(1);
    expect(isDead(queued!)).toBe(false);
  });

  it('retries a spend-capped edit: the cap means not today, not never', async () => {
    await enqueue({ kind: 'updateNote', payload: 'a' });

    await flush(async () => {
      throw spendCapped();
    });

    const [queued] = await pending();
    expect(queued?.attempts).toBe(1);
    expect(isDead(queued!)).toBe(false);
  });

  it.each([400, 403, 404, 409, 413, 422])('retires a %d on first sight', async (status) => {
    await enqueue({ kind: 'updateNote', payload: 'a' });

    await flush(async () => {
      throw new ApiError({ kind: 'http', status, title: 'Refused' });
    });

    const [queued] = await pending();
    expect(isDead(queued!)).toBe(true);
  });
});

describe('remove', () => {
  it('deletes a single mutation', async () => {
    const mutation = await enqueue({ kind: 'updateNote', payload: 'a' });
    await remove(mutation.id);
    expect(await count()).toBe(0);
  });
});
