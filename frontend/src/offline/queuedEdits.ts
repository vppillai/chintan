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

import type { NoteUpdateWire } from '@/api/schema.ts';

import { openChintanDB, type QueuedMutation } from './db.ts';
import { isDead } from './queue.ts';

/** What a queued `updateNote` carries: the note and the whole PATCH body. */
export interface QueuedEditPayload {
  noteId: string;
  body: NoteUpdateWire;
  /** What the edit was made over; see `ParkedBase`. Absent on an entry an older build wrote. */
  base?: ParkedBase;
  /**
   * Set on an entry parked already retired, which the flush never sends:
   * `false` for a conflict, `true` for an edit the server refused on its
   * merits or one left under "Couldn't save" — asked about, not re-sent,
   * when the note has not moved.
   */
  refused?: boolean;
}

/**
 * The note as the edit found it: the body it was typed over and the
 * recordings already filed into it. Kept with the entry because the editor
 * that reads it back has been loaded from the server's newer copy, and can
 * no longer tell what that copy added — and judged against the copy fetched
 * at read-back, not the one the save was refused over, since the note may
 * have moved again in between.
 */
interface ParkedBase {
  body: string;
  captures: string[];
}

/**
 * Narrows a stored mutation's payload, or returns null when it does not have
 * the shape this build writes — which happens if an older build wrote it. A
 * malformed entry is dropped by the flush rather than replayed with missing
 * fields, which would fail forever and block everything queued behind it.
 */
export function queuedEditPayload(mutation: QueuedMutation): QueuedEditPayload | null {
  const { payload } = mutation;
  if (typeof payload !== 'object' || payload === null) return null;
  const candidate = payload as Record<string, unknown>;
  if (typeof candidate['noteId'] !== 'string') return null;
  if (typeof candidate['body'] !== 'object' || candidate['body'] === null) return null;
  return candidate as unknown as QueuedEditPayload;
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
  /** The PATCH body held, for a direct save that will supersede the entry. */
  body: NoteUpdateWire | null;
  /** The entry's idempotency key: which entry that save may forget. */
  key: string;
  base?: ParkedBase | undefined;
  refused?: boolean | undefined;
}

/** `null` means the server has it — the entry is removed only on success. */
export async function queuedEditFor(noteId: string): Promise<QueuedEdit | null> {
  const db = await openChintanDB();
  const entry = await db.get('mutations', queuedEditId(noteId));
  if (!entry) return null;
  const dead = isDead(entry);
  return {
    pending: !dead,
    dead,
    error: entry.lastError,
    body: queuedEditPayload(entry)?.body ?? null,
    key: entry.idempotencyKey,
    base: queuedEditPayload(entry)?.base,
    refused: queuedEditPayload(entry)?.refused,
  };
}

/**
 * The PATCH body the device is holding for a note, or null. What the editor
 * folds a later offline keystroke into — `enqueueReplacing` keeps one entry
 * per note, so a replacement that carried only the field just typed would
 * drop a title or tag change queued before it — and what it shows as the
 * user's side once the server has refused the entry.
 */
export async function queuedEditBody(noteId: string): Promise<NoteUpdateWire | null> {
  const db = await openChintanDB();
  const entry = await db.get('mutations', queuedEditId(noteId));
  return entry ? (queuedEditPayload(entry)?.body ?? null) : null;
}

/**
 * Forgets a note's queued edit, once a direct save has superseded it — only
 * the entry under `key`, when one is named: an offline keystroke queued while
 * that save was on the wire is newer text the save never carried. True when
 * the entry is gone.
 */
export async function clearQueuedEdit(noteId: string, key?: string): Promise<boolean> {
  const db = await openChintanDB();
  const tx = db.transaction('mutations', 'readwrite');
  const stored = await tx.store.get(queuedEditId(noteId));
  const gone = !stored || key === undefined || stored.idempotencyKey === key;
  if (stored && gone) await tx.store.delete(queuedEditId(noteId));
  await tx.done;
  return gone;
}
