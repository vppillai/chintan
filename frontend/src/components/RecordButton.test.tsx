import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { INITIAL_CAPTURE } from '@/features/capture/machine.ts';
import type { RecorderDeps } from '@/features/capture/recorder.ts';
import { useCaptureStore } from '@/features/capture/store.ts';
import { HOLD_DELAY_MS, HOLD_NOTICE_MS, MIN_TALK_MS } from '@/features/capture/useHoldToTalk.ts';
import { onAFakeClock } from '@/test/clock.ts';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';

import { RecordButton } from './RecordButton.tsx';

/* ---------------------------------------------------------------------------
   A recorder that produces one chunk on demand. jsdom has no media APIs; the
   point is the gesture, not the encoder (the fake is `CaptureScreen.test`'s).
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
let stream = new FakeStream();
let micRequests = 0;

const fakeDeps: RecorderDeps = {
  requestMicrophone: async () => {
    micRequests += 1;
    stream = new FakeStream();
    return stream as unknown as MediaStream;
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

let creates = 0;
const acceptingFetch: typeof fetch = async (input, init) => {
  const body = (payload: unknown, status = 200) =>
    new Response(JSON.stringify(payload), {
      status,
      headers: { 'content-type': 'application/json' },
    });
  if (String(input).includes('/v1/captures') && init?.method === 'POST') {
    creates += 1;
    return body(
      {
        capture: { id: `srv-${String(creates)}`, status: 'uploaded', created_at: '', version: 1 },
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

function Where() {
  const { pathname, search } = useLocation();
  return <output>{pathname + search}</output>;
}

function mount(noteId: string | null = null, path = '/') {
  return render(
    <TestProviders api={testApiContext(acceptingFetch)}>
      <MemoryRouter initialEntries={[path]}>
        <RecordButton noteId={noteId} />
        <Routes>
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </TestProviders>,
  );
}

const wait = (ms: number) => act(() => new Promise((resolve) => setTimeout(resolve, ms)));

const down = { pointerId: 1, pointerType: 'touch', button: 0, clientX: 100, clientY: 700 };
const state = () => useCaptureStore.getState().model.state;
/** Where the router is: the probe's `<output>`, which is also a status, so it is read by tag. */
const where = () => document.querySelector('output')?.textContent;
const overlay = () => document.querySelector('.hold-overlay');
/** The one live region the overlay speaks from; the probe's `<output>` is a status too. */
const status = () => document.querySelector('p[role="status"]');
/** The OS covering the page — a call, the lock screen — which jsdom cannot do on its own. */
const pageHidden = (hidden: boolean) => {
  act(() => {
    Object.defineProperty(document, 'visibilityState', {
      value: hidden ? 'hidden' : 'visible',
      configurable: true,
    });
    document.dispatchEvent(new Event('visibilitychange'));
  });
};

beforeEach(() => {
  micRequests = 0;
  creates = 0;
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

/**
 * Push-to-talk on the mic (owner feedback 2026-09-24). A tap is still the
 * capture screen; a hold records in place and letting go sends.
 */
describe('the record button, held', () => {
  it('still opens the capture screen on a tap, without touching the microphone', async () => {
    mount();
    const mic = screen.getByRole('button', { name: /^PTT: tap to record/ });
    await onAFakeClock(() => {
      fireEvent.pointerDown(mic, down);
      act(() => {
        vi.advanceTimersByTime(50);
      });
      fireEvent.pointerUp(mic, down);
      fireEvent.click(mic);
    });
    expect(where()).toBe('/capture');
    expect(micRequests).toBe(0);
    expect(state()).toBe('idle');
  });

  it('records into the note while held, and sends on release', async () => {
    mount('roof-repair');
    const mic = screen.getByRole('button', { name: /^PTT into this note/ });

    fireEvent.pointerDown(mic, down);
    await wait(HOLD_DELAY_MS + 50);
    // The microphone is live, the card above the bar says what release does,
    // and the button is named for the state it is in.
    expect(state()).toBe('recording');
    expect(useCaptureStore.getState().model.noteId).toBe('roof-repair');
    expect(overlay()).toHaveTextContent('Release to send · slide away to cancel');
    expect(screen.getByRole('button', { name: 'Recording: release to send' })).toHaveAttribute(
      'data-holding',
    );
    expect(document.querySelector('canvas.waveform')).not.toBeNull();

    await wait(MIN_TALK_MS + 100);
    act(() => {
      recorder.emitChunk(10);
    });
    fireEvent.pointerUp(mic, down);
    // The click that ends the hold is not a tap: no navigation.
    fireEvent.click(mic);
    expect(where()).toBe('/');

    await waitFor(() => {
      expect(state()).toBe('uploaded');
    });
    expect(recorder.state).toBe('inactive');
    expect(creates).toBe(1);
    expect(screen.queryByText(/release to send/i)).toBeNull();
  });

  it('discards a hold too short to be a message, with a hint, and one that slid away, silently', async () => {
    mount();
    const mic = screen.getByRole('button', { name: /^PTT: tap to record/ });
    // The live region is there before anything is said in it, and stays the
    // same node: a screen reader announces what a region changes to, not
    // what a freshly mounted one contains.
    const region = status();
    expect(region).toHaveTextContent('');

    // Too short: the microphone opened, but there is no message in 100 ms.
    await onAFakeClock(async () => {
      fireEvent.pointerDown(mic, down);
      await wait(HOLD_DELAY_MS + 100);
      expect(state()).toBe('recording');
      expect(status()).toBe(region);
      expect(region).toHaveTextContent('Release to send · slide away to cancel');
      act(() => {
        vi.advanceTimersByTime(100);
      });
      fireEvent.pointerUp(mic, down);
      fireEvent.click(mic);
      expect(overlay()).toHaveTextContent('Too short — hold to talk');
      expect(status()).toBe(region);
      expect(region).toHaveTextContent('Too short — hold to talk');
    });
    await waitFor(() => {
      expect(state()).toBe('idle');
    });
    expect(creates).toBe(0);
    // And no navigation for that click either.
    expect(where()).toBe('/');

    // Slid away: the card says so, and release cancels without a hint.
    fireEvent.pointerDown(mic, down);
    await wait(HOLD_DELAY_MS + 50);
    expect(state()).toBe('recording');
    fireEvent.pointerMove(mic, { ...down, clientX: 100, clientY: 600 });
    expect(overlay()).toHaveTextContent('Release to cancel');
    expect(mic).toHaveAttribute('data-away');
    await wait(MIN_TALK_MS + 100);
    act(() => {
      recorder.emitChunk(10);
    });
    fireEvent.pointerUp(mic, down);
    await waitFor(() => {
      expect(state()).toBe('idle');
    });
    expect(creates).toBe(0);
    expect(screen.queryByText('Too short — hold to talk')).toBeNull();
  });

  it('sends what was said before a call ended the track, rather than discarding it', async () => {
    mount();
    const mic = screen.getByRole('button', { name: /^PTT: tap to record/ });
    fireEvent.pointerDown(mic, down);
    await wait(HOLD_DELAY_MS + 50);
    expect(state()).toBe('recording');
    await wait(MIN_TALK_MS + 100);
    act(() => {
      recorder.emitChunk(10);
    });

    // The track ends under the finger — a phone call, the headset coming out.
    // The machine settles the take on its own; the hold must treat that as
    // the message, not as a microphone that never came up.
    act(() => {
      stream.track.dispatchEvent(new Event('ended'));
    });
    await waitFor(() => {
      expect(state()).toBe('review');
    });
    expect(overlay()).toHaveTextContent('Release to send');

    fireEvent.pointerUp(mic, down);
    fireEvent.click(mic);
    await waitFor(() => {
      expect(state()).toBe('uploaded');
    });
    expect(creates).toBe(1);
    expect(where()).toBe('/');
  });

  it('ends a hold as a release when the page is hidden, so a slip gets the hint rather than the mic', async () => {
    // A call, the lock screen, an app switch: not every browser sends
    // `pointercancel` for it, and the microphone stayed open until the next
    // press, whose release sent everything recorded meanwhile. The hold ends
    // as a release — the rule is a partial recording, never a discard — and
    // one hidden inside MIN_TALK_MS is a slip.
    mount();
    const mic = screen.getByRole('button', { name: /^PTT: tap to record/ });
    await onAFakeClock(async () => {
      fireEvent.pointerDown(mic, down);
      act(() => {
        vi.advanceTimersByTime(HOLD_DELAY_MS + 50);
      });
      await waitFor(() => {
        expect(state()).toBe('recording');
      });

      pageHidden(true);
      expect(overlay()).toHaveTextContent('Too short — hold to talk');
      await waitFor(() => {
        expect(state()).toBe('idle');
      });
      pageHidden(false);

      // Back on the page, the finger lifts: not a send, and not a tap either.
      fireEvent.pointerUp(mic, down);
      fireEvent.click(mic);
    });
    expect(creates).toBe(0);
    expect(where()).toBe('/');
  });

  it('sends what was said before the page was hidden, rather than discarding it', async () => {
    // An incoming call on Android both ends the track and covers Chrome; the
    // words before it are the message, as when only the track ends.
    mount('roof-repair');
    const mic = screen.getByRole('button', { name: /^PTT into this note/ });
    await onAFakeClock(async () => {
      fireEvent.pointerDown(mic, down);
      act(() => {
        vi.advanceTimersByTime(HOLD_DELAY_MS + 50);
      });
      await waitFor(() => {
        expect(state()).toBe('recording');
      });
      act(() => {
        vi.advanceTimersByTime(MIN_TALK_MS + 100);
      });
      act(() => {
        recorder.emitChunk(10);
      });

      pageHidden(true);
      await waitFor(() => {
        expect(state()).toBe('uploaded');
      });
      pageHidden(false);
      fireEvent.pointerUp(mic, down);
      fireEvent.click(mic);
    });
    expect(recorder.state).toBe('inactive');
    expect(creates).toBe(1);
    expect(where()).toBe('/');
  });

  it('says the last one is still sending when held mid-upload, and leaves the tap to the capture screen', async () => {
    act(() => {
      useCaptureStore.setState({
        model: { ...INITIAL_CAPTURE, state: 'uploading', localId: 'busy', uploadProgress: 0.4 },
      });
    });
    mount();
    const mic = screen.getByRole('button', { name: /^PTT: tap to record/ });

    await onAFakeClock(() => {
      // Held: a word rather than a dead button, and the release is not a tap.
      fireEvent.pointerDown(mic, down);
      act(() => {
        vi.advanceTimersByTime(HOLD_DELAY_MS + 50);
      });
      expect(micRequests).toBe(0);
      expect(state()).toBe('uploading');
      expect(overlay()).toHaveTextContent('Still sending the last one…');
      fireEvent.pointerUp(mic, down);
      fireEvent.click(mic);
      expect(where()).toBe('/');
      // The notice outlives the release that would otherwise have cleared its timer.
      act(() => {
        vi.advanceTimersByTime(HOLD_NOTICE_MS + 50);
      });
      expect(overlay()).toBeNull();

      // Tapped: the capture screen, where the upload is shown with its bar.
      fireEvent.pointerDown(mic, down);
      act(() => {
        vi.advanceTimersByTime(50);
      });
      fireEvent.pointerUp(mic, down);
      fireEvent.click(mic);
      expect(where()).toBe('/capture');
    });
  });

  it('only taps on /talk, where the screen\'s own button is the hold', async () => {
    mount(null, '/talk');
    const mic = screen.getByRole('button', { name: /^PTT: tap to record/ });
    fireEvent.pointerDown(mic, down);
    await wait(HOLD_DELAY_MS + 100);
    expect(micRequests).toBe(0);
    expect(state()).toBe('idle');
    fireEvent.pointerUp(mic, down);
    fireEvent.click(mic);
    expect(where()).toBe('/capture');
  });
});
