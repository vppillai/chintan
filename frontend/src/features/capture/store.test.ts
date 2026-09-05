import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { openChintanDB } from '@/offline/db.ts';

import { appendChunk, readCaptureRecord, unconfirmedCaptures } from './buffer.ts';
import { INITIAL_CAPTURE } from './machine.ts';
import type { RecorderDeps } from './recorder.ts';
import { useCaptureStore } from './store.ts';

/* ---------------------------------------------------------------------------
   A recorder that produces chunks on demand. jsdom has no media APIs; what is
   under test is the store's bookkeeping around them.
   --------------------------------------------------------------------------- */

class FakeTrack extends EventTarget {
  kind = 'audio';
  stop(): void {}
}

class FakeStream {
  readonly track = new FakeTrack();
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
  start(): void {
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
  emitChunk(size: number): void {
    this.ondataavailable?.({ data: new Blob(['x'.repeat(size)]) } as BlobEvent);
  }
}

let recorder = new FakeRecorder();

function fakeDeps(): RecorderDeps {
  return {
    requestMicrophone: async () => new FakeStream() as unknown as MediaStream,
    chooseEncoder: () => ({ mimeType: 'audio/mp4', contentType: 'audio/mp4' }),
    isSupported: () => true,
    createRecorder: () => {
      recorder = new FakeRecorder();
      return recorder as unknown as MediaRecorder;
    },
    createAudioContext: () => null,
    acquireWakeLock: async () => null,
    persistChunk: async () => {},
    now: () => Date.now(),
  };
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
  useCaptureStore.getState().__configure({ recorder: fakeDeps() });
});

afterEach(async () => {
  await useCaptureStore.getState().discard();
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
});

describe('the capture record', () => {
  it('exists from the moment the microphone opens, so a recording killed mid-way can be found', async () => {
    /*
     * The chunks stream to IndexedDB from the first `ondataavailable`, but only
     * the `captures` row makes them findable, and that row used to be written
     * on Stop. A tab reloaded — or a backgrounded PWA jettisoned by iOS —
     * during a twenty-minute dictation left the audio on disk with nothing to
     * list it, offer it back, upload it or prune it.
     */
    await useCaptureStore.getState().start();
    expect(useCaptureStore.getState().model.state).toBe('recording');
    const { localId } = useCaptureStore.getState().model;
    expect(localId).not.toBeNull();
    await flush();

    const record = await readCaptureRecord(localId!);
    expect(record).toBeDefined();
    expect(record?.uploadedAt).toBeNull();
    expect(record?.serverCaptureId).toBeNull();
    expect(record?.contentType).toBe('audio/mp4');

    // And it is what a fresh boot would offer back.
    const stranded = await unconfirmedCaptures();
    expect(stranded.map((r) => r.localId)).toContain(localId);
  });

  it('is refreshed with the final size and duration when the recording stops', async () => {
    await useCaptureStore.getState().start();
    const { localId } = useCaptureStore.getState().model;
    await flush();
    const early = await readCaptureRecord(localId!);
    expect(early?.bytes).toBe(0);

    recorder.emitChunk(2_048);
    recorder.emitChunk(1_024);
    await useCaptureStore.getState().stop();
    await flush();
    expect(useCaptureStore.getState().model.state).toBe('review');

    const late = await readCaptureRecord(localId!);
    expect(late?.bytes).toBe(3_072);
    expect(late?.chunkCount).toBe(2);
    expect(late?.uploadedAt).toBeNull();
  });

  it('is removed when the recording is discarded', async () => {
    await useCaptureStore.getState().start();
    const { localId } = useCaptureStore.getState().model;
    await flush();
    expect(await readCaptureRecord(localId!)).toBeDefined();

    await useCaptureStore.getState().discard();
    expect(await readCaptureRecord(localId!)).toBeUndefined();
  });
});

/**
 * MediaRecorder hands over its final chunk *after* `stop()` returns. A
 * recorder that does the same, so Cancel can be tested against the timing
 * that produced the orphans.
 */
class LateChunkRecorder extends FakeRecorder {
  override stop(): void {
    this.state = 'inactive';
    queueMicrotask(() => {
      this.emitChunk(512);
      this.onstop?.();
    });
  }
}

describe('Cancel leaves nothing behind on the device', () => {
  it('drops the chunk the recorder delivers after stop, and prunes the ones before it', async () => {
    /*
     * QA D15: four cancels, `captureChunks` 1 → 2 → 3 → 4 while `captures`
     * stayed 0. The last chunk arrived after the prune, so it was never
     * pruned; nothing lists it and nothing ever removes it.
     */
    useCaptureStore.getState().__configure({
      recorder: {
        ...fakeDeps(),
        createRecorder: () => {
          recorder = new LateChunkRecorder();
          return recorder as unknown as MediaRecorder;
        },
        // The real buffer, so the count is what a device would hold.
        persistChunk: appendChunk,
      },
    });
    const store = useCaptureStore.getState();
    await store.start();
    recorder.emitChunk(1_024);
    await flush();

    await store.discard();
    // Let the recorder's late chunk arrive and any write settle.
    await flush();
    await flush();

    const db = await openChintanDB();
    expect(await db.count('captureChunks')).toBe(0);
    expect(await db.count('captures')).toBe(0);
    expect(useCaptureStore.getState().model.state).toBe('idle');
  });

  it('leaves nothing when Cancel comes while the microphone is still being asked for', async () => {
    let release: (stream: MediaStream) => void = () => {};
    useCaptureStore.getState().__configure({
      recorder: {
        ...fakeDeps(),
        requestMicrophone: () =>
          new Promise<MediaStream>((resolve) => {
            release = resolve;
          }),
        createRecorder: () => {
          recorder = new LateChunkRecorder();
          return recorder as unknown as MediaRecorder;
        },
        persistChunk: appendChunk,
      },
    });
    const store = useCaptureStore.getState();
    const started = store.start();
    expect(useCaptureStore.getState().model.state).toBe('requesting');

    await store.discard();
    release(new FakeStream() as unknown as MediaStream);
    await started;
    await flush();

    const db = await openChintanDB();
    expect(await db.count('captureChunks')).toBe(0);
    expect(await db.count('captures')).toBe(0);
    expect(recorder.state).toBe('inactive');
  });
});

describe('the previous recording is gone before the next one is requested', () => {
  class FakeAnalyser {
    fftSize = 1024;
    smoothingTimeConstant = 0;
    getByteTimeDomainData(frame: Uint8Array): void {
      frame.fill(255);
    }
  }

  it('reads no amplitudes at the moment `requesting` becomes visible', async () => {
    /*
     * `RecorderController.start()` drops the old session, but the store used
     * to dispatch `request` first — so the render that dispatch queued could
     * be committed with the previous recording's peaks still there to read.
     * Observed through a subscriber, which sees the store exactly as that
     * render would.
     */
    useCaptureStore.getState().__configure({
      recorder: {
        ...fakeDeps(),
        createAudioContext: () =>
          ({
            state: 'running',
            createMediaStreamSource: () => ({ connect() {} }),
            createAnalyser: () => new FakeAnalyser(),
            close: () => Promise.resolve(),
          }) as unknown as AudioContext,
      },
    });

    await useCaptureStore.getState().start();
    await new Promise((resolve) => setTimeout(resolve, 120));
    expect(useCaptureStore.getState().amplitudes(8).length).toBeGreaterThan(0);
    recorder.emitChunk(10);
    await useCaptureStore.getState().stop();
    expect(useCaptureStore.getState().model.state).toBe('review');

    let atRequest: number[] | null = null;
    const unsubscribe = useCaptureStore.subscribe((state) => {
      if (state.model.state === 'requesting' && atRequest === null) {
        atRequest = state.amplitudes(8);
      }
    });
    await useCaptureStore.getState().start();
    unsubscribe();

    expect(atRequest).toEqual([]);
  });
});
