import { beforeEach, describe, expect, it, vi } from 'vitest';

import { captureReducer, INITIAL_CAPTURE, type CaptureEvent, type CaptureModel } from './machine.ts';
import { RecorderController, type RecorderDeps } from './recorder.ts';

/* ---------------------------------------------------------------------------
   Fakes. jsdom implements none of the media APIs, and the paths worth testing
   — permission refusal, an interrupting phone call, an encoder fault — cannot
   be produced by hand in a browser anyway.
   --------------------------------------------------------------------------- */

class FakeTrack extends EventTarget {
  kind = 'audio';
  stopped = false;
  stop(): void {
    this.stopped = true;
  }
  /** The OS ending the track: an incoming call, or an unplugged headset. */
  end(): void {
    this.dispatchEvent(new Event('ended'));
  }
  mute(): void {
    this.dispatchEvent(new Event('mute'));
  }
  unmute(): void {
    this.dispatchEvent(new Event('unmute'));
  }
}

class FakeStream {
  constructor(readonly track = new FakeTrack()) {}
  getAudioTracks(): FakeTrack[] {
    return [this.track];
  }
  getTracks(): FakeTrack[] {
    return [this.track];
  }
}

class FakeRecorder extends EventTarget {
  state: 'inactive' | 'recording' | 'paused' = 'inactive';
  ondataavailable: ((event: BlobEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onstop: (() => void) | null = null;
  startCalls = 0;

  start(): void {
    this.startCalls += 1;
    this.state = 'recording';
  }
  pause(): void {
    this.state = 'paused';
  }
  resume(): void {
    this.state = 'recording';
  }
  stop(): void {
    this.state = 'inactive';
    this.onstop?.();
  }
  /** Simulates `ondataavailable`. */
  emitChunk(size: number): void {
    this.ondataavailable?.({ data: new Blob(['x'.repeat(size)]) } as BlobEvent);
  }
  fail(): void {
    this.onerror?.();
  }
}

interface Harness {
  controller: RecorderController;
  /** Seeds the machine's `request` state, which the store owns in production. */
  start: (localId?: string) => Promise<void>;
  events: CaptureEvent[];
  model: () => CaptureModel;
  recorder: FakeRecorder;
  stream: FakeStream;
  persisted: { localId: string; index: number; bytes: number }[];
  wakeLockReleased: () => boolean;
}

function harness(
  overrides: Partial<RecorderDeps> = {},
  clock: { value: number } = { value: 1_000 },
): Harness {
  const events: CaptureEvent[] = [];
  let model = INITIAL_CAPTURE;
  const recorder = new FakeRecorder();
  const stream = new FakeStream();
  const persisted: { localId: string; index: number; bytes: number }[] = [];
  let released = false;

  const deps: RecorderDeps = {
    requestMicrophone: async () => stream as unknown as MediaStream,
    chooseEncoder: () => ({ mimeType: 'audio/webm;codecs=opus', contentType: 'audio/webm' }),
    isSupported: () => true,
    createRecorder: () => recorder as unknown as MediaRecorder,
    // No AnalyserNode: the live waveform is a rendering concern and its
    // absence must never stop a recording.
    createAudioContext: () => null,
    acquireWakeLock: async () => ({
      async release() {
        released = true;
      },
    }),
    persistChunk: async (localId, index, blob) => {
      persisted.push({ localId, index, bytes: blob.size });
    },
    now: () => clock.value,
    ...overrides,
  };

  const controller = new RecorderController((event) => {
    events.push(event);
    model = captureReducer(model, event);
  }, deps);

  return {
    controller,
    async start(localId = 'cap-1') {
      model = captureReducer(model, { type: 'request', localId });
      await controller.start(localId);
    },
    events,
    model: () => model,
    recorder,
    stream,
    persisted,
    wakeLockReleased: () => released,
  };
}

const kinds = (events: CaptureEvent[]) => events.map((event) => event.type);

beforeEach(() => {
  vi.useRealTimers();
});

describe('starting', () => {
  it('does not report recording until the stream resolves', async () => {
    let resolveStream: (stream: MediaStream) => void = () => {};
    const h = harness({
      requestMicrophone: () =>
        new Promise<MediaStream>((resolve) => {
          resolveStream = resolve;
        }),
    });

    const starting = h.start();
    expect(kinds(h.events)).not.toContain('streamReady');
    expect(h.model().state).toBe('requesting');

    resolveStream(new FakeStream() as unknown as MediaStream);
    await starting;

    expect(kinds(h.events)).toContain('streamReady');
    expect(h.model().state).toBe('recording');
  });

  it('holds a wake lock for the recording and releases it on stop', async () => {
    const h = harness();
    await h.start();
    expect(h.wakeLockReleased()).toBe(false);

    h.recorder.emitChunk(10);
    await h.controller.stop();

    expect(h.wakeLockReleased()).toBe(true);
  });

  it('stops the microphone track when the recording ends', async () => {
    const h = harness();
    await h.start();
    h.recorder.emitChunk(10);
    await h.controller.stop();

    expect(h.stream.track.stopped).toBe(true);
  });
});

describe('permission and support failures', () => {
  it('reports a refused permission', async () => {
    const denied = Object.assign(new Error('denied'), { name: 'NotAllowedError' });
    const h = harness({
      requestMicrophone: async () => {
        throw denied;
      },
    });

    await h.start();

    expect(kinds(h.events)).toEqual(['permissionDenied']);
    expect(h.model().failure?.kind).toBe('permission-denied');
  });

  it('reports a missing microphone distinctly', async () => {
    const missing = Object.assign(new Error('none'), { name: 'NotFoundError' });
    const h = harness({
      requestMicrophone: async () => {
        throw missing;
      },
    });

    await h.start();
    expect(kinds(h.events)).toEqual(['noMicrophone']);
  });

  it('reports an unsupported browser without touching the microphone', async () => {
    const requestMicrophone = vi.fn();
    const h = harness({
      isSupported: () => false,
      requestMicrophone: requestMicrophone as unknown as () => Promise<MediaStream>,
    });

    await h.start();

    expect(kinds(h.events)).toEqual(['unsupported']);
    expect(requestMicrophone).not.toHaveBeenCalled();
  });
});

describe('chunks reach the disk as they are produced', () => {
  it('persists every chunk with an increasing index', async () => {
    const h = harness();
    await h.start();

    h.recorder.emitChunk(100);
    h.recorder.emitChunk(200);
    await Promise.resolve();

    expect(h.persisted).toEqual([
      { localId: 'cap-1', index: 0, bytes: 100 },
      { localId: 'cap-1', index: 1, bytes: 200 },
    ]);
    expect(h.model().bytes).toBe(300);
  });

  it('ignores an empty chunk', async () => {
    const h = harness();
    await h.start();

    h.recorder.emitChunk(0);

    expect(h.persisted).toEqual([]);
    expect(h.model().chunks).toBe(0);
  });

  it('surfaces a failed disk write rather than recording into nothing', async () => {
    const h = harness({
      persistChunk: async () => {
        throw new Error('QuotaExceededError');
      },
    });
    await h.start();

    h.recorder.emitChunk(100);
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(kinds(h.events)).toContain('recorderError');
    // And it stops. Surfacing the error alone moved the machine to `review`
    // while the recorder, the track and the wake lock stayed live behind it —
    // an open microphone with no UI admitting to it.
    expect(h.recorder.state).toBe('inactive');
    expect(h.stream.track.stopped).toBe(true);
    expect(h.wakeLockReleased()).toBe(true);
  });

  it('closes the previous recording’s AudioContext before opening the next', async () => {
    // WebKit caps live AudioContexts; one leaked per recording meant the
    // constructor started throwing after a few captures and the waveform and
    // tones vanished on iOS.
    const contexts: { state: string; close: () => Promise<void> }[] = [];
    const h = harness({
      createAudioContext: () => {
        const context = {
          state: 'running',
          async close() {
            context.state = 'closed';
          },
        };
        contexts.push(context);
        return context as unknown as AudioContext;
      },
    });

    await h.start('cap-1');
    await h.controller.stop();
    expect(contexts).toHaveLength(1);
    // Still open right after stop, so the stop tone scheduled on it plays.
    expect(contexts[0]?.state).toBe('running');

    await h.start('cap-2');
    expect(contexts).toHaveLength(2);
    expect(contexts[0]?.state).toBe('closed');
    expect(contexts[1]?.state).toBe('running');
  });
});

describe('interruption', () => {
  it('turns an ended track into a saved partial recording', async () => {
    // An incoming call. Without a track handler the recording would truncate
    // in silence while the timer kept counting.
    const h = harness();
    await h.start();
    h.recorder.emitChunk(5_000);

    h.stream.track.end();
    await Promise.resolve();

    expect(kinds(h.events)).toContain('trackEnded');
    expect(h.model().interrupted).toBe(true);
    expect(h.model().bytes).toBe(5_000);
    expect(h.model().state).toBe('review');
  });

  it('pauses the recorder when the track mutes, and resumes it when the track unmutes', async () => {
    /*
     * An incoming call on iOS mutes the track and unmutes it when the call
     * ends. The handler used to only raise a flag, so MediaRecorder encoded
     * silence for the whole call and the clock kept counting over it.
     */
    const clock = { value: 1_000 };
    const h = harness({}, clock);
    await h.start();
    h.recorder.emitChunk(100);

    clock.value = 6_000;
    h.stream.track.mute();

    expect(h.recorder.state).toBe('paused');
    expect(h.model().state).toBe('paused');
    expect(h.model().micTaken).toBe(true);
    expect(h.model().elapsedMs).toBe(5_000);

    clock.value = 66_000;
    h.stream.track.unmute();

    expect(h.recorder.state).toBe('recording');
    expect(h.model().state).toBe('recording');
    expect(h.model().micReturned).toBe(true);
    clock.value = 68_000;
    h.controller.stop();
    // The minute on the call is not in the recording.
    expect(h.model().elapsedMs).toBe(7_000);
  });

  it('stops the tick while muted and restarts it on unmute', async () => {
    vi.useFakeTimers();
    const h = harness();
    await h.start();

    h.stream.track.mute();
    const before = kinds(h.events).filter((kind) => kind === 'tick').length;
    vi.advanceTimersByTime(2_000);
    expect(kinds(h.events).filter((kind) => kind === 'tick').length).toBe(before);

    h.stream.track.unmute();
    vi.advanceTimersByTime(1_000);
    expect(kinds(h.events).filter((kind) => kind === 'tick').length).toBeGreaterThan(before);
    vi.useRealTimers();
  });

  it('leaves a pause the user asked for alone when the track mutes and unmutes', async () => {
    const h = harness();
    await h.start();
    h.controller.pause();

    h.stream.track.mute();
    h.stream.track.unmute();

    expect(h.recorder.state).toBe('paused');
    expect(h.model().state).toBe('paused');
    expect(h.model().micTaken).toBe(false);
  });

  it('lets the user resume during a mute, and the later unmute does not double-resume', async () => {
    const h = harness();
    await h.start();

    h.stream.track.mute();
    expect(h.recorder.state).toBe('paused');
    h.controller.resume();
    expect(h.recorder.state).toBe('recording');

    h.stream.track.unmute();
    expect(h.recorder.state).toBe('recording');
    expect(kinds(h.events).filter((kind) => kind === 'resume')).toHaveLength(1);
    expect(h.model().micReturned).toBe(false);
  });

  it('still stops and saves when the track ends after a mute', async () => {
    const h = harness();
    await h.start();
    h.recorder.emitChunk(4_000);

    h.stream.track.mute();
    h.stream.track.end();
    await Promise.resolve();

    expect(h.model().state).toBe('review');
    expect(h.model().interrupted).toBe(true);
    expect(h.model().bytes).toBe(4_000);
  });

  it('keeps buffered audio when the encoder faults', async () => {
    const h = harness();
    await h.start();
    h.recorder.emitChunk(2_000);

    h.recorder.fail();
    await Promise.resolve();

    expect(h.model().state).toBe('review');
    expect(h.model().bytes).toBe(2_000);
  });
});

describe('the buffer is readable the moment the recording is finished', () => {
  it('flushed() waits for chunk writes still in flight', async () => {
    /*
     * `ondataavailable` tells the machine about a chunk synchronously; the
     * IndexedDB write behind it is not. The final chunk arrives just before
     * `onstop`, so the review player — which mounts on `finalised` — read an
     * empty buffer and offered a Play button that could play nothing.
     */
    let releaseWrite: () => void = () => {};
    let written = false;
    const h = harness({
      persistChunk: () =>
        new Promise<void>((resolve) => {
          releaseWrite = () => {
            written = true;
            resolve();
          };
        }),
    });
    await h.start();
    h.recorder.emitChunk(500);

    let flushed = false;
    const waiting = h.controller.flushed().then(() => {
      flushed = true;
    });
    await Promise.resolve();
    expect(flushed).toBe(false);

    releaseWrite();
    await waiting;
    expect(written).toBe(true);
    expect(flushed).toBe(true);
  });

  it('flushed() resolves at once when nothing is pending', async () => {
    const h = harness();
    await h.start();
    await expect(h.controller.flushed()).resolves.toBeUndefined();
  });
});

describe('pause, resume, stop, cancel', () => {
  it('pauses and resumes the underlying recorder', async () => {
    const clock = { value: 1_000 };
    const h = harness({}, clock);
    await h.start();

    clock.value = 6_000;
    h.controller.pause();
    expect(h.recorder.state).toBe('paused');
    expect(h.model().elapsedMs).toBe(5_000);

    clock.value = 20_000;
    h.controller.resume();
    expect(h.recorder.state).toBe('recording');

    clock.value = 23_000;
    h.controller.stop();
    expect(h.model().elapsedMs).toBe(8_000);
  });

  it('is idempotent on a double stop', async () => {
    const h = harness();
    await h.start();
    h.recorder.emitChunk(10);

    await h.controller.stop();
    await h.controller.stop();

    expect(kinds(h.events).filter((kind) => kind === 'stop')).toHaveLength(1);
  });

  it('a cancel during microphone acquisition stops the stream and never records', async () => {
    /*
     * First use, a permission prompt, a cold radio, or a Bluetooth handover
     * makes acquisition slow. The screen says "Starting the microphone…" with a
     * Cancel button.
     *
     * `stopping` was cleared *after* the await, so the cancel was erased when
     * the promise resolved and the recorder started anyway: the track stayed
     * `readyState=live`, the OS recording indicator stayed on, and chunks kept
     * growing in IndexedDB that nothing could ever list or prune, because
     * `discardCapture` had already run and there was no `captures` row.
     */
    let resolveStream: (stream: MediaStream) => void = () => {};
    const late = new FakeStream();
    const h = harness({
      requestMicrophone: () =>
        new Promise<MediaStream>((resolve) => {
          resolveStream = resolve;
        }),
    });

    const starting = h.start();
    h.controller.cancel();
    resolveStream(late as unknown as MediaStream);
    await starting;

    expect(late.track.stopped, 'the microphone track was left live').toBe(true);
    expect(kinds(h.events)).not.toContain('streamReady');
    expect(h.recorder.startCalls, 'the recorder was started after a cancel').toBe(0);
    expect(h.persisted).toEqual([]);
  });

  it('a cancel while the wake lock is being acquired releases it', async () => {
    let resolveLock: (lock: { release: () => Promise<void> }) => void = () => {};
    let released = false;
    const h = harness({
      acquireWakeLock: () =>
        new Promise((resolve) => {
          resolveLock = resolve;
        }),
    });

    const starting = h.start();
    // Let the microphone resolve and the recorder start, so the cancel lands
    // across the *second* await rather than the first.
    await new Promise((resolve) => setTimeout(resolve, 0));
    h.controller.cancel();
    resolveLock({
      async release() {
        released = true;
      },
    });
    await starting;

    expect(released, 'the screen was left awake for the rest of the session').toBe(true);
    expect(kinds(h.events)).not.toContain('streamReady');
  });

  it('cancel tears down without emitting a finalised recording', async () => {
    const h = harness();
    await h.start();
    h.recorder.emitChunk(10);

    h.controller.cancel();

    expect(kinds(h.events)).not.toContain('finalised');
    expect(h.stream.track.stopped).toBe(true);
    expect(h.wakeLockReleased()).toBe(true);
  });
});

describe('the live waveform starts empty', () => {
  it('reads no amplitudes from the previous recording while the microphone is pending', async () => {
    /*
     * The capture screen draws the waveform from its first frame by calling
     * `recentAmplitudes()`. The new session — with its empty PeakCollector —
     * used to be created only after `getUserMedia` resolved, so for that whole
     * wait the canvas was fed the previous recording's peaks: a new recording
     * opened on the tail of the last one's bars, which then vanished.
     */
    let resolveStream: (stream: MediaStream) => void = () => {};
    let requests = 0;
    const h = harness({
      requestMicrophone: () => {
        requests += 1;
        if (requests === 1) return Promise.resolve(new FakeStream() as unknown as MediaStream);
        return new Promise<MediaStream>((resolve) => {
          resolveStream = resolve;
        });
      },
    });

    // First recording, with something on its envelope.
    await h.start('cap-1');
    const loud = new Uint8Array(64).fill(255);
    h.controller.current()?.peaks.push(loud);
    h.controller.current()?.peaks.push(loud);
    expect(h.controller.recentAmplitudes(8)).toHaveLength(2);
    h.recorder.emitChunk(10);
    await h.controller.stop();

    // Second recording: the microphone has been asked for and has not answered.
    const starting = h.controller.start('cap-2');
    expect(h.controller.recentAmplitudes(8)).toEqual([]);
    expect(h.controller.envelope()).toEqual([]);

    resolveStream(new FakeStream() as unknown as MediaStream);
    await starting;

    // And once it has, the new session is the one being read.
    expect(h.controller.current()?.localId).toBe('cap-2');
    expect(h.controller.recentAmplitudes(8)).toEqual([]);
  });
});
