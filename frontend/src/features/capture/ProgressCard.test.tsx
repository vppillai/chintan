import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import type { CaptureWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { ProgressCard } from './ProgressCard.tsx';

function capture(overrides: Partial<CaptureWire> = {}): CaptureWire {
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

const STUCK_CREATED_AT = '2026-08-07T10:00:00.000Z';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

/** Serves the pending list, and records every request for assertions. */
function mount(items: CaptureWire[]) {
  const calls: { url: string; method: string }[] = [];

  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    calls.push({ url, method });

    if (url.includes('/v1/captures/') && url.endsWith('/retry')) {
      return json(capture({ status: 'transcribing' }));
    }
    if (url.includes('/v1/captures/') && url.endsWith('/target')) {
      return json(capture({ status: 'appending' }));
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
        <ProgressCard />
      </MemoryRouter>
    </TestProviders>,
  );

  return { view, calls, fetchImpl };
}

describe('the progress card is server state, not a JavaScript variable', () => {
  it('renders from GET /v1/captures?status=pending', async () => {
    // This is what makes it survive navigation, reload, and app restart. v1
    // held the in-flight capture id in a module-level field, so a refresh
    // stranded the audio with no UI able to find it.
    const { calls } = mount([capture()]);

    expect(await screen.findByText('Transcribing')).toBeInTheDocument();
    expect(calls[0]?.url).toContain('status=pending');
  });

  it('renders nothing when there is no capture in flight', async () => {
    mount([]);
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /captures in progress/i })).toBeNull();
    });
  });

  it('names the pipeline stage rather than showing a fake percentage', async () => {
    mount([capture({ status: 'cleaning' })]);
    expect(await screen.findByText('Cleaning up')).toBeInTheDocument();
    // No determinate bar: v1 pinned one at 100% and pulsed it, which reads as
    // stuck.
    expect(screen.queryByRole('progressbar')).toBeNull();
  });

  it('announces stage changes politely', async () => {
    mount([capture({ status: 'routing' })]);
    const label = await screen.findByText('Filing');
    expect(label).toHaveAttribute('aria-live', 'polite');
  });

  it('shows a safe error message for a failed capture', async () => {
    mount([capture({ status: 'failed', error: 'Transcription provider timed out' })]);
    expect(await screen.findByText('Transcription provider timed out')).toBeInTheDocument();
  });

  it('explains the spend cap distinctly from a generic failure', async () => {
    mount([capture({ status: 'spend_capped' })]);
    expect(await screen.findByText(/daily spending cap/i)).toBeInTheDocument();
  });
});

describe('a failed capture has a Retry that is actually wired', () => {
  it('calls POST /v1/captures/{id}/retry', async () => {
    // In v1 the client method for this existed and was called from nowhere, so
    // a failed capture was a dead end with a toast.
    const user = userEvent.setup();
    const { calls } = mount([capture({ id: 'srv-9', status: 'failed', error: 'Timed out' })]);

    await user.click(await screen.findByRole('button', { name: 'Retry' }));

    await waitFor(() => {
      expect(
        calls.some(
          (call) => call.method === 'POST' && call.url.endsWith('/v1/captures/srv-9/retry'),
        ),
      ).toBe(true);
    });
  });

  it('offers no Retry while the capture is still progressing', async () => {
    mount([capture({ status: 'transcribing' })]);
    await screen.findByText('Transcribing');
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });

  it('offers Open once the capture has been filed', async () => {
    mount([capture({ status: 'appended', note_id: 'roof-repair' })]);
    expect(await screen.findByRole('button', { name: /open/i })).toBeInTheDocument();
  });
});

describe('a capture that never left "uploaded" is not a permanent dead end', () => {
  // If the S3 upload event that should drive the worker never arrives — a
  // cancelled upload, a lost event — the capture sits at whatever non-terminal
  // status it last reached forever, `failed` is never set, and the card polled
  // silently with no error and no Retry. `chintanctl reconcile` calls this
  // finding `stuck_capture`; the card now recognises it live instead of only
  // being detectable from an operator's terminal.
  it('offers Retry once a non-terminal capture has sat past the stuck threshold', async () => {
    mount([capture({ id: 'srv-stuck', status: 'uploaded', created_at: STUCK_CREATED_AT })]);

    expect(
      await screen.findByText(/still not done.*something may have gone wrong/i),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
  });

  it('still calls POST /v1/captures/{id}/retry from the stuck state', async () => {
    const user = userEvent.setup();
    const { calls } = mount([
      capture({ id: 'srv-stuck-2', status: 'transcribing', created_at: STUCK_CREATED_AT }),
    ]);

    await user.click(await screen.findByRole('button', { name: 'Retry' }));

    await waitFor(() => {
      expect(
        calls.some(
          (call) => call.method === 'POST' && call.url.endsWith('/v1/captures/srv-stuck-2/retry'),
        ),
      ).toBe(true);
    });
  });

  it('does not treat a recent capture the same way', async () => {
    mount([capture({ status: 'uploaded' })]);
    await screen.findByText('Queued');
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });
});

describe('a terminal capture is something the user can act on', () => {
  it('lets the user answer "which note should this go in?"', async () => {
    /*
     * The card asked the question, marked all five pipeline stages complete,
     * and rendered zero buttons. `useSetCaptureTarget` wrapped the contract's
     * target endpoint and was called from nowhere in the app, and the schema
     * lists `needs_target` as terminal pending user action — so the capture,
     * and the thought in it, was stuck permanently.
     */
    const user = userEvent.setup();
    const { calls } = mount([capture({ id: 'srv-7', status: 'needs_target' })]);

    await screen.findByText(/which note should this go in/i);
    await user.click(screen.getByRole('button', { name: /choose a note/i }));

    await user.click(await screen.findByRole('button', { name: 'Roof repair' }));

    await waitFor(() => {
      expect(
        calls.some(
          (call) => call.method === 'POST' && call.url.endsWith('/v1/captures/srv-7/target'),
        ),
      ).toBe(true);
    });
  });

  it('can file the recording into a brand new note', async () => {
    const user = userEvent.setup();
    const { calls, fetchImpl } = mount([capture({ id: 'srv-8', status: 'needs_target' })]);

    await user.click(await screen.findByRole('button', { name: /choose a note/i }));
    await user.type(await screen.findByLabelText(/new note title/i), 'Loft insulation');
    await user.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => {
      expect(
        calls.some(
          (call) => call.method === 'POST' && call.url.endsWith('/v1/captures/srv-8/target'),
        ),
      ).toBe(true);
    });

    const target = fetchImpl.mock.calls.find(
      ([input]) => String(input).endsWith('/v1/captures/srv-8/target'),
    );
    expect(JSON.parse(String(target?.[1]?.body))).toEqual({ new_note_title: 'Loft insulation' });
  });

  it('does not mark every stage complete for a capture that stopped', async () => {
    // Five filled stage pips over "Which note should this go in?" says the
    // pipeline finished. It did not — it is waiting for the user.
    mount([capture({ status: 'needs_target' })]);
    await screen.findByText(/which note should this go in/i);
    expect(screen.queryByRole('list', { name: /pipeline stage/i })).toBeNull();
  });

  it('lets an unactionable capture be dismissed', async () => {
    // `no_content` has no retry, no target, and nothing to open, so without a
    // dismiss the card sat on the record surface indefinitely.
    const user = userEvent.setup();
    mount([capture({ id: 'srv-quiet', status: 'no_content' })]);

    await screen.findByText(/nothing to save from that recording/i);
    await user.click(screen.getByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(screen.queryByText(/nothing to save from that recording/i)).toBeNull();
    });
  });
});

/**
 * The routing suggestion the pipeline pays an LLM call for.
 *
 * `SuggestedNoteID` and `SuggestedTitle` have always been computed and stored,
 * and `handler/wire.go` dropped both before the response left the API — so the
 * "where should this go?" prompt could only offer an unranked list of every
 * note the user has, with no indication of what the router thought. v1 led with
 * `Add to "<note>"`.
 */
describe('the card says where it thinks the recording goes', () => {
  it('leads with the note the router proposed', async () => {
    const user = userEvent.setup();
    const { calls, fetchImpl } = mount([
      capture({ id: 'srv-9', status: 'needs_target', suggested_note_id: 'roof-repair' }),
    ]);

    const add = await screen.findByRole('button', { name: /add to .*roof repair/i });

    // The unranked list is not the first thing on screen any more.
    expect(screen.queryByRole('button', { name: 'Roof repair' })).toBeNull();

    await user.click(add);

    await waitFor(() => {
      expect(
        calls.some(
          (call) => call.method === 'POST' && call.url.endsWith('/v1/captures/srv-9/target'),
        ),
      ).toBe(true);
    });

    const target = fetchImpl.mock.calls.find(([input]) =>
      String(input).endsWith('/v1/captures/srv-9/target'),
    );
    expect(JSON.parse(String(target?.[1]?.body))).toEqual({ note_id: 'roof-repair' });
  });

  it('leads with the title it would give a new note', async () => {
    const user = userEvent.setup();
    const { fetchImpl } = mount([
      capture({ id: 'srv-10', status: 'needs_target', suggested_title: 'Kitchen rebuild' }),
    ]);

    await user.click(
      await screen.findByRole('button', { name: /start .*kitchen rebuild/i }),
    );

    await waitFor(() => {
      expect(
        fetchImpl.mock.calls.some(([input]) =>
          String(input).endsWith('/v1/captures/srv-10/target'),
        ),
      ).toBe(true);
    });

    const target = fetchImpl.mock.calls.find(([input]) =>
      String(input).endsWith('/v1/captures/srv-10/target'),
    );
    expect(JSON.parse(String(target?.[1]?.body))).toEqual({
      new_note_title: 'Kitchen rebuild',
    });
  });

  it('still lets the user disagree with it', async () => {
    const user = userEvent.setup();
    mount([capture({ id: 'srv-11', status: 'needs_target', suggested_title: 'Kitchen rebuild' })]);

    await user.click(await screen.findByRole('button', { name: /choose another note/i }));

    // The full library, and the new-note field, exactly as before.
    expect(await screen.findByRole('button', { name: 'Roof repair' })).toBeInTheDocument();
    expect(screen.getByLabelText(/new note title/i)).toBeInTheDocument();
  });

  it('falls back to the plain picker when the suggested note is not loaded', async () => {
    // The router can name a note beyond the first page of the library. Offering
    // `Add to ""` would be worse than offering the list.
    mount([
      capture({ id: 'srv-12', status: 'needs_target', suggested_note_id: 'page-two-note' }),
    ]);

    expect(await screen.findByRole('button', { name: /choose a note/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /add to/i })).toBeNull();
  });
});

/**
 * A capture's outcome — success or failure — used to be invisible.
 *
 * `usePendingCaptures` polled `GET /v1/captures?status=pending` alone.
 * Backend `CaptureIsPending` (internal/service/capture_status.go) excludes
 * `failed`, `spend_capped` and `needs_target` by design — those are stopped,
 * not moving — but `ProgressCard` renders a distinct, actionable outcome for
 * every one of them. None of them ever satisfied `CaptureIsPending`, so a
 * capture that failed, or that was waiting on "which note should this go
 * in?", simply vanished from every poll — no error, no prompt, nothing,
 * indistinguishable from one that had quietly succeeded. Real reproduction:
 * a capture stuck for six days finally reconciled to `failed` mid-session,
 * and the progress card that had shown "Queued… in progress" the entire time
 * disappeared with nothing said.
 *
 * These mock each of the three server-side filters distinctly — unlike
 * `mount()` above, which serves the same items regardless of query string —
 * specifically to prove the merge happens, not just that the rendering does
 * once data arrives.
 */
describe('a capture the pipeline stopped on is not indistinguishable from one that succeeded', () => {
  function mountDistinctFilters(byStatus: {
    pending?: CaptureWire[];
    failed?: CaptureWire[];
    needs_target?: CaptureWire[];
  }) {
    const calls: string[] = [];
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      calls.push(url);
      if (url.includes('status=failed')) return json({ items: byStatus.failed ?? [] });
      if (url.includes('status=needs_target')) return json({ items: byStatus.needs_target ?? [] });
      if (url.includes('status=pending')) return json({ items: byStatus.pending ?? [] });
      return json({ items: [] });
    });

    render(
      <TestProviders api={testApiContext(fetchImpl)}>
        <MemoryRouter>
          <ProgressCard />
        </MemoryRouter>
      </TestProviders>,
    );
    return { calls };
  }

  it('polls all three filters, not pending alone', async () => {
    const { calls } = mountDistinctFilters({});
    await waitFor(() => {
      expect(calls.some((u) => u.includes('status=pending'))).toBe(true);
      expect(calls.some((u) => u.includes('status=failed'))).toBe(true);
      expect(calls.some((u) => u.includes('status=needs_target'))).toBe(true);
    });
  });

  it('shows a capture that only the failed filter returns', async () => {
    mountDistinctFilters({
      failed: [capture({ id: 'srv-fail', status: 'failed', error: 'Transcription provider timed out' })],
    });
    expect(await screen.findByText('Transcription provider timed out')).toBeInTheDocument();
  });

  it('shows a capture that only the needs_target filter returns', async () => {
    mountDistinctFilters({
      needs_target: [capture({ id: 'srv-ask', status: 'needs_target' })],
    });
    expect(await screen.findByText(/which note should this go in/i)).toBeInTheDocument();
  });
});
