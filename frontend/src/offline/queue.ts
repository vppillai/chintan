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
  /**
   * Stored at the attempt ceiling: kept for its screen to read back, never
   * sent by the flush. For an edit the server has already refused, where a
   * replay could only repeat the refusal — or, worse, be accepted later.
   */
  retired?: boolean;
}

export async function enqueue(input: EnqueueInput): Promise<QueuedMutation> {
  const db = await openChintanDB();
  const id = input.id ?? crypto.randomUUID();
  const existing = await db.get('mutations', id);
  if (existing) return existing;

  const mutation: QueuedMutation = {
    id,
    kind: input.kind,
    // Minted once, at enqueue time, and never regenerated. Regenerating per
    // flush attempt would make each retry a new logical write.
    idempotencyKey: input.idempotencyKey ?? crypto.randomUUID(),
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
    idempotencyKey: input.idempotencyKey ?? crypto.randomUUID(),
    payload: input.payload,
    createdAt: existing?.createdAt ?? Date.now(),
    // Reset: this is new content, and it should not inherit the failures of the
    // text it replaced.
    attempts: input.retired ? MAX_ATTEMPTS : 0,
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

/** Everything the device holds, retired entries included: what signing out would destroy. */
export async function count(): Promise<number> {
  const db = await openChintanDB();
  return db.count('mutations');
}

/**
 * What is still going to be sent: the offline banner's "Waiting to sync".
 * A retired entry is not waiting for anything — its note's screen says what
 * became of it — so counting it kept the banner up, online, for good.
 */
export async function waiting(): Promise<number> {
  const db = await openChintanDB();
  return (await db.getAll('mutations')).filter((mutation) => !isDead(mutation)).length;
}

export async function remove(id: string): Promise<void> {
  const db = await openChintanDB();
  await db.delete('mutations', id);
}

/**
 * Writes the outcome of sending `sent` — the entry as it was read before the
 * request — or deletes it on `null`, but only while the stored entry is still
 * that one. A note's offline keystrokes replace its entry under the same id
 * with a new idempotency key (`enqueueReplacing`), and one typed while the
 * older entry's PATCH was on the wire is newer text: deleting by id on
 * success threw it away, and writing the snapshot back on failure put the
 * older text over it. Read and write in one transaction, so nothing lands
 * between them.
 */
async function settle(
  sent: QueuedMutation,
  next: (stored: QueuedMutation) => QueuedMutation | null,
): Promise<void> {
  const db = await openChintanDB();
  const tx = db.transaction('mutations', 'readwrite');
  const stored = await tx.store.get(sent.id);
  if (stored?.idempotencyKey === sent.idempotencyKey) {
    const written = next(stored);
    if (written) await tx.store.put(written);
    else await tx.store.delete(sent.id);
  }
  await tx.done;
}

async function recordFailure(mutation: QueuedMutation, error: unknown): Promise<void> {
  // Giving up silently would be worse than keeping it: the entry stays, with
  // its attempt count, so the UI can show it as needing attention.
  await settle(mutation, (stored) => ({
    ...stored,
    attempts: stored.attempts + 1,
    lastAttemptAt: Date.now(),
    lastError: describe(error),
  }));
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
  await settle(mutation, (stored) => ({
    ...stored,
    attempts: MAX_ATTEMPTS,
    lastAttemptAt: Date.now(),
    lastError: describe(error),
  }));
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
    // Already retired: not a failure of this pass, and counting it as one
    // had the caller refetch every note list once a minute for good.
    if (isDead(mutation)) continue;
    try {
      await run(mutation);
      await settle(mutation, () => null);
      result.applied += 1;
    } catch (error) {
      // No attempt is counted: the request never reached anyone. A captive
      // portal that keeps `navigator.onLine` true fails every pass, and
      // counting those retired an edit in eight minutes of a hotel lobby.
      if (error instanceof ApiError && error.isOffline) {
        result.stoppedOffline = true;
        break;
      }
      // A recording is being filed into the note: the same PATCH is
      // accepted once it has landed, so this pass leaves the entry as it is.
      if (error instanceof ApiError && error.isAppendInProgress) continue;
      // The note is gone, and with it everything an edit to it could mean.
      // Kept dead, nothing could clear it: no screen opens a deleted note.
      if (error instanceof ApiError && error.isNotFound) {
        await settle(mutation, () => null);
        result.failed += 1;
        continue;
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
 * 422), forbidden (403), or settled (409). A 404 is past them too, but its
 * entry is removed rather than retired (`flushOnce`).
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
 * URL: repeating a request that cannot change. The edit is kept, and the note
 * editor reads it back and shows it as the conflict it is — Keep my edits or
 * Use the newer version (`useNoteEditor`); it is simply not sent again as is.
 */
const TERMINAL_STATUSES: ReadonlySet<number> = new Set([400, 403, 409, 413, 422]);

export function isTerminal(error: ApiError): boolean {
  return error.kind === 'http' && TERMINAL_STATUSES.has(error.status);
}
