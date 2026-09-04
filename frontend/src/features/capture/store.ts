/**
 * Capture and UI state (Zustand), deliberately outside React's tree.
 *
 * A recording must survive navigation between screens. Held in component state
 * or in variables belonging to a page script, it is lost the moment the user
 * navigates away mid-capture.
 */

import { create } from 'zustand';

import type { ChintanApi } from '@/api/endpoints.ts';

import { assembleBlob, discardCapture, saveCaptureRecord } from './buffer.ts';
import { errorFeedback, startFeedback, stopFeedback } from './feedback.ts';
import {
  INITIAL_CAPTURE,
  captureReducer,
  type CaptureEvent,
  type CaptureModel,
} from './machine.ts';
import { RecorderController, defaultRecorderDeps, type RecorderDeps } from './recorder.ts';
import { uploadCapture, type UploadDeps } from './uploader.ts';

function newLocalId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return `cap-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

export interface CaptureStore {
  model: CaptureModel;
  /** Live amplitudes for the canvas. Read outside React's render cycle. */
  amplitudes: (count: number) => number[];
  dispatch: (event: CaptureEvent) => void;
  start: (noteId?: string | null) => Promise<void>;
  /** Changes where the recording will be filed; `null` means a new note. */
  setTarget: (noteId: string | null) => void;
  pause: () => void;
  resume: () => void;
  stop: () => Promise<void>;
  /** Abandon: stops the recorder and deletes the buffered audio. */
  discard: () => Promise<void>;
  /** Discard, then start again into the same target. The review screen's "Re-record". */
  rerecord: () => Promise<void>;
  send: (api: ChintanApi) => Promise<void>;
  reset: () => void;
  /**
   * The recording as one playable Blob, reassembled from the chunks on disk.
   * For the review player; the uploader assembles its own copy when sending.
   */
  clip: () => Promise<Blob>;
  /** The finished amplitude envelope, for drawing the recording before it is sent. */
  envelope: () => number[];
  /** Test seam. */
  __configure: (deps: { recorder?: RecorderDeps; upload?: UploadDeps }) => void;
}

export const useCaptureStore = create<CaptureStore>((set, get) => {
  let controller: RecorderController | null = null;
  let recorderDeps: RecorderDeps = defaultRecorderDeps;
  let uploadDeps: UploadDeps | undefined;

  const dispatch = (event: CaptureEvent): void => {
    const before = get().model;
    const after = captureReducer(before, event);
    if (after === before) return;
    set({ model: after });

    // A cap does not just flag itself, it stops the recording. The machine
    // moves to `stopping`; something has to actually tell the recorder.
    if (after.state === 'stopping' && before.state !== 'stopping' && event.type === 'tick') {
      void controller?.stop();
    }
    if (after.state === 'stopping' && before.state !== 'stopping' && event.type === 'data') {
      void controller?.stop();
    }
    if (after.state === 'failed' && before.state !== 'failed') {
      errorFeedback();
    }

    /*
     * The capture record is written the moment the microphone opens — before a
     * single chunk exists — and refreshed when the recording is finished.
     *
     * The chunks stream to IndexedDB from the first `ondataavailable`, but a
     * chunk group nothing indexes is unreachable: `unconfirmedCaptures()` reads
     * the `captures` store only. Writing the record on `review` (which is what
     * this did before) covered a tab killed mid-upload, and left the bigger
     * hole open: a tab reloaded, or a backgrounded PWA jettisoned by iOS,
     * *while recording* — a twenty-minute dictation with nothing to find it by.
     * The audio was durable and unreachable, which is indistinguishable from
     * lost. So the row goes in first, with what is known at that point, and
     * `review` fills in the duration, size and envelope.
     */
    const started = after.state === 'recording' && before.state === 'requesting';
    const reviewed = after.state === 'review' && before.state !== 'review';
    // A changed target is rewritten too, or a recording resumed from disk
    // after a reload would be sent to the note the user had moved it away from.
    const retargeted = event.type === 'target' && after.state !== 'requesting';
    if ((started || reviewed || retargeted) && after.localId) {
      void saveCaptureRecord({
        localId: after.localId,
        serverCaptureId: null,
        noteId: after.noteId,
        contentType: controller?.current()?.encoder.contentType ?? 'audio/webm',
        durationMs: after.elapsedMs,
        bytes: after.bytes,
        chunkCount: after.chunks,
        createdAt: Date.now(),
        uploadedAt: null,
        peaks: started ? null : (controller?.envelope() ?? null),
      }).catch(() => {
        /* Storage denied. The chunks are still on disk. */
      });
    }
  };

  const ensureController = (): RecorderController => {
    controller ??= new RecorderController(dispatch, recorderDeps);
    return controller;
  };

  return {
    model: INITIAL_CAPTURE,

    amplitudes: (count) => controller?.recentAmplitudes(count) ?? [],

    dispatch,

    async start(noteId = null) {
      const localId = newLocalId();
      dispatch({ type: 'request', localId, noteId });
      const active = ensureController();
      await active.start(localId);
      if (get().model.state === 'recording') {
        startFeedback(active.current()?.audioContext ?? null);
      }
    },

    setTarget(noteId) {
      dispatch({ type: 'target', noteId });
    },

    pause() {
      controller?.pause();
    },

    resume() {
      controller?.resume();
    },

    async stop() {
      const active = controller;
      stopFeedback(active?.current()?.audioContext ?? null);
      await active?.stop();
    },

    async discard() {
      const { localId } = get().model;
      controller?.cancel();
      // A chunk handed over just before Cancel may still be on its way to
      // disk; the prune has to run after it lands or it would survive it.
      await controller?.flushed();
      if (localId) {
        await discardCapture(localId).catch(() => {
          /* Nothing on disk. */
        });
      }
      dispatch({ type: 'discard' });
    },

    async rerecord() {
      // Read before the discard resets it: the target is the one thing about
      // the abandoned take worth keeping.
      const { noteId } = get().model;
      await get().discard();
      await get().start(noteId);
    },

    async clip() {
      const { localId } = get().model;
      const contentType = controller?.current()?.encoder.contentType ?? 'audio/webm';
      // The last chunk lands on disk a beat after `finalised`; read after it.
      await controller?.flushed();
      return assembleBlob(localId, contentType);
    },

    envelope() {
      return controller?.envelope() ?? [];
    },

    async send(api) {
      const { model } = get();
      const encoder = controller?.current()?.encoder;
      // A Send the instant Stop finishes must not assemble a buffer that is
      // still being written.
      await controller?.flushed();
      await uploadCapture(
        api,
        {
          localId: model.localId,
          contentType: encoder?.contentType ?? 'audio/webm',
          durationMs: model.elapsedMs,
          noteId: model.noteId,
          peaks: controller?.envelope() ?? [],
        },
        dispatch,
        uploadDeps,
      );
    },

    reset() {
      dispatch({ type: 'reset' });
    },

    __configure(deps) {
      if (deps.recorder) {
        recorderDeps = deps.recorder;
        controller = new RecorderController(dispatch, recorderDeps);
      }
      if (deps.upload) uploadDeps = deps.upload;
    },
  };
});

/** Selector for the shell's recording indicator: what the machine is doing. */
export function selectCaptureModel(state: CaptureStore): CaptureModel {
  return state.model;
}
