import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { queryKeys } from '@/api/queries.ts';
import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';
import {
  AUDIO_URL,
  CAPTURE,
  NOTE,
  SEGMENTS_DOC,
  apiStub,
  artifactsStub,
  bucketStub,
  captureSaves,
  isSummary,
  json,
  mount,
  recordingRows,
  withSegments,
} from '@/test/recordings.tsx';

/*
 * One row: its player, its More menu, its swipe tray, the chip for what
 * Whisper heard, and transcribing it again. The row is mounted inside
 * `Recordings` rather than alone: which row is open and the notice line an
 * action reports on are the list's, and a row rendered with twenty props and
 * no line under it would prove nothing about what the user is told.
 * `Recordings.test.tsx` has the list's own concerns — deleting, moving,
 * selecting, the row still being made — and `labels.test.ts` the sentences.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * The audio lives in a bucket on another origin, behind a presigned URL. Two
 * requests go there for one recording — the `<audio>` element's, and the
 * "Download audio" fetch — and on the live app the second failed every time:
 * the element had fetched in no-cors mode, S3 had answered without
 * `Access-Control-Allow-Origin` (no `Origin` was sent), and Chromium served
 * that cached response to the CORS `fetch()`, which then failed its check.
 */
describe('the audio is fetched the same way twice', () => {
  it('loads the recording into the element as a CORS request', async () => {
    bucketStub();
    mount(apiStub().fetchImpl);

    await screen.findByRole('region', { name: 'Recording' });
    const audio = document.querySelector('audio');
    expect(audio).not.toBeNull();
    expect(audio).toHaveAttribute('src', AUDIO_URL);
    // Without this the element's no-cors response poisons the cache for the
    // download's CORS fetch. See the component.
    expect(audio).toHaveAttribute('crossorigin', 'anonymous');
  });

  it('downloads with a request the media element’s cached answer can never satisfy', async () => {
    const user = userEvent.setup();
    const saves = captureSaves();
    try {
      const bucket = bucketStub();
      mount(apiStub().fetchImpl);
      // From the row's menu: the open row's own Download button is gone (T41).
      await screen.findByRole('region', { name: 'Recording' });
      expect(screen.queryByRole('button', { name: 'Download audio' })).toBeNull();
      await user.click(screen.getByRole('button', { name: /more for recording from/i }));
      await user.click(screen.getByRole('menuitem', { name: 'Download audio' }));

      expect(await screen.findByText('Downloaded')).toBeInTheDocument();
      expect(bucket).toHaveBeenCalledWith(
        'https://chintan-content.s3.test/cap-1/audio.webm?sig=1',
        expect.objectContaining({ cache: 'no-store' }),
      );
      // Named by the server's manifest, like every download from this tab.
      expect(saves.names).toEqual(['roof-repair-cap-1.webm']);
    } finally {
      saves.restore();
    }
  });
});

describe('a row’s More menu', () => {
  it('offers Move to…, Delete recording, Download audio, Transcribe again and Select', async () => {
    const user = userEvent.setup();
    bucketStub();
    mount(apiStub().fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    const menu = screen.getByRole('menu');
    expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Move to…',
      'Delete recording',
      'Download audio',
      'Transcribe again in English',
      'Select',
    ]);
    // Escape closes it and puts focus back on the trigger.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.getByRole('button', { name: /more for recording from/i })).toHaveFocus();
  });
});

describe('copying from a row’s menu', () => {

  it('copies this recording’s transcript from the open row and says so on the notice line', async () => {
    // The item sits in one recording's menu. "Copy transcript" read as the
    // note's transcript; a whole-note copy already exists under Share, so this
    // one says which recording it copies.
    const user = userEvent.setup();
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    artifactsStub();
    mount(withSegments().fetchImpl);

    await screen.findByRole('button', { name: /Ellis quoted nine hundred\./ });
    await user.click(screen.getAllByRole('button', { name: /more for recording from/i })[0]!);
    await user.click(screen.getByRole('menuitem', { name: 'Copy this transcript' }));

    expect(writeText).toHaveBeenCalledWith('Ridge tiles have slipped.\nEllis quoted nine hundred.');
    expect(await screen.findByText('Copied')).toBeInTheDocument();
    // The transcript panel has no copy control of its own any more.
    expect(screen.queryByRole('button', { name: /copy/i })).toBeNull();
  });

  it('offers no copy on a closed row, whose text is not in hand', async () => {
    const user = userEvent.setup();
    artifactsStub();
    mount(withSegments().fetchImpl);

    await screen.findByRole('button', { name: /Ellis quoted nine hundred\./ });
    await user.click(screen.getAllByRole('button', { name: /more for recording from/i })[1]!);
    expect(
      within(screen.getByRole('menu'))
        .getAllByRole('menuitem')
        .map((item) => item.textContent),
    ).toEqual(['Move to…', 'Delete recording', 'Download audio', 'Transcribe again in English', 'Select']);
  });
});

/**
 * What Whisper heard (T8): the worker has always stored the detected language
 * in `segments.json` and the app never read it, so a Malayalam recording that
 * came back in Tamil script looked like any other. On the open row a chip says
 * "Heard as Tamil" when that is not the language the note asked for, and is
 * the way to transcribe it again.
 */
describe('the language Whisper heard', () => {
  it('is a chip, and the fix, when it is not the note’s language', async () => {
    const user = userEvent.setup();
    artifactsStub('Tamil');
    const api = withSegments({ language: 'ml' }, [{ ...CAPTURE, has_segments: true }]);
    mount(api.fetchImpl);

    const chip = await screen.findByRole('button', {
      name: 'Heard as Tamil — transcribe again in Malayalam',
    });
    await user.click(chip);
    await waitFor(() => {
      expect(api.calls).toContainEqual(
        expect.objectContaining({ method: 'POST', path: '/v1/captures/cap-1/retranscribe' }),
      );
    });
  });

  it('says nothing when Whisper heard what was asked for', async () => {
    artifactsStub('English');
    mount(withSegments({}, [{ ...CAPTURE, has_segments: true }]).fetchImpl);
    await screen.findByRole('button', { name: /Ellis quoted nine hundred\./ });
    expect(screen.queryByText(/heard as/i)).toBeNull();
  });
});

/**
 * Transcribe again (T7): a recording that came back in the wrong script, or
 * with sentences missing, could only be deleted and re-recorded — the
 * pipeline transcribes once, and Retry resumes from the last good artifact.
 */
describe('transcribing a recording again', () => {
  it('is worded for the note’s language, posts to the capture and follows the run', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub({ ...NOTE, language: 'ml' });
    mount(api.fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Transcribe again in Malayalam' }));

    await waitFor(() => {
      expect(api.calls).toContainEqual(
        expect.objectContaining({ method: 'POST', path: '/v1/captures/cap-1/retranscribe', body: {} }),
      );
    });
    // The 202's capture is on the row at once: the stage strip is back.
    expect(await screen.findByRole('list', { name: 'Filing progress' })).toBeInTheDocument();
    expect(screen.getByText('Filing…')).toBeInTheDocument();
  });

  it('shows the transcript the run lands, not the one the row had in hand', async () => {
    const user = userEvent.setup();
    const bucket = artifactsStub('Tamil');
    const api = withSegments({ language: 'ml' }, [{ ...CAPTURE, has_segments: true }]);
    const { queryClient } = mount(api.fetchImpl);

    const chip = await screen.findByRole('button', {
      name: 'Heard as Tamil — transcribe again in Malayalam',
    });
    expect(screen.getByRole('button', { name: /Ellis quoted nine hundred\./ })).toBeInTheDocument();
    await user.click(chip);

    // While it runs the row says so; the old text and its chip are not
    // offered, because a second tap on the chip would be a 409.
    await screen.findByRole('list', { name: 'Filing progress' });
    expect(screen.queryByText(/heard as/i)).toBeNull();
    expect(screen.queryByRole('button', { name: /Ellis quoted/ })).toBeNull();
    // The 202's refetch of the note has settled, so what lands next stays.
    await waitFor(() => {
      expect(queryClient.getQueryState(queryKeys.note(NOTE.id))?.fetchStatus).toBe('idle');
    });

    // The worker writes a new document, and every stage bumps the capture's
    // version; the poll brings the landed row.
    bucket.doc = {
      ...SEGMENTS_DOC,
      language: 'Malayalam',
      segments: [{ start_ms: 0, end_ms: 3_000, text: ' Nine hundred, Ellis said, in Malayalam.' }],
    };
    const landed: CaptureWire = {
      ...CAPTURE,
      has_segments: true,
      status: 'appended',
      version: CAPTURE.version + 1,
    };
    api.note.captures = [landed];
    act(() => {
      queryClient.setQueryData<NoteDetailWire>(queryKeys.note(NOTE.id), (current) =>
        current ? { ...current, captures: [landed] } : current,
      );
    });

    expect(
      await screen.findByRole('button', { name: /Nine hundred, Ellis said, in Malayalam\./ }),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Ellis quoted/ })).toBeNull();
    expect(screen.queryByText(/heard as/i)).toBeNull();
  });

  it('is not offered while the recording is still moving', async () => {
    const user = userEvent.setup();
    bucketStub();
    mount(apiStub({ ...NOTE, captures: [{ ...CAPTURE, status: 'cleaning' }] }).fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    expect(screen.queryByRole('menuitem', { name: /transcribe again/i })).toBeNull();
  });

  it('says so when the server is older than the route', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub(NOTE, {
      retranscribe: () => json({ type: 'about:blank', title: 'Not found', status: 404 }, 404),
    });
    mount(api.fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Transcribe again in English' }));

    expect(
      await screen.findByText('Transcribing again is not available on this server yet.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('list', { name: 'Filing progress' })).toBeNull();
  });
});

describe('a row swiped aside', () => {
  const touch = { pointerId: 1, pointerType: 'touch', button: 0 };

  function swipeOpen(summary: HTMLElement): HTMLElement {
    const row = summary.closest('.swipe') as HTMLElement;
    fireEvent.pointerDown(row, { ...touch, clientX: 300, clientY: 10 });
    fireEvent.pointerMove(row, { ...touch, clientX: 280, clientY: 10 });
    fireEvent.pointerMove(row, { ...touch, clientX: 160, clientY: 10 });
    fireEvent.pointerUp(row, { ...touch, clientX: 160, clientY: 10 });
    fireEvent.click(summary); // the lifted finger's click, swallowed
    return row;
  }

  it('offers Move and Delete, hidden from assistive technology until it is open', async () => {
    bucketStub();
    mount(apiStub().fetchImpl);
    await screen.findByRole('button', { name: /more for recording from/i });

    // Closed, the tray has no accessible name to query by — hidden things do
    // not — so it is found by class and its label read off the attribute.
    const tray = document.querySelector('.swipe__tray') as HTMLElement;
    expect(tray).toHaveAttribute('role', 'group');
    expect(tray).toHaveAttribute('aria-label', expect.stringMatching(/actions for recording from/i));
    expect(tray).toHaveAttribute('aria-hidden', 'true');
    expect(
      within(tray)
        .getAllByRole('button', { hidden: true })
        .map((button) => button.textContent),
    ).toEqual(['Move', 'Delete']);
  });

  it('Delete opens the same confirmation the menu does; Move the same sheet', async () => {
    const user = userEvent.setup();
    bucketStub();
    mount(apiStub().fetchImpl);
    const summary = await screen.findByRole('button', { name: isSummary, expanded: true });

    swipeOpen(summary);
    await user.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete this recording?' });
    // Nothing to type (owner, 2026-09-26): the sentence is the warning.
    expect(within(dialog).queryByRole('textbox')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    swipeOpen(summary);
    await user.click(screen.getByRole('button', { name: 'Move' }));
    expect(await screen.findByRole('dialog', { name: /move this recording to/i })).toBeInTheDocument();
  });

  it('is not offered while recordings are being selected', async () => {
    const user = userEvent.setup();
    bucketStub();
    mount(apiStub().fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));
    await screen.findByRole('toolbar', { name: 'Recording actions' });

    expect(document.querySelector('.swipe__tray')).toBeNull();
  });
});

/*
 * Rows that came in through the inbox (2026-09-24 contract): a watch or a
 * shortcut sent them under a device key, and some of them were words rather
 * than audio.
 */
describe('a capture that a device sent', () => {
  const DEVICES = { items: [{ id: 'dev_1', name: 'Watch', created_at: '2026-08-01T00:00:00Z' }] };

  /** The stub server plus the devices list, and no audio for a text capture. */
  function withDevices(api: ReturnType<typeof apiStub>, textId?: string): typeof fetch {
    return async (input, init) => {
      const url = new URL(String(input));
      if (url.pathname.endsWith('/v1/devices')) return json(DEVICES);
      if (textId && url.pathname.includes(`/captures/${textId}/download`)) {
        if (url.searchParams.get('kind') === 'clean') {
          return json({ url: 'https://bucket.test/clean.txt', expires_at: '2099-01-01T00:00:00Z' });
        }
        return json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
      }
      return api.fetchImpl(input, init);
    };
  }

  it('says which device, from the devices list, and "a device" when the list does not know it', async () => {
    bucketStub();
    const api = apiStub({
      ...NOTE,
      captures: [
        { ...CAPTURE, id: 'cap-w', created_at: '2026-08-06T10:00:00.000Z', source: 'device:dev_1' },
        { ...CAPTURE, id: 'cap-g', created_at: '2026-08-06T09:30:00.000Z', source: 'device:dev_gone' },
        CAPTURE,
      ],
    });
    mount(withDevices(api));

    const rows = await recordingRows();
    await waitFor(() => {
      expect(rows[0]).toHaveTextContent('From Watch');
    });
    expect(rows[1]).toHaveTextContent('From a device');
    // A recording made here says nothing about where it came from.
    expect(rows[2]).not.toHaveTextContent('From');
  });

  it('renders words sent as words with the transcript and no player', async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('Buy milk on the way home.', { status: 200 })),
    );
    const text: CaptureWire = {
      ...CAPTURE,
      id: 'cap-t',
      created_at: '2026-08-06T10:00:00.000Z',
      source: 'device:dev_1',
      has_audio: false,
      has_peaks: false,
      has_segments: false,
      duration_ms: 0,
    };
    const api = apiStub({ ...NOTE, captures: [text, CAPTURE] });
    mount(withDevices(api, 'cap-t'));

    const rows = await recordingRows();
    const row = rows[0]!;
    // The newest finished row opens on arrival, and this one is words.
    expect(await within(row).findByText('Buy milk on the way home.')).toBeInTheDocument();
    expect(within(row).queryByRole('region', { name: 'Recording' })).toBeNull();
    expect(within(row).queryByRole('button', { name: /play recording/i })).toBeNull();
    expect(row).not.toHaveTextContent(/no longer stored/i);
    // Words never had timestamps for cleanup to lose, so the panel does not say so.
    expect(row).not.toHaveTextContent(/no reliable timestamps/i);
    expect(row).toHaveTextContent('From Watch');

    // Nothing to download or transcribe again; the words themselves copy.
    await user.click(within(row).getByRole('button', { name: /more for recording from/i }));
    expect(
      within(screen.getByRole('menu'))
        .getAllByRole('menuitem')
        .map((item) => item.textContent),
    ).toEqual(['Move to…', 'Delete recording', 'Copy this cleaned text', 'Select']);
  });
});
