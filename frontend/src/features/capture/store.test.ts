import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { readCaptureRecord, unconfirmedCaptures } from './buffer.ts';
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
