import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { INITIAL_CAPTURE } from '@/features/capture/machine.ts';
import type { RecorderDeps } from '@/features/capture/recorder.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { HOLD_NOTICE_MS, MIN_TALK_MS } from '@/features/capture/useHoldToTalk.ts';
import { onAFakeClock } from '@/test/clock.ts';
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
/** A test's hold on the answer to POST /v1/captures: the upload lands when it resolves. */
let postGate: Promise<void> | null = null;
const acceptingFetch: typeof fetch = async (input, init) => {
  const body = (payload: unknown, status = 200) =>
    new Response(JSON.stringify(payload), {
      status,
      headers: { 'content-type': 'application/json' },
    });
  if (String(input).includes('/v1/captures') && init?.method === 'POST') {
    creates.push(JSON.parse(String(init.body)) as { note_id: string | null });
    await postGate;
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
const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

beforeEach(() => {
  creates.length = 0;
  postGate = null;
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
  it('records from the first frame of a press, says Sending until the server has it, and is ready again', async () => {
    mount('/talk?note=roof-repair');
    const button = screen.getByRole('button', { name: /^PTT: hold to talk/ });
    // The target pill, seeded from the URL as on the capture screen.
    expect(await screen.findByRole('button', { name: /into roof repair/i })).toBeInTheDocument();

    await onAFakeClock(async () => {
      fireEvent.pointerDown(button, { pointerId: 1, pointerType: 'touch', button: 0, clientX: 0, clientY: 0 });
      await wait(50);
      expect(state()).toBe('recording');
      expect(screen.getByRole('button', { name: 'Release to send' })).toHaveAttribute('data-holding');
      act(() => {
        vi.advanceTimersByTime(MIN_TALK_MS + 100);
      });
      act(() => {
        recorder.emitChunk(10);
      });
      fireEvent.pointerUp(button, { pointerId: 1, pointerType: 'touch' });
      fireEvent.click(button);

      // The upload's row under the button reads "Uploading… N%" until the PUT
      // lands; a status above it already saying "Sent" contradicted it.
      expect(document.querySelector('.talk__status')).toHaveTextContent('Sending…');
      await waitFor(() => {
        expect(state()).toBe('uploaded');
      });
      expect(document.querySelector('.talk__status')).toHaveTextContent('Sent · filing');
    });
    expect(creates).toEqual([{ note_id: 'roof-repair' }].map((c) => expect.objectContaining(c)));
    // Ready for the next one, into the same note.
    expect(screen.getByRole('button', { name: /^PTT: hold to talk/ })).toBeInTheDocument();
  });

  it('still says Sending when the upload outlives the hold notice, and Sent once it lands', async () => {
    // A real clip on a phone link uploads for longer than HOLD_NOTICE_MS. Read
    // from the hold's phase the status went blank at 1.5 s over a row still
    // saying "Uploading… N%" and never said Sent; it is the store's.
    let land: () => void = () => {};
    postGate = new Promise<void>((resolve) => {
      land = resolve;
    });
    mount();
    const button = screen.getByRole('button', { name: /^PTT: hold to talk/ });
    await onAFakeClock(async () => {
      fireEvent.pointerDown(button, { ...touch, clientX: 0, clientY: 0 });
      await wait(50);
      expect(state()).toBe('recording');
      act(() => {
        vi.advanceTimersByTime(MIN_TALK_MS + 100);
      });
      act(() => {
        recorder.emitChunk(10);
      });
      fireEvent.pointerUp(button, touch);
      fireEvent.click(button);
      await waitFor(() => {
        expect(state()).toBe('uploading');
      });
      act(() => {
        vi.advanceTimersByTime(HOLD_NOTICE_MS + 100);
      });
      expect(document.querySelector('.talk__status')).toHaveTextContent('Sending…');

      land();
      await waitFor(() => {
        expect(state()).toBe('uploaded');
      });
      expect(document.querySelector('.talk__status')).toHaveTextContent('Sent · filing');
    });
    expect(creates).toHaveLength(1);
  });

  it('is the Space bar on a keyboard, and hints at a press too short to keep', async () => {
    mount();
    await onAFakeClock(async () => {
      // From the page, with nothing focused, as a fresh screen is.
      fireEvent.keyDown(document.body, { key: ' ' });
      await wait(50);
      expect(state()).toBe('recording');
      // A held key repeats; the repeats are not new presses.
      fireEvent.keyDown(document.body, { key: ' ', repeat: true });
      // Lifted 100 ms in: fewer than MIN_TALK_MS, however long the runner paused.
      act(() => {
        vi.advanceTimersByTime(100);
      });
      fireEvent.keyUp(document.body, { key: ' ' });
      expect(screen.getByRole('status')).toHaveTextContent('Too short — hold to talk');
    });
    await waitFor(() => {
      expect(state()).toBe('idle');
    });
    expect(creates).toHaveLength(0);
  });

  it('cancels a Space-bar hold when the window loses focus, as a finger gets pointercancel', async () => {
    mount();
    fireEvent.keyDown(document.body, { key: ' ' });
    await wait(50);
    expect(state()).toBe('recording');
    // Alt-Tab, the lock screen, a notification: the keyup never arrives.
    fireEvent.blur(window);
    await waitFor(() => {
      expect(state()).toBe('idle');
    });
    expect(screen.getByRole('button', { name: /^PTT: hold to talk/ })).toBeInTheDocument();
    expect(creates).toHaveLength(0);
    // And the next press is a new hold, not blocked by the one that never released.
    fireEvent.keyDown(document.body, { key: ' ' });
    await wait(50);
    expect(state()).toBe('recording');
  });

  it('says the last one is still sending rather than going dead under a second press', async () => {
    act(() => {
      useCaptureStore.setState({
        model: { ...INITIAL_CAPTURE, state: 'uploading', localId: 'busy', uploadProgress: 0.4 },
      });
    });
    mount();
    const button = screen.getByRole('button', { name: /^PTT: hold to talk/ });
    fireEvent.pointerDown(button, { ...touch, clientX: 0, clientY: 0 });
    expect(state()).toBe('uploading');
    expect(document.querySelector('.talk__status')).toHaveTextContent('Still sending the last one…');
    fireEvent.pointerUp(button, touch);
    fireEvent.click(button);
    expect(document.querySelector('.talk__status')).toHaveTextContent('Still sending the last one…');
  });

  it('measures sliding away from the disc, not from where the thumb landed on it', async () => {
    mount();
    const button = screen.getByRole('button', { name: /^PTT: hold to talk/ });
    // jsdom lays nothing out; give the disc the box it has on a phone.
    button.getBoundingClientRect = () =>
      ({ x: 0, y: 0, left: 0, top: 0, right: 400, bottom: 400, width: 400, height: 400 }) as DOMRect;
    fireEvent.pointerDown(button, { ...touch, clientX: 200, clientY: 200 });
    await wait(50);
    expect(state()).toBe('recording');

    // A hand's width of drift, still on the disc: not a cancel.
    fireEvent.pointerMove(button, { ...touch, clientX: 200, clientY: 350 });
    expect(button).not.toHaveAttribute('data-away');
    expect(button).toHaveTextContent('Release to send');
    // Off it by more than the margin.
    fireEvent.pointerMove(button, { ...touch, clientX: 200, clientY: 500 });
    expect(button).toHaveAttribute('data-away');
    expect(button).toHaveTextContent('Release to cancel');
    // And back on.
    fireEvent.pointerMove(button, { ...touch, clientX: 390, clientY: 390 });
    expect(button).not.toHaveAttribute('data-away');

    fireEvent.keyDown(document.body, { key: 'Escape' });
    await waitFor(() => {
      expect(state()).toBe('idle');
    });
    expect(creates).toHaveLength(0);
  });
});
