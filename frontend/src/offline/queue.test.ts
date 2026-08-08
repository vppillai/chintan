import { IDBFactory } from 'fake-indexeddb';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '@/api/problem.ts';

import { resetDatabaseHandle, type QueuedMutation } from './db.ts';
import { MAX_ATTEMPTS, count, enqueue, flush, pending, remove } from './queue.ts';

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

describe('enqueue', () => {
  it('stores a mutation with an idempotency key', async () => {
    const mutation = await enqueue({ kind: 'updateNote', payload: { id: 'n1' } });

    expect(mutation.idempotencyKey).toBeTruthy();
    expect(mutation.attempts).toBe(0);
    expect(await count()).toBe(1);
  });

  it('is itself idempotent, so a double-tap queues one mutation', async () => {
    await enqueue({ id: 'fixed', kind: 'archiveNote', payload: { id: 'n1' } });
    await enqueue({ id: 'fixed', kind: 'archiveNote', payload: { id: 'n1' } });

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
    const created = await enqueue({ kind: 'createCapture', payload: 'a' });

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

  it('drops a mutation the server rejected on its merits', async () => {
    // Replaying a 400 forever would block everything behind it.
    await enqueue({ kind: 'updateNote', payload: 'a' });
    await enqueue({ kind: 'updateNote', payload: 'b' });

    const result = await flush(async (mutation: QueuedMutation) => {
      if (mutation.payload === 'a') throw badRequest();
    });

    expect(result).toEqual({ applied: 1, failed: 1, stoppedOffline: false });
    expect(await count()).toBe(0);
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

describe('remove', () => {
  it('deletes a single mutation', async () => {
    const mutation = await enqueue({ kind: 'updateNote', payload: 'a' });
    await remove(mutation.id);
    expect(await count()).toBe(0);
  });
});
