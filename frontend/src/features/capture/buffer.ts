/**
 * The audio buffer on disk.
 *
 * Chunks are written to IndexedDB as `ondataavailable` produces them and are
 * pruned only after the server confirms the upload. That ordering is the whole
 * point: v1 accumulated chunks in a JavaScript array, so a backgrounded tab
 * reclaimed by the OS — routine on a phone during a twenty-minute recording —
 * took the recording with it, and there was nothing on disk to recover.
 */

import { openChintanDB, type CaptureChunkRecord, type StoredCapture } from '@/offline/db.ts';

function chunkKey(localId: string, index: number): string {
  // Zero-padded so IndexedDB's lexicographic key order is chunk order. An
  // unpadded index reassembles chunk 10 before chunk 2, which produces a file
  // that decodes to noise.
  return `${localId}:${String(index).padStart(6, '0')}`;
}

/**
 * `Blob.arrayBuffer()` is comparatively recent and absent on the older WebKit
 * builds this app has to run on, so FileReader is the fallback rather than the
 * exception.
 */
export function blobToArrayBuffer(blob: Blob): Promise<ArrayBuffer> {
  if (typeof blob.arrayBuffer === 'function') return blob.arrayBuffer();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(reader.error ?? new Error('Could not read chunk'));
    reader.readAsArrayBuffer(blob);
  });
}

export async function appendChunk(
  localId: string,
  index: number,
  blob: Blob,
): Promise<void> {
  const db = await openChintanDB();
  const data = await blobToArrayBuffer(blob);
  await db.put('captureChunks', {
    id: chunkKey(localId, index),
    localId,
    index,
    data,
    bytes: blob.size,
  });
}

export async function readChunks(localId: string): Promise<CaptureChunkRecord[]> {
  const db = await openChintanDB();
  const records = await db.getAllFromIndex('captureChunks', 'byLocalId', localId);
  return records.sort((a, b) => a.index - b.index);
}

/** Reassembles the recording. Order comes from the index, not insertion order. */
export async function assembleBlob(localId: string, contentType: string): Promise<Blob> {
  const chunks = await readChunks(localId);
  return new Blob(
    chunks.map((chunk) => chunk.data),
    { type: contentType },
  );
}

export async function bufferedBytes(localId: string): Promise<number> {
  const chunks = await readChunks(localId);
  return chunks.reduce((total, chunk) => total + chunk.bytes, 0);
}

export async function saveCaptureRecord(record: StoredCapture): Promise<void> {
  const db = await openChintanDB();
  await db.put('captures', record);
}

export async function readCaptureRecord(localId: string): Promise<StoredCapture | undefined> {
  const db = await openChintanDB();
  return db.get('captures', localId);
}

export async function listCaptureRecords(): Promise<StoredCapture[]> {
  const db = await openChintanDB();
  return db.getAll('captures');
}

/**
 * Recordings still on this device that the server has not confirmed.
 *
 * Read on startup: a tab killed mid-upload leaves audio here, and this is what
 * lets the app offer to resend it rather than losing it.
 */
export async function unconfirmedCaptures(): Promise<StoredCapture[]> {
  const records = await listCaptureRecords();
  return records
    .filter((record) => record.uploadedAt === null)
    .sort((a, b) => a.createdAt - b.createdAt);
}

/**
 * Prunes a capture's audio. Called ONLY after the server has confirmed the
 * upload — the one ordering constraint the product cannot get wrong.
 */
export async function pruneCapture(localId: string): Promise<void> {
  const db = await openChintanDB();
  const chunks = await db.getAllKeysFromIndex('captureChunks', 'byLocalId', localId);
  const tx = db.transaction('captureChunks', 'readwrite');
  await Promise.all([...chunks.map((key) => tx.store.delete(key)), tx.done]);
}

/** Marks the upload confirmed and drops the audio in one step. */
export async function confirmUploaded(localId: string, at: number = Date.now()): Promise<void> {
  const record = await readCaptureRecord(localId);
  if (record) {
    await saveCaptureRecord({ ...record, uploadedAt: at });
  }
  await pruneCapture(localId);
}

/** Discards a recording the user abandoned. */
export async function discardCapture(localId: string): Promise<void> {
  const db = await openChintanDB();
  await pruneCapture(localId);
  await db.delete('captures', localId);
}
