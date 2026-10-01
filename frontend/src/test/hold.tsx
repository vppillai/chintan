import { act, render } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { vi } from 'vitest';

import { queryKeys } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { TabBar } from '@/components/TabBar.tsx';
import { INITIAL_CAPTURE } from '@/features/capture/machine.ts';
import type { RecorderDeps } from '@/features/capture/recorder.ts';
import { useCaptureStore } from '@/features/capture/store.ts';

import { TEST_NOTES, TestProviders, testApiContext, testQueryClient } from './providers.tsx';

/**
 * The tab bar with a recorder that records nothing real, for the
 * hold-to-talk tests in `TabBar.test.tsx` and `RecordButton.test.tsx`. The
 * capture store is the real one; only the microphone, the MediaRecorder and
 * the upload's network are fakes.
 */

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

export class FakeRecorder {
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

export const fake = {
  recorder: new FakeRecorder(),
  stream: new FakeStream(),
  /** Set to hold `getUserMedia` open, as the permission prompt does. */
  micGate: null as Promise<void> | null,
  /** Every `POST /v1/captures` body. */
  creates: [] as { note_id: string | null }[],
};

const fakeDeps: RecorderDeps = {
  requestMicrophone: async () => {
    await fake.micGate;
    fake.stream = new FakeStream();
    return fake.stream as unknown as MediaStream;
  },
  chooseEncoder: () => ({ mimeType: 'audio/webm;codecs=opus', contentType: 'audio/webm' }),
  isSupported: () => true,
  createRecorder: () => {
    fake.recorder = new FakeRecorder();
    return fake.recorder as unknown as MediaRecorder;
  },
  createAudioContext: () => null,
  acquireWakeLock: async () => null,
  persistChunk: async () => {},
  now: () => Date.now(),
};

const json = (payload: unknown, status = 200) =>
  new Response(JSON.stringify(payload), {
    status,
    headers: { 'content-type': 'application/json' },
  });

function fetchFor(note?: NoteDetailWire): typeof fetch {
  return async (input, init) => {
    if (String(input).includes('/v1/captures') && init?.method === 'POST') {
      fake.creates.push(JSON.parse(String(init.body)) as { note_id: string | null });
      return json(
        {
          capture: { id: `srv-${String(fake.creates.length)}`, status: 'uploaded', created_at: '', version: 1 },
          upload: {
            url: 'https://s3.test/audio',
            expires_at: new Date(Date.now() + 60_000).toISOString(),
            max_bytes: 1_000_000,
          },
        },
        201,
      );
    }
    return note ? json(note) : json({ items: TEST_NOTES });
  };
}

/** A fresh store wired to the fakes, on a fake clock that still moves with real time. */
export function resetHold(): void {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  fake.creates.length = 0;
  fake.micGate = null;
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
  useCaptureStore.getState().__configure({
    recorder: fakeDeps,
    upload: {
      assemble: async () => new Blob(['audio']),
      put: async () => {},
      confirm: async () => {},
      saveRecord: async () => {},
    },
  });
}

export function endHold(): void {
  vi.useRealTimers();
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
}

function Where() {
  const { pathname, search } = useLocation();
  return <output>{pathname + search}</output>;
}

/** Where the router is: the probe's `<output>`. */
export const where = () => document.querySelector('output')?.textContent;

/** The bar at `path`; with `note`, the open note is in the cache as the screen would have it. */
export function mountBar(path = '/', note?: NoteDetailWire) {
  const queryClient = testQueryClient();
  if (note) queryClient.setQueryData(queryKeys.note(note.id), note);
  return render(
    <TestProviders api={testApiContext(fetchFor(note))} queryClient={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <TabBar />
        <Routes>
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </TestProviders>,
  );
}

/** Moves the fake clock on by `ms`, firing what was due, inside `act`. */
export const wait = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));

export const captureState = () => useCaptureStore.getState().model.state;

export const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

/** Some audio arrives, so a release has something to send. */
export function speak(): void {
  act(() => {
    fake.recorder.emitChunk(10);
  });
}

/** The bar's own status line (the probe's `<output>` is a status too). */
export const spoken = () => document.querySelector('.tab-bar [role="status"]');
