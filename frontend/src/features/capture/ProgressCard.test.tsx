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
    created_at: '2026-08-07T10:00:00.000Z',
    version: 1,
    ...overrides,
  };
}

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
