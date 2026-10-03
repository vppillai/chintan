import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { StatusRegion, announce } from '@/components/StatusRegion.tsx';
import { saveCaptureRecord } from '@/features/capture/buffer.ts';
import { STUCK_CREATED_AT, capture, json, mount } from '@/test/filing.tsx';

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();
const conflict = (detail: string) =>
  json({ type: 'about:blank', title: 'Conflict', status: 409, detail }, 409);

afterEach(() => {
  announce('');
});

/** This device's capture store names `serverCaptureId`: it made the recording. */
async function recordHere(serverCaptureId: string): Promise<void> {
  await saveCaptureRecord({
    localId: `local-${serverCaptureId}`,
    serverCaptureId,
    noteId: null,
    contentType: 'audio/webm',
    durationMs: 1_000,
    bytes: 1,
    chunkCount: 1,
    createdAt: Date.now(),
    uploadedAt: null,
    peaks: null,
  });
}

describe('a failed capture has a Retry that is actually wired', () => {
  it('calls POST /v1/captures/{id}/retry', async () => {
    // The client method has to be reachable from the UI; a Retry that nothing
    // calls leaves a failed capture as a dead end with a toast.
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

  it('says why a Retry the server refused did nothing, under the row', async () => {
    // Review S14: the row's Retry had an `onSuccess` and nothing for failure,
    // so a 409 — the capture is terminal, or an identical retry is still in
    // flight — left the button re-enabled and the user none the wiser.
    const user = userEvent.setup();
    mount([capture({ id: 'srv-9', status: 'failed', error: 'Timed out' })], {
      retry: json(
        {
          type: 'about:blank',
          title: 'Conflict',
          status: 409,
          detail: 'That recording has already been filed.',
        },
        409,
      ),
    });

    await user.click(await screen.findByRole('button', { name: 'Retry' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'That recording has already been filed.',
    );
    expect(screen.getByRole('button', { name: 'Retry' })).toBeEnabled();
  });

  it('offers no Retry while the capture is still progressing', async () => {
    mount([capture({ status: 'transcribing' })]);
    await screen.findByText('Filing your recording');
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });
});

describe('a capture that never left "uploaded" is not a permanent dead end', () => {
  // If the S3 upload event that should drive the worker never arrives — a
  // cancelled upload, a lost event — the capture sits at whatever non-terminal
  // status it last reached forever, `failed` is never set, and the row polled
  // silently with no error and no Retry. `chintanctl reconcile` calls this
  // finding `stuck_capture`; the row recognises it live instead of only being
  // detectable from an operator's terminal.
  it('offers Retry once a non-terminal capture has sat past the stuck threshold', async () => {
    await recordHere('srv-stuck');
    mount([capture({ id: 'srv-stuck', status: 'uploaded', created_at: STUCK_CREATED_AT })]);

    expect(await screen.findByText('Still not done. Retry, or dismiss it.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
  });

  it('says how long it has sat and draws no stage strip, since no stage is in progress', async () => {
    // Prod: a capture sat at "Filing your recording" for ten minutes over a
    // strip lit at Saving, then flipped to "something might have gone wrong"
    // with the strip still lit and nothing to tap.
    mount([capture({ status: 'appending', created_at: ago(40), last_progress_at: ago(12) })]);

    // Twelve minutes into an append the server would still refuse a retry:
    // the sentence names only the way out that is on the row.
    expect(await screen.findByText('Still not done. You can dismiss it.')).toBeInTheDocument();
    expect(screen.getByText('· 12 min')).toBeInTheDocument();
    expect(screen.queryByRole('list', { name: 'Filing progress' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
  });

  it('shows the age of a row that is moving once it is a minute old, outside the live region', async () => {
    mount([capture({ status: 'transcribing', created_at: ago(4) })]);
    const live = await screen.findByRole('status');
    expect(live).toHaveTextContent('Filing your recording');
    const age = screen.getByText('· 4 min');
    expect(age).toHaveAttribute('aria-hidden', 'true');
    expect(live).not.toContainElement(age);
    expect(screen.getByRole('list', { name: 'Filing progress' })).toBeInTheDocument();
  });

  it('is not re-announced because a minute passed: the live sentence stands while the age moves', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      mount([capture({ status: 'transcribing', created_at: ago(4) })]);
      const live = await screen.findByRole('status');
      const before = live.textContent;
      expect(screen.getByText('· 4 min')).toBeInTheDocument();

      act(() => {
        vi.advanceTimersByTime(60_000);
      });

      expect(screen.getByText('· 5 min')).toBeInTheDocument();
      expect(live.textContent).toBe(before);
    } finally {
      vi.useRealTimers();
    }
  });

  it('offers Retry when the server says it will take one, whatever the client\'s own bound says', async () => {
    const user = userEvent.setup();
    const { calls } = mount([
      capture({ id: 'told', status: 'appending', created_at: ago(12), retry_after: ago(1) }),
      capture({ id: 'held', status: 'transcribing', created_at: ago(30), retry_after: new Date(Date.now() + 60_000).toISOString() }),
    ]);

    await screen.findAllByText(/still not done/i);
    const rows = document.querySelectorAll<HTMLElement>('.filing-row');
    expect(within(rows[0]!).getByRole('button', { name: 'Retry' })).toBeInTheDocument();
    expect(within(rows[1]!).queryByRole('button', { name: 'Retry' })).toBeNull();

    await user.click(within(rows[0]!).getByRole('button', { name: 'Retry' }));
    await waitFor(() => {
      expect(calls.some((call) => call.method === 'POST' && call.url.endsWith('/v1/captures/told/retry'))).toBe(true);
    });
  });

  it('answers an early tap with the server\'s own sentence', async () => {
    const user = userEvent.setup();
    mount([capture({ status: 'transcribing', created_at: ago(12), retry_after: ago(1) })], {
      retry: conflict('That recording is still being worked on.'),
    });

    await user.click(await screen.findByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('That recording is still being worked on.');
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
    await recordHere('srv-1');
    mount([capture({ status: 'uploaded' })]);
    await screen.findByText('Filing your recording');
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });

  it('measures from the last progress the server reports, not from the start', async () => {
    // Uploaded long ago, but the pipeline moved it a moment ago: still filing.
    mount([
      capture({
        status: 'transcribing',
        created_at: STUCK_CREATED_AT,
        last_progress_at: new Date().toISOString(),
      }),
    ]);
    await screen.findByText('Filing your recording');
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });

  it('never offers Retry on a row the server has not dated, however long it has sat', async () => {
    // The row used to keep its own copy of the server's thresholds and the
    // two disagreed for five minutes of every stall. Now `retry_after` is
    // the one source; a pending row without it — none the server sends —
    // gets the dismiss-only sentence for good rather than a guess.
    mount([capture({ status: 'transcribing', created_at: ago(30), retry_after: null })]);

    expect(await screen.findByText('Still not done. You can dismiss it.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
  });
});

describe('the × on a stuck row is not cosmetic', () => {
  // Dismiss only hid the row on this device: the server kept the capture,
  // every other device kept drawing it, and the poll asked after it once a
  // minute for ever. Once the server will let it go, the × deletes it.
  it('deletes the capture once the server will let it go, and the row leaves', async () => {
    const user = userEvent.setup();
    const { calls } = mount([capture({ id: 'gone', status: 'transcribing', created_at: ago(16) })]);

    await user.click(await screen.findByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(calls.some((call) => call.method === 'DELETE' && call.url.endsWith('/v1/captures/gone'))).toBe(true);
    });
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: 'Filing' })).toBeNull();
    });
  });

  it('follows retry_after for the delete too', async () => {
    const user = userEvent.setup();
    const { calls } = mount([capture({ id: 'told', status: 'appending', created_at: ago(12), retry_after: ago(1) })]);
    await user.click(await screen.findByRole('button', { name: 'Dismiss' }));
    await waitFor(() => {
      expect(calls.some((call) => call.method === 'DELETE' && call.url.endsWith('/v1/captures/told'))).toBe(true);
    });
  });

  it('only hides the row before the server would allow the delete, and sends nothing', async () => {
    const user = userEvent.setup();
    const { calls } = mount([capture({ id: 'early', status: 'transcribing', created_at: ago(12) })]);

    await user.click(await screen.findByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(screen.queryByRole('region', { name: 'Filing' })).toBeNull();
    });
    expect(calls.some((call) => call.method === 'DELETE')).toBe(false);
  });

  it('hides the row and says why when the delete never reached the server', async () => {
    const user = userEvent.setup();
    render(<StatusRegion />);
    mount([capture({ id: 'offline', status: 'transcribing', created_at: ago(16) })], {
      remove: new TypeError('Failed to fetch'),
    });

    await user.click(await screen.findByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(screen.queryByRole('region', { name: 'Filing' })).toBeNull();
    });
    // The client's own sentence for a request that never left the device —
    // never the Retry button's "The retry did not go through".
    expect(screen.getByRole('status')).toHaveTextContent('No connection, so that did not reach the server.');
  });

  it('hides the row and says the server\'s sentence when the delete is refused', async () => {
    const user = userEvent.setup();
    render(<StatusRegion />);
    mount([capture({ id: 'kept', status: 'uploaded', created_at: ago(16) })], {
      remove: conflict('The upload may still land; try again in a few minutes.'),
    });

    await user.click(await screen.findByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(screen.queryByRole('region', { name: 'Filing' })).toBeNull();
    });
    expect(screen.getByRole('status')).toHaveTextContent(
      'The upload may still land; try again in a few minutes.',
    );
  });
});

describe('an upload that has not landed, read on another device', () => {
  /*
   * R7-11: the app was closed mid-upload, so the row sat at `uploaded` and
   * every device said "Filing your recording — Upload in progress" for good.
   * Only the device holding the bytes can move it; every other one says so.
   */
  it('says it is waiting for the device that recorded it, stuck or not', async () => {
    mount([
      capture({ id: 'fresh', status: 'uploaded' }),
      capture({ id: 'old', status: 'uploaded', created_at: STUCK_CREATED_AT }),
    ]);
    expect(await screen.findAllByText('Waiting for the device that recorded it')).toHaveLength(2);
    expect(screen.queryByText(/Filing your recording|still not done/i)).toBeNull();
    // The stuck one keeps its way off the screen.
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
  });

  it('keeps the usual words on the device that recorded it', async () => {
    await recordHere('mine');
    mount([capture({ id: 'mine', status: 'uploaded' })]);
    await screen.findByText('Filing your recording');
    expect(screen.queryByText('Waiting for the device that recorded it')).toBeNull();
  });

  it('is only about the upload: a capture past it reads the same everywhere', async () => {
    mount([capture({ id: 'theirs', status: 'transcribing' })]);
    await screen.findByText('Filing your recording');
  });
});

describe('a recording nothing was heard in', () => {
  it('says "Nothing heard" and offers Transcribe anyway, which reaches /retranscribe', async () => {
    // The gate is the recorder's or the provider's judgement; the person's
    // outranks it, and a row with only Dismiss would make the judgement final.
    const user = userEvent.setup();
    const { calls } = mount([capture({ id: 'srv-quiet', status: 'no_content', gate: 'quiet' })]);

    await screen.findByText('Nothing heard');
    await user.click(await screen.findByRole('button', { name: 'Transcribe anyway' }));

    await waitFor(() => {
      expect(
        calls.some(
          (call) =>
            call.method === 'POST' && call.url.endsWith('/v1/captures/srv-quiet/retranscribe'),
        ),
      ).toBe(true);
    });
    // Still a way off the screen beside it.
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
  });

  it('offers no Transcribe anyway on a recording that was heard and was only an instruction', async () => {
    mount([capture({ id: 'srv-empty', status: 'no_content' })]);

    await screen.findByText('Nothing to save from that recording');
    expect(screen.queryByRole('button', { name: 'Transcribe anyway' })).toBeNull();
  });
});

describe('every notice wears a glyph for its kind (F9)', () => {
  it('marks each row with data-kind and draws its glyph', async () => {
    mount([
      capture({ id: 'moving', status: 'transcribing' }),
      capture({ id: 'needs', status: 'needs_target' }),
      capture({ id: 'failed', status: 'failed', error: 'Timed out' }),
      capture({ id: 'empty', status: 'no_content' }),
      capture({ id: 'filed', status: 'appended', note_id: 'n1', appended_at: new Date().toISOString() }),
    ]);
    await screen.findByText(/^Filed/);
    const kinds = Array.from(document.querySelectorAll('.filing-row'), (row) => ({
      kind: row.getAttribute('data-kind'),
      glyph: row.querySelector('.filing-row__glyph svg') !== null,
    }));
    expect(kinds).toEqual([
      { kind: 'moving', glyph: true },
      { kind: 'needs', glyph: true },
      { kind: 'failed', glyph: true },
      { kind: 'failed', glyph: true },
      { kind: 'filed', glyph: true },
    ]);
  });

  it('says "Started" with the started kind', async () => {
    mount([
      capture({
        status: 'appended',
        note_id: 'n1',
        created_note: true,
        appended_at: new Date().toISOString(),
      }),
    ]);
    const title = await screen.findByText(/^Started/);
    expect(title.closest('article')).toHaveAttribute('data-kind', 'started');
  });

  it('puts a failed row away with an × named Dismiss, drawn before anything has focus', async () => {
    mount([capture({ status: 'failed', error: 'Timed out' })]);
    await screen.findByText('Timed out');
    expect(document.activeElement).toBe(document.body);
    // The × replaced the word: there is no "Dismiss" text on the row.
    expect(screen.getByRole('button', { name: 'Dismiss' })).toHaveClass('filing-row__dismiss');
    expect(screen.queryByText('Dismiss')).toBeNull();
  });

  it('offers no × on a row asking which note, since the recording is in no note yet', async () => {
    mount([capture({ status: 'needs_target' })]);
    await screen.findByText(/which note should this go in/i);
    expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
  });
});
