import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { capture } from '@/test/filing.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { usePendingCaptures } from './captures.ts';

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

afterEach(() => {
  vi.useRealTimers();
});

describe('the filing row’s captures', () => {
  // A ring files twenty recordings in an afternoon; the one that failed this
  // morning still needs the user, and must not drop out of Home.
  const now = new Date().toISOString();
  const recent = [
    capture({ id: 'moving', status: 'transcribing' }),
    ...Array.from({ length: 19 }, (_, i) =>
      capture({ id: `filed-${String(i)}`, status: 'appended', created_at: now, appended_at: now }),
    ),
  ];
  const failed = capture({ id: 'failed-old', status: 'failed', created_at: '2026-01-01T00:00:00Z' });
  const stray = capture({ id: 'stray-old', status: 'needs_target', created_at: '2026-01-01T00:00:00Z' });

  function serve(needsTarget: Response | (() => Response) = () => json({ items: [stray] })) {
    const asked: string[] = [];
    const fetchImpl: typeof fetch = async (input) => {
      const status = new URL(String(input)).searchParams.get('status') ?? 'all';
      asked.push(status);
      if (status === 'failed') return json({ items: [failed] });
      if (status === 'needs_target') return typeof needsTarget === 'function' ? needsTarget() : needsTarget;
      return json({ items: recent, cursor: 'more' });
    };
    const wrapper = ({ children }: { children: ReactNode }) => (
      <TestProviders api={testApiContext(fetchImpl)}>{children}</TestProviders>
    );
    return { asked, wrapper };
  }

  it('keeps a failed and a needs-target capture that twenty newer ones pushed off the first page, asked once, not per tick', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { asked, wrapper } = serve();
    const view = renderHook(() => usePendingCaptures(), { wrapper });

    await waitFor(() => {
      expect(view.result.current.data?.items.map((item) => item.id)).toEqual(
        expect.arrayContaining(['failed-old', 'stray-old']),
      );
    });
    expect(view.result.current.data?.items).toHaveLength(22);

    // The poll runs on (a capture is moving); the two lists are not re-asked.
    await act(() => vi.advanceTimersByTimeAsync(6_000));
    expect(asked.filter((status) => status === 'all').length).toBeGreaterThan(1);
    expect(asked.filter((status) => status === 'failed')).toHaveLength(1);
  });

  it('keeps what one list returned when the other fails', async () => {
    const { wrapper } = serve(() => json({ title: 'Not permitted', status: 403 }, 403));
    const view = renderHook(() => usePendingCaptures(), { wrapper });

    await waitFor(() => {
      expect(view.result.current.data?.items.map((item) => item.id)).toContain('failed-old');
    });
    expect(view.result.current.isError).toBe(false);
  });
});
