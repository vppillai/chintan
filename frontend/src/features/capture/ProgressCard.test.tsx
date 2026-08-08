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
    if (url.includes('/v1/captures')) {
      return json({ items });
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
