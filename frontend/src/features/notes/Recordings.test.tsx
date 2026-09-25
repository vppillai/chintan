import type { QueryClient } from '@tanstack/react-query';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { unzipSync } from 'fflate';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { queryKeys, useNote } from '@/api/queries.ts';
import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';
import { INITIAL_CAPTURE, type CaptureModel } from '@/features/capture/machine.ts';
import { LONG_PRESS_MS } from '@/hooks/useLongPress.ts';
import { bytesOf } from '@/test/blob.ts';
import { TEST_NOTES, TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import {
  Recordings,
  filedLabel,
  heardAs,
  justLanded,
  retranscribeLabel,
  sourceLabel,
} from './Recordings.tsx';
import { describeMoment } from './groups.ts';

/**
 * The audio lives in a bucket on another origin, behind a presigned URL. Two
 * requests go there for one recording — the `<audio>` element's, and the
 * "Download audio" fetch — and on the live app the second failed every time:
 * the element had fetched in no-cors mode, S3 had answered without
 * `Access-Control-Allow-Origin` (no `Origin` was sent), and Chromium served
 * that cached response to the CORS `fetch()`, which then failed its check.
 */

const AUDIO_URL =
  'https://chintan-content.s3.test/tenants/t1/captures/cap-1/audio.webm?X-Amz-Signature=abc';

const CAPTURE: CaptureWire = {
  id: 'cap-1',
  status: 'appended',
  created_at: '2026-08-06T09:10:00.000Z',
  version: 1,
  note_id: 'roof-repair',
  duration_ms: 12_000,
  has_peaks: false,
  has_segments: false,
};

const OLDER: CaptureWire = {
  ...CAPTURE,
  id: 'cap-0',
  created_at: '2026-08-05T17:40:00.000Z',
};

const NOTE: NoteDetailWire = {
  id: 'roof-repair',
  title: 'Roof repair',
  body: 'Ridge tiles.\n\nEllis quoted nine hundred.',
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
  captures: [CAPTURE],
};

/**
 * The row's disclosure button, named by when the recording was made and how
 * long it runs: a filed row says nothing about being filed (T19), so the
 * moment is what tells the rows apart.
 */
function isSummary(name: string): boolean {
  return [CAPTURE, OLDER].some((capture) => name.startsWith(describeMoment(capture.created_at)));
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'content-type': status >= 400 ? 'application/problem+json' : 'application/json',
    },
  });
}

/**
 * A small stateful server: the note as `GET /v1/notes/{id}` answers it,
 * presigned URLs, the note list for the move sheet, a recordings manifest, and
 * the two mutations answering as the contract says — and *changing the note*,
 * so the refetch the app makes afterwards sees one recording fewer, as the
 * real server's would. Tests override what they need to.
 */
function apiStub(
  initial: NoteDetailWire = NOTE,
  overrides: Partial<
    Record<
      'delete' | 'move' | 'manifest' | 'segments' | 'retranscribe',
      (init?: RequestInit) => Response
    >
  > = {},
) {
  const note: NoteDetailWire = structuredClone(initial);
  const calls: { method: string; path: string; body?: unknown }[] = [];
  const drop = (captureId: string): void => {
    note.captures = (note.captures ?? []).filter((capture) => capture.id !== captureId);
    note.version += 1;
  };
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    calls.push({
      method,
      path: url.pathname + url.search,
      ...(typeof init?.body === 'string' ? { body: JSON.parse(init.body) as unknown } : {}),
    });
    if (url.pathname.endsWith('/download')) {
      if (url.searchParams.get('kind') === 'audio') {
        return json({ url: AUDIO_URL, expires_at: new Date(Date.now() + 900_000).toISOString() });
      }
      if (url.searchParams.get('kind') === 'segments' && overrides.segments) {
        return overrides.segments(init);
      }
      return json({ type: 'about:blank', title: 'Not found', status: 404 }, 404);
    }
    if (url.pathname.endsWith('/recordings/urls')) {
      return (
        overrides.manifest?.(init) ??
        json({
          items: [OLDER, CAPTURE].map((capture) => ({
            capture_id: capture.id,
            filename: `roof-repair-${capture.id}.webm`,
            url: `https://chintan-content.s3.test/${capture.id}/audio.webm?sig=1`,
            expires_at: new Date(Date.now() + 900_000).toISOString(),
          })),
        })
      );
    }
    const captureId = /\/v1\/captures\/([^/]+)/.exec(url.pathname)?.[1] ?? '';
    if (method === 'DELETE' && captureId) {
      if (overrides.delete) return overrides.delete(init);
      drop(captureId);
      return new Response(null, { status: 204 });
    }
    if (method === 'POST' && url.pathname.endsWith('/move')) {
      if (overrides.move) return overrides.move(init);
      drop(captureId);
      return json({ ...CAPTURE, id: captureId, note_id: 'reading-list' });
    }
    if (method === 'POST' && url.pathname.endsWith('/retranscribe')) {
      if (overrides.retranscribe) return overrides.retranscribe(init);
      // Back at the start of the pipeline, as the real server answers.
      const row = (note.captures ?? []).find((capture) => capture.id === captureId);
      if (row) row.status = 'transcribing';
      return json({ ...CAPTURE, id: captureId, status: 'transcribing' }, 202);
    }
    if (url.pathname.endsWith('/v1/notes')) {
      return json({
        items: [
          ...TEST_NOTES,
          { ...TEST_NOTES[0]!, id: 'fence', title: 'Old fence', archived: false },
        ],
      });
    }
    if (url.pathname.endsWith(`/v1/notes/${note.id}`)) return json(note);
    return json({ items: [] });
  });
  return { fetchImpl, calls, note };
}

function bucketStub() {
  // A string body: the runtime's `Response` serialises a jsdom `Blob` as
  // "[object Blob]", which is not what a bucket returns.
  const bucket = vi.fn<typeof fetch>(
    async () =>
      new Response('webm bytes', {
        status: 200,
        headers: { 'content-type': 'audio/webm' },
      }),
  );
  vi.stubGlobal('fetch', bucket);
  return bucket;
}

/** The note screen's own wiring: the recordings read the note from the query. */
function Host({
  noteId,
  localUpload,
  onSelectingChange,
}: {
  noteId: string;
  localUpload?: CaptureModel | null;
  onSelectingChange: () => void;
}) {
  const { data } = useNote(noteId);
  if (!data) return <p>Loading…</p>;
  return (
    <Recordings note={data} localUpload={localUpload ?? null} onSelectingChange={onSelectingChange} />
  );
}

function mount(
  fetchImpl: typeof fetch,
  queryClient: QueryClient = testQueryClient(),
  localUpload: CaptureModel | null = null,
) {
  const onSelectingChange = vi.fn();
  render(
    <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
      <MemoryRouter>
        <Host noteId={NOTE.id} localUpload={localUpload} onSelectingChange={onSelectingChange} />
      </MemoryRouter>
    </TestProviders>,
  );
  return { queryClient, onSelectingChange };
}

/** The save path: what filename each `<a download>` was clicked with. */
function captureSaves(): { names: string[]; blobs: Blob[]; restore: () => void } {
  const names: string[] = [];
  const blobs: Blob[] = [];
  URL.createObjectURL = vi.fn((blob: Blob | MediaSource) => {
    blobs.push(blob as Blob);
    return 'blob:mock-url';
  });
  URL.revokeObjectURL = vi.fn();
  const originalClick = HTMLAnchorElement.prototype.click;
  HTMLAnchorElement.prototype.click = function (this: HTMLAnchorElement) {
    names.push(this.download);
  };
  return {
    names,
    blobs,
    restore: () => {
      HTMLAnchorElement.prototype.click = originalClick;
    },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

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

const SEGMENTS_DOC = {
  version: 1,
  language: 'English',
  segments: [
    { start_ms: 0, end_ms: 3_000, text: ' Ridge tiles have slipped.' },
    { start_ms: 3_000, end_ms: 6_000, text: ' Ellis quoted nine hundred.' },
  ],
};

/**
 * The bucket answers the segments document for its URL and audio bytes for
 * the rest. `doc` is what it answers next, so a test can land a new one.
 */
function artifactsStub(language = 'English'): { doc: typeof SEGMENTS_DOC } {
  const bucket = { doc: { ...SEGMENTS_DOC, language } };
  // jsdom implements no scrolling, and the transcript follows playback.
  Element.prototype.scrollIntoView = vi.fn();
  vi.stubGlobal(
    'fetch',
    vi.fn<typeof fetch>(async (input) =>
      String(input).includes('/segments')
        ? new Response(JSON.stringify(bucket.doc), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          })
        : new Response('webm bytes', { status: 200, headers: { 'content-type': 'audio/webm' } }),
    ),
  );
  return bucket;
}

/** The roof note with a transcript behind its newest recording, and a presigned URL for it. */
function withSegments(note: Partial<NoteDetailWire> = {}, captures = [{ ...CAPTURE, has_segments: true }, OLDER]) {
  return apiStub(
    { ...NOTE, ...note, captures },
    {
      segments: () =>
        json({
          url: 'https://chintan-content.s3.test/cap-1/segments.json?sig=1',
          expires_at: new Date(Date.now() + 900_000).toISOString(),
        }),
    },
  );
}

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

  it('is decided by name against the effective language, and always said under auto-detect', () => {
    expect(heardAs('Tamil', 'ml')).toBe('Tamil');
    expect(heardAs('malayalam', 'ml')).toBeNull();
    expect(heardAs('English', 'en')).toBeNull();
    expect(heardAs('English', 'auto')).toBe('English');
    expect(heardAs(null, 'ml')).toBeNull();
    // A code the curated list lacks still compares by its Intl name.
    expect(heardAs('Icelandic', 'is')).toBeNull();
    expect(heardAs('Icelandic', 'en')).toBe('Icelandic');
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

  it('is honest about auto-detect', () => {
    expect(retranscribeLabel('ta')).toBe('Transcribe again in Tamil');
    expect(retranscribeLabel('auto')).toBe('Transcribe again (auto-detect)');
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

  it('Delete opens the same typed confirmation the menu does; Move the same sheet', async () => {
    const user = userEvent.setup();
    bucketStub();
    mount(apiStub().fetchImpl);
    const summary = await screen.findByRole('button', { name: isSummary, expanded: true });

    swipeOpen(summary);
    await user.click(screen.getByRole('button', { name: 'Delete' }));
    expect(await screen.findByRole('dialog', { name: 'Delete this recording?' })).toBeInTheDocument();
    expect(screen.getByLabelText('Type "delete" to confirm')).toBeInTheDocument();
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

describe('deleting a recording', () => {
  it('asks for the word, deletes the capture, drops the row and refetches the note', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub();
    const { queryClient } = mount(api.fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete recording' }));

    const dialog = await screen.findByRole('dialog');
    // It says what else goes: the paragraph the recording dictated.
    expect(dialog).toHaveTextContent(/paragraph it dictated/i);
    const confirm = within(dialog).getByRole('button', { name: 'Delete it' });
    expect(confirm).toBeDisabled();
    await user.type(within(dialog).getByLabelText('Type "delete" to confirm'), 'delete');
    await user.click(confirm);

    await waitFor(() => {
      expect(api.calls).toContainEqual(
        expect.objectContaining({ method: 'DELETE', path: '/v1/captures/cap-1' }),
      );
    });
    // The row is gone at once, and the note is asked for again for its body.
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: /more for recording from/i })).toBeNull();
    });
    await waitFor(() => {
      expect(
        api.calls.filter((c) => c.method === 'GET' && c.path === '/v1/notes/roof-repair').length,
      ).toBeGreaterThanOrEqual(2);
    });
    await waitFor(() => {
      expect(
        queryClient.getQueryData<NoteDetailWire>(queryKeys.note('roof-repair'))?.captures,
      ).toEqual([]);
    });
    expect(await screen.findByText('Recording deleted')).toBeInTheDocument();
  });

  it('says to wait when the recording is still filing (409)', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub(
      { ...NOTE, captures: [{ ...CAPTURE, status: 'transcribing' }] },
      {
        delete: () =>
          json(
            { type: 'about:blank', title: 'Conflict', status: 409, detail: 'Still in the pipeline.' },
            409,
          ),
      },
    );
    mount(api.fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete recording' }));
    await user.type(await screen.findByLabelText('Type "delete" to confirm'), 'delete');
    await user.click(screen.getByRole('button', { name: 'Delete it' }));

    expect(await screen.findByText('Wait until it has finished filing.')).toBeInTheDocument();
    // The row stays.
    expect(screen.getByRole('button', { name: /more for recording from/i })).toBeInTheDocument();
  });
});

describe('moving a recording to another note', () => {
  it('lists the other active notes, recent first, with no way to create one', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub();
    mount(api.fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Move to…' }));

    const sheet = await screen.findByRole('dialog', { name: /move this recording to/i });
    const options = await within(sheet).findAllByRole('button', { name: /reading list|old fence/i });
    // The note being moved out of is not offered, and there is no "new note".
    expect(within(sheet).queryByRole('button', { name: /roof repair/i })).toBeNull();
    expect(within(sheet).queryByPlaceholderText(/new note/i)).toBeNull();
    expect(within(sheet).queryByRole('button', { name: /create/i })).toBeNull();
    expect(options.length).toBe(2);
    // Each option says when it was touched and how its text begins (T42), so
    // two notes with the same dictated title can be told apart.
    expect(options[0]).toHaveTextContent(/Ridge tiles on the south slope have slip…/);
    expect(options[0]).toHaveTextContent(/Aug/);

    // The search field narrows the list.
    await user.type(within(sheet).getByRole('searchbox', { name: 'Search notes' }), 'fence');
    expect(within(sheet).queryByRole('button', { name: /reading list/i })).toBeNull();
    expect(within(sheet).getByRole('button', { name: /old fence/i })).toBeInTheDocument();
  });

  it('moves it, removes the row here, invalidates both notes and offers to open the target', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub();
    const queryClient = testQueryClient();
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries');
    mount(api.fetchImpl, queryClient);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Move to…' }));
    const sheet = await screen.findByRole('dialog');
    await user.click(await within(sheet).findByRole('button', { name: /reading list/i }));

    await waitFor(() => {
      expect(api.calls).toContainEqual(
        expect.objectContaining({
          method: 'POST',
          path: '/v1/captures/cap-1/move',
          body: { note_id: 'reading-list' },
        }),
      );
    });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    expect(screen.queryByRole('button', { name: /more for recording from/i })).toBeNull();
    expect(await screen.findByText(/recording moved to “reading list”/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Open Reading list' })).toBeInTheDocument();

    const invalidated = invalidate.mock.calls.map(([filters]) => JSON.stringify(filters?.queryKey));
    expect(invalidated).toContain(JSON.stringify(queryKeys.note('roof-repair')));
    expect(invalidated).toContain(JSON.stringify(queryKeys.note('reading-list')));
  });
});

describe('selecting several recordings', () => {
  const TWO = { ...NOTE, captures: [CAPTURE, OLDER] };

  it('enters selection from the menu, with a bar above the tab bar and Select all', async () => {
    const user = userEvent.setup();
    bucketStub();
    const { onSelectingChange } = mount(apiStub(TWO).fetchImpl);

    await user.click((await screen.findAllByRole('button', { name: /more for recording from/i }))[0]!);
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));

    const bar = await screen.findByRole('toolbar', { name: 'Recording actions' });
    expect(onSelectingChange).toHaveBeenLastCalledWith(true);
    expect(screen.getAllByRole('checkbox')).toHaveLength(2);
    expect(within(bar).getByText((_c, el) => el?.textContent === '1 selected')).toBeInTheDocument();
    // The open player closed: a checkbox row has one thing to tap.
    expect(screen.queryByRole('region', { name: 'Recording' })).toBeNull();

    await user.click(within(bar).getByRole('button', { name: 'Select all' }));
    expect(within(bar).getByText((_c, el) => el?.textContent === '2 selected')).toBeInTheDocument();
    expect(within(bar).getByRole('button', { name: 'Download' })).toBeInTheDocument();
    expect(within(bar).getByRole('button', { name: 'Move' })).toBeInTheDocument();
    expect(within(bar).getByRole('button', { name: 'Delete' })).toBeInTheDocument();

    await user.click(within(bar).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('toolbar')).toBeNull();
    expect(onSelectingChange).toHaveBeenLastCalledWith(false);
  });

  it('enters selection on a long press, and the press is not also a tap', async () => {
    bucketStub();
    mount(apiStub(TWO).fetchImpl);
    const rows = await screen.findAllByRole('button', { name: isSummary });
    const older = rows[1]!;

    fireEvent.pointerDown(older, { pointerType: 'touch', clientX: 10, clientY: 10 });
    await act(() => new Promise((resolve) => setTimeout(resolve, LONG_PRESS_MS + 60)));
    fireEvent.pointerUp(older, { pointerType: 'touch' });

    const bar = await screen.findByRole('toolbar', { name: 'Recording actions' });
    expect(within(bar).getByText((_c, el) => el?.textContent === '1 selected')).toBeInTheDocument();
    const boxes = screen.getAllByRole('checkbox');
    expect(boxes[1]).toBeChecked();
    expect(boxes[0]).not.toBeChecked();
  });

  it('enters selection on a held mouse button too, the same gesture Home teaches', async () => {
    // `useLongPress` takes every pointer's primary button since the hover
    // checkbox left Home (2026-09-24, C); this tab shares the hook, on purpose.
    bucketStub();
    mount(apiStub(TWO).fetchImpl);
    const rows = await screen.findAllByRole('button', { name: isSummary });
    const older = rows[1]!;

    fireEvent.pointerDown(older, { pointerType: 'mouse', button: 0, clientX: 10, clientY: 10 });
    await act(() => new Promise((resolve) => setTimeout(resolve, LONG_PRESS_MS + 60)));
    fireEvent.pointerUp(older, { pointerType: 'mouse', button: 0 });

    await screen.findByRole('toolbar', { name: 'Recording actions' });
    expect(screen.getAllByRole('checkbox')[1]).toBeChecked();
  });

  it('a press that moves is a scroll, not a selection', async () => {
    bucketStub();
    mount(apiStub(TWO).fetchImpl);
    const [row] = await screen.findAllByRole('button', { name: isSummary });

    fireEvent.pointerDown(row!, { pointerType: 'touch', clientX: 10, clientY: 10 });
    fireEvent.pointerMove(row!, { pointerType: 'touch', clientX: 10, clientY: 40 });
    await act(() => new Promise((resolve) => setTimeout(resolve, LONG_PRESS_MS + 60)));
    fireEvent.pointerUp(row!, { pointerType: 'touch' });

    expect(screen.queryByRole('toolbar')).toBeNull();
  });

  it('downloads the selected recordings as one stored zip named after the note', async () => {
    const user = userEvent.setup();
    const saves = captureSaves();
    try {
      const bucket = bucketStub();
      const api = apiStub(TWO);
      mount(api.fetchImpl);

      await user.click((await screen.findAllByRole('button', { name: /more for recording from/i }))[0]!);
      await user.click(screen.getByRole('menuitem', { name: 'Select' }));
      const bar = await screen.findByRole('toolbar', { name: 'Recording actions' });
      await user.click(within(bar).getByRole('button', { name: 'Select all' }));
      await user.click(within(bar).getByRole('button', { name: 'Download' }));

      await waitFor(() => {
        expect(saves.names).toEqual(['roof-repair-recordings.zip']);
      });
      // One manifest request, then each file fetched with `no-store`.
      expect(api.calls.filter((c) => c.path === '/v1/notes/roof-repair/recordings/urls')).toHaveLength(1);
      expect(bucket).toHaveBeenCalledTimes(2);
      for (const [, init] of bucket.mock.calls) {
        expect(init).toEqual(expect.objectContaining({ cache: 'no-store' }));
      }
      const zip = unzipSync(await bytesOf(saves.blobs[0]!));
      expect(Object.keys(zip).sort()).toEqual(['roof-repair-cap-0.webm', 'roof-repair-cap-1.webm']);
      expect(new TextDecoder().decode(zip['roof-repair-cap-1.webm'])).toBe('webm bytes');
      expect(await screen.findByText(/downloaded 2 recordings as one archive/i)).toBeInTheDocument();
    } finally {
      saves.restore();
    }
  });

  it('deletes every selected recording behind one typed confirmation', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub(TWO);
    mount(api.fetchImpl);

    await user.click((await screen.findAllByRole('button', { name: /more for recording from/i }))[0]!);
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));
    const bar = await screen.findByRole('toolbar', { name: 'Recording actions' });
    await user.click(within(bar).getByRole('button', { name: 'Select all' }));
    await user.click(within(bar).getByRole('button', { name: 'Delete' }));

    const dialog = await screen.findByRole('dialog', { name: 'Delete 2 recordings?' });
    await user.type(within(dialog).getByLabelText('Type "delete" to confirm'), 'delete');
    await user.click(within(dialog).getByRole('button', { name: 'Delete them' }));

    await waitFor(() => {
      const deleted = api.calls.filter((c) => c.method === 'DELETE').map((c) => c.path).sort();
      expect(deleted).toEqual(['/v1/captures/cap-0', '/v1/captures/cap-1']);
    });
    await waitFor(() => {
      expect(screen.queryByRole('toolbar')).toBeNull();
    });
    expect(await screen.findByText('2 recordings deleted')).toBeInTheDocument();
  });
});

/** The rows of the recordings list — not the segments of a stage strip inside one. */
async function recordingRows(): Promise<HTMLElement[]> {
  const region = await screen.findByRole('region', { name: 'Recordings' });
  return within(region)
    .getAllByRole('listitem')
    .filter((item) => item.matches('.recording, .recordings__filing'));
}

/**
 * A recording on its way into this note. Send returns to the note's
 * Recordings tab, so the upload and then the pipeline show here, as the
 * library's filing row shows them, and the row turns into a recording when
 * it lands.
 */
describe('a recording still being made into this note', () => {
  it('is the first row, with the upload bar from the store', async () => {
    bucketStub();
    const uploading: CaptureModel = {
      ...INITIAL_CAPTURE,
      state: 'uploading',
      localId: 'cap-local',
      noteId: 'roof-repair',
      elapsedMs: 9_000,
      uploadProgress: 0.4,
    };
    mount(apiStub().fetchImpl, testQueryClient(), uploading);

    const rows = await recordingRows();
    expect(rows[0]).toHaveTextContent('Uploading… 40%');
    expect(rows[0]).toHaveTextContent('0:09');
    // The filed recording is still there, after it — and says nothing about
    // being filed, which every row on this tab is.
    expect(rows[1]).toHaveTextContent('0:12');
    expect(rows[1]).not.toHaveTextContent('Filed');
  });

  it('shows the filing stages under a row the pipeline is still working on, and asks for no audio', async () => {
    bucketStub();
    const api = apiStub({ ...NOTE, captures: [{ ...CAPTURE, id: 'cap-2', status: 'transcribing' }, CAPTURE] });
    mount(api.fetchImpl);

    const rows = await recordingRows();
    // Newest first; the moving one is on top, and says so twice: in words
    // and as the strip.
    expect(rows[0]).toHaveTextContent('Filing…');
    expect(within(rows[0]!).getByRole('list', { name: 'Filing progress' })).toBeInTheDocument();
    expect(within(rows[0]!).getByText(/transcribing in progress/i)).toBeInTheDocument();
    // The finished one is the row that opened on arrival.
    expect(within(rows[1]!).getByRole('button', { name: isSummary })).toHaveAttribute(
      'aria-expanded',
      'true',
    );

    // Opening the moving row explains itself rather than asking for artifacts
    // that do not exist yet.
    await userEvent.click(within(rows[0]!).getByRole('button', { name: /filing/i }));
    expect(await screen.findByText(/being filed/i)).toBeInTheDocument();
    expect(api.calls.some((call) => call.path.includes('/captures/cap-2/download'))).toBe(false);
  });

  it('opens itself when it lands', async () => {
    bucketStub();
    const moving: CaptureWire = { ...CAPTURE, id: 'cap-2', status: 'transcribing' };
    const api = apiStub({ ...NOTE, captures: [moving] });
    const queryClient = testQueryClient();
    mount(api.fetchImpl, queryClient);
    await screen.findByRole('list', { name: 'Filing progress' });
    expect(screen.queryByRole('region', { name: 'Recording' })).toBeNull();

    // The poll brings the landed row.
    act(() => {
      queryClient.setQueryData<NoteDetailWire>(queryKeys.note(NOTE.id), (current) =>
        current ? { ...current, captures: [{ ...moving, status: 'appended' }] } : current,
      );
    });

    expect(await screen.findByRole('region', { name: 'Recording' })).toBeInTheDocument();
    expect(screen.queryByRole('list', { name: 'Filing progress' })).toBeNull();
    expect(screen.getByRole('button', { name: isSummary })).toHaveAttribute('aria-expanded', 'true');
  });

  it('says nothing about a row that is filed, and names every other state', () => {
    expect(filedLabel(CAPTURE)).toBe('');
    expect(filedLabel({ ...CAPTURE, status: 'transcribing' })).toBe('Filing…');
    expect(filedLabel({ ...CAPTURE, status: 'needs_target' })).toBe('Needs a target');
    expect(filedLabel({ ...CAPTURE, status: 'failed' })).toBe('Failed');
  });

  it('knows a landing from a row that arrived already filed', () => {
    const moving: CaptureWire = { ...CAPTURE, status: 'cleaning' };
    expect(justLanded([moving], [{ ...moving, status: 'appended' }])?.id).toBe(CAPTURE.id);
    expect(justLanded([], [CAPTURE])).toBeUndefined();
    expect(justLanded([CAPTURE], [CAPTURE])).toBeUndefined();
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

  it('is decided by the source prefix alone', () => {
    const names = new Map([['dev_1', 'Watch']]);
    expect(sourceLabel({ source: 'device:dev_1' }, names)).toBe('From Watch');
    expect(sourceLabel({ source: 'device:dev_2' }, names)).toBe('From a device');
    expect(sourceLabel({ source: 'app' }, names)).toBeNull();
    expect(sourceLabel({}, names)).toBeNull();
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
