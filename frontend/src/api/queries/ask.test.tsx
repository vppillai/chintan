import { focusManager } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { askPending } from '@/api/__fixtures__/responses.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { useAsk } from './ask.ts';

function server() {
  let polls = 0;
  const fetchImpl: typeof fetch = async () => {
    polls += 1;
    return new Response(JSON.stringify(askPending), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  };
  return { fetchImpl, polls: () => polls };
}

const wrapper =
  (fetchImpl: typeof fetch) =>
  ({ children }: { children: ReactNode }) => (
    <TestProviders api={testApiContext(fetchImpl)}>{children}</TestProviders>
  );

afterEach(() => {
  focusManager.setFocused(undefined);
  vi.useRealTimers();
});

describe('the Ask poll', () => {
  it('asks nothing while the app is in the background, and again once it is back', async () => {
    // The one poller that kept going with the document hidden (R7-17a's rule
    // for the others): a question pocketed for a minute was asked after sixty
    // times on a cellular connection nobody was looking at.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { fetchImpl, polls } = server();
    focusManager.setFocused(false);
    renderHook(() => useAsk(askPending.id, Date.now()), { wrapper: wrapper(fetchImpl) });
    await waitFor(() => {
      expect(polls()).toBe(1);
    });

    await act(() => vi.advanceTimersByTimeAsync(3_500));
    expect(polls()).toBe(1);

    act(() => {
      focusManager.setFocused(true);
    });
    await act(() => vi.advanceTimersByTimeAsync(1_500));
    expect(polls()).toBeGreaterThan(1);
  });
});
