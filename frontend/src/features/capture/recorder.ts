/**
 * The imperative half of capture: the browser APIs the state machine cannot
 * touch.
 *
 * Owns the `MediaStream`, the `MediaRecorder`, the `AnalyserNode`, the wake
 * lock and the tick timer, and reports everything back as a `CaptureEvent`.
 * The machine decides what any of it means.
 *
 * Every dependency is injectable so the whole thing runs under Vitest with
 * fakes. That is not gold-plating: interruption, cap, and permission paths are
 * the ones that matter and the ones no manual test ever exercises.
 */

import { appendChunk } from './buffer.ts';
import { PeakCollector } from './peaks.ts';
import {
  chooseEncoder,
  classifyMicrophoneError,
  isRecordingSupported,
  requestMicrophone,
  type EncoderChoice,
} from './audio.ts';
import { acquireWakeLock, type WakeLockHandle } from './wakeLock.ts';
import type { CaptureEvent } from './machine.ts';

/** How often the recorder is asked to hand over a chunk. */
export const CHUNK_INTERVAL_MS = 3_000;
/** Timer resolution for the elapsed display and the duration cap. */
export const TICK_INTERVAL_MS = 200;
/** Analyser frames per second fed into the peak collector. */
const ANALYSER_INTERVAL_MS = 50;

export interface RecorderDeps {
  requestMicrophone: () => Promise<MediaStream>;
  chooseEncoder: () => EncoderChoice | null;
  isSupported: () => boolean;
  createRecorder: (stream: MediaStream, mimeType: string) => MediaRecorder;
  createAudioContext: () => AudioContext | null;
  acquireWakeLock: () => Promise<WakeLockHandle | null>;
  persistChunk: (localId: string, index: number, blob: Blob) => Promise<void>;
  now: () => number;
}

export const defaultRecorderDeps: RecorderDeps = {
  requestMicrophone,
  chooseEncoder,
  isSupported: isRecordingSupported,
  createRecorder: (stream, mimeType) =>
    new MediaRecorder(stream, {
      ...(mimeType ? { mimeType } : {}),
      audioBitsPerSecond: 24_000,
    }),
  createAudioContext: () => {
    try {
      return new AudioContext();
    } catch {
      return null;
    }
  },
  acquireWakeLock,
  persistChunk: appendChunk,
  now: () => Date.now(),
};

export type EventSink = (event: CaptureEvent) => void;

export interface RecorderSession {
  localId: string;
  encoder: EncoderChoice;
  peaks: PeakCollector;
  audioContext: AudioContext | null;
}

/**
 * Drives one recording from microphone request to final chunk.
 *
 * Deliberately not a React hook: a recording must outlive any particular
 * component, and tying its lifetime to a render tree is how v1 lost recordings
 * on navigation.
 */
export class RecorderController {
  private stream: MediaStream | null = null;
  private recorder: MediaRecorder | null = null;
  private wakeLock: WakeLockHandle | null = null;
  private tickTimer: ReturnType<typeof setInterval> | null = null;
  private analyserTimer: ReturnType<typeof setInterval> | null = null;
  private analyser: AnalyserNode | null = null;
  private frame: Uint8Array<ArrayBuffer> | null = null;
  private chunkIndex = 0;
  private session: RecorderSession | null = null;
  private stopping = false;

  constructor(
    private readonly emit: EventSink,
    private readonly deps: RecorderDeps = defaultRecorderDeps,
  ) {}

  current(): RecorderSession | null {
    return this.session;
  }

  /** Live amplitudes for the canvas, newest last. */
  recentAmplitudes(count: number): number[] {
    return this.session?.peaks.recent(count) ?? [];
  }

  /**
   * True once `cancel()` or `stop()` has been asked for.
   *
   * Cleared at the very top of `start()`, before any await, so a cancel raised
   * *during* microphone acquisition is still visible when the promise resolves.
   * It used to be cleared afterwards, which erased the cancel and started the
   * recorder anyway: the screen said "Starting the microphone…", the user tapped
   * Cancel and went Home, and the track stayed live with the OS indicator on and
   * chunks piling up in IndexedDB that nothing would ever list or prune.
   */
  private cancelled(): boolean {
    return this.stopping;
  }

  /** Releases a stream acquired for a recording that is no longer wanted. */
  private static discardStream(stream: MediaStream): void {
    for (const track of stream.getTracks()) {
      try {
        track.stop();
      } catch {
        /* Already stopped. */
      }
    }
  }

  async start(localId: string): Promise<void> {
    this.stopping = false;

    if (!this.deps.isSupported()) {
      this.emit({ type: 'unsupported' });
      return;
    }

    const encoder = this.deps.chooseEncoder();
    if (!encoder) {
      this.emit({ type: 'unsupported' });
      return;
    }

    let stream: MediaStream;
    try {
      // Nothing has claimed a recording started at this point, and nothing
      // may: the machine is still in `requesting`.
      stream = await this.deps.requestMicrophone();
    } catch (error) {
      const kind = classifyMicrophoneError(error);
      this.emit(
        kind === 'permission-denied'
          ? { type: 'permissionDenied' }
          : kind === 'no-microphone'
            ? { type: 'noMicrophone' }
            : { type: 'unsupported' },
      );
      return;
    }

    // The cancel that arrived while `getUserMedia` was pending wins. Nothing
    // has been attached to this stream yet, so releasing it is the whole job.
    if (this.cancelled()) {
      RecorderController.discardStream(stream);
      return;
    }

    this.stream = stream;
    this.chunkIndex = 0;
    this.session = {
      localId,
      encoder,
      peaks: new PeakCollector(),
      audioContext: this.deps.createAudioContext(),
    };

    this.attachTrackHandlers(stream);
    this.attachAnalyser(stream);

    let recorder: MediaRecorder;
    try {
      recorder = this.deps.createRecorder(stream, encoder.mimeType);
    } catch {
      this.teardown();
      this.emit({ type: 'unsupported' });
      return;
    }
    this.recorder = recorder;

    recorder.ondataavailable = (event: BlobEvent) => {
      const blob = event.data;
      if (!blob || blob.size === 0) return;
      const index = this.chunkIndex;
      this.chunkIndex += 1;
      this.emit({ type: 'data', bytes: blob.size });
      // Written to disk immediately. The recording must exist somewhere other
      // than this tab from the first chunk onward.
      void this.deps.persistChunk(localId, index, blob).catch(() => {
        this.emit({ type: 'recorderError', message: 'Could not save audio to this device.' });
      });
    };

    recorder.onerror = () => {
      // v1 registered no error handler at all, so an encoder fault presented
      // as a timer that kept counting over a dead recorder.
      this.emit({ type: 'recorderError' });
      void this.stop();
    };

    recorder.onstop = () => {
      this.emit({ type: 'finalised' });
      this.teardown();
    };

    try {
      recorder.start(CHUNK_INTERVAL_MS);
    } catch {
      this.teardown();
      this.emit({ type: 'recorderError' });
      return;
    }

    // The wake lock is the second await, and a cancel can land across it too:
    // `cancel()` has already torn everything down by then, so assigning the
    // lock here would leave the screen awake for the rest of the session.
    const wakeLock = await this.deps.acquireWakeLock();
    if (this.cancelled()) {
      void wakeLock?.release();
      this.teardown();
      return;
    }
    this.wakeLock = wakeLock;
    this.startTicking();
    this.emit({ type: 'streamReady', now: this.deps.now() });
  }

  pause(): void {
    if (this.recorder?.state !== 'recording') return;
    this.recorder.pause();
    this.stopTicking();
    this.emit({ type: 'pause', now: this.deps.now() });
  }

  resume(): void {
    if (this.recorder?.state !== 'paused') return;
    this.recorder.resume();
    this.startTicking();
    this.emit({ type: 'resume', now: this.deps.now() });
  }

  /** Requests a stop. `finalised` follows when the last chunk has arrived. */
  async stop(): Promise<void> {
    if (this.stopping) return;
    this.stopping = true;
    this.stopTicking();
    this.emit({ type: 'stop', now: this.deps.now() });
    try {
      if (this.recorder && this.recorder.state !== 'inactive') {
        this.recorder.stop();
      } else {
        this.emit({ type: 'finalised' });
        this.teardown();
      }
    } catch {
      this.emit({ type: 'finalised' });
      this.teardown();
    }
  }

  /** Abandons the recording. The buffer is dropped by the caller. */
  cancel(): void {
    this.stopping = true;
    this.stopTicking();
    try {
      if (this.recorder && this.recorder.state !== 'inactive') {
        this.recorder.onstop = null;
        this.recorder.stop();
      }
    } catch {
      /* Already stopped. */
    }
    this.teardown();
  }

  /** The finished envelope for `peaks.json`. */
  envelope(): number[] {
    return this.session?.peaks.envelope() ?? [];
  }

  private attachTrackHandlers(stream: MediaStream): void {
    for (const track of stream.getAudioTracks()) {
      // An incoming call ends the track. v1 handled neither of these, so the
      // recording truncated in silence while the UI kept counting up.
      track.addEventListener('ended', () => {
        this.emit({ type: 'trackEnded', now: this.deps.now() });
        void this.stop();
      });
      track.addEventListener('mute', () => {
        this.emit({ type: 'trackMuted' });
      });
      track.addEventListener('unmute', () => {
        this.emit({ type: 'trackUnmuted' });
      });
    }
  }

  private attachAnalyser(stream: MediaStream): void {
    const context = this.session?.audioContext;
    if (!context) return;
    try {
      const source = context.createMediaStreamSource(stream);
      const analyser = context.createAnalyser();
      analyser.fftSize = 1024;
      analyser.smoothingTimeConstant = 0.6;
      source.connect(analyser);
      this.analyser = analyser;
      this.frame = new Uint8Array(new ArrayBuffer(analyser.fftSize));
    } catch {
      // No analyser means no live waveform. It must not stop the recording —
      // the audio is what matters.
      this.analyser = null;
    }
  }

  private startTicking(): void {
    this.stopTicking();
    this.tickTimer = setInterval(() => {
      this.emit({ type: 'tick', now: this.deps.now() });
    }, TICK_INTERVAL_MS);

    if (this.analyser && this.frame) {
      this.analyserTimer = setInterval(() => {
        if (!this.analyser || !this.frame) return;
        this.analyser.getByteTimeDomainData(this.frame);
        this.session?.peaks.push(this.frame);
      }, ANALYSER_INTERVAL_MS);
    }
  }

  private stopTicking(): void {
    if (this.tickTimer !== null) clearInterval(this.tickTimer);
    if (this.analyserTimer !== null) clearInterval(this.analyserTimer);
    this.tickTimer = null;
    this.analyserTimer = null;
  }

  private teardown(): void {
    this.stopTicking();
    for (const track of this.stream?.getTracks() ?? []) {
      try {
        track.stop();
      } catch {
        /* Already stopped. */
      }
    }
    this.stream = null;
    this.recorder = null;
    this.analyser = null;
    this.frame = null;
    void this.wakeLock?.release();
    this.wakeLock = null;
  }
}
