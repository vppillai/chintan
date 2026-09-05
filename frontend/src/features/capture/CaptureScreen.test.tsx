import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation, useParams } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';
import type { ApiContextValue } from '@/api/ApiProvider.tsx';

import { CaptureScreen, captureReturnPath } from './CaptureScreen.tsx';
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

/**
 * Enough of an AudioContext for the recorder to attach its analyser and feed
 * the peak collector a loud frame every tick. The start/stop tones ask it for
 * an oscillator, find none, and are swallowed as they would be under autoplay
 * policy.
 */
class FakeAnalyser {
  fftSize = 1024;
  smoothingTimeConstant = 0;
  getByteTimeDomainData(frame: Uint8Array): void {
    frame.fill(255);
  }
}

function fakeAudioContext(): AudioContext {
  return {
    state: 'running',
    createMediaStreamSource: () => ({ connect() {} }),
    createAnalyser: () => new FakeAnalyser(),
    close: () => Promise.resolve(),
  } as unknown as AudioContext;
}

/**
 * jsdom's `getContext` returns null, which stops the waveform's draw effect
 * before it reads a single amplitude. This lets the effect run to the read.
 */
function stubCanvas(): void {
  const context = {
    fillStyle: '',
    setTransform() {},
    clearRect() {},
    fillRect() {},
  } as unknown as CanvasRenderingContext2D;
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(() => context);
}

let recorder = new FakeRecorder();
let micRequests = 0;

function fakeDeps(overrides: Partial<RecorderDeps> = {}): RecorderDeps {
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
    ...overrides,
  };
}

/** Stands in for the note screen: says which note, and which tab was asked for. */
function NoteProbe() {
  const { id } = useParams();
  const { search } = useLocation();
  return (
    <p>
      Note {id}
      {search}
    </p>
  );
}

function mount(path = '/capture', api?: ApiContextValue) {
  return render(
    <TestProviders {...(api ? { api } : {})}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/capture" element={<CaptureScreen />} />
          <Route path="/notes/:id" element={<NoteProbe />} />
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

describe('the live waveform starts empty', () => {
  it('reads nothing of the previous recording on any frame before the next one is live', async () => {
    /*
     * The recorder already dropped the old session at the top of `start()`,
     * and the flash survived that. What remained: the canvas's first draw runs
     * in the child's effect, before this screen's own effect has called
     * `start()` at all, so it read the previous recording's collector — and
     * the draw effect does not re-run until the stream is live, so whatever it
     * painted stayed on the element for the whole microphone request.
     */
    stubCanvas();
    useCaptureStore
      .getState()
      .__configure({ recorder: fakeDeps({ createAudioContext: fakeAudioContext }) });

    // Recording one, with something on its envelope.
    const first = mount();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });
    await waitFor(() => {
      expect(useCaptureStore.getState().amplitudes(8).length).toBeGreaterThan(0);
    });
    recorder.emitChunk(10);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Stop' }));
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('review');
    });
    // Send hands off and the screen leaves; the controller still holds the
    // session, as it must for a retry to find the envelope.
    act(() => {
      useCaptureStore.setState({
        model: { ...useCaptureStore.getState().model, state: 'uploaded', uploadProgress: 1 },
      });
    });
    first.unmount();

    // Everything the new screen's canvas reads, and the machine's state when it did.
    const reads: { state: string; values: number[] }[] = [];
    const amplitudes = useCaptureStore.getState().amplitudes;
    useCaptureStore.setState({
      amplitudes: (count) => {
        const values = amplitudes(count);
        reads.push({ state: useCaptureStore.getState().model.state, values });
        return values;
      },
    });
    try {
      mount();
      await waitFor(() => {
        expect(useCaptureStore.getState().model.state).toBe('recording');
      });
    } finally {
      useCaptureStore.setState({ amplitudes });
    }

    const beforeLive = reads.filter((read) => read.state !== 'recording');
    // The first frame is drawn before this screen has armed anything.
    expect(beforeLive.length).toBeGreaterThan(0);
    for (const read of beforeLive) expect(read.values).toEqual([]);
  });

  it('gives each recording its own canvas', async () => {
    // A canvas keeps its bitmap until it is repainted, and the draw effect
    // only re-runs when the stream goes live. The element itself is replaced
    // the moment the next recording is requested. Arming is held back so the
    // screen can be seen with the canvas it had before that.
    const start = useCaptureStore.getState().start;
    useCaptureStore.setState({ start: async () => {} });
    try {
      mount();
      const before = document.querySelector('canvas.waveform');
      expect(before).not.toBeNull();
      await act(() => start(null));
      await waitFor(() => {
        expect(useCaptureStore.getState().model.state).toBe('recording');
      });
      const after = document.querySelector('canvas.waveform');
      expect(after).not.toBeNull();
      expect(after).not.toBe(before);
    } finally {
      useCaptureStore.setState({ start });
    }
  });
});

describe('review before send', () => {
  /** Puts the machine at review with a recording of the given length. */
  function reviewed(elapsedMs = 8_000, noteId: string | null = 'roof-repair') {
    act(() => {
      useCaptureStore.setState({
        model: {
          ...INITIAL_CAPTURE,
          state: 'review',
          localId: 'cap-review',
          bytes: 12_000,
          chunks: 4,
          elapsedMs,
          noteId,
        },
      });
    });
  }

  it('shows a player for the recording, with the length from the machine clock', async () => {
    reviewed(95_000);
    mount();

    // A slider, so the position is reachable without a pointer; its range is
    // the recording's length from the machine, not from the audio element,
    // which cannot know the duration of a WebM straight out of MediaRecorder.
    const slider = await screen.findByRole('slider', { name: 'Playback position' });
    expect(slider).toHaveAttribute('aria-valuemax', '95');
    expect(screen.getByText('1:35')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /play recording/i })).toBeInTheDocument();
    // The live canvas is gone: this is the recording, not the microphone.
    expect(document.querySelector('canvas.waveform')).toBeNull();
  });

  it('offers Send, Re-record and Discard', async () => {
    reviewed();
    mount();
    expect(await screen.findByRole('button', { name: 'Send' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Re-record' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Discard' })).toBeInTheDocument();
  });

  it('Send hands off at once, with the upload still running in the store', async () => {
    /*
     * The upload has always lived in the store, outside the tree; the only
     * thing that kept the user watching "Sending 40%" was the screen waiting
     * for it. Now Send is a tap and a navigation, and the filing row shows
     * the progress. A recording with no target goes to the library.
     */
    useCaptureStore.getState().__configure({
      upload: {
        assemble: async () => new Blob(['audio']),
        put: async () => {},
        confirm: async () => {},
        saveRecord: async () => {},
      },
    });
    // A create that does not answer until the test lets it.
    let releaseCreate: () => void = () => {};
    const created = new Promise<void>((resolve) => {
      releaseCreate = resolve;
    });
    const fetchImpl: typeof fetch = async (input, init) => {
      const url = String(input);
      const body = (payload: unknown, status = 200) =>
        new Response(JSON.stringify(payload), {
          status,
          headers: { 'content-type': 'application/json' },
        });
      if (url.includes('/v1/captures') && init?.method === 'POST') {
        await created;
        return body(
          {
            capture: { id: 'srv-1', status: 'uploaded', created_at: '', version: 1 },
            upload: {
              url: 'https://s3.test/audio',
              expires_at: new Date(Date.now() + 60_000).toISOString(),
              max_bytes: 1_000_000,
            },
          },
          201,
        );
      }
      if (url.includes('/v1/notes')) return body({ items: TEST_NOTES });
      return body({ items: [] });
    };

    reviewed(8_000, null);
    mount('/capture', testApiContext(fetchImpl));
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Send' }));

    // Home, immediately — before the server has even answered the create.
    expect(await screen.findByText('Home')).toBeInTheDocument();
    expect(useCaptureStore.getState().model.state).toBe('uploading');

    releaseCreate();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('uploaded');
    });
    expect(useCaptureStore.getState().model.serverCaptureId).toBe('srv-1');
  });

  it('Send from a recording aimed at a note returns to that note, on its recordings', async () => {
    /*
     * "Record into this", then Send, used to land on the library: a screen
     * away from the note the user had just added to, with the filing row
     * the only sign anything had happened. The note's Recordings tab shows
     * the same row, so that is where Send goes.
     */
    useCaptureStore.getState().__configure({
      upload: {
        assemble: async () => new Blob(['audio']),
        put: async () => {},
        confirm: async () => {},
        saveRecord: async () => {},
      },
    });
    const fetchImpl: typeof fetch = async (input, init) => {
      const body = (payload: unknown, status = 200) =>
        new Response(JSON.stringify(payload), {
          status,
          headers: { 'content-type': 'application/json' },
        });
      if (String(input).includes('/v1/captures') && init?.method === 'POST') {
        return body(
          {
            capture: { id: 'srv-2', status: 'uploaded', created_at: '', version: 1 },
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
    reviewed();
    mount('/capture', testApiContext(fetchImpl));
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Send' }));

    expect(await screen.findByText('Note roof-repair?tab=recordings')).toBeInTheDocument();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('uploaded');
    });
  });

  it('Discard from a recording aimed at a note returns to that note, on its text', async () => {
    reviewed();
    mount();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Discard' }));

    expect(await screen.findByText('Note roof-repair')).toBeInTheDocument();
    expect(useCaptureStore.getState().model.state).toBe('idle');
  });

  it('Re-record discards the take and opens the microphone again into the same note', async () => {
    reviewed();
    mount();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Re-record' }));

    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });
    expect(micRequests).toBe(1);
    expect(useCaptureStore.getState().model.localId).not.toBe('cap-review');
    // The target survives the retake; the audio does not.
    expect(useCaptureStore.getState().model.noteId).toBe('roof-repair');
    expect(useCaptureStore.getState().model.bytes).toBe(0);
  });
});

describe('the capture route does not wait on the library', () => {
  it('asks for no notes until the microphone is live', async () => {
    /*
     * On a cold launch of the shortcut the notes request left with the
     * microphone request and the two shared a slow link's first seconds. The
     * list is for the target pill, and the pill can wait until "Recording".
     */
    let releaseMic: () => void = () => {};
    const deps = fakeDeps();
    deps.requestMicrophone = () =>
      new Promise((resolve) => {
        releaseMic = () => {
          resolve(new FakeStream() as unknown as MediaStream);
        };
      });
    useCaptureStore.getState().__configure({ recorder: deps });

    const requested: string[] = [];
    const fetchImpl: typeof fetch = async (input) => {
      const url = new URL(String(input));
      requested.push(url.pathname);
      return new Response(JSON.stringify({ items: TEST_NOTES }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    };

    mount('/capture', testApiContext(fetchImpl));
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('requesting');
    });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
    expect(requested.filter((path) => path.endsWith('/v1/notes'))).toEqual([]);

    releaseMic();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });
    await waitFor(() => {
      expect(requested.some((path) => path.endsWith('/v1/notes'))).toBe(true);
    });
  });
});

describe('the target chooser', () => {
  it('files into a new note by default', async () => {
    mount();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });
    expect(useCaptureStore.getState().model.noteId).toBeNull();
    expect(screen.getByRole('button', { name: /into new note/i })).toBeInTheDocument();
  });

  it('files into the note named by ?note=, and says so by title', async () => {
    mount('/capture?note=roof-repair');
    await waitFor(() => {
      expect(useCaptureStore.getState().model.noteId).toBe('roof-repair');
    });
    // Named by its title, which comes from the library the app already holds.
    expect(await screen.findByRole('button', { name: /into roof repair/i })).toBeInTheDocument();
  });

  it('moves the recording to a chosen note before Send, and back to a new note', async () => {
    const user = userEvent.setup();
    mount();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('recording');
    });

    await user.click(screen.getByRole('button', { name: /into new note/i }));
    await user.click(await screen.findByRole('button', { name: 'Reading list' }));
    expect(useCaptureStore.getState().model.noteId).toBe('reading-list');
    expect(screen.getByRole('button', { name: /into reading list/i })).toBeInTheDocument();
    // The recording itself is untouched by the choice.
    expect(useCaptureStore.getState().model.state).toBe('recording');

    await user.click(screen.getByRole('button', { name: /into reading list/i }));
    await user.click(screen.getByRole('button', { name: 'New note' }));
    expect(useCaptureStore.getState().model.noteId).toBeNull();
  });
});

describe('where the capture screen goes when it is done', () => {
  it('returns to the target note — its recordings once sent — and otherwise to the library', () => {
    expect(captureReturnPath('roof-repair', true)).toBe('/notes/roof-repair?tab=recordings');
    expect(captureReturnPath('roof-repair', false)).toBe('/notes/roof-repair');
    expect(captureReturnPath(null, true)).toBe('/');
    expect(captureReturnPath(null, false)).toBe('/');
  });
});
