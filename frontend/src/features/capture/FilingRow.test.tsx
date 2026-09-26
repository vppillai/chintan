import { focusManager } from '@tanstack/react-query';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Link, MemoryRouter, Route, Routes } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import {
  CAPTURE_POLL_FAST_MS,
  CAPTURE_POLL_FAST_WINDOW_MS,
  CAPTURE_POLL_INTERVAL_MS,
  CAPTURE_POLL_SLOW_MS,
  CAPTURE_POLL_STUCK_MS,
  capturePollInterval,
  isFilingRelevant,
  newlyAppendedNoteIds,
  queryKeys,
  useNote,
} from '@/api/queries.ts';
import type { CaptureWire } from '@/api/schema.ts';
import { cacheNoteList } from '@/offline/notesCache.ts';
import { capture, json, mount } from '@/test/filing.tsx';
import { TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import { FILED_ROWS_MAX, FilingRow } from './FilingRow.tsx';
import { DISMISSED_KEY, DISMISSED_LIMIT, dismissCapture, loadDismissed } from './dismissed.ts';
import { INITIAL_CAPTURE, type CaptureModel } from './machine.ts';
import { useCaptureStore } from './store.ts';
import {
  TARGETED_KEY,
  TARGETED_LIMIT,
  isTargeted,
  loadTargeted,
  rememberTargeted,
} from './targeted.ts';

/** A note row as the device's copy of the library holds it, so a receipt can name it. */
function note(id: string, title: string) {
  return { id, title, updated_at: '2026-08-06T09:14:00.000Z', version: 1, archived: false };
}

beforeEach(() => {
  // Dismissals are kept on the device; each test starts with none.
  localStorage.clear();
  useCaptureStore.setState({ model: INITIAL_CAPTURE });
});

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

  it('shows a failed upload that was aimed at a note, since only this device holds it', async () => {
    /*
     * A moving upload aimed at a note is that note's Recordings tab's to
     * show, and Home left it out. A failed one was left out too — while
     * ResumePrompt leaves the machine's own recording out — so after a spend
     * cap or an expired link bounced the person to Home, the recording had
     * no handle anywhere until a reload.
     */
    sending({
      state: 'failed',
      noteId: 'roof-repair',
      failure: {
        kind: 'spend-capped',
        message: 'Daily spending cap reached. Resend tomorrow.',
        recoverable: true,
      },
    });
    mount([]);

    expect(await screen.findByText(/spending cap reached/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Discard' })).toBeInTheDocument();
  });

  it('still leaves a moving upload aimed at a note to that note', async () => {
    sending({ noteId: 'roof-repair' });
    mount([]);
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

  it('backs off to 15 s two minutes after the last progress', () => {
    expect(
      capturePollInterval(
        [
          capture({
            status: 'transcribing',
            created_at: seconds(3 * 60),
            last_progress_at: seconds(150),
          }),
        ],
        NOW,
      ),
    ).toBe(CAPTURE_POLL_SLOW_MS);
  });

  it('polls once a minute for a capture stuck past ten minutes, and never stops', () => {
    /*
     * It stayed at 4 s for ever: one capture stuck for hours kept an open
     * Home at nine hundred requests an hour. Never `false`, though — the
     * row's Retry appears at fifteen minutes, read at render, and the
     * poll's re-render is what makes it appear on time.
     */
    expect(
      capturePollInterval([capture({ status: 'uploaded', created_at: seconds(11 * 60) })], NOW),
    ).toBe(CAPTURE_POLL_STUCK_MS);
  });

  it('measures from last_progress_at when the server sends it', () => {
    expect(
      capturePollInterval(
        [capture({ status: 'cleaning', created_at: seconds(5 * 60), last_progress_at: seconds(20) })],
        NOW,
      ),
    ).toBe(CAPTURE_POLL_INTERVAL_MS);
  });

  it('the youngest moving capture decides', () => {
    expect(
      capturePollInterval(
        [
          capture({ id: 'stuck', status: 'uploaded', created_at: seconds(11 * 60) }),
          capture({ id: 'new', status: 'uploaded', created_at: seconds(10) }),
        ],
        NOW,
      ),
    ).toBe(CAPTURE_POLL_FAST_MS);
  });

  it('asks again when the app returns to the foreground', async () => {
    /*
     * A ring files three recordings while the phone is in a pocket. Nothing
     * is moving, so the interval is off, and the notes list refetches on
     * focus (the client default) while this query opted out — Home showed
     * the notes moved to the top of Today and no receipt until a pull. The
     * owner: "appears once you refresh".
     */
    const items = [
      capture({ status: 'appended', note_id: 'roof-repair', appended_at: new Date().toISOString() }),
    ];
    const { calls } = mount(items);
    await screen.findByText(/^Filed/);
    const polls = () => calls.filter((call) => call.url.includes('/v1/captures?')).length;
    expect(polls()).toBe(1);

    items.push(
      capture({
        id: 'srv-ring',
        status: 'appended',
        note_id: 'kitchen',
        appended_at: new Date().toISOString(),
        targeted: true,
        source: 'device:dev_1',
      }),
    );
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });

    await waitFor(() => {
      expect(polls()).toBe(2);
    });
    expect(await screen.findAllByRole('button', { name: /open the note/i })).toHaveLength(2);
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

    expect(await screen.findByText(/^Filed/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(screen.queryByText(/^Filed/)).toBeNull();
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

    expect(await screen.findByText(/^Filed/)).toBeInTheDocument();
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
      expect(screen.queryByText(/^Filed/)).toBeNull();
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
    expect(screen.queryByText(/^Filed/)).toBeNull();
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

    // Two rows, two Dismiss buttons; the receipt's is its ×. (The failed row
    // is drawn first whatever the server's order — see the tiers.)
    const receipt = await screen.findByRole('button', { name: /open the note/i });
    await user.click(
      within(receipt.closest('article') as HTMLElement).getByRole('button', { name: 'Dismiss' }),
    );
    await waitFor(() => {
      expect(screen.queryByText(/^Filed/)).toBeNull();
    });
    expect(screen.getByText('Timed out')).toBeInTheDocument();
  });

  it('survives a reload, which is what the device store is for', () => {
    let ids = loadDismissed();
    expect(ids.size).toBe(0);
    ids = dismissCapture('a', ids);
    dismissCapture('b', ids);
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
 * One receipt per note, not one per capture. Live on prod with 4 + 2 + 1
 * filings the two Kitchen-rebuild receipts showed twice while all four
 * Shopping-list receipts were the hidden ones behind "4 more filed"; the
 * owner filed thirteen ring recordings into one note in a day.
 */
describe('receipts are one row per note, behind the rows that still need something', () => {
  const now = () => new Date().toISOString();
  const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString();

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

  it('three captures into one note are one receipt reading "3 filed into “Roof repair”"; opening it dismisses all three', async () => {
    await cacheNoteList([note('roof-repair', 'Roof repair')]);
    const user = userEvent.setup();
    mount(
      ['a', 'b', 'c'].map((id) =>
        capture({ id, status: 'appended', note_id: 'roof-repair', appended_at: now() }),
      ),
      { noteRoute: true },
    );

    const receipt = await screen.findByRole('button', { name: /open the note/i });
    expect(document.querySelectorAll('.filing-row--receipt')).toHaveLength(1);
    expect(screen.getByRole('status')).toHaveTextContent('3 filed into “Roof repair”');
    expect(screen.getByText('just now')).toBeInTheDocument();

    await user.click(receipt);
    expect(await screen.findByText('note screen: roof-repair')).toBeInTheDocument();
    expect(Array.from(loadDismissed()).sort()).toEqual(['a', 'b', 'c']);
  });

  it('groups are newest-landing first and beyond the third sit behind "and N more filed into M notes"', async () => {
    await cacheNoteList([1, 2, 3, 4, 5].map((n) => note(`n${n}`, `Note ${n}`)));
    // Served oldest first, so the order on screen is the landing's, not the server's.
    mount(
      [5, 4, 3, 2, 1].map((n) =>
        capture({ id: `c${n}`, status: 'appended', note_id: `n${n}`, appended_at: minutesAgo(n) }),
      ),
    );

    await screen.findAllByRole('button', { name: /open the note/i });
    const visible = document.querySelectorAll('.filing > .filing-row--receipt');
    expect(visible).toHaveLength(FILED_ROWS_MAX);
    expect(Array.from(visible, (row) => row.textContent)).toEqual([
      expect.stringContaining('Note 1'),
      expect.stringContaining('Note 2'),
      expect.stringContaining('Note 3'),
    ]);

    const more = document.querySelector('details.filing__more');
    expect(more).not.toBeNull();
    expect(more?.querySelector('summary')).toHaveTextContent('and 2 more filed into 2 notes');
    expect(more?.querySelectorAll('.filing-row--receipt')).toHaveLength(2);
    // A native disclosure: the folded rows are on the page, reachable at zero state.
    expect(within(more as HTMLElement).getAllByRole('button', { name: /open the note/i })).toHaveLength(2);
  });

  it('rows are drawn moving → needs you → filed regardless of server order', async () => {
    mount([
      capture({ id: 'done', status: 'appended', note_id: 'roof-repair', appended_at: now() }),
      capture({ id: 'broke', status: 'failed', error: 'Timed out' }),
      capture({ id: 'going', status: 'transcribing' }),
    ]);

    await screen.findByText('Timed out');
    expect(
      Array.from(document.querySelectorAll('.filing-row'), (row) => row.getAttribute('data-status')),
    ).toEqual(['transcribing', 'failed', 'appended']);
  });

  it('the group × dismisses every capture in it and no other', async () => {
    await cacheNoteList([note('roof-repair', 'Roof repair'), note('kitchen', 'Kitchen rebuild')]);
    const user = userEvent.setup();
    mount([
      capture({ id: 'k2', status: 'appended', note_id: 'kitchen', appended_at: now() }),
      capture({ id: 'k1', status: 'appended', note_id: 'kitchen', appended_at: minutesAgo(1) }),
      capture({ id: 'r1', status: 'appended', note_id: 'roof-repair', appended_at: minutesAgo(2) }),
    ]);

    const kitchen = await screen.findByRole('button', { name: /filed into “kitchen rebuild”/i });
    await user.click(within(kitchen.closest('article') as HTMLElement).getByRole('button', { name: 'Dismiss' }));

    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /kitchen rebuild/i })).toBeNull();
    });
    expect(screen.getByRole('button', { name: /filed into “roof repair”/i })).toBeInTheDocument();
    expect(Array.from(loadDismissed()).sort()).toEqual(['k1', 'k2']);
  });

  it('needs_target and failed rows are never grouped or hidden', async () => {
    mount([
      ...[1, 2, 3, 4].map((n) => capture({ id: `ask-${n}`, status: 'needs_target' })),
      capture({ id: 'broke', status: 'failed', error: 'Timed out' }),
    ]);

    expect(await screen.findAllByText(/which note should this go in/i)).toHaveLength(4);
    expect(document.querySelectorAll('.filing-row')).toHaveLength(5);
    expect(document.querySelector('details')).toBeNull();
    expect(document.querySelector('.filing-row--receipt')).toBeNull();
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

  it('keeps the live region when the row turns into a receipt, so the landing is announced', async () => {
    /*
     * A live region announces a change to its text, not the text it mounted
     * with. When the receipt was its own subtree, React swapped the
     * `role="status"` node for a fresh one at the flip, and the moment of
     * filing — what a person waiting on Home is listening for — went unspoken.
     */
    const { refetchCaptures } = mountWithPolls([[filing], [filed]]);
    const live = await screen.findByRole('status');
    expect(live).toHaveTextContent('Filing your recording');

    await refetchCaptures();

    await waitFor(() => {
      expect(live).toHaveTextContent(/^Filed/);
    });
    expect(screen.getByRole('status')).toBe(live);
    expect(screen.getByRole('button', { name: /open the note/i })).toHaveAccessibleName(/^Filed/);
  });

  it('does not refetch for a capture that was already appended last time', async () => {
    const { refetchCaptures, noteReads } = mountWithPolls([[filed], [filed]]);

    await screen.findByText('body: before');
    await screen.findByText(/^Filed/);
    await refetchCaptures();

    // Same answer twice; nothing changed, nothing to refresh.
    await waitFor(() => {
      expect(screen.getByText(/^Filed/)).toBeInTheDocument();
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

    expect(await screen.findByText(/^Filed/)).toBeInTheDocument();
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

  it('shows a capture a device sent into a note, since nobody watched it land', async () => {
    /*
     * `X-Chintan-Note-Id` on the inbox makes the capture `targeted` on the
     * wire — a note was chosen — but no person was on that note's Recordings
     * tab to see a ring's recording arrive. The moment the ring's recipe
     * gained the header, every receipt would have disappeared.
     */
    mount([
      capture({
        id: 'srv-ring',
        status: 'appended',
        note_id: 'roof-repair',
        targeted: true,
        source: 'device:dev_1',
        appended_at: new Date().toISOString(),
      }),
    ]);
    expect(await screen.findByRole('button', { name: /open the note/i })).toBeInTheDocument();
  });

  it('still leaves a recording this device made into a note to that note', async () => {
    mount([
      capture({ id: 'srv-app', status: 'appended', note_id: 'roof-repair', targeted: true, source: 'app' }),
      capture({ id: 'srv-legacy', status: 'appended', note_id: 'roof-repair', targeted: true }),
    ]);
    await waitFor(() => {
      expect(screen.queryByRole('region', { name: /recordings being filed/i })).toBeNull();
    });
  });

  it('isTargeted answers no for a device, whatever the flag or the memory says', () => {
    const remembered = new Set(['srv-ring']);
    expect(isTargeted({ id: 'srv-ring', targeted: true, source: 'device:dev_1' }, remembered)).toBe(false);
    expect(isTargeted({ id: 'srv-app', targeted: true, source: 'app' }, new Set())).toBe(true);
    expect(isTargeted({ id: 'srv-legacy', targeted: true }, new Set())).toBe(true);
    expect(isTargeted({ id: 'srv-routed', targeted: false, source: 'app' }, new Set())).toBe(false);
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
