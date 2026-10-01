import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { saveCaptureRecord } from '@/features/capture/buffer.ts';
import { STUCK_CREATED_AT, capture, json, mount } from '@/test/filing.tsx';

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

  it('offers Retry only once the server will accept it: fifteen minutes, twenty while appending', async () => {
    /*
     * The row said "still not done" and offered Retry at ten minutes; the
     * server refuses a retry of an in-flight capture until no worker can
     * still be on it — fifteen minutes since it last wrote the row, or the
     * twenty-minute append lease — so every tap in between was answered
     * "still in flight". The copy stays at ten; the button waits.
     */
    const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();
    mount([
      capture({ id: 'twelve', status: 'transcribing', created_at: ago(12) }),
      capture({ id: 'sixteen', status: 'transcribing', created_at: ago(16) }),
      capture({ id: 'appending-sixteen', status: 'appending', created_at: ago(16) }),
      capture({ id: 'appending-twenty-one', status: 'appending', created_at: ago(21) }),
      // The server measures from the row's last write, not its creation. No
      // backend sends `last_progress_at` yet, so `sixteen` above is offered
      // Retry whether or not it moved; when the field is carried, a capture
      // that made progress twelve minutes ago is not, however old its row.
      capture({
        id: 'progressed',
        status: 'transcribing',
        created_at: ago(16),
        last_progress_at: ago(12),
      }),
      capture({
        id: 'stalled',
        status: 'transcribing',
        created_at: ago(30),
        last_progress_at: ago(16),
      }),
    ]);

    expect(await screen.findAllByText(/still not done/i)).toHaveLength(6);
    const rows = document.querySelectorAll<HTMLElement>('.filing-row');
    const retryIn = (row: HTMLElement | undefined) =>
      row ? within(row).queryByRole('button', { name: 'Retry' }) !== null : null;
    expect(retryIn(rows[0])).toBe(false);
    expect(retryIn(rows[1])).toBe(true);
    expect(retryIn(rows[2])).toBe(false);
    expect(retryIn(rows[3])).toBe(true);
    expect(retryIn(rows[4])).toBe(false);
    expect(retryIn(rows[5])).toBe(true);
    // Dismiss is still the way off the screen for every one of them.
    expect(screen.getAllByRole('button', { name: 'Dismiss' })).toHaveLength(6);
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
