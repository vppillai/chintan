/**
 * The app's IndexedDB schema, in one place.
 *
 * Three stores, three jobs:
 *
 *   captureChunks  audio as it is produced, so a crash or a killed tab does not
 *                  lose the recording. v1 accumulated chunks in a JS array,
 *                  which meant the recording existed only in the tab.
 *   captures       per-recording metadata, so the progress card can be rebuilt
 *                  from disk on a cold start.
 *   mutations      the offline mutation queue.
 */

import { openDB, type DBSchema, type IDBPDatabase } from 'idb';

export const DB_NAME = 'chintan';
export const DB_VERSION = 1;

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
    },
  });
  return dbPromise;
}

/** Tests reopen a fresh database between cases. */
export function resetDatabaseHandle(): void {
  dbPromise = null;
}
