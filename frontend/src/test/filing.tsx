import { render } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useParams } from 'react-router';
import { vi } from 'vitest';

import { isTerminalStatus, type CaptureWire } from '@/api/schema.ts';
import { FilingRow } from '@/features/capture/FilingRow.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

/**
 * The library's filing row under test, shared by the row's own tests and by
 * the tests of its parts (`filing/FilingItem.test.tsx`,
 * `filing/TargetPrompt.test.tsx`): a part is exercised the way the app reaches
 * it, through the row's poll and its wiring, so a Retry that the row never
 * connected would fail here rather than pass against a bare component.
 */

/** The worker's timeout: when the server first lets a pending capture be retried or deleted. */
const RETRY_AFTER_MS = 15 * 60_000;

/**
 * A capture as the server sends it. Recent by default, so a plain
 * in-progress fixture never trips the stuck rule; tests for that set an old
 * `created_at` or `last_progress_at`. `retry_after` is derived the server's
 * way (`service.CaptureRetryAfter`: the last progress, else the creation,
 * plus the worker's timeout) unless the test says otherwise — `null` is a
 * row the server did not date.
 */
export function capture(overrides: Partial<CaptureWire> = {}): CaptureWire {
  const row: CaptureWire = {
    id: 'srv-1',
    status: 'transcribing',
    created_at: new Date().toISOString(),
    version: 1,
    ...overrides,
  };
  if ('retry_after' in overrides || isTerminalStatus(row.status)) return row;
  const since = Date.parse(row.last_progress_at ?? row.created_at);
  return { ...row, retry_after: new Date(since + RETRY_AFTER_MS).toISOString() };
}

export const STUCK_CREATED_AT = '2026-08-07T10:00:00.000Z';

export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

/** Where a receipt's "Open the note" lands, so a test can read the route it chose. */
function NoteScreenProbe() {
  const { id } = useParams();
  return <p>note screen: {id}</p>;
}

/**
 * Serves the capture list, and records every request for assertions. `retry`
 * is what `POST /v1/captures/{id}/retry` answers, and `remove` what
 * `DELETE /v1/captures/{id}` answers, when a test needs one to refuse; a
 * DELETE that is not refused drops the row from `items`. `items` is served
 * by reference, so a test can add to it and ask again. With `noteRoute` the row sits on `/` and `/notes/:id` is a probe
 * naming the note, since a `MemoryRouter` with nothing else has nowhere to go.
 */
export function mount(
  items: CaptureWire[],
  {
    retry,
    remove,
    noteRoute = false,
  }: { retry?: Response; remove?: Response | Error; noteRoute?: boolean } = {},
) {
  const calls: { url: string; method: string }[] = [];

  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    calls.push({ url, method });

    if (method === 'DELETE' && /\/v1\/captures\/[^/]+$/.test(url)) {
      if (remove instanceof Error) throw remove;
      if (remove) return remove;
      const id = url.slice(url.lastIndexOf('/') + 1);
      items.splice(0, items.length, ...items.filter((item) => item.id !== id));
      return new Response(null, { status: 204 });
    }
    if (url.includes('/v1/captures/') && url.endsWith('/retry')) {
      return retry ?? json(capture({ status: 'transcribing' }));
    }
    if (url.includes('/v1/captures/') && url.endsWith('/retranscribe')) {
      return json(capture({ status: 'transcribing' }), 202);
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
      <MemoryRouter initialEntries={['/']}>
        {noteRoute ? (
          <Routes>
            <Route path="/" element={<FilingRow />} />
            <Route path="/notes/:id" element={<NoteScreenProbe />} />
          </Routes>
        ) : (
          <FilingRow />
        )}
      </MemoryRouter>
    </TestProviders>,
  );

  return { view, calls, fetchImpl };
}
