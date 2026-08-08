import { IDBFactory } from 'fake-indexeddb';
import { beforeEach, describe, expect, it } from 'vitest';

import { resetDatabaseHandle } from '@/offline/db.ts';

import {
  appendChunk,
  assembleBlob,
  bufferedBytes,
  confirmUploaded,
  discardCapture,
  pruneCapture,
  readCaptureRecord,
  readChunks,
  saveCaptureRecord,
  unconfirmedCaptures,
} from './buffer.ts';

beforeEach(() => {
  // A fresh database per test: leaked state between storage tests produces
  // failures that look like ordering bugs.
  globalThis.indexedDB = new IDBFactory();
  resetDatabaseHandle();
});

function chunk(text: string): Blob {
  return new Blob([text], { type: 'audio/webm' });
}

/** jsdom's Blob has no `.text()`; FileReader is the portable read. */
function readBlob(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsText(blob);
  });
}

describe('chunks are written as they are produced', () => {
  it('round-trips a chunk', async () => {
    await appendChunk('cap-1', 0, chunk('abc'));

    const chunks = await readChunks('cap-1');
    expect(chunks).toHaveLength(1);
    expect(chunks[0]?.bytes).toBe(3);
  });

  it('reassembles chunks in recording order, not insertion order', async () => {
    // The failure this guards: an unpadded key sorts chunk 10 before chunk 2,
    // producing a file that decodes to noise.
    await appendChunk('cap-1', 2, chunk('c'));
    await appendChunk('cap-1', 10, chunk('k'));
    await appendChunk('cap-1', 0, chunk('a'));
    await appendChunk('cap-1', 1, chunk('b'));

    const blob = await assembleBlob('cap-1', 'audio/webm');
    expect(await readBlob(blob)).toBe('abck');
    expect(blob.type).toBe('audio/webm');
  });

  it('keeps two recordings separate', async () => {
    await appendChunk('cap-1', 0, chunk('one'));
    await appendChunk('cap-2', 0, chunk('twotwo'));

    expect(await bufferedBytes('cap-1')).toBe(3);
    expect(await bufferedBytes('cap-2')).toBe(6);
  });

  it('reports an empty buffer for an unknown recording', async () => {
    expect(await bufferedBytes('nope')).toBe(0);
    expect((await assembleBlob('nope', 'audio/webm')).size).toBe(0);
  });
});

describe('audio is pruned only after the server confirms', () => {
  it('keeps the audio until confirmation', async () => {
    await appendChunk('cap-1', 0, chunk('audio'));
    await saveCaptureRecord({
      localId: 'cap-1',
      serverCaptureId: 'srv-1',
      noteId: null,
      contentType: 'audio/webm',
      durationMs: 5_000,
      bytes: 5,
      chunkCount: 1,
      createdAt: Date.now(),
      uploadedAt: null,
      peaks: null,
    });

    expect(await bufferedBytes('cap-1')).toBe(5);
    expect(await unconfirmedCaptures()).toHaveLength(1);
  });

  it('drops the audio and marks the record once confirmed', async () => {
    await appendChunk('cap-1', 0, chunk('audio'));
    await saveCaptureRecord({
      localId: 'cap-1',
      serverCaptureId: 'srv-1',
      noteId: null,
      contentType: 'audio/webm',
      durationMs: 5_000,
      bytes: 5,
      chunkCount: 1,
      createdAt: Date.now(),
      uploadedAt: null,
      peaks: null,
    });

    await confirmUploaded('cap-1', 1_700_000_000_000);

    expect(await bufferedBytes('cap-1')).toBe(0);
    expect((await readCaptureRecord('cap-1'))?.uploadedAt).toBe(1_700_000_000_000);
    expect(await unconfirmedCaptures()).toHaveLength(0);
  });

  it('surfaces a recording stranded by a killed tab, oldest first', async () => {
    // This is what makes a crash mid-upload recoverable rather than terminal.
    for (const [localId, createdAt] of [
      ['cap-late', 2_000],
      ['cap-early', 1_000],
    ] as const) {
      await appendChunk(localId, 0, chunk('a'));
      await saveCaptureRecord({
        localId,
        serverCaptureId: null,
        noteId: null,
        contentType: 'audio/webm',
        durationMs: 1_000,
        bytes: 1,
        chunkCount: 1,
        createdAt,
        uploadedAt: null,
        peaks: null,
      });
    }

    expect((await unconfirmedCaptures()).map((record) => record.localId)).toEqual([
      'cap-early',
      'cap-late',
    ]);
  });

  it('prunes without needing a capture record', async () => {
    await appendChunk('cap-1', 0, chunk('x'));
    await pruneCapture('cap-1');
    expect(await bufferedBytes('cap-1')).toBe(0);
  });
});

describe('discard', () => {
  it('removes both the audio and the record', async () => {
    await appendChunk('cap-1', 0, chunk('x'));
    await saveCaptureRecord({
      localId: 'cap-1',
      serverCaptureId: null,
      noteId: null,
      contentType: 'audio/webm',
      durationMs: 1,
      bytes: 1,
      chunkCount: 1,
      createdAt: Date.now(),
      uploadedAt: null,
      peaks: null,
    });

    await discardCapture('cap-1');

    expect(await bufferedBytes('cap-1')).toBe(0);
    expect(await readCaptureRecord('cap-1')).toBeUndefined();
  });
});
