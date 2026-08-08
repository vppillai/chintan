/**
 * Capture and UI state (Zustand), deliberately outside React's tree.
 *
 * A recording must survive navigation between screens. Holding it in component
 * state — which is what v1 did, in module-level variables belonging to a page
 * script — is why navigating away mid-capture lost the recording.
 */

import { create } from 'zustand';

import type { ChintanApi } from '@/api/endpoints.ts';

import { discardCapture, saveCaptureRecord } from './buffer.ts';
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
  pause: () => void;
  resume: () => void;
  stop: () => Promise<void>;
  /** Abandon: stops the recorder and deletes the buffered audio. */
  discard: () => Promise<void>;
  send: (api: ChintanApi) => Promise<void>;
  reset: () => void;
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
     * The capture record is written the moment a recording exists, not when the
     * server first acknowledges it.
     *
     * Writing it in the uploader — after `POST /v1/captures` returns — left a
     * window where the chunks were safely on disk but nothing indexed them, so
     * a recording stranded by going offline before the create, or by the tab
     * dying during it, could never be offered back. The audio was durable and
     * unreachable, which is indistinguishable from lost.
     */
    if (after.state === 'review' && before.state !== 'review') {
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
        peaks: controller?.envelope() ?? null,
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
      if (localId) {
        await discardCapture(localId).catch(() => {
          /* Nothing on disk. */
        });
      }
      dispatch({ type: 'discard' });
    },

    async send(api) {
      const { model } = get();
      const encoder = controller?.current()?.encoder;
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

/** Selector: the sheet must stay shut while any of this is in flight. */
export function selectCaptureModel(state: CaptureStore): CaptureModel {
  return state.model;
}
