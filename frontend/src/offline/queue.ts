/**
 * The offline mutation queue.
 *
 * Mutations made without a connection are written here and flushed when one
 * returns. Every entry carries the idempotency key it was created with, so a
 * flush that partly succeeded before dying replays rather than double-applying
 * — which is the only reason retrying a queued POST is safe at all.
 *
 * Flushed from the foreground only — on reconnect, on focus and on a slow
 * interval, by `useOfflineQueue`. There is no service-worker half: a worker
 * has no session to send with, and the one that used to exist only ever asked
 * an open tab to do what the tab was already doing.
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
  // Giving up silently would be worse than keeping it: the entry stays, with
  // its attempt count, so the UI can show it as needing attention.
  await db.put('mutations', {
    ...mutation,
    attempts,
    lastAttemptAt: Date.now(),
    lastError: describe(error),
  });
}

/**
 * True when this entry will never be attempted again.
 *
 * The queue keeps such entries rather than deleting them, which is what lets a
 * screen say "this edit did not go through, and here is why" instead of an
 * edit quietly ceasing to exist. `flush` skips them, so a dead entry never
 * blocks anything queued behind it.
 */
export function isDead(mutation: QueuedMutation): boolean {
  return mutation.attempts >= MAX_ATTEMPTS;
}

/**
 * Retires an entry that cannot succeed however often it is replayed.
 *
 * Recorded at the attempt ceiling rather than removed. Dropping it left the
 * screen that queued it saying "Saved on this device — will sync" forever: the
 * entry was gone, so nothing could distinguish "it synced" from "it was thrown
 * away", and the app went on promising a sync that would never come.
 */
async function markDead(mutation: QueuedMutation, error: unknown): Promise<void> {
  const db = await openChintanDB();
  await db.put('mutations', {
    ...mutation,
    attempts: MAX_ATTEMPTS,
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
 * The flush in progress, if one is. Module-level: there is one queue on the
 * device, so there is one pass over it at a time whoever asked.
 */
let flushing: Promise<FlushResult> | null = null;

/**
 * Flushes the queue in order.
 *
 * Stops at the first offline error rather than burning every entry's attempt
 * budget against a network that is not there. A permanent failure — a 4xx the
 * server will answer the same way every time — retires the entry: replaying a
 * request rejected on its merits will never succeed, and keeping it live
 * blocks everything behind it forever.
 *
 * Never two at once. A reconnect and a window focus can land in the same
 * moment, and TanStack's `refetchQueries` cancels the earlier *promise* but not
 * the earlier loop — so both read the same `pending()` list and both ran the
 * same PATCH under the same idempotency key. Whichever lost got a 4xx, called
 * `markDead`, and re-inserted, at the attempt ceiling, an edit the other had
 * just delivered and removed; the note then read "That edit did not save" over
 * text the server had. A second caller now awaits the pass already running.
 * It does not start another afterwards: anything enqueued during the pass is
 * picked up by the next reconnect, focus or interval tick, all of which are
 * seconds away.
 */
export function flush(run: MutationRunner): Promise<FlushResult> {
  if (flushing) return flushing;
  flushing = flushOnce(run).finally(() => {
    flushing = null;
  });
  return flushing;
}

async function flushOnce(run: MutationRunner): Promise<FlushResult> {
  const result: FlushResult = { applied: 0, failed: 0, stoppedOffline: false };

  for (const mutation of await pending()) {
    if (isDead(mutation)) {
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
      if (error instanceof ApiError && isTerminal(error)) {
        await markDead(mutation, error);
        result.failed += 1;
        continue;
      }
      await recordFailure(mutation, error);
      result.failed += 1;
    }
  }

  return result;
}

/**
 * The statuses no replay can get past: the request itself is wrong (400, 413,
 * 422), forbidden (403), aimed at something that is gone (404), or settled
 * (409).
 *
 * Named rather than "any 4xx but 401", which is what this was. That net also
 * caught 429 — a rate limit, which `ApiError.isRetryable` rightly says to try
 * again — and the spend-cap 429, which means "not today", not "never". Both
 * were marked dead on first sight, so an edit made during a busy minute was
 * reported as one that "did not save" when it merely had not saved yet.
 *
 * 401 is not here either: the client refreshes and the next flush succeeds.
 *
 * 409 **is**. A queued mutation carries the version the note was loaded at,
 * and that version never changes — so a 409 was once retried on every flush
 * until the attempt budget ran out, eight guaranteed conflicts for an outcome
 * settled at the first. It is the same shape as replaying an expired presigned
 * URL: repeating a request that cannot change. The edit is kept and surfaced so
 * the user can reconcile it; it is simply not sent again.
 */
const TERMINAL_STATUSES: ReadonlySet<number> = new Set([400, 403, 404, 409, 413, 422]);

function isTerminal(error: ApiError): boolean {
  return error.kind === 'http' && TERMINAL_STATUSES.has(error.status);
}
