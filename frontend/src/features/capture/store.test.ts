import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { ChintanApi } from '@/api/endpoints.ts';
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

describe('Send from the recording screen', () => {
  /** A server that answers every create with a credential the fake `put` never uses. */
  function fakeApi(creates: { body: unknown; key: string }[]): ChintanApi {
    return {
      createCapture: async (body: unknown, key: string) => {
        creates.push({ body, key });
        return {
          capture: { id: `srv-${creates.length}`, status: 'uploaded', created_at: '', version: 1 },
          upload: {
            url: 'https://s3.test/audio',
            expires_at: new Date(Date.now() + 60_000).toISOString(),
            max_bytes: 1_000_000,
          },
        };
      },
    } as unknown as ChintanApi;
  }

  /** The state the machine was in each time the uploader read the buffer. */
  let assembledAt: string[] = [];

  beforeEach(() => {
    assembledAt = [];
    useCaptureStore.getState().__configure({
      recorder: {
        ...fakeDeps(),
        // The real recorder's timing: the last chunk and `onstop` land after
        // `stop()` has returned, which is the whole reason this is not
        // `await stop(); await send()`.
        createRecorder: () => {
          recorder = new LateChunkRecorder();
          return recorder as unknown as MediaRecorder;
        },
        // A fixed clock, so two recordings report the same length.
        now: () => 1_000,
      },
      upload: {
        assemble: async () => {
          assembledAt.push(useCaptureStore.getState().model.state);
          return new Blob(['audio']);
        },
        put: async () => {},
        confirm: async () => {},
        saveRecord: async () => {},
      },
    });
  });

  const settled = async (state: string) => {
    for (let attempt = 0; attempt < 50; attempt += 1) {
      if (useCaptureStore.getState().model.state === state) return;
      await flush();
    }
    throw new Error(`never reached ${state}: ${useCaptureStore.getState().model.state}`);
  };

  it('stops first, then sends what Stop followed by Send would have sent', async () => {
    const creates: { body: unknown; key: string }[] = [];
    const api = fakeApi(creates);
    const states: string[] = [];
    const unsubscribe = useCaptureStore.subscribe((state) => {
      if (states.at(-1) !== state.model.state) states.push(state.model.state);
    });

    // One tap: Send while recording.
    await useCaptureStore.getState().start('roof-repair');
    recorder.emitChunk(2_048);
    await useCaptureStore.getState().stopAndSend(api);
    await settled('uploaded');
    unsubscribe();

    // Two taps: Stop, then Send from review.
    useCaptureStore.getState().reset();
    await useCaptureStore.getState().start('roof-repair');
    recorder.emitChunk(2_048);
    await useCaptureStore.getState().stop();
    await settled('review');
    await useCaptureStore.getState().send(api);
    await settled('uploaded');

    // The recorder was stopped and the buffer complete before it was read.
    expect(states).toEqual(['requesting', 'recording', 'stopping', 'review', 'uploading', 'uploaded']);
    expect(assembledAt).toEqual(['uploading', 'uploading']);
    // And the server saw the same request both ways, under each take's own key.
    expect(creates).toHaveLength(2);
    expect(creates[0]?.body).toEqual(creates[1]?.body);
    expect(creates[0]?.body).toMatchObject({ note_id: 'roof-repair', content_type: 'audio/mp4' });
    expect(creates[0]?.key).not.toBe(creates[1]?.key);
  });

  it('sends nothing when the stop finds no audio, and does not send the next take by itself', async () => {
    const creates: { body: unknown; key: string }[] = [];
    const api = fakeApi(creates);

    await useCaptureStore.getState().start();
    // The late recorder's final chunk is what makes this take non-empty, so
    // a plain fake is used here to have a stop with nothing behind it.
    recorder = new FakeRecorder();
    useCaptureStore.getState().__configure({
      recorder: { ...fakeDeps(), now: () => 1_000 },
    });
    await useCaptureStore.getState().start();
    await useCaptureStore.getState().stopAndSend(api);
    await settled('failed');
    expect(useCaptureStore.getState().model.failure?.message).toBe('Nothing was recorded.');
    expect(creates).toHaveLength(0);

    // The request died with that take: the next Stop lands on review and stays there.
    useCaptureStore.getState().reset();
    await useCaptureStore.getState().start();
    recorder.emitChunk(512);
    await useCaptureStore.getState().stop();
    await settled('review');
    await flush();
    await flush();
    expect(useCaptureStore.getState().model.state).toBe('review');
    expect(creates).toHaveLength(0);
  });
});

describe('a Send outside review does nothing', () => {
  it('ignores a send while the recorder is still finishing, and sends once it has', async () => {
    /*
     * The reducer drops an `uploadStart` from `stopping`, but the uploader
     * used to run regardless: it assembled and PUT a buffer the recorder was
     * still writing to — the last 512 bytes of a take were missing from the
     * body — then pruned it, with the machine never having said "uploading".
     * `stopAndSend` is the supported way to send from the recording screen;
     * a bare `send` in any other state has to be a no-op.
     */
    let assembled = 0;
    const creates: string[] = [];
    useCaptureStore.getState().__configure({
      recorder: fakeDeps(),
      upload: {
        assemble: async () => {
          assembled += 1;
          return new Blob(['audio']);
        },
        put: async () => {},
        confirm: async () => {},
        saveRecord: async () => {},
      },
    });
    const api = {
      createCapture: async (_body: unknown, key: string) => {
        creates.push(key);
        return {
          capture: { id: 'srv-1', status: 'uploaded', created_at: '', version: 1 },
          upload: {
            url: 'https://s3.test/audio',
            expires_at: new Date(Date.now() + 60_000).toISOString(),
            max_bytes: 1_000_000,
          },
        };
      },
    } as unknown as ChintanApi;

    await useCaptureStore.getState().start();
    recorder.emitChunk(1_024);
    // The machine is told to stop without the recorder finishing: the state
    // a Send tapped on the frame after Stop finds.
    useCaptureStore.getState().dispatch({ type: 'stop', now: Date.now() });
    expect(useCaptureStore.getState().model.state).toBe('stopping');

    await useCaptureStore.getState().send(api);
    expect(assembled).toBe(0);
    expect(creates).toEqual([]);
    expect(useCaptureStore.getState().model.state).toBe('stopping');

    // Once the recorder has handed over its last chunk, the same send works.
    useCaptureStore.getState().dispatch({ type: 'finalised' });
    expect(useCaptureStore.getState().model.state).toBe('review');
    await useCaptureStore.getState().send(api);
    expect(assembled).toBe(1);
    expect(creates).toHaveLength(1);
    expect(useCaptureStore.getState().model.state).toBe('uploaded');
  });
});

describe('Retry after the upload link has expired', () => {
  const STALE = 'https://s3.test/stale';
  const FRESH = 'https://s3.test/fresh';

  afterEach(() => {
    vi.useRealTimers();
  });

  it('asks for a fresh credential under a new key instead of replaying the dead one', async () => {
    /*
     * The uploader has re-keyed a resume past an expired presign since
     * 2026-08-08, but only when the request names the server capture — and
     * the store never passed it, although the machine held it from the first
     * create. So every Retry tap, and every resend on reconnect after a long
     * offline stretch, replayed the original create verbatim and landed on
     * "the upload link had expired"; only a reload, through ResumePrompt,
     * ever recovered.
     */
    vi.useFakeTimers({ toFake: ['Date'] });
    const creates: string[] = [];
    const puts: string[] = [];
    let putFails = true;
    useCaptureStore.getState().__configure({
      recorder: fakeDeps(),
      upload: {
        assemble: async () => new Blob(['audio']),
        put: async (upload) => {
          puts.push(upload.url);
          if (putFails) throw new Error('socket closed');
        },
        confirm: async () => {},
        saveRecord: async () => {},
      },
    });
    const api = {
      // The server replays the first create, credential and all, for the
      // recording's own key; any other key mints a live one.
      createCapture: async (_body: unknown, key: string) => {
        creates.push(key);
        const first = creates.length === 1 || key === creates[0];
        return {
          capture: { id: first ? 'srv-1' : 'srv-2', status: 'uploaded', created_at: '', version: 1 },
          upload: {
            url: first ? STALE : FRESH,
            expires_at: new Date(
              (first ? Date.parse('2026-09-21T10:30:00.000Z') : Date.now() + 30 * 60_000),
            ).toISOString(),
            max_bytes: 1_000_000,
          },
        };
      },
      getCapture: async () => ({ id: 'srv-1', status: 'uploaded', created_at: '', version: 1 }),
    } as unknown as ChintanApi;

    vi.setSystemTime(Date.parse('2026-09-21T10:00:00.000Z'));
    await useCaptureStore.getState().start();
    recorder.emitChunk(1_024);
    await useCaptureStore.getState().stop();
    expect(useCaptureStore.getState().model.state).toBe('review');

    // The create lands, the PUT does not: a Retry is what the row offers.
    await useCaptureStore.getState().send(api);
    expect(useCaptureStore.getState().model.state).toBe('failed');
    expect(useCaptureStore.getState().model.serverCaptureId).toBe('srv-1');
    expect(puts).toEqual([STALE]);

    // Forty-five minutes later — a reconnect, or a tap.
    vi.setSystemTime(Date.parse('2026-09-21T10:45:00.000Z'));
    putFails = false;
    await useCaptureStore.getState().send(api);

    expect(useCaptureStore.getState().model.state).toBe('uploaded');
    expect(useCaptureStore.getState().model.serverCaptureId).toBe('srv-2');
    const localId = creates[0];
    expect(creates).toHaveLength(3);
    expect(creates[1]).toBe(localId);
    expect(creates[2]).not.toBe(localId);
    expect(puts).toEqual([STALE, FRESH]);
  });
});
