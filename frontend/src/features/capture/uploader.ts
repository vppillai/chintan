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
import { presignExpired, putPresigned } from '@/api/endpoints.ts';
import type { CaptureContentType, CaptureCreatedWire } from '@/api/schema.ts';

import { assembleBlob, confirmUploaded, saveCaptureRecord } from './buffer.ts';
import { peaksDocument } from './peaks.ts';
import type { CaptureEvent } from './machine.ts';

/**
 * One sentence for every "it did not reach the server" outcome. The user does
 * not care whether the create or the PUT failed; they care where the recording
 * is.
 */
export const OFFLINE_MESSAGE =
  'The upload did not finish. Your recording is safe on this device.';

/**
 * The upload credential died before the bytes could use it.
 *
 * Named separately because it is not a network problem and telling the user it
 * is sends them to tap Send against a URL that can never work. Three attempts
 * in production went out with an identical signature, an hour after it expired,
 * and said nothing but "the upload did not finish".
 */
export const EXPIRED_MESSAGE =
  'That could not be sent: the upload link had expired. The recording is still on this device — try again.';

export interface UploadRequest {
  localId: string;
  contentType: CaptureContentType;
  durationMs: number;
  noteId: string | null;
  peaks: number[];
  /**
   * The capture the server already minted for these bytes.
   *
   * Set only on a resume. Its presence is what tells this function that the
   * create it is about to make will be answered from the server's idempotency
   * record rather than done afresh — see `freshUpload` below.
   */
  serverCaptureId?: string | null;
}

/**
 * A key that is *not* the one the server has already pinned a response to.
 *
 * Minted once per call so the HTTP client's own retries still replay within
 * this attempt; only a new attempt gets a new key.
 */
function resumeKey(localId: string): string {
  return `${localId}-resume-${Date.now().toString(36)}`;
}

/**
 * Whether the server already has these bytes.
 *
 * A capture sits at `uploaded` from the moment it is created until the object
 * event fires, so any other status means the audio arrived and the pipeline
 * moved on — and sending again would append the same dictation to the note
 * twice. This is only ever asked on a resume whose credential is at least
 * thirty minutes old, so there is no window where the object has landed and
 * the status has not caught up.
 *
 * An error here is deliberately not fatal: if the server cannot be reached the
 * upload is not going to succeed either, and the ordinary path reports that
 * better than this does.
 */
async function alreadyLanded(api: ChintanApi, captureId: string): Promise<boolean> {
  try {
    const capture = await api.getCapture(captureId);
    return capture.status !== 'uploaded';
  } catch {
    return false;
  }
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

  /*
   * A resume, and the bytes may already be there.
   *
   * Only the local confirmation is lost when a tab dies between the PUT
   * succeeding and the prune; the audio is on the server and the note already
   * has the dictation. Uploading it again would append it a second time.
   */
  if (request.serverCaptureId && (await alreadyLanded(api, request.serverCaptureId))) {
    await deps.confirm(request.localId);
    emit({ type: 'uploadDone' });
    return;
  }

  emit({ type: 'uploadProgress', progress: 0.1 });

  const body = {
    content_type: request.contentType,
    note_id: request.noteId,
    duration_ms: Math.round(request.durationMs),
    size_bytes: blob.size,
  };

  let created: CaptureCreatedWire;
  try {
    // The local id IS the idempotency key. A resumed upload after a crash
    // replays the original create rather than making a second capture.
    created = await api.createCapture(body, request.localId);

    /*
     * …and that replay is verbatim, presigned URL included.
     *
     * `handler/idempotency.go` stores the whole response body against the key
     * and writes it back on any repeat, so the same key that stops a resume
     * creating a second capture also pins it to a thirty-minute credential
     * minted whenever the recording was first attempted. In production that
     * meant three resume attempts an hour later, all carrying
     * `X-Amz-Date=20260808T204149Z` and the same signature, all 403, with the
     * object never landing and no number of taps able to change it.
     *
     * So: if the credential we were handed is already dead, ask again under a
     * key the server has nothing stored for. That costs a second capture row
     * with no object behind it — which the purge sweeper reaps — and it is the
     * only way to get live credentials without the server re-minting on
     * replay, which is where this belongs and where it is not yet done. When
     * that lands, this branch simply stops being taken.
     */
    if (request.serverCaptureId && presignExpired(created.upload)) {
      created = await api.createCapture(body, resumeKey(request.localId));
    }
  } catch (error) {
    if (error instanceof ApiError && error.isSpendCapped) {
      emit({ type: 'spendCapped', message: error.userMessage });
      return;
    }
    // The same reassurance the PUT path gives, because from the user's side
    // these are one event: the recording did not reach the server. Deferring to
    // the generic network message here would tell them something subtly
    // different depending on which call happened to fail first.
    emit({
      type: 'uploadFailed',
      message: OFFLINE_MESSAGE,
      recoverable: true,
    });
    return;
  }

  /*
   * Expiry is knowable before the request, so it is not spent finding out.
   * Reaching here means even a fresh key produced a dead credential — a clock
   * far out of step, or a server minting expired presigns — and a PUT would be
   * a guaranteed 403 dressed up as a network failure.
   */
  if (presignExpired(created.upload)) {
    emit({ type: 'uploadFailed', message: EXPIRED_MESSAGE, recoverable: true });
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
    // S3 refused the signature rather than the connection refusing the
    // request. Saying "the upload did not finish" here is what made an expired
    // credential look like a flaky network for three attempts running.
    if ((error as Error)?.name === 'PresignRejected') {
      emit({ type: 'uploadFailed', message: EXPIRED_MESSAGE, recoverable: true });
      return;
    }
    emit({ type: 'uploadFailed', message: OFFLINE_MESSAGE, recoverable: true });
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
