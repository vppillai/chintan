import { renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it } from 'vitest';

import { capture } from '@/test/filing.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { usePendingCaptures } from './captures.ts';

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });

describe('the filing row’s captures', () => {
  it('keeps a failed and a needs-target capture that twenty newer ones pushed off the first page', async () => {
    // A ring files twenty recordings in an afternoon; the one that failed this
    // morning still needs the user, and must not drop out of Home.
    const now = new Date().toISOString();
    const recent = Array.from({ length: 20 }, (_, i) =>
      capture({ id: `filed-${String(i)}`, status: 'appended', created_at: now, appended_at: now }),
    );
    const failed = capture({ id: 'failed-old', status: 'failed', created_at: '2026-01-01T00:00:00Z' });
    const stray = capture({ id: 'stray-old', status: 'needs_target', created_at: '2026-01-01T00:00:00Z' });
    const fetchImpl: typeof fetch = async (input) => {
      const status = new URL(String(input)).searchParams.get('status');
      if (status === 'failed') return json({ items: [failed] });
      if (status === 'needs_target') return json({ items: [stray] });
      return json({ items: recent, cursor: 'more' });
    };
    const wrapper = ({ children }: { children: ReactNode }) => (
      <TestProviders api={testApiContext(fetchImpl)}>{children}</TestProviders>
    );

    const view = renderHook(() => usePendingCaptures(), { wrapper });

    await waitFor(() => {
      expect(view.result.current.data?.items.map((item) => item.id)).toEqual(
        expect.arrayContaining(['failed-old', 'stray-old']),
      );
    });
    expect(view.result.current.data?.items).toHaveLength(22);
  });
});
