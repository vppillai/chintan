import { render } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { vi } from 'vitest';

import type { CaptureWire } from '@/api/schema.ts';
import { FilingRow } from '@/features/capture/FilingRow.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

/**
 * The library's filing row under test, shared by the row's own tests and by
 * the tests of its parts (`filing/FilingItem.test.tsx`,
 * `filing/TargetPrompt.test.tsx`): a part is exercised the way the app reaches
 * it, through the row's poll and its wiring, so a Retry that the row never
 * connected would fail here rather than pass against a bare component.
 */

export function capture(overrides: Partial<CaptureWire> = {}): CaptureWire {
  return {
    id: 'srv-1',
    status: 'transcribing',
    // Recent by default so a plain in-progress fixture never trips the
    // stuck-capture timeout below. Tests for that behaviour set an old
    // `created_at` explicitly.
    created_at: new Date().toISOString(),
    version: 1,
    ...overrides,
  };
}

export const STUCK_CREATED_AT = '2026-08-07T10:00:00.000Z';

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

/**
 * Serves the capture list, and records every request for assertions. `retry`
 * is what `POST /v1/captures/{id}/retry` answers, when a test needs it to
 * refuse.
 */
export function mount(items: CaptureWire[], { retry }: { retry?: Response } = {}) {
  const calls: { url: string; method: string }[] = [];

  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    calls.push({ url, method });

    if (url.includes('/v1/captures/') && url.endsWith('/retry')) {
      return retry ?? json(capture({ status: 'transcribing' }));
    }
    if (url.includes('/v1/captures/') && url.endsWith('/target')) {
      return json(capture({ status: 'appending' }));
    }
    if (url.endsWith('/v1/captures') && method === 'POST') {
      return json(
        {
          capture: capture({ id: 'srv-new', status: 'uploaded' }),
          upload: {
            url: 'https://s3.test/audio',
            expires_at: new Date(Date.now() + 60_000).toISOString(),
            max_bytes: 1_000_000,
          },
        },
        201,
      );
    }
    if (url.includes('/v1/captures')) {
      return json({ items });
    }
    if (url.includes('/v1/notes')) {
      return json({
        items: [
          {
            id: 'roof-repair',
            title: 'Roof repair',
            updated_at: '2026-08-06T09:14:00.000Z',
            version: 3,
            archived: false,
          },
        ],
      });
    }
    return json({});
  });

  const view = render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter>
        <FilingRow />
      </MemoryRouter>
    </TestProviders>,
  );

  return { view, calls, fetchImpl };
}
