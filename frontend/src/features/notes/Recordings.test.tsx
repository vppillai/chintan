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

import { Recordings, justLanded } from './Recordings.tsx';

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
  overrides: Partial<Record<'delete' | 'move' | 'manifest', (init?: RequestInit) => Response>> = {},
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
      await user.click(await screen.findByRole('button', { name: 'Download audio' }));

      expect(await screen.findByText('Downloaded')).toBeInTheDocument();
      expect(bucket).toHaveBeenCalledWith(AUDIO_URL, expect.objectContaining({ cache: 'no-store' }));
      // Named after the object's real extension, not the query string.
      expect(saves.names).toEqual(['chintan-cap-1.webm']);
    } finally {
      saves.restore();
    }
  });
});

describe('a row’s More menu', () => {
  it('offers Move to…, Delete recording, Download audio and Select', async () => {
    const user = userEvent.setup();
    bucketStub();
    mount(apiStub().fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    const menu = screen.getByRole('menu');
    expect(within(menu).getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Move to…',
      'Delete recording',
      'Download audio',
      'Select',
    ]);
    // Escape closes it and puts focus back on the trigger.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.getByRole('button', { name: /more for recording from/i })).toHaveFocus();
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
    const rows = await screen.findAllByRole('button', { name: /filed/i });
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

  it('a press that moves is a scroll, not a selection', async () => {
    bucketStub();
    mount(apiStub(TWO).fetchImpl);
    const [row] = await screen.findAllByRole('button', { name: /filed/i });

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
    // The filed recording is still there, after it.
    expect(rows[1]).toHaveTextContent('Filed');
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
    expect(within(rows[1]!).getByRole('button', { name: /filed/i })).toHaveAttribute(
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
    expect(screen.getByRole('button', { name: /filed/i })).toHaveAttribute('aria-expanded', 'true');
  });

  it('knows a landing from a row that arrived already filed', () => {
    const moving: CaptureWire = { ...CAPTURE, status: 'cleaning' };
    expect(justLanded([moving], [{ ...moving, status: 'appended' }])?.id).toBe(CAPTURE.id);
    expect(justLanded([], [CAPTURE])).toBeUndefined();
    expect(justLanded([CAPTURE], [CAPTURE])).toBeUndefined();
  });
});
