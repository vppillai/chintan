import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Link, MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import {
  CAPTURE_POLL_FAST_MS,
  CAPTURE_POLL_FAST_WINDOW_MS,
  CAPTURE_POLL_INTERVAL_MS,
  capturePollInterval,
  isFilingRelevant,
  newlyAppendedNoteIds,
  queryKeys,
  useNote,
} from '@/api/queries.ts';
import type { CaptureWire } from '@/api/schema.ts';
import { TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import { FILED_ROWS_MAX, FilingRow } from './FilingRow.tsx';
import { DISMISSED_KEY, DISMISSED_LIMIT, dismissCapture, loadDismissed } from './dismissed.ts';
import { INITIAL_CAPTURE, type CaptureModel } from './machine.ts';
import { useCaptureStore } from './store.ts';
import { TARGETED_KEY, TARGETED_LIMIT, loadTargeted, rememberTargeted } from './targeted.ts';

beforeEach(() => {
  // Dismissals are kept on the device; each test starts with none.
  localStorage.clear();
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
});

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

/**
 * Serves the capture list, and records every request for assertions. `retry`
 * is what `POST /v1/captures/{id}/retry` answers, when a test needs it to
 * refuse.
 */
function mount(items: CaptureWire[], { retry }: { retry?: Response } = {}) {
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

describe('the upload this device is still making has a row of its own', () => {
  /*
   * Send hands off to the library at once, so for the seconds before
   * `POST /v1/captures` answers there is no server row — and the server never
   * sees the PUT at all. The store knows, and the row reads it.
   */
  function sending(overrides: Partial<CaptureModel> = {}): void {
    act(() => {
      useCaptureStore.setState({
        model: {
          ...INITIAL_CAPTURE,
          state: 'uploading',
          localId: 'cap-local',
          bytes: 20_000,
          chunks: 7,
          elapsedMs: 41_000,
          uploadProgress: 0.4,
          ...overrides,
        },
      });
    });
  }

  it('shows "Uploading… N%" from the store when the server has nothing yet', async () => {
    sending();
    mount([]);

    const row = await screen.findByRole('status');
    expect(row).toHaveTextContent('Uploading… 40%');
    expect(screen.getByText('0:41')).toBeInTheDocument();
    expect(screen.getByRole('region', { name: /recordings being filed/i })).toBeInTheDocument();
  });

  it('follows the store as the upload progresses', async () => {
    sending({ uploadProgress: 0.1 });
    mount([]);
    expect(await screen.findByText(/uploading/i)).toHaveTextContent('10%');

    act(() => {
      useCaptureStore.getState().dispatch({ type: 'uploadProgress', progress: 0.85 });
    });
    expect(screen.getByText(/uploading/i)).toHaveTextContent('85%');
  });

  it('is replaced by the server row once the poll returns it, and releases the machine', async () => {
    sending({ state: 'uploaded', uploadProgress: 1, serverCaptureId: 'srv-1' });
    mount([capture({ id: 'srv-1', status: 'transcribing' })]);

    expect(await screen.findByText('Filing your recording')).toBeInTheDocument();
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('idle');
    });
    // One row, not two, for the one recording — and it is the server's.
    expect(document.querySelectorAll('.filing-row[data-local]')).toHaveLength(0);
    expect(document.querySelectorAll('.filing-row')).toHaveLength(1);
  });

  it('says "Uploaded" while the server row is still on its way', async () => {
    sending({ state: 'uploaded', uploadProgress: 1, serverCaptureId: 'srv-late' });
    mount([]);
    expect(await screen.findByRole('status')).toHaveTextContent('Uploaded');
    expect(useCaptureStore.getState().model.state).toBe('uploaded');
  });

  it('offers Retry and Discard when the upload failed, since only this device can act', async () => {
    sending({
      state: 'failed',
      failure: {
        kind: 'upload-failed',
        message: 'The upload did not finish. Your recording is safe on this device.',
        recoverable: true,
      },
    });
    // The bytes are "on disk" as far as the uploader is concerned.
    useCaptureStore.getState().__configure({
      upload: {
        assemble: async () => new Blob(['audio']),
        put: async () => {},
        confirm: async () => {},
        saveRecord: async () => {},
      },
    });
    const { calls } = mount([]);

    expect(await screen.findByText(/safe on this device/i)).toBeInTheDocument();
    const retry = screen.getByRole('button', { name: 'Retry' });
    expect(screen.getByRole('button', { name: 'Discard' })).toBeInTheDocument();

    // Retry is the store's send, not the server's retry endpoint: there is no
    // server capture to retry yet.
    await userEvent.setup().click(retry);
    await waitFor(() => {
      expect(calls.some((call) => call.method === 'POST' && call.url.endsWith('/v1/captures'))).toBe(
        true,
      );
    });
    expect(calls.some((call) => call.url.endsWith('/retry'))).toBe(false);
  });

  it('Discard on a failed upload drops the recording and the row', async () => {
    sending({
      state: 'failed',
      failure: { kind: 'upload-failed', message: 'The upload did not finish.', recoverable: true },
    });
    mount([]);

    await userEvent.setup().click(await screen.findByRole('button', { name: 'Discard' }));
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('idle');
    });
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
    });
  });

  it('shows nothing of its own for a recording that failed before any upload', async () => {
    // A refused microphone is the capture screen's to explain, not the library's.
    sending({
      state: 'failed',
      bytes: 0,
      failure: { kind: 'permission-denied', message: 'No microphone access.', recoverable: false },
    });
    mount([]);
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
    });
  });
});

describe('the filing row is server state, not a JavaScript variable', () => {
  it('renders from one GET /v1/captures, filtered here', async () => {
    /*
     * This is what makes it survive navigation, reload, and app restart. An
     * in-flight capture id held in a module-level field is lost on refresh,
     * stranding the audio with no UI able to find it.
     *
     * One request, not four: polling `pending`, `failed`, `needs_target` and
     * `all` separately every four seconds would quadruple the traffic for the
     * same rows.
     */
    const { calls } = mount([capture()]);

    expect(await screen.findByText('Filing your recording')).toBeInTheDocument();
    const listCalls = calls.filter((call) => call.url.includes('/v1/captures?'));
    expect(listCalls).toHaveLength(1);
    expect(listCalls[0]?.url).toContain('status=all');
    expect(listCalls[0]?.url).toContain('limit=20');
    expect(calls.some((call) => call.url.includes('status=pending'))).toBe(false);
    expect(calls.some((call) => call.url.includes('status=failed'))).toBe(false);
    expect(calls.some((call) => call.url.includes('status=needs_target'))).toBe(false);
  });

  it('renders nothing when there is no capture in flight', async () => {
    mount([]);
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
    });
  });

  it('shows the recording length beside the title', async () => {
    mount([capture({ duration_ms: 41_000 })]);
    expect(await screen.findByText('0:41')).toBeInTheDocument();
  });

  it('shows four segments and names the stage, rather than a fake percentage', async () => {
    mount([capture({ status: 'cleaning' })]);
    const strip = await screen.findByRole('list', { name: /filing progress/i });
    expect(strip.querySelectorAll('li')).toHaveLength(4);
    // Routing and cleaning are one segment to the user — the third.
    expect(strip.querySelectorAll('[data-state="done"]')).toHaveLength(2);
    expect(strip.querySelector('[data-state="active"]')).toHaveTextContent(/filing in progress/i);
    // No determinate bar: one pinned at 100% and pulsing reads as stuck.
    expect(screen.queryByRole('progressbar')).toBeNull();
  });

  it('announces stage changes politely', async () => {
    mount([capture({ status: 'routing' })]);
    const label = await screen.findByText('Filing your recording');
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
});

describe('a terminal capture is something the user can act on', () => {
  it('lets the user answer "which note should this go in?"', async () => {
    /*
     * The card asked the question, marked every pipeline stage complete, and
     * rendered zero buttons. `useSetCaptureTarget` wrapped the contract's target
     * endpoint and was called from nowhere in the app, and the schema lists
     * `needs_target` as terminal pending user action — so the capture, and the
     * thought in it, was stuck permanently.
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
    // Four filled segments over "Which note should this go in?" says the
    // pipeline finished. It did not — it is waiting for the user.
    mount([capture({ status: 'needs_target' })]);
    await screen.findByText(/which note should this go in/i);
    expect(screen.queryByRole('list', { name: /filing progress/i })).toBeNull();
  });

  it('lets an unactionable capture be dismissed', async () => {
    // `no_content` has no retry, no target, and nothing to open, so without a
    // dismiss the row sat at the top of the library indefinitely.
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
 * `SuggestedNoteID` and `SuggestedTitle` are computed, stored and returned by
 * the API, so the "where should this go?" prompt must lead with what the router
 * thought rather than an unranked list of every note the user has.
 */
describe('the row says where it thinks the recording goes', () => {
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

    await user.click(await screen.findByRole('button', { name: /start .*kitchen rebuild/i }));

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
 * One list, filtered here, has to carry everything the four server-side
 * filters used to: the moving captures, the stopped-and-actionable ones, and
 * the just-finished one the user wants to tap through to — without dragging
 * old history back to the top of the library.
 */
describe('how often the one poll asks', () => {
  const NOW = Date.parse('2026-09-04T12:00:00.000Z');
  const seconds = (n: number) => new Date(NOW - n * 1000).toISOString();

  it('asks every 1.5 s while a capture is in its first half-minute', () => {
    /*
     * The median pipeline is ~4 s with a target note. A fixed 4 s poll added a
     * median 2 s of waiting on top of it, which is what the owner felt as
     * "even tiny recordings take a while".
     */
    expect(capturePollInterval([capture({ status: 'uploaded', created_at: seconds(2) })], NOW)).toBe(
      CAPTURE_POLL_FAST_MS,
    );
    expect(
      capturePollInterval([capture({ status: 'transcribing', created_at: seconds(29) })], NOW),
    ).toBe(CAPTURE_POLL_FAST_MS);
    expect(CAPTURE_POLL_FAST_WINDOW_MS).toBe(30_000);
  });

  it('relaxes to 4 s once nothing in flight is that young', () => {
    expect(
      capturePollInterval([capture({ status: 'routing', created_at: seconds(31) })], NOW),
    ).toBe(CAPTURE_POLL_INTERVAL_MS);
  });

  it('is driven by the youngest moving capture, not by anything settled', () => {
    expect(
      capturePollInterval(
        [
          capture({ id: 'old', status: 'cleaning', created_at: seconds(120) }),
          capture({ id: 'new', status: 'uploaded', created_at: seconds(3) }),
        ],
        NOW,
      ),
    ).toBe(CAPTURE_POLL_FAST_MS);
    // A capture that has just appended is young but not moving.
    expect(
      capturePollInterval(
        [
          capture({ id: 'done', status: 'appended', created_at: seconds(3) }),
          capture({ id: 'slow', status: 'routing', created_at: seconds(90) }),
        ],
        NOW,
      ),
    ).toBe(CAPTURE_POLL_INTERVAL_MS);
  });

  it('stops asking when nothing is moving', () => {
    expect(capturePollInterval([capture({ status: 'appended', created_at: seconds(1) })], NOW)).toBe(
      false,
    );
    expect(capturePollInterval([], NOW)).toBe(false);
  });
});

describe('what the one poll keeps and what it drops', () => {
  const NOW = Date.parse('2026-09-03T12:00:00.000Z');
  const recent = new Date(NOW - 60_000).toISOString();
  const old = '2026-01-01T00:00:00.000Z';

  it('keeps anything still moving, however old', () => {
    expect(isFilingRelevant(capture({ status: 'transcribing', created_at: old }), NOW)).toBe(true);
    expect(isFilingRelevant(capture({ status: 'uploaded', created_at: old }), NOW)).toBe(true);
  });

  it('keeps a capture that stopped on the user, however old', () => {
    // Backend `CaptureIsPending` excludes these by design — they are stopped,
    // not moving — but each has an action the user must take, and a capture
    // waiting on the user must not vanish silently.
    for (const status of ['failed', 'spend_capped', 'needs_target'] as const) {
      expect(isFilingRelevant(capture({ status, created_at: old }), NOW)).toBe(true);
    }
  });

  it('keeps a filed capture for a day, then lets the note be its record', () => {
    // The row used to fade ten minutes after the append: a recording made on
    // the walk home had no receipt by the time the user sat down to read it.
    // Then it never faded, and a device that had dismissed nothing showed
    // receipts from weeks ago above an empty library. A day covers the walk
    // home; after that the recording is on its note's Recordings tab.
    const hoursAgo = (h: number) => new Date(NOW - h * 60 * 60 * 1000).toISOString();
    expect(
      isFilingRelevant(capture({ status: 'appended', appended_at: recent }), NOW),
    ).toBe(true);
    expect(isFilingRelevant(capture({ status: 'appended', appended_at: hoursAgo(23) }), NOW)).toBe(
      true,
    );
    expect(isFilingRelevant(capture({ status: 'appended', appended_at: hoursAgo(25) }), NOW)).toBe(
      false,
    );
    expect(isFilingRelevant(capture({ status: 'appended', appended_at: old }), NOW)).toBe(false);
    // Without an append time the capture's own time stands in.
    expect(isFilingRelevant(capture({ status: 'appended', created_at: hoursAgo(1) }), NOW)).toBe(
      true,
    );
    expect(isFilingRelevant(capture({ status: 'appended', created_at: old }), NOW)).toBe(false);
  });

  it('still lets a recording that produced nothing expire on its own', () => {
    // Nothing to open and nothing to retry: the one receipt with no action.
    expect(isFilingRelevant(capture({ status: 'no_content', created_at: old }), NOW)).toBe(false);
    expect(isFilingRelevant(capture({ status: 'no_content', created_at: recent }), NOW)).toBe(true);
  });

  it('shows Filed for an appended capture, and lets it be dismissed', async () => {
    // Once the last active capture appends, polling stops entirely
    // (refetchInterval returns false), so nothing would ever refetch this row
    // away on its own. Dismiss is one of the two ways it leaves.
    const user = userEvent.setup();
    mount([
      capture({
        id: 'srv-done',
        status: 'appended',
        note_id: 'roof-repair',
        appended_at: new Date().toISOString(),
      }),
    ]);

    expect(await screen.findByText('Filed')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(screen.queryByText('Filed')).toBeNull();
    });
  });

  it('shows nothing for a capture appended weeks ago, whatever this device has dismissed', async () => {
    // Three of these, above a library of zero notes, is what the owner saw
    // after deleting everything: receipts for recordings filed in August.
    mount([
      capture({
        id: 'srv-old',
        status: 'appended',
        note_id: 'roof-repair',
        appended_at: '2026-01-01T00:00:00.000Z',
      }),
      capture({
        id: 'srv-today',
        status: 'appended',
        note_id: 'roof-repair',
        appended_at: new Date().toISOString(),
      }),
    ]);

    expect(await screen.findByText('Filed')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /open the note/i })).toHaveLength(1);
  });
});

/**
 * Acting on a row is what removes it, and the removal has to outlive the
 * screen: the library remounts on every trip into a note, and a reload must
 * not bring back a row the user closed a minute earlier.
 */
describe('a row leaves when it is acted on, and stays gone', () => {
  const filed = capture({
    id: 'srv-read',
    status: 'appended',
    note_id: 'roof-repair',
    appended_at: new Date().toISOString(),
  });

  it('is dismissed by "Open the note", and does not come back on remount', async () => {
    const user = userEvent.setup();
    const { view } = mount([filed]);

    await user.click(await screen.findByRole('button', { name: /open the note/i }));
    await waitFor(() => {
      expect(screen.queryByText('Filed')).toBeNull();
    });

    // Back to the library: a fresh mount, reading the device.
    view.unmount();
    mount([filed]);
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
    });
    expect(JSON.parse(localStorage.getItem(DISMISSED_KEY) ?? '[]')).toEqual(['srv-read']);
  });

  it('is dismissed by "Open the note" when the library unmounts in the same tick, as it does', async () => {
    /*
     * The QA pass's steps, with the real navigation in between: the click
     * navigates, the library and this row unmount before React renders again,
     * and the user comes Back to a fresh mount. The dismissal used to be
     * written inside a `setState` updater, which React runs on the next
     * render — a render this fiber never gets.
     */
    const user = userEvent.setup();
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url.includes('/v1/captures')) return json({ items: [filed] });
      return json({ id: 'roof-repair', title: 'Roof repair', body: '', version: 1, archived: false, updated_at: '' });
    });
    render(
      <TestProviders api={testApiContext(fetchImpl)}>
        <MemoryRouter initialEntries={['/']}>
          <Routes>
            <Route path="/" element={<FilingRow />} />
            <Route path="/notes/:id" element={<Link to="/">Back to Notes</Link>} />
          </Routes>
        </MemoryRouter>
      </TestProviders>,
    );

    await user.click(await screen.findByRole('button', { name: /open the note/i }));
    // On the device before the note screen has even drawn.
    expect(JSON.parse(localStorage.getItem(DISMISSED_KEY) ?? '[]')).toEqual(['srv-read']);

    await user.click(await screen.findByRole('link', { name: /back to notes/i }));
    // The library is back, the poll has answered again, and the row is not offered.
    await waitFor(() => {
      expect(fetchImpl.mock.calls.filter(([input]) => String(input).includes('/v1/captures')).length)
        .toBeGreaterThan(1);
    });
    expect(screen.queryByText('Filed')).toBeNull();
    expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
  });

  it('is dismissed by Dismiss across a remount too', async () => {
    const user = userEvent.setup();
    const { view } = mount([filed]);

    await user.click(await screen.findByRole('button', { name: 'Dismiss' }));
    view.unmount();
    mount([filed]);

    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
    });
  });

  it('hides only the dismissed row, not its neighbours', async () => {
    const user = userEvent.setup();
    mount([filed, capture({ id: 'srv-other', status: 'failed', error: 'Timed out' })]);

    // Two rows, two Dismiss buttons: the first belongs to the filed row, which
    // the list renders first because the fixture does.
    const [first] = await screen.findAllByRole('button', { name: 'Dismiss' });
    await user.click(first as HTMLElement);
    await waitFor(() => {
      expect(screen.queryByText('Filed')).toBeNull();
    });
    expect(screen.getByText('Timed out')).toBeInTheDocument();
  });

  it('shows the three newest receipts and counts the rest, never hiding a row that needs something', async () => {
    /*
     * On a device that has dismissed nothing, every appended capture among the
     * newest twenty is a full-height card: the QA account showed nineteen of
     * them above the first note.
     */
    const user = userEvent.setup();
    const appended = Array.from({ length: 5 }, (_, index) =>
      capture({
        id: `srv-filed-${index}`,
        status: 'appended',
        note_id: 'roof-repair',
        appended_at: new Date(Date.now() - index * 60_000).toISOString(),
      }),
    );
    mount([capture({ id: 'srv-moving', status: 'transcribing' }), ...appended]);

    expect(await screen.findAllByRole('button', { name: /open the note/i })).toHaveLength(
      FILED_ROWS_MAX,
    );
    expect(screen.getByText('Filing your recording')).toBeInTheDocument();
    expect(screen.getByText(/more filed/)).toHaveTextContent('2 more filed');

    // Acting on one of the three lets the next oldest through.
    const [first] = screen.getAllByRole('button', { name: 'Dismiss' });
    await user.click(first as HTMLElement);
    await waitFor(() => {
      expect(screen.getByText(/more filed/)).toHaveTextContent('1 more filed');
    });
    expect(screen.getAllByRole('button', { name: /open the note/i })).toHaveLength(FILED_ROWS_MAX);
  });

  it('survives a reload, which is what the device store is for', () => {
    let ids = loadDismissed();
    expect(ids.size).toBe(0);
    ids = dismissCapture('a', ids);
    ids = dismissCapture('b', ids);
    expect(Array.from(loadDismissed())).toEqual(['a', 'b']);
  });

  it('keeps only the most recent two hundred', () => {
    let ids = loadDismissed();
    for (let index = 0; index < DISMISSED_LIMIT + 5; index += 1) {
      ids = dismissCapture(`cap-${index}`, ids);
    }
    const stored = loadDismissed();
    expect(stored.size).toBe(DISMISSED_LIMIT);
    expect(stored.has('cap-0')).toBe(false);
    expect(stored.has(`cap-${DISMISSED_LIMIT + 4}`)).toBe(true);
  });

  it('treats unreadable storage as nothing dismissed rather than failing', () => {
    localStorage.setItem(DISMISSED_KEY, '{not json');
    expect(loadDismissed().size).toBe(0);
  });
});

/**
 * The append is written by the worker, not by this client, so no mutation ever
 * invalidated the note. A note the user had open while recording into it — or
 * opened from this row's "Open the note" — showed the body from before the
 * recording until a second visit.
 */
describe('a capture that has just been filed refreshes its note', () => {
  /** The note screen's own query, so an invalidation has an observer to refetch. */
  function NoteProbe() {
    const { data } = useNote('roof-repair');
    return <p>{data ? `body: ${data.body}` : 'no note'}</p>;
  }

  /** Serves a different capture list on each poll, and counts note reads. */
  function mountWithPolls(polls: CaptureWire[][]) {
    let poll = 0;
    let noteReads = 0;
    const fetchImpl = vi.fn<typeof fetch>(async (input) => {
      const url = String(input);
      if (url.includes('/v1/captures')) {
        const items = polls[Math.min(poll, polls.length - 1)] ?? [];
        poll += 1;
        return json({ items });
      }
      if (url.endsWith('/v1/notes/roof-repair')) {
        noteReads += 1;
        return json({
          id: 'roof-repair',
          title: 'Roof repair',
          body: noteReads === 1 ? 'before' : 'after the recording',
          updated_at: '2026-08-06T09:14:00.000Z',
          version: noteReads,
          archived: false,
        });
      }
      return json({ items: [] });
    });
    const queryClient = testQueryClient();
    render(
      <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
        <MemoryRouter>
          <NoteProbe />
          <FilingRow />
        </MemoryRouter>
      </TestProviders>,
    );
    const refetchCaptures = () =>
      act(() => queryClient.refetchQueries({ queryKey: queryKeys.pendingCaptures() }));
    return { queryClient, refetchCaptures, noteReads: () => noteReads };
  }

  const filing = capture({ id: 'srv-f', status: 'uploaded', note_id: 'roof-repair' });
  const filed = capture({
    id: 'srv-f',
    status: 'appended',
    note_id: 'roof-repair',
    appended_at: new Date().toISOString(),
  });

  it('refetches the note detail when a capture goes uploaded → appended', async () => {
    const { refetchCaptures, noteReads } = mountWithPolls([[filing], [filed]]);

    expect(await screen.findByText('body: before')).toBeInTheDocument();
    await screen.findByText('Filing your recording');
    expect(noteReads()).toBe(1);

    // The next poll sees the capture appended. That is the moment the note's
    // text changed on the server.
    await refetchCaptures();

    expect(await screen.findByText('body: after the recording')).toBeInTheDocument();
    expect(noteReads()).toBe(2);
  });

  it('does not refetch for a capture that was already appended last time', async () => {
    const { refetchCaptures, noteReads } = mountWithPolls([[filed], [filed]]);

    await screen.findByText('body: before');
    await screen.findByText('Filed');
    await refetchCaptures();

    // Same answer twice; nothing changed, nothing to refresh.
    await waitFor(() => {
      expect(screen.getByText('Filed')).toBeInTheDocument();
    });
    expect(noteReads()).toBe(1);
  });

  it('refetches the note when "Open the note" is tapped', async () => {
    // The poll may have first seen the capture already appended, in which case
    // no transition was observed — but the row is still saying the note has
    // just been written to, so opening it must not hand over the old body.
    const user = userEvent.setup();
    const { noteReads } = mountWithPolls([[filed]]);

    await screen.findByText('body: before');
    await user.click(await screen.findByRole('button', { name: /open the note/i }));

    expect(await screen.findByText('body: after the recording')).toBeInTheDocument();
    expect(noteReads()).toBe(2);
  });

  it('names only the notes whose captures crossed into appended', () => {
    const before = [
      capture({ id: 'a', status: 'transcribing', note_id: 'n1' }),
      capture({ id: 'b', status: 'appended', note_id: 'n2' }),
    ];
    const after = [
      capture({ id: 'a', status: 'appended', note_id: 'n1' }),
      capture({ id: 'b', status: 'appended', note_id: 'n2' }),
      // Not seen before, appended now: the poll missed the transition, but the
      // note is no less changed for that.
      capture({ id: 'c', status: 'appended', note_id: 'n3' }),
      capture({ id: 'd', status: 'appended' }),
    ];
    expect(newlyAppendedNoteIds(before, after)).toEqual(['n1', 'n3']);
    // A cold start has no cache to be stale.
    expect(newlyAppendedNoteIds(undefined, after)).toEqual([]);
  });
});

describe('a recording made into a note is the note\'s to show, not the library\'s', () => {
  /*
   * "Record into this" sends the user back to the note's Recordings tab, where
   * the upload and then the finished row already show (N3). Listing it here
   * as well made Home a wall of "Filed" receipts after a day of recording into
   * one note. Home keeps only what the router had to place — contract §3.
   */
  it('shows the untargeted server row and not the targeted one', async () => {
    mount([
      capture({ id: 'srv-targeted', status: 'appended', note_id: 'roof-repair', targeted: true }),
      capture({ id: 'srv-routed', status: 'appended', note_id: 'roof-repair', targeted: false }),
    ]);

    expect(await screen.findByText('Filed')).toBeInTheDocument();
    expect(document.querySelectorAll('.filing-row')).toHaveLength(1);
    expect(screen.queryByText(/more filed/)).toBeNull();
  });

  it('hides a row this device sent with a note even when the server does not say so', async () => {
    // Older backends send no `targeted`; the uploader's own memory stands in.
    rememberTargeted('srv-old-backend');
    mount([
      capture({ id: 'srv-old-backend', status: 'appended', note_id: 'roof-repair' }),
      capture({ id: 'srv-routed', status: 'transcribing' }),
    ]);

    expect(await screen.findByText('Filing your recording')).toBeInTheDocument();
    expect(document.querySelectorAll('.filing-row')).toHaveLength(1);
  });

  it('renders nothing at all when every capture is a note\'s', async () => {
    mount([capture({ id: 'srv-targeted', status: 'appended', note_id: 'roof-repair', targeted: true })]);
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
    });
  });

  it('does not show the upload this device is making into a note', async () => {
    // Send went back to that note's Recordings tab, which is showing it.
    act(() => {
      useCaptureStore.setState({
        model: {
          ...INITIAL_CAPTURE,
          state: 'uploading',
          localId: 'cap-local',
          noteId: 'roof-repair',
          bytes: 20_000,
          elapsedMs: 41_000,
          uploadProgress: 0.4,
        },
      });
    });
    mount([]);
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
    });
  });

  it('still releases the machine for a targeted upload once its server row exists', async () => {
    // The note screen usually does this hand-over; if the user came Home
    // first, Home must not leave the machine stuck at `uploaded`.
    act(() => {
      useCaptureStore.setState({
        model: {
          ...INITIAL_CAPTURE,
          state: 'uploaded',
          localId: 'cap-local',
          noteId: 'roof-repair',
          serverCaptureId: 'srv-t',
          uploadProgress: 1,
        },
      });
    });
    mount([capture({ id: 'srv-t', status: 'transcribing', note_id: 'roof-repair', targeted: true })]);
    await waitFor(() => {
      expect(useCaptureStore.getState().model.state).toBe('idle');
    });
    expect(document.querySelectorAll('.filing-row')).toHaveLength(0);
  });

  it('remembers ids across a reload, newest last, and keeps only the most recent', () => {
    rememberTargeted('a');
    rememberTargeted('b');
    rememberTargeted('a');
    expect(Array.from(loadTargeted())).toEqual(['b', 'a']);
    expect(JSON.parse(localStorage.getItem(TARGETED_KEY) ?? '[]')).toEqual(['b', 'a']);

    for (let index = 0; index < TARGETED_LIMIT + 5; index += 1) rememberTargeted(`id-${index}`);
    const kept = loadTargeted();
    expect(kept.size).toBe(TARGETED_LIMIT);
    expect(kept.has('id-0')).toBe(false);
    expect(kept.has(`id-${TARGETED_LIMIT + 4}`)).toBe(true);
  });

  it('treats unreadable storage as nothing remembered', () => {
    localStorage.setItem(TARGETED_KEY, '{not json');
    expect(loadTargeted().size).toBe(0);
    localStorage.setItem(TARGETED_KEY, JSON.stringify([1, 'ok', null]));
    expect(Array.from(loadTargeted())).toEqual(['ok']);
  });
});
