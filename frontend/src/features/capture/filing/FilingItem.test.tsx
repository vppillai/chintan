import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { cacheNoteList } from '@/offline/notesCache.ts';
import { STUCK_CREATED_AT, capture, json, mount } from '@/test/filing.tsx';

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

  it('offers the note once the capture has been filed', async () => {
    mount([
      capture({ status: 'appended', note_id: 'roof-repair', appended_at: new Date().toISOString() }),
    ]);
    expect(await screen.findByRole('button', { name: /open the note/i })).toBeInTheDocument();
  });

  it('says which note it was filed into, and the whole receipt opens it', async () => {
    /*
     * Two receipts stacked read "Filed" and "Filed". The note is on the
     * capture and its title is on the device whenever the library has listed
     * it, so the receipt names it — and is itself the control, with a
     * chevron, rather than carrying an "Open the note" pill under a bare word.
     */
    await cacheNoteList([
      {
        id: 'roof-repair',
        title: 'Roof repair',
        updated_at: '2026-08-06T09:14:00.000Z',
        version: 3,
        archived: false,
      },
    ]);
    const user = userEvent.setup();
    mount([
      capture({ status: 'appended', note_id: 'roof-repair', appended_at: new Date().toISOString() }),
    ]);

    const receipt = await screen.findByRole('button', { name: /filed into “roof repair”/i });
    expect(receipt).toHaveAccessibleName(/open the note/i);
    expect(screen.queryByText('Filed')).toBeNull();
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();

    // Opening is acting on the receipt: it leaves with the navigation.
    await user.click(receipt);
    await waitFor(() => {
      expect(screen.queryByText(/^Filed/)).toBeNull();
    });
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
