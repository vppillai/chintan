/**
 * Getting a finished recording onto the server.
 *
 * The ordering is the contract:
 *
 *   1. `POST /v1/captures` keyed by the recording's local id, so a retry after
 *      a flaky response replays and returns the *same* upload URL rather than
 *      creating a second capture.
 *   2. PUT the audio to the presigned URL.
 *   3. PUT the client-computed peaks, best effort.
 *   4. Only then prune the local audio.
 *
 * Step 4 last is the rule the product cannot get wrong. Anything that fails
 * before it leaves the recording on the device, which is what makes the
 * failure recoverable instead of terminal.
 */

import { ApiError } from '@/api/problem.ts';
import type { ChintanApi } from '@/api/endpoints.ts';
import { putPresigned } from '@/api/endpoints.ts';
import type { CaptureContentType } from '@/api/schema.ts';

import { assembleBlob, confirmUploaded, saveCaptureRecord } from './buffer.ts';
import { peaksDocument } from './peaks.ts';
import type { CaptureEvent } from './machine.ts';

export interface UploadRequest {
  localId: string;
  contentType: CaptureContentType;
  durationMs: number;
  noteId: string | null;
  peaks: number[];
}

export interface UploadDeps {
  assemble: (localId: string, contentType: string) => Promise<Blob>;
  put: typeof putPresigned;
  confirm: (localId: string) => Promise<void>;
  saveRecord: typeof saveCaptureRecord;
}

export const defaultUploadDeps: UploadDeps = {
  assemble: assembleBlob,
  put: putPresigned,
  confirm: confirmUploaded,
  saveRecord: saveCaptureRecord,
};

/**
 * Runs one upload, reporting progress as capture events.
 *
 * Progress is coarse — three steps, not a byte counter — because `fetch` gives
 * no upload progress without XHR, and a fake determinate bar is worse than an
 * honest coarse one. v1 set a determinate bar to 100% and pulsed its opacity,
 * which reads as "stuck".
 */
export async function uploadCapture(
  api: ChintanApi,
  request: UploadRequest,
  emit: (event: CaptureEvent) => void,
  deps: UploadDeps = defaultUploadDeps,
  signal?: AbortSignal,
): Promise<void> {
  emit({ type: 'uploadStart' });

  let blob: Blob;
  try {
    blob = await deps.assemble(request.localId, request.contentType);
  } catch {
    emit({
      type: 'uploadFailed',
      message: 'Could not read the recording from this device.',
      recoverable: false,
    });
    return;
  }

  if (blob.size === 0) {
    emit({
      type: 'uploadFailed',
      message: 'The recording is empty.',
      recoverable: false,
    });
    return;
  }

  emit({ type: 'uploadProgress', progress: 0.1 });

  let created;
  try {
    created = await api.createCapture(
      {
        content_type: request.contentType,
        note_id: request.noteId,
        duration_ms: Math.round(request.durationMs),
        size_bytes: blob.size,
      },
      // The local id IS the idempotency key. A resumed upload after a crash
      // replays the original create rather than making a second capture.
      request.localId,
    );
  } catch (error) {
    if (error instanceof ApiError && error.isSpendCapped) {
      emit({ type: 'spendCapped', message: error.userMessage });
      return;
    }
    emit({
      type: 'uploadFailed',
      message: error instanceof ApiError ? error.userMessage : 'Could not reach the server.',
      recoverable: true,
    });
    return;
  }

  emit({ type: 'captureCreated', serverCaptureId: created.capture.id });
  emit({ type: 'uploadProgress', progress: 0.3 });

  // Recorded before the bytes move, so a crash mid-upload still leaves a row
  // pointing at the server capture rather than an orphan on disk.
  await deps
    .saveRecord({
      localId: request.localId,
      serverCaptureId: created.capture.id,
      noteId: request.noteId,
      contentType: request.contentType,
      durationMs: request.durationMs,
      bytes: blob.size,
      chunkCount: 0,
      createdAt: Date.now(),
      uploadedAt: null,
      peaks: request.peaks,
    })
    .catch(() => {
      /* A failed bookkeeping write must not abort a good upload. */
    });

  try {
    await deps.put(created.upload, blob, {
      contentType: request.contentType,
      ...(signal ? { signal } : {}),
      onAttempt: (attempt) => {
        emit({ type: 'uploadProgress', progress: attempt === 0 ? 0.4 : 0.35 });
      },
    });
  } catch (error) {
    if ((error as Error)?.name === 'AbortError') return;
    emit({
      type: 'uploadFailed',
      message: 'The upload did not finish. Your recording is safe on this device.',
      recoverable: true,
    });
    return;
  }

  emit({ type: 'uploadProgress', progress: 0.85 });

  // Peaks are optional by contract: a capture without them renders a plain
  // player. Failing the whole upload over a waveform would be absurd.
  if (created.peaks_upload && request.peaks.length > 0) {
    try {
      await deps.put(
        created.peaks_upload,
        JSON.stringify(peaksDocument(request.peaks, request.durationMs)),
        { contentType: 'application/json', maxRetries: 1, ...(signal ? { signal } : {}) },
      );
    } catch {
      /* No waveform on the note screen. Nothing else is affected. */
    }
  }

  // Last, and only now: the server has the audio.
  await deps.confirm(request.localId);
  emit({ type: 'uploadDone' });
}
