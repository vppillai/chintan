import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { INITIAL_CAPTURE, type CaptureModel } from './machine.ts';
import { useCaptureStore } from './store.ts';
import { awaitsConnection, useResendOnReconnect } from './useResendOnReconnect.ts';

function setOnline(online: boolean): void {
  Object.defineProperty(navigator, 'onLine', { value: online, configurable: true });
  window.dispatchEvent(new Event(online ? 'online' : 'offline'));
}

/** An API that mints a capture and an upload credential, as the real one does. */
function api() {
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input);
    if (url.endsWith('/v1/captures') && init?.method === 'POST') {
      return new Response(
        JSON.stringify({
          capture: { id: 'srv-1', status: 'uploaded', created_at: new Date().toISOString(), version: 1 },
          upload: {
            url: 'https://bucket.test/audio',
            expires_at: new Date(Date.now() + 900_000).toISOString(),
            max_bytes: 1_000_000,
          },
        }),
        { status: 201, headers: { 'content-type': 'application/json' } },
      );
    }
    return new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  });
  return testApiContext(fetchImpl);
}

const wrapper = ({ children }: { children: ReactNode }) => (
  <TestProviders api={api()}>{children}</TestProviders>
);

/** A recording the user tapped Send on, that the network refused. */
const UNSENT: CaptureModel = {
  ...INITIAL_CAPTURE,
  state: 'failed',
  localId: 'cap-offline',
  bytes: 20_000,
  chunks: 7,
  elapsedMs: 12_000,
  failure: {
    kind: 'upload-failed',
    message: 'The upload did not finish. Your recording is safe on this device.',
    recoverable: true,
  },
};

let sends = 0;
let unsubscribe: () => void = () => {};

beforeEach(() => {
  sends = 0;
  useCaptureStore.getState().__configure({
    upload: {
      assemble: async () => new Blob(['audio']),
      put: async () => {},
      confirm: async () => {},
      saveRecord: async () => {},
    },
  });
  // A send is observed through the machine: it leaves `failed` for
  // `uploading` the moment it is asked.
  unsubscribe = useCaptureStore.subscribe((state, previous) => {
    if (state.model.state === 'uploading' && previous.model.state !== 'uploading') sends += 1;
  });
});

afterEach(() => {
  unsubscribe();
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
  setOnline(true);
});

describe('what a reconnect is allowed to send', () => {
  it('is a recording the user asked to send that the network refused', () => {
    expect(awaitsConnection(UNSENT)).toBe(true);
  });

  it('is not a recording still at review — the user has not said Send', () => {
    expect(awaitsConnection({ ...UNSENT, state: 'review', failure: null })).toBe(false);
  });

  it('is not a spend cap, which a connection does not lift', () => {
    expect(
      awaitsConnection({
        ...UNSENT,
        failure: { kind: 'spend-capped', message: 'Cap reached', recoverable: true },
      }),
    ).toBe(false);
  });

  it('is not a failure with nothing on disk to send', () => {
    expect(awaitsConnection({ ...UNSENT, bytes: 0 })).toBe(false);
  });
});

describe('a sent recording goes out on its own when the connection returns', () => {
  it('retries the send once on offline → online', async () => {
    setOnline(false);
    useCaptureStore.setState({ model: UNSENT });
    renderHook(() => useResendOnReconnect(), { wrapper });
    expect(sends).toBe(0);

    act(() => {
      setOnline(true);
    });

    await waitFor(() => {
      expect(sends).toBe(1);
    });
    // And it went all the way: the server has it, the row can hand over.
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('uploaded');
    });
    expect(useCaptureStore.getState().model.serverCaptureId).toBe('srv-1');
  });

  it('does not send a recording the user never sent', async () => {
    setOnline(false);
    useCaptureStore.setState({ model: { ...UNSENT, state: 'review', failure: null } });
    renderHook(() => useResendOnReconnect(), { wrapper });

    act(() => {
      setOnline(true);
    });
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(sends).toBe(0);
    expect(useCaptureStore.getState().model.state).toBe('review');
  });

  it('does nothing while the connection has never dropped', async () => {
    setOnline(true);
    useCaptureStore.setState({ model: UNSENT });
    renderHook(() => useResendOnReconnect(), { wrapper });
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(sends).toBe(0);
  });
});
