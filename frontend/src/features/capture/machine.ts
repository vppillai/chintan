/**
 * The capture state machine.
 *
 * Pure and total: no timers, no browser APIs, no promises. Every effect —
 * `getUserMedia`, `MediaRecorder`, the wake lock, IndexedDB, the upload — is
 * driven by the controller in `recorder.ts` and reported back here as an
 * event. That is what makes interruption, cap, and offline behaviour testable
 * without a browser, and those are precisely the paths a browser-driven test
 * struggles to reach.
 *
 * Two invariants the machine exists to hold:
 *
 * 1. **Nothing claims recording has started before the microphone is live.**
 *    `requesting` is a distinct state; `isLive` is false in it.
 * 2. **An interruption produces a saved partial recording, never a discard.**
 *    A phone call ends the track; left unhandled, the recording silently
 *    truncates while the UI keeps counting.
 */

import { config } from '@/config/env.ts';

export type CaptureState =
  /** Nothing in progress. */
  | 'idle'
  /** `getUserMedia` is in flight. The UI must not say "recording". */
  | 'requesting'
  | 'recording'
  | 'paused'
  /** Stop requested; waiting for the recorder's final chunk. */
  | 'stopping'
  /** Audio captured and buffered. The user reviews before it is sent. */
  | 'review'
  | 'uploading'
  /** Handed off to the server. The progress card takes over from here. */
  | 'uploaded'
  | 'failed';

type CaptureFailureKind =
  | 'permission-denied'
  | 'no-microphone'
  | 'recorder-failed'
  | 'upload-failed'
  | 'spend-capped'
  | 'unsupported';

export interface CaptureFailure {
  kind: CaptureFailureKind;
  message: string;
  /** True when the buffered audio is still on the device and can be resent. */
  recoverable: boolean;
}

type CapReason = 'duration' | 'size';

/* ---------------------------------------------------------------------------
   Limits
   --------------------------------------------------------------------------- */

/** Twenty minutes. The success criterion the pipeline is built to satisfy. */
export const MAX_DURATION_MS = 20 * 60_000;
/** Warn a minute out, so a long dictation can be wrapped up deliberately. */
export const DURATION_WARNING_MS = MAX_DURATION_MS - 60_000;

/** 16 kHz mono Opus runs ~24 kbps, so 20 minutes is ~3.6 MB. This is slack. */
export const MAX_BYTES = 32 * 1024 * 1024;
export const SIZE_WARNING_BYTES = 28 * 1024 * 1024;

/**
 * A take at least this long asks once before Cancel or Discard throws it
 * away; a shorter one is gone at once. Ten seconds of speech is worth one
 * more tap, and a slip is not worth a dialog.
 */
export const DISCARD_CONFIRM_AFTER_MS = 10_000;

export interface CaptureModel {
  state: CaptureState;
  /**
   * Stable local id, minted before the microphone is touched. It is the
   * IndexedDB key AND the `Idempotency-Key` for `POST /v1/captures`, so a
   * resumed upload after a crash replays rather than creating a second capture.
   */
  localId: string;
  /** When the current running segment began. Null while paused or stopped. */
  startedAt: number | null;
  /** Milliseconds from segments already completed (i.e. before a pause). */
  accumulatedMs: number;
  elapsedMs: number;
  bytes: number;
  chunks: number;
  /** Set when a track ended mid-recording; the audio is still good. */
  interrupted: boolean;
  /**
   * Paused because the OS muted the track — an incoming call, Siri, another
   * app claiming the microphone. Distinct from a pause the user asked for,
   * because this one is undone automatically when the track unmutes.
   */
  micTaken: boolean;
  /** Recording again after a `micTaken` pause. Cleared by the next pause or stop. */
  micReturned: boolean;
  /** The page is hidden right now: the screen locked, or another app is in front. */
  hidden: boolean;
  /**
   * The current pause or interruption arrived while the page was hidden. On a
   * phone that is the screen locking, and the screen says so — "the
   * microphone was taken by another app" would blame the wrong thing.
   */
  background: boolean;
  /** Which cap stopped the recording, if either did. */
  capReached: CapReason | null;
  nearDurationLimit: boolean;
  nearSizeLimit: boolean;
  failure: CaptureFailure | null;
  /** 0..1 while uploading. */
  uploadProgress: number;
  /** Assigned by the server once `POST /v1/captures` succeeds. */
  serverCaptureId: string | null;
  /** Target note, when the user picked one before recording. */
  noteId: string | null;
}

export type CaptureEvent =
  | { type: 'request'; localId: string; noteId?: string | null }
  | { type: 'streamReady'; now: number }
  | { type: 'permissionDenied'; message?: string }
  | { type: 'noMicrophone'; message?: string }
  | { type: 'unsupported'; message?: string }
  | { type: 'tick'; now: number }
  | { type: 'data'; bytes: number }
  | { type: 'pause'; now: number }
  | { type: 'resume'; now: number }
  | { type: 'stop'; now: number }
  /** The recorder emitted its final chunk; the buffer is complete. `now` settles a stop nobody asked for. */
  | { type: 'finalised'; now?: number }
  | { type: 'recorderError'; message?: string }
  /** A track ended — an incoming call, a disconnected headset. */
  | { type: 'trackEnded'; now: number }
  /** The OS took the microphone without ending the track. Timed: it pauses. */
  | { type: 'trackMuted'; now: number }
  | { type: 'trackUnmuted'; now: number }
  /** The document went hidden — the screen locked, or the app went behind another. */
  | { type: 'pageHidden'; now: number }
  | { type: 'pageVisible'; now: number }
  | { type: 'uploadStart' }
  | { type: 'uploadProgress'; progress: number }
  | { type: 'captureCreated'; serverCaptureId: string }
  | { type: 'uploadDone' }
  | { type: 'uploadFailed'; message: string; recoverable?: boolean }
  | { type: 'spendCapped'; message: string }
  /**
   * The user changed where this recording goes, from the chooser on the
   * capture screen. Accepted from the moment the microphone is asked for until
   * the upload begins; after that the target has been sent.
   */
  | { type: 'target'; noteId: string | null }
  | { type: 'discard' }
  | { type: 'reset' };

export const INITIAL_CAPTURE: CaptureModel = {
  state: 'idle',
  localId: '',
  startedAt: null,
  accumulatedMs: 0,
  elapsedMs: 0,
  bytes: 0,
  chunks: 0,
  interrupted: false,
  micTaken: false,
  micReturned: false,
  hidden: false,
  background: false,
  capReached: null,
  nearDurationLimit: false,
  nearSizeLimit: false,
  failure: null,
  uploadProgress: 0,
  serverCaptureId: null,
  noteId: null,
};

/** True only when the microphone is actually live and capturing. */
export function isLive(model: CaptureModel): boolean {
  return model.state === 'recording';
}

/** True whenever a recording exists that has not been handed to the server. */
export function hasBufferedAudio(model: CaptureModel): boolean {
  return (
    model.bytes > 0 &&
    (model.state === 'review' || model.state === 'uploading' || model.state === 'failed')
  );
}

/**
 * True while a recording is in flight and leaving would endanger it. Anything
 * after the microphone opens and before the audio is safely on the server
 * counts, because navigating away mid-upload is how a recording gets lost. The
 * shell's recording indicator reads this to say so on every other screen.
 */
export function isCaptureBusy(model: CaptureModel): boolean {
  return (
    model.state === 'requesting' ||
    model.state === 'recording' ||
    model.state === 'paused' ||
    model.state === 'stopping' ||
    model.state === 'uploading'
  );
}

/** A failure the user can act on by resending rather than re-recording. */
export function canRetryUpload(model: CaptureModel): boolean {
  return model.state === 'failed' && (model.failure?.recoverable ?? false) && model.bytes > 0;
}

/** The recording's length at `now`, the running segment included. */
export function elapsedAt(model: CaptureModel, now: number): number {
  return model.accumulatedMs + (model.startedAt === null ? 0 : now - model.startedAt);
}

/** Freezes the running segment into `accumulatedMs`. */
function settle(model: CaptureModel, now: number): CaptureModel {
  const elapsedMs = elapsedAt(model, now);
  return { ...model, accumulatedMs: elapsedMs, elapsedMs, startedAt: null };
}

function fail(
  model: CaptureModel,
  kind: CaptureFailureKind,
  message: string,
  recoverable: boolean,
): CaptureModel {
  return {
    ...model,
    state: 'failed',
    startedAt: null,
    failure: { kind, message, recoverable },
  };
}

const RECORDING_STATES = new Set<CaptureState>(['recording', 'paused']);

export function captureReducer(model: CaptureModel, event: CaptureEvent): CaptureModel {
  switch (event.type) {
    case 'request':
      // Everything resets here except the caller's chosen target note.
      return {
        ...INITIAL_CAPTURE,
        state: 'requesting',
        localId: event.localId,
        noteId: event.noteId ?? null,
      };

    case 'streamReady':
      // Only now is it true that we are recording.
      if (model.state !== 'requesting') return model;
      return { ...model, state: 'recording', startedAt: event.now };

    case 'permissionDenied':
      return fail(
        model,
        'permission-denied',
        // The instance's own name: this sentence is the one a person who
        // refused the prompt reads, and a build called something else must
        // not name Chintan in it.
        event.message ?? `${config.appName} needs microphone access to record.`,
        false,
      );

    case 'noMicrophone':
      return fail(
        model,
        'no-microphone',
        event.message ?? 'No microphone was found.',
        false,
      );

    case 'unsupported':
      return fail(
        model,
        'unsupported',
        event.message ?? 'This browser cannot record audio.',
        false,
      );

    case 'tick': {
      if (model.state !== 'recording') return model;
      const elapsedMs = elapsedAt(model, event.now);
      const next: CaptureModel = {
        ...model,
        elapsedMs,
        nearDurationLimit: elapsedMs >= DURATION_WARNING_MS,
      };
      // The cap stops the recording rather than truncating it silently: the
      // buffered audio is kept and goes to review like any other stop.
      if (elapsedMs >= MAX_DURATION_MS) {
        return { ...settle(next, event.now), state: 'stopping', capReached: 'duration' };
      }
      return next;
    }

    case 'data': {
      if (!RECORDING_STATES.has(model.state) && model.state !== 'stopping') return model;
      const bytes = model.bytes + event.bytes;
      const next: CaptureModel = {
        ...model,
        bytes,
        chunks: model.chunks + 1,
        nearSizeLimit: bytes >= SIZE_WARNING_BYTES,
      };
      if (bytes >= MAX_BYTES && model.state !== 'stopping') {
        return { ...next, state: 'stopping', capReached: 'size', startedAt: null };
      }
      return next;
    }

    case 'pause':
      if (model.state !== 'recording') return model;
      return { ...settle(model, event.now), state: 'paused', micReturned: false };

    case 'resume':
      // A Resume the user asked for outranks the OS: even if the track is
      // still muted, they chose to carry on, so the automatic resume is off.
      if (model.state !== 'paused') return model;
      return {
        ...model,
        state: 'recording',
        startedAt: event.now,
        micTaken: false,
        micReturned: false,
        background: false,
      };

    case 'stop':
      if (!RECORDING_STATES.has(model.state)) return model;
      return {
        ...settle(model, event.now),
        state: 'stopping',
        micTaken: false,
        micReturned: false,
        background: false,
      };

    case 'finalised': {
      /*
       * A final chunk nobody asked for: the platform stopped the recorder
       * itself — a page thawed after the screen was locked, a recorder the OS
       * ended without ending the track. Settled as an interruption, with the
       * audio kept; left as it was, the screen said "Recording" over a dead
       * recorder until Stop was tapped.
       */
      if (RECORDING_STATES.has(model.state)) {
        const settled =
          event.now === undefined
            ? { ...model, accumulatedMs: model.elapsedMs, startedAt: null }
            : settle(model, event.now);
        if (settled.bytes === 0) {
          return fail(settled, 'recorder-failed', 'Recording was interrupted.', false);
        }
        return { ...settled, state: 'review', interrupted: true, background: model.hidden };
      }
      if (model.state !== 'stopping') return model;
      // No audio at all is a failure, not an empty note: uploading zero bytes
      // would burn a transcription call to produce nothing.
      if (model.bytes === 0) {
        return fail(
          model,
          'recorder-failed',
          model.interrupted ? 'Recording was interrupted.' : 'Nothing was recorded.',
          false,
        );
      }
      return { ...model, state: 'review' };
    }

    case 'recorderError':
      // If audio was already buffered, keep it — a partial recording is worth
      // far more than a clean error.
      if (model.bytes > 0) {
        return {
          ...model,
          state: 'review',
          startedAt: null,
          interrupted: true,
          background: model.hidden,
        };
      }
      return fail(
        model,
        'recorder-failed',
        event.message ?? 'Recording stopped unexpectedly.',
        false,
      );

    case 'trackEnded':
      /*
       * An incoming call, or the headset being unplugged. The recording so
       * far is good; stop cleanly and let the user review it. Whether there
       * is any is `finalised`'s to say, not this event's: the first chunk
       * arrives three seconds in, and a track that ended before it used to
       * be failed here at once, when the recorder's final chunk — the
       * seconds already spoken — was still on its way.
       */
      if (!RECORDING_STATES.has(model.state)) return model;
      return { ...settle(model, event.now), state: 'stopping', interrupted: true, background: model.hidden };

    case 'trackMuted':
      /*
       * The OS took the microphone but left the track alive — a call coming
       * in, Siri, another app. Recoverable, so not a stop; but not merely a
       * flag either, which is what this used to be: the clock kept counting
       * and the recorder kept encoding silence for as long as the call lasted,
       * and the screen said "Recording" over a microphone that was not.
       *
       * So it is a pause, of the kind the machine undoes itself on `unmute`.
       * A pause the user already asked for is left exactly as it is: they
       * chose to stop, and a call ending must not restart them.
       */
      if (model.state !== 'recording') return model;
      return {
        ...settle(model, event.now),
        state: 'paused',
        micTaken: true,
        micReturned: false,
        background: model.hidden,
      };

    case 'trackUnmuted':
      if (model.state !== 'paused' || !model.micTaken) return model;
      return {
        ...model,
        state: 'recording',
        startedAt: event.now,
        micTaken: false,
        micReturned: true,
        background: false,
      };

    case 'pageHidden':
      // Nothing stops here. Android keeps the recorder running behind a
      // locked screen; iOS mutes the track or freezes the page, and those
      // arrive as their own events. The flag is what tells them apart from a
      // call taking the microphone.
      if (model.hidden) return model;
      return { ...model, hidden: true };

    case 'pageVisible':
      if (!model.hidden) return model;
      return { ...model, hidden: false };

    case 'uploadStart':
      if (model.state !== 'review' && model.state !== 'failed') return model;
      return { ...model, state: 'uploading', uploadProgress: 0, failure: null };

    case 'uploadProgress':
      if (model.state !== 'uploading') return model;
      return { ...model, uploadProgress: Math.min(1, Math.max(0, event.progress)) };

    case 'captureCreated':
      return { ...model, serverCaptureId: event.serverCaptureId };

    case 'uploadDone':
      if (model.state !== 'uploading') return model;
      return { ...model, state: 'uploaded', uploadProgress: 1 };

    case 'uploadFailed':
      // Recoverable by default: the bytes are still in IndexedDB, so this is a
      // Resend button, not a lost recording.
      return fail(model, 'upload-failed', event.message, event.recoverable ?? true);

    case 'spendCapped':
      // Distinct from a generic failure so the UI can explain it. The audio is
      // kept: the cap resets tomorrow.
      return fail(model, 'spend-capped', event.message, true);

    case 'target':
      if (
        model.state === 'idle' ||
        model.state === 'uploading' ||
        model.state === 'uploaded'
      ) {
        return model;
      }
      if (model.noteId === event.noteId) return model;
      return { ...model, noteId: event.noteId };

    case 'discard':
      return { ...INITIAL_CAPTURE };

    case 'reset':
      return { ...INITIAL_CAPTURE };

    default: {
      const exhaustive: never = event;
      return exhaustive;
    }
  }
}

/** `mm:ss`, or `h:mm:ss` past an hour. Rendered with tabular numerals. */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const seconds = total % 60;
  const minutes = Math.floor(total / 60) % 60;
  const hours = Math.floor(total / 3600);
  const pad = (value: number) => String(value).padStart(2, '0');
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${pad(minutes)}:${pad(seconds)}`;
}
