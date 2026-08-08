/**
 * The offline mutation queue.
 *
 * Mutations made without a connection are written here and flushed when one
 * returns. Every entry carries the idempotency key it was created with, so a
 * flush that partly succeeded before dying replays rather than double-applying
 * — which is the only reason retrying a queued POST is safe at all.
 *
 * v1 shipped the shape of this and none of the substance: the service worker
 * registered a `sync` listener whose handler was an empty stub, and the tag it
 * listened for was never registered by anything.
 */

import { ApiError } from '@/api/problem.ts';
import {
  openChintanDB,
  type QueuedMutation,
  type QueuedMutationKind,
} from './db.ts';

export const MAX_ATTEMPTS = 8;

export interface EnqueueInput {
  kind: QueuedMutationKind;
  payload: unknown;
  /** Supply to make enqueueing itself idempotent across a double-tap. */
  id?: string;
  idempotencyKey?: string;
}

function newId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `q-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

export async function enqueue(input: EnqueueInput): Promise<QueuedMutation> {
  const db = await openChintanDB();
  const id = input.id ?? newId();
  const existing = await db.get('mutations', id);
  if (existing) return existing;

  const mutation: QueuedMutation = {
    id,
    kind: input.kind,
    // Minted once, at enqueue time, and never regenerated. Regenerating per
    // flush attempt would make each retry a new logical write.
    idempotencyKey: input.idempotencyKey ?? newId(),
    payload: input.payload,
    createdAt: Date.now(),
    attempts: 0,
    lastAttemptAt: null,
    lastError: null,
  };
  await db.put('mutations', mutation);
  return mutation;
}

/**
 * Queues a mutation under a caller-chosen id, replacing whatever was there.
 *
 * For a write that carries the whole of its subject rather than a delta. A note
 * PATCH sends title, body, aliases and tags together, so three offline edits to
 * one note are not three writes — they are one write, made three times, and
 * queueing all three would replay two supersedes against the server and burn two
 * attempt budgets for a result nobody can observe.
 *
 * `createdAt` survives the replacement, so an edit made before some other
 * queued mutation still flushes before it: the user's order of intent is not
 * rewritten by them going back to fix a typo.
 *
 * The idempotency key does NOT survive it. The key is a promise that a replay
 * is the *same* logical write; a different payload under the same key would
 * either be rejected or, worse, replay the earlier response and report success
 * for text the server never received.
 */
export async function enqueueReplacing(
  input: EnqueueInput & { id: string },
): Promise<QueuedMutation> {
  const db = await openChintanDB();
  const existing = await db.get('mutations', input.id);

  const mutation: QueuedMutation = {
    id: input.id,
    kind: input.kind,
    idempotencyKey: input.idempotencyKey ?? newId(),
    payload: input.payload,
    createdAt: existing?.createdAt ?? Date.now(),
    // Reset: this is new content, and it should not inherit the failures of the
    // text it replaced.
    attempts: 0,
    lastAttemptAt: null,
    lastError: null,
  };
  await db.put('mutations', mutation);
  return mutation;
}

/** Oldest first: order of intent is preserved across a flush. */
export async function pending(): Promise<QueuedMutation[]> {
  const db = await openChintanDB();
  return db.getAllFromIndex('mutations', 'byCreatedAt');
}

export async function count(): Promise<number> {
  const db = await openChintanDB();
  return db.count('mutations');
}

export async function remove(id: string): Promise<void> {
  const db = await openChintanDB();
  await db.delete('mutations', id);
}

async function recordFailure(mutation: QueuedMutation, error: unknown): Promise<void> {
  const db = await openChintanDB();
  const attempts = mutation.attempts + 1;
  if (attempts >= MAX_ATTEMPTS) {
    // Giving up silently would be worse than keeping it: the entry stays, with
    // its attempt count, so the UI can show it as needing attention.
    await db.put('mutations', {
      ...mutation,
      attempts,
      lastAttemptAt: Date.now(),
      lastError: describe(error),
    });
    return;
  }
  await db.put('mutations', {
    ...mutation,
    attempts,
    lastAttemptAt: Date.now(),
    lastError: describe(error),
  });
}

function describe(error: unknown): string {
  if (error instanceof ApiError) return error.userMessage;
  if (error instanceof Error) return error.message;
  return 'Unknown error';
}

export type MutationRunner = (mutation: QueuedMutation) => Promise<void>;

export interface FlushResult {
  applied: number;
  failed: number;
  /** True when the flush stopped early because the device went offline. */
  stoppedOffline: boolean;
}

/**
 * Flushes the queue in order.
 *
 * Stops at the first offline error rather than burning every entry's attempt
 * budget against a network that is not there. A permanent failure (a 4xx that
 * is not 401) drops the entry: replaying a request the server has already
 * rejected on its merits will never succeed, and keeping it blocks everything
 * behind it forever.
 */
export async function flush(run: MutationRunner): Promise<FlushResult> {
  const result: FlushResult = { applied: 0, failed: 0, stoppedOffline: false };

  for (const mutation of await pending()) {
    if (mutation.attempts >= MAX_ATTEMPTS) {
      result.failed += 1;
      continue;
    }
    try {
      await run(mutation);
      await remove(mutation.id);
      result.applied += 1;
    } catch (error) {
      if (error instanceof ApiError && error.isOffline) {
        result.stoppedOffline = true;
        await recordFailure(mutation, error);
        break;
      }
      if (error instanceof ApiError && isPermanent(error)) {
        await remove(mutation.id);
        result.failed += 1;
        continue;
      }
      await recordFailure(mutation, error);
      result.failed += 1;
    }
  }

  return result;
}

function isPermanent(error: ApiError): boolean {
  if (error.kind !== 'http') return false;
  // 401 is not permanent — the client refreshes and the next flush succeeds.
  // 409 is not permanent either; it means reconcile, which the caller does.
  if (error.status === 401 || error.status === 409) return false;
  return error.status >= 400 && error.status < 500;
}
