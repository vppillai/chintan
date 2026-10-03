/**
 * The app's IndexedDB schema, in one place.
 *
 * Four stores, four jobs:
 *
 *   captureChunks  audio as it is produced, so a crash or a killed tab does not
 *                  lose the recording. Chunks accumulated in a JS array exist
 *                  only in the tab.
 *   captures       per-recording metadata, so the progress card can be rebuilt
 *                  from disk on a cold start.
 *   mutations      the offline mutation queue.
 *   notes          the note corpus, for reading and searching with no
 *                  connection. Without it, opening a note in a tunnel reports
 *                  that it "may have been archived or purged" — about a note
 *                  the user was looking at one screen earlier.
 */

import { openDB, type DBSchema, type IDBPDatabase } from 'idb';

import type { NoteDetailWire, NoteWire } from '@/api/schema.ts';

const DB_NAME = 'chintan';
/** 2 added `notes`. The upgrade is additive; no capture data is touched. */
const DB_VERSION = 2;

/**
 * What the queue can hold: a note PATCH, and nothing else.
 *
 * Six kinds were declared here and one was ever enqueued. Archive, restore,
 * retry and set-target are online actions with a button the user is looking
 * at — they report failure to the person who pressed them — and a capture
 * upload resumes from the audio on disk, not from a queue entry. Declaring the
 * others bought an exhaustively-typed runner for five branches nothing could
 * reach. The field stays a union so a second kind is a one-line addition, with
 * its enqueue call site.
 */
export type QueuedMutationKind = 'updateNote';

export interface QueuedMutation {
  id: string;
  kind: QueuedMutationKind;
  /** Sent as `Idempotency-Key`, so a flush that partly succeeded replays safely. */
  idempotencyKey: string;
  payload: unknown;
  createdAt: number;
  attempts: number;
  lastAttemptAt: number | null;
  lastError: string | null;
}

export interface StoredCapture {
  localId: string;
  serverCaptureId: string | null;
  noteId: string | null;
  contentType: string;
  durationMs: number;
  bytes: number;
  chunkCount: number;
  createdAt: number;
  /** Set once the server has confirmed the upload; until then, never pruned. */
  uploadedAt: number | null;
  peaks: number[] | null;
  /** The loudest frame, unscaled (`PeakCollector.max`); absent on records written before it was kept. */
  peak?: number | null;
}

export interface CaptureChunkRecord {
  /** `${localId}:${String(index).padStart(6,'0')}` — ordered by key. */
  id: string;
  localId: string;
  index: number;
  /**
   * The chunk as raw bytes rather than a Blob. Blob support in IndexedDB is
   * patchy on older WebKit, where a stored Blob can also be invalidated out
   * from under the record; an ArrayBuffer structured-clones everywhere.
   */
  data: ArrayBuffer;
  bytes: number;
}

/**
 * A note as last seen from the server.
 *
 * `detail` says whether `body` and `captures` are present: a list response
 * carries neither, and a cached list row must not be served to the note screen
 * as though it were a full note with an empty body. `archived` is lifted out of
 * the record so the archive and the library can be read back separately without
 * deserialising every note.
 */
export interface CachedNote {
  id: string;
  note: NoteWire | NoteDetailWire;
  detail: boolean;
  archived: boolean;
  updatedAt: string;
  cachedAt: number;
}

interface ChintanDB extends DBSchema {
  captureChunks: {
    key: string;
    value: CaptureChunkRecord;
    indexes: { byLocalId: string };
  };
  captures: {
    key: string;
    value: StoredCapture;
  };
  mutations: {
    key: string;
    value: QueuedMutation;
    indexes: { byCreatedAt: number };
  };
  notes: {
    key: string;
    value: CachedNote;
    indexes: { byUpdatedAt: string };
  };
}

export type ChintanDatabase = IDBPDatabase<ChintanDB>;

let dbPromise: Promise<ChintanDatabase> | null = null;
/** Tests only: between `settleDatabase` and the next `resetDatabaseHandle`, nothing may open the store. */
let sealed = false;

export function openChintanDB(): Promise<ChintanDatabase> {
  if (sealed) return Promise.reject(new Error('IndexedDB is closed between tests'));
  dbPromise ??= openDB<ChintanDB>(DB_NAME, DB_VERSION, {
    upgrade(db) {
      if (!db.objectStoreNames.contains('captureChunks')) {
        const chunks = db.createObjectStore('captureChunks', { keyPath: 'id' });
        chunks.createIndex('byLocalId', 'localId');
      }
      if (!db.objectStoreNames.contains('captures')) {
        db.createObjectStore('captures', { keyPath: 'localId' });
      }
      if (!db.objectStoreNames.contains('mutations')) {
        const mutations = db.createObjectStore('mutations', { keyPath: 'id' });
        mutations.createIndex('byCreatedAt', 'createdAt');
      }
      if (!db.objectStoreNames.contains('notes')) {
        const notes = db.createObjectStore('notes', { keyPath: 'id' });
        // Most-recent-first is the library's own order, so the offline list
        // does not have to sort the whole corpus in memory to match it.
        notes.createIndex('byUpdatedAt', 'updatedAt');
      }
    },
    // Safari closes IndexedDB connections behind a backgrounded page. With the
    // handle cached forever, every later write — chunks, capture records,
    // queued edits — rejected until a reload, and every caller swallows that.
    // Dropping the cached promise makes the next call reopen.
    terminated() {
      dbPromise = null;
    },
  });
  // An open that fails must not be cached as a permanent rejection either.
  dbPromise.catch(() => {
    dbPromise = null;
  });
  return dbPromise;
}

/** Tests reopen a fresh database between cases. */
export function resetDatabaseHandle(): void {
  dbPromise = null;
  sealed = false;
}

/**
 * Tests: ends the test's IndexedDB work inside the test. A readwrite
 * transaction over every store may start only once each earlier transaction
 * that overlaps it has finished, so its `done` is a barrier behind the cache
 * writes and queue reads the test left in flight; then the connection is
 * closed and the store sealed until the next test opens it afresh, so a
 * write that starts later still — a prefetch landing after the barrier —
 * fails at once in the test's own realm, where the cache writers swallow
 * it, instead of running after the file's realm is gone, where `idb` finds
 * no `IDBRequest` and vitest's vm pool reports an unhandled error.
 */
export async function settleDatabase(): Promise<void> {
  const db = await (dbPromise ?? Promise.resolve(null)).catch(() => null);
  sealed = true;
  if (!db) return;
  await db.transaction(['captureChunks', 'captures', 'mutations', 'notes'], 'readwrite').done;
  // Closing refuses every later `transaction()` on this handle; a transaction
  // queued behind the barrier still runs, and fake-indexeddb marks the
  // connection closed only once each one has finished — the exact moment
  // the test's IndexedDB work is over. (`_closed` is fake-indexeddb's, not
  // the spec's; this runs under it alone. ponytail: poll, since the spec
  // gives no event for a normal close.)
  db.close();
  const raw = db as unknown as { _closed?: boolean };
  while (raw._closed === false) await new Promise((resolve) => setTimeout(resolve, 0));
}

/**
 * Empties every store. Used by sign-out, and by nothing else.
 *
 * All four stores hold one person's data: audio they recorded, the index over
 * it, mutations queued against their notes, and the notes themselves. Leaving
 * any of it behind after a sign-out would at best show the next person a
 * previous user's notes, and at worst flush their queued edits under the new
 * session's token.
 */
export async function clearAllLocalData(): Promise<void> {
  const db = await openChintanDB();
  const tx = db.transaction(
    ['captureChunks', 'captures', 'mutations', 'notes'],
    'readwrite',
  );
  await Promise.all([
    tx.objectStore('captureChunks').clear(),
    tx.objectStore('captures').clear(),
    tx.objectStore('mutations').clear(),
    tx.objectStore('notes').clear(),
    tx.done,
  ]);
}
