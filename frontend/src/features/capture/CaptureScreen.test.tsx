import { act, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { TestProviders } from '@/test/providers.tsx';

import { CaptureScreen } from './CaptureScreen.tsx';
import { INITIAL_CAPTURE } from './machine.ts';
import type { RecorderDeps } from './recorder.ts';
import { useCaptureStore } from './store.ts';

/* ---------------------------------------------------------------------------
   A recorder that produces one chunk and nothing else. jsdom has no media
   APIs, and the point here is the screen's arming logic, not the encoder.
   --------------------------------------------------------------------------- */

class FakeTrack extends EventTarget {
  kind = 'audio';
  stopped = false;
  stop(): void {
    this.stopped = true;
  }
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
let micRequests = 0;

function fakeDeps(): RecorderDeps {
  return {
    requestMicrophone: async () => {
      micRequests += 1;
      return new FakeStream() as unknown as MediaStream;
    },
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
}

function mount() {
  return render(
    <TestProviders>
      <MemoryRouter initialEntries={['/capture']}>
        <Routes>
          <Route path="/capture" element={<CaptureScreen />} />
          <Route path="/" element={<p>Home</p>} />
        </Routes>
      </MemoryRouter>
    </TestProviders>,
  );
}

beforeEach(() => {
  micRequests = 0;
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
  useCaptureStore.getState().__configure({ recorder: fakeDeps() });
});

afterEach(() => {
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
});

describe('opening /capture arms a recording', () => {
  it('opens the microphone on a fresh mount', async () => {
    mount();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });
    expect(micRequests).toBe(1);
  });

  it('opens the microphone again after a capture has been sent', async () => {
    /*
     * The single most damaging defect in the shipped app: one recording per
     * page load. A sent capture leaves the machine in the terminal `uploaded`
     * state, nothing reset it, and the mount guard was `state === 'idle'` — so
     * every later tap of Record showed "Sent" and the previous recording's
     * elapsed time, bounced back Home after 600ms, and never opened the
     * microphone. Only a full page reload recovered.
     */
    act(() => {
      useCaptureStore.setState({
        model: {
          ...INITIAL_CAPTURE,
          state: 'uploaded',
          localId: 'cap-previous',
          bytes: 4_096,
          chunks: 2,
          elapsedMs: 17_000,
          uploadProgress: 1,
          serverCaptureId: 'srv-1',
        },
      });
    });

    mount();

    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });
    expect(micRequests).toBe(1);
    // Not the previous recording's clock, and not "Sent".
    expect(document.querySelector('.capture__state')).toHaveTextContent('Recording');
    expect(document.querySelector('.capture__timer')).toHaveTextContent('00:00');
    expect(useCaptureStore.getState().model.elapsedMs).toBeLessThan(17_000);
    expect(useCaptureStore.getState().model.serverCaptureId).toBeNull();
  });

  it('opens the microphone again after a failure that buffered nothing', async () => {
    // A refused permission or a recorder that produced no audio is terminal
    // with nothing to resend. Returning to the screen must ask again.
    act(() => {
      useCaptureStore.setState({
        model: {
          ...INITIAL_CAPTURE,
          state: 'failed',
          failure: { kind: 'permission-denied', message: 'No microphone access.', recoverable: false },
        },
      });
    });

    mount();

    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });
    expect(micRequests).toBe(1);
  });

  it('keeps unsent audio on screen rather than recording over it', async () => {
    // Those bytes exist in exactly one place. Arriving here must offer them
    // back, not silently start a second recording on top.
    act(() => {
      useCaptureStore.setState({
        model: { ...INITIAL_CAPTURE, state: 'review', localId: 'cap-unsent', bytes: 9_001 },
      });
    });

    mount();

    expect(await screen.findByRole('button', { name: 'Send' })).toBeInTheDocument();
    expect(micRequests).toBe(0);
    expect(useCaptureStore.getState().model.localId).toBe('cap-unsent');
  });

  it('releases a finished capture when the screen goes away', async () => {
    mount().unmount();
    act(() => {
      useCaptureStore.setState({ model: { ...INITIAL_CAPTURE, state: 'uploaded' } });
    });

    const view = mount();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });

    // Finish it, then leave: the machine is idle for whatever comes next.
    act(() => {
      useCaptureStore.setState({
        model: { ...useCaptureStore.getState().model, state: 'uploaded' },
      });
    });
    view.unmount();

    expect(useCaptureStore.getState().model.state).toBe('idle');
  });
});
