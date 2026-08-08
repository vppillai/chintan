/**
 * The app's IndexedDB schema, in one place.
 *
 * Four stores, four jobs:
 *
 *   captureChunks  audio as it is produced, so a crash or a killed tab does not
 *                  lose the recording. v1 accumulated chunks in a JS array,
 *                  which meant the recording existed only in the tab.
 *   captures       per-recording metadata, so the progress card can be rebuilt
 *                  from disk on a cold start.
 *   mutations      the offline mutation queue.
 *   notes          the note corpus, for reading and searching with no
 *                  connection. Spec §5.5 promised this from the start; without
 *                  it, opening a note in a tunnel reported that it "may have
 *                  been archived or purged" — about a note the user had been
 *                  looking at one screen earlier.
 */

import { openDB, type DBSchema, type IDBPDatabase } from 'idb';

import type { NoteDetailWire, NoteWire } from '@/api/schema.ts';

export const DB_NAME = 'chintan';
/** 2 added `notes`. The upgrade is additive; no capture data is touched. */
export const DB_VERSION = 2;

export type QueuedMutationKind =
  | 'createCapture'
  | 'updateNote'
  | 'archiveNote'
  | 'restoreNote'
  | 'setCaptureTarget'
  | 'retryCapture';

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

export function openChintanDB(): Promise<ChintanDatabase> {
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
  });
  return dbPromise;
}

/** Tests reopen a fresh database between cases. */
export function resetDatabaseHandle(): void {
  dbPromise = null;
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
