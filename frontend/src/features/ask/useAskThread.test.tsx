import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it } from 'vitest';

import { askPending } from '@/api/__fixtures__/pending.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { THREAD_KEY, loadThread } from './thread.ts';
import { useAskThread } from './useAskThread.ts';

/**
 * The panel's own behaviour is tested through the screen in
 * `AskPanel.test.tsx`. This file is for what the hook has to get right when
 * there is no screen: an outcome that lands after the library has unmounted.
 */

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

/** A `POST /v1/ask` that answers 202 only once the test says so. */
function heldPost(): { fetchImpl: typeof fetch; release: () => void } {
  let release: () => void = () => undefined;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fetchImpl: typeof fetch = async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/v1/ask') && init?.method === 'POST') {
      await gate;
      return json(askPending, 202);
    }
    return json({ items: [] });
  };
  return { fetchImpl, release };
}

afterEach(() => {
  sessionStorage.clear();
});

describe('a question whose 202 lands after the screen has gone', () => {
  it('still records its row in the saved thread, so it is polled — not shown as never sent — on return', async () => {
    const { fetchImpl, release } = heldPost();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <TestProviders api={testApiContext(fetchImpl)}>{children}</TestProviders>
    );
    const { result, unmount } = renderHook(() => useAskThread(), { wrapper });

    act(() => {
      result.current.ask('what did I decide about the roof?');
    });
    // Read raw: `loadThread` would already call an `asking` turn never sent.
    const saved = JSON.parse(sessionStorage.getItem(THREAD_KEY) ?? '[]') as unknown[];
    expect(saved[0]).toMatchObject({ status: 'asking', askId: null });

    // A source chip tapped while the POST is in flight: the library is gone.
    unmount();
    release();

    await waitFor(() => {
      expect(sessionStorage.getItem(THREAD_KEY)).toContain(askPending.id);
    });
    const [turn] = loadThread();
    expect(turn).toMatchObject({
      status: 'pending',
      askId: askPending.id,
      question: 'what did I decide about the roof?',
    });
  });
});
