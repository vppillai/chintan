/**
 * What the device is still holding for one note.
 *
 * The note editor used to *mirror* this: `saveQueued` set the model to
 * `queued` and nothing ever transitioned out of it, because no event meant
 * "the queued mutation reached the server" and the flush notified the editor of
 * nothing. So the screen said "Saved on this device — will sync" until a
 * reload, whether the flush had succeeded, failed permanently, or lost a
 * conflict — a user could not tell "not yet" from "never".
 *
 * The fix is to stop mirroring and start deriving. The queue is the truth about
 * what is still owed to the server, so the editor reads it. That also answers
 * "what if the note is not open?" for free: nothing has to be delivered to a
 * screen that is not mounted, and coming back to the note later reads the same
 * fact from the same place.
 */

import { openChintanDB } from './db.ts';
import { isDead } from './queue.ts';

/** Query key prefix. Deliberately not under `['offline','queue']`, which the
 * flush query owns — invalidating that from inside its own queryFn would loop. */
export const QUEUED_EDIT_KEY = 'queued-edit';

export function queuedEditKey(noteId: string) {
  return [QUEUED_EDIT_KEY, noteId] as const;
}

/**
 * One queued edit per note, by construction.
 *
 * A note PATCH carries the whole note, so three offline edits are one write
 * made three times; `enqueueReplacing` keeps them as a single entry under this
 * id. That is also what makes "is there anything outstanding for this note?" a
 * single point read rather than a scan.
 */
export function queuedEditId(noteId: string): string {
  return `updateNote:${noteId}`;
}

export interface QueuedEdit {
  /** Still owed to the server, and still going to be attempted. */
  pending: boolean;
  /** Attempted as far as it ever will be. `error` says why it stopped. */
  dead: boolean;
  error: string | null;
}

/** `null` means the server has it — the entry is removed only on success. */
export async function queuedEditFor(noteId: string): Promise<QueuedEdit | null> {
  const db = await openChintanDB();
  const entry = await db.get('mutations', queuedEditId(noteId));
  if (!entry) return null;
  const dead = isDead(entry);
  return { pending: !dead, dead, error: entry.lastError };
}

/** Forgets a note's queued edit, once a direct save has superseded it. */
export async function clearQueuedEdit(noteId: string): Promise<void> {
  const db = await openChintanDB();
  await db.delete('mutations', queuedEditId(noteId));
}
