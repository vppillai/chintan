import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { unzipSync } from 'fflate';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { queryKeys } from '@/api/queries.ts';
import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';
import { INITIAL_CAPTURE, type CaptureModel } from '@/features/capture/machine.ts';
import { LONG_PRESS_MS } from '@/hooks/useLongPress.ts';
import { bytesOf } from '@/test/blob.ts';
import { testQueryClient } from '@/test/providers.tsx';
import {
  CAPTURE,
  NOTE,
  OLDER,
  apiStub,
  bucketStub,
  captureSaves,
  isSummary,
  json,
  mount,
  recordingRows,
} from '@/test/recordings.tsx';

/*
 * The list: deleting and moving recordings, selecting several, and the row
 * for a recording still on its way in. A single row's own behaviour — its
 * player, menu, swipe tray, "Heard as" chip and Transcribe again — is in
 * `recordings/RecordingRow.test.tsx`; the sentences are in
 * `recordings/labels.test.ts`. The stub server and the mount are
 * `@/test/recordings.tsx`.
 */

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('deleting a recording', () => {
  it('confirms plainly, deletes the capture, drops the row and refetches the note', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub();
    const { queryClient } = mount(api.fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Delete recording' }));

    const dialog = await screen.findByRole('dialog');
    // It says what else goes: the paragraph the recording dictated.
    expect(dialog).toHaveTextContent(/paragraph it dictated/i);
    expect(within(dialog).queryByRole('textbox')).toBeNull();
    await user.click(within(dialog).getByRole('button', { name: 'Delete it' }));

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
    await user.click(await screen.findByRole('button', { name: 'Delete it' }));

    expect(await screen.findByText('Wait until it has finished filing.')).toBeInTheDocument();
    // The row stays.
    expect(screen.getByRole('button', { name: /more for recording from/i })).toBeInTheDocument();
  });
});

describe('moving a recording to another note', () => {
  it('lists the other active notes, recent first, under a New note… row', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub();
    mount(api.fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Move to…' }));

    const sheet = await screen.findByRole('dialog', { name: /move this recording to/i });
    const options = await within(sheet).findAllByRole('button', { name: /reading list|old fence/i });
    // The note being moved out of is not offered; a note not yet made is.
    expect(within(sheet).queryByRole('button', { name: /roof repair/i })).toBeNull();
    expect(within(sheet).getByRole('button', { name: 'New note…' })).toBeInTheDocument();
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

  it('into a note named here: the move carries the title, and the note it made is offered', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub();
    const queryClient = testQueryClient();
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries');
    mount(api.fetchImpl, queryClient);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Move to…' }));
    const sheet = await screen.findByRole('dialog');
    await user.click(within(sheet).getByRole('button', { name: 'New note…' }));
    await user.type(within(sheet).getByLabelText('Name the new note'), 'Trip notes{Enter}');

    // One request: the server makes the note and moves the paragraph in it.
    await waitFor(() => {
      expect(api.calls).toContainEqual(
        expect.objectContaining({
          method: 'POST',
          path: '/v1/captures/cap-1/move',
          body: { new_note_title: 'Trip notes' },
        }),
      );
    });
    expect(
      api.calls.some((call) => call.method === 'POST' && call.path.startsWith('/v1/notes')),
    ).toBe(false);
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    expect(screen.queryByRole('button', { name: /more for recording from/i })).toBeNull();
    // The same words as for a note that existed, with the name just typed,
    // and the offer to open it — at the id only the answer knew.
    expect(await screen.findByText(/recording moved to “trip notes”/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Open Trip notes' })).toBeInTheDocument();

    const invalidated = invalidate.mock.calls.map(([filters]) => JSON.stringify(filters?.queryKey));
    expect(invalidated).toContain(JSON.stringify(queryKeys.note('roof-repair')));
    expect(invalidated).toContain(JSON.stringify(queryKeys.note('made-from-title')));
    expect(invalidated).toContain(JSON.stringify(['notes']));
  });

  it('several recordings into one new note: the first move makes it, the rest follow it by id', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub({ ...NOTE, captures: [CAPTURE, OLDER] });
    mount(api.fetchImpl);

    await user.click((await screen.findAllByRole('button', { name: /more for recording from/i }))[0]!);
    await user.click(screen.getByRole('menuitem', { name: 'Select' }));
    const bar = await screen.findByRole('toolbar', { name: 'Recording actions' });
    await user.click(within(bar).getByRole('button', { name: 'Select all' }));
    await user.click(within(bar).getByRole('button', { name: 'Move' }));
    const sheet = await screen.findByRole('dialog', { name: /move 2 recordings to/i });
    await user.click(within(sheet).getByRole('button', { name: 'New note…' }));
    await user.type(within(sheet).getByLabelText('Name the new note'), 'Trip notes{Enter}');

    // Run together, two titles would have made two notes named alike.
    await waitFor(() => {
      expect(api.calls.filter((call) => call.path.endsWith('/move')).map((call) => call.body)).toEqual([
        { new_note_title: 'Trip notes' },
        { note_id: 'made-from-title' },
      ]);
    });
    expect(await screen.findByText(/2 recordings moved to “trip notes”/i)).toBeInTheDocument();
  });

  it('a refused title moves nothing and the sheet says why', async () => {
    const user = userEvent.setup();
    bucketStub();
    const api = apiStub(NOTE, {
      move: () =>
        json(
          { type: 'about:blank', title: 'Bad Request', status: 400, detail: 'title is too long' },
          400,
        ),
    });
    mount(api.fetchImpl);

    await user.click(await screen.findByRole('button', { name: /more for recording from/i }));
    await user.click(screen.getByRole('menuitem', { name: 'Move to…' }));
    const sheet = await screen.findByRole('dialog');
    await user.click(within(sheet).getByRole('button', { name: 'New note…' }));
    await user.type(within(sheet).getByLabelText('Name the new note'), 'Trip notes{Enter}');

    expect(await within(sheet).findByRole('alert')).toHaveTextContent('title is too long');
    expect(screen.getByRole('button', { name: /more for recording from/i })).toBeInTheDocument();
    expect(screen.queryByText(/moved to/i)).toBeNull();
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

  it('deletes every selected recording behind one plain confirmation', async () => {
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
    expect(within(dialog).queryByRole('textbox')).toBeNull();
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
});
