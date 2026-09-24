import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { INITIAL_CAPTURE } from '@/features/capture/machine.ts';
import type { RecorderDeps } from '@/features/capture/recorder.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { MIN_TALK_MS } from '@/features/capture/useHoldToTalk.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';

import { TalkScreen } from './TalkScreen.tsx';

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

class FakeRecorder {
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

const fakeDeps: RecorderDeps = {
  requestMicrophone: async () => new FakeStream() as unknown as MediaStream,
  chooseEncoder: () => ({ mimeType: 'audio/webm;codecs=opus', contentType: 'audio/webm' }),
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

const creates: { note_id: string | null }[] = [];
const acceptingFetch: typeof fetch = async (input, init) => {
  const body = (payload: unknown, status = 200) =>
    new Response(JSON.stringify(payload), {
      status,
      headers: { 'content-type': 'application/json' },
    });
  if (String(input).includes('/v1/captures') && init?.method === 'POST') {
    creates.push(JSON.parse(String(init.body)) as { note_id: string | null });
    return body(
      {
        capture: { id: `srv-${String(creates.length)}`, status: 'uploaded', created_at: '', version: 1 },
        upload: {
          url: 'https://s3.test/audio',
          expires_at: new Date(Date.now() + 60_000).toISOString(),
          max_bytes: 1_000_000,
        },
      },
      201,
    );
  }
  return body({ items: TEST_NOTES });
};

function mount(path = '/talk') {
  return render(
    <TestProviders api={testApiContext(acceptingFetch)}>
      <MemoryRouter initialEntries={[path]}>
        <TalkScreen />
      </MemoryRouter>
    </TestProviders>,
  );
}

const wait = (ms: number) => act(() => new Promise((resolve) => setTimeout(resolve, ms)));
const state = () => useCaptureStore.getState().model.state;

beforeEach(() => {
  creates.length = 0;
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
});

afterEach(() => {
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
});

describe('the talk screen', () => {
  it('records from the first frame of a press, sends on release, and is ready again', async () => {
    mount('/talk?note=roof-repair');
    const button = screen.getByRole('button', { name: 'Hold to talk' });
    // The target pill, seeded from the URL as on the capture screen.
    expect(await screen.findByRole('button', { name: /into roof repair/i })).toBeInTheDocument();

    fireEvent.pointerDown(button, { pointerId: 1, pointerType: 'touch', button: 0, clientX: 0, clientY: 0 });
    await wait(50);
    expect(state()).toBe('recording');
    expect(screen.getByRole('button', { name: 'Release to send' })).toHaveAttribute('data-holding');
    await wait(MIN_TALK_MS + 100);
    act(() => {
      recorder.emitChunk(10);
    });
    fireEvent.pointerUp(button, { pointerId: 1, pointerType: 'touch' });
    fireEvent.click(button);

    expect(screen.getByRole('status')).toHaveTextContent('Sent · filing');
    await waitFor(() => {
      expect(state()).toBe('uploaded');
    });
    expect(creates).toEqual([{ note_id: 'roof-repair' }].map((c) => expect.objectContaining(c)));
    // Ready for the next one, into the same note.
    expect(screen.getByRole('button', { name: 'Hold to talk' })).toBeInTheDocument();
  });

  it('is the Space bar on a keyboard, and hints at a press too short to keep', async () => {
    mount();
    // From the page, with nothing focused, as a fresh screen is.
    fireEvent.keyDown(document.body, { key: ' ' });
    await wait(50);
    expect(state()).toBe('recording');
    // A held key repeats; the repeats are not new presses.
    fireEvent.keyDown(document.body, { key: ' ', repeat: true });
    fireEvent.keyUp(document.body, { key: ' ' });
    expect(screen.getByRole('status')).toHaveTextContent('Too short — hold to talk');
    await waitFor(() => {
      expect(state()).toBe('idle');
    });
    expect(creates).toHaveLength(0);
  });
});
