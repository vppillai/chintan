import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { queryKeys, useColorNote, useNote, usePinNote } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { enqueueReplacing } from '@/offline/queue.ts';
import { queuedEditFor, queuedEditId } from '@/offline/queuedEdits.ts';
import { useOfflineQueue } from '@/offline/useOfflineQueue.ts';
import { onAFakeClock } from '@/test/clock.ts';
import { TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import { AUTOSAVE_DELAY_MS } from './autosave.ts';
import { useNoteEditor } from './useNoteEditor.ts';

/*
 * The parked edit across visits, against a server with the real version rule
 * — a PATCH is applied only when its version equals the note's
 * (`service/notes.go` `UpdateNote`) — and with the shell's flusher mounted
 * for the whole session, as `AppShell` mounts it. These are the review's
 * probes of the first parking (PR 281), kept: each failed against it.
 */

const NOTE: NoteDetailWire = {
  id: 'roof-repair',
  title: 'Roof repair',
  body: 'Start.',
  aliases: [],
  tags: [],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
  captures: [],
};

const capture = (id: string) =>
  ({ id, status: 'appended', created_at: '2026-08-06T09:20:00.000Z', version: 1, note_id: NOTE.id }) as NonNullable<
    NoteDetailWire['captures']
  >[number];

const settle = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

/** The device opened the note at v3; the server holds `stored`. */
function session(
  stored: NoteDetailWire,
  refuse: { patch?: number; get?: number; dark?: boolean; slowGetMs?: number } = {},
) {
  const sent: Record<string, unknown>[] = [];
  const failing = { ...refuse };
  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
      status,
      headers: { 'content-type': status >= 400 ? 'application/problem+json' : 'application/json' },
    });
  const fetchImpl = vi.fn<typeof fetch>(async (_input, init) => {
    if (failing.dark) throw new TypeError('Failed to fetch');
    if ((init?.method ?? 'GET') !== 'PATCH') {
      if (failing.slowGetMs) await settle(failing.slowGetMs);
      return failing.get ? json({ type: 'about:blank', title: 'Refused', status: failing.get }, failing.get) : json(stored);
    }
    const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
    sent.push(body);
    if (failing.patch) return json({ type: 'about:blank', title: 'Refused', status: failing.patch }, failing.patch);
    if (body['version'] !== stored.version) return json({ type: 'about:blank', title: 'Conflict', status: 409 }, 409);
    if (typeof body['body'] === 'string') stored.body = body['body'];
    if (typeof body['pinned'] === 'boolean') stored.pinned = body['pinned'];
    if (typeof body['color'] === 'string') stored.color = body['color'] as NonNullable<NoteDetailWire['color']>;
    stored.version += 1;
    const { body: _body, captures: _captures, ...row } = stored;
    return json(row);
  });
  const queryClient = testQueryClient();
  // The provider's own freshness: a reopened note is served from the cache.
  queryClient.setDefaultOptions({ queries: { retry: false, staleTime: 30_000 } });
  queryClient.setQueryData(queryKeys.note(NOTE.id), NOTE);
  const wrapper = ({ children }: { children: ReactNode }) => (
    <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
      {children}
    </TestProviders>
  );
  renderHook(() => useOfflineQueue(), { wrapper });
  const open = () =>
    renderHook(
      () => {
        const { data } = useNote(NOTE.id);
        return useNoteEditor(data);
      },
      { wrapper },
    );
  /** Type, then Back inside the debounce; resolves once the edit is parked. */
  const typeAndLeave = async (body: string) => {
    const view = open();
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start.');
    });
    act(() => {
      view.result.current.edit({ body });
    });
    view.unmount();
    // Long enough for the client's own retries when nothing answers.
    await waitFor(
      async () => {
        expect(await queuedEditFor(NOTE.id)).not.toBeNull();
      },
      { timeout: 6_000 },
    );
  };
  /** A pass of the flusher, and time for anything it started. */
  const flush = async () => {
    await act(() => queryClient.refetchQueries({ queryKey: queryKeys.offlineQueue() }));
    await settle(200);
  };
  /** The note's refetch lands: the next editor is loaded from the server's copy. */
  const refetched = () => {
    queryClient.setQueryData(queryKeys.note(NOTE.id), structuredClone(stored));
  };
  return { open, typeAndLeave, flush, refetched, sent, failing, stored, queryClient, wrapper };
}

describe('a parked conflict is never sent without the user’s choice', () => {
  it('leaving the read-back prompt unanswered writes nothing, and asks again', async () => {
    const server = session({ ...NOTE, version: 4, body: 'Written on the other device.' });
    await server.typeAndLeave('Start. Mine.');
    const parked = await queuedEditFor(NOTE.id);

    server.refetched();
    const second = server.open();
    await waitFor(() => {
      expect(second.result.current.model.state).toBe('conflict');
    });
    expect(second.result.current.model.draft.body).toBe('Start. Mine.');

    // Neither button: Back. The editor is by now on the server's version;
    // re-parked from it, my text passed the version check on the next flush.
    second.unmount();
    await server.flush();

    expect(server.stored).toMatchObject({ version: 4, body: 'Written on the other device.' });
    // Still retired, on the version the edit was made over: never the editor's.
    expect(await queuedEditFor(NOTE.id)).toMatchObject({ dead: true, body: parked?.body, refused: false });
    const third = server.open();
    await waitFor(() => {
      expect(third.result.current.model.state).toBe('conflict');
    });
    expect(third.result.current.model.draft.body).toBe('Start. Mine.');
  });

  it('keeps what was typed under the read-back prompt, in the entry and on its version', async () => {
    const filed = { version: 4, body: 'Start.\n\nDictated paragraph.' };
    const server = session({ ...NOTE, ...filed, captures: [capture('cap-1')] });
    await server.typeAndLeave('Start. Mine.');
    server.refetched();
    const second = server.open();
    await waitFor(() => {
      expect(second.result.current.model.state).toBe('conflict');
    });
    // The field stays editable under the prompt.
    act(() => {
      second.result.current.edit({ body: 'Start. Mine. And a second sentence.' });
    });
    second.unmount();
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).toMatchObject({
        dead: true,
        body: { version: 3, body: 'Start. Mine. And a second sentence.' },
      });
    });
    await server.flush();
    expect(server.stored, 'nothing written without a choice').toMatchObject(filed);

    const third = server.open();
    await waitFor(() => {
      expect(third.result.current.model.state).toBe('conflict');
    });
    expect(third.result.current.model.draft.body).toBe('Start. Mine. And a second sentence.');
    // Still judged against what the first edit was made over, not against
    // the server's copy the second editor had been loaded from.
    expect(third.result.current.model.theirs).toMatchObject({ addition: 'Dictated paragraph.', recording: true });
  });

  it('a conflict met on screen and left is parked retired, on the version the edit was made over', async () => {
    const server = session({ ...NOTE, version: 4, body: 'Written on the other device.' });
    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start.');
    });
    act(() => {
      view.result.current.edit({ body: 'Start. Mine.' });
    });
    await act(async () => {
      await view.result.current.saveNow();
    });
    expect(view.result.current.model.state).toBe('conflict');
    expect(await queuedEditFor(NOTE.id)).toBeNull();

    view.unmount();
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).toMatchObject({
        dead: true,
        body: { version: 3, body: 'Start. Mine.' },
        refused: false,
      });
    });
    await server.flush();
    expect(server.sent).toHaveLength(1);
    expect(server.stored.body).toBe('Written on the other device.');
  });
});

describe('a parked text is never paired with a version newer than the copy it was written over', () => {
  it('an unanswered conflict, reopened with no connection, is not written over the newer copy by one more keystroke', async () => {
    const server = session({ ...NOTE });
    const first = server.open();
    await waitFor(() => {
      expect(first.result.current.model.draft.body).toBe('Start.');
    });
    // A recording is filed while the note is open; the save meets it.
    Object.assign(server.stored, {
      version: 4,
      body: 'Start.\n\nFirst dictated paragraph.',
      captures: [capture('cap-1')],
    });
    act(() => {
      first.result.current.edit({ body: 'Start. Mine.' });
    });
    await act(async () => {
      await first.result.current.saveNow();
    });
    expect(first.result.current.model.state).toBe('conflict');
    // The screen's own poll for the recording reads the note again, under
    // the prompt: the `note` the editor is handed is now the server's v4.
    await act(() => server.queryClient.refetchQueries({ queryKey: queryKeys.note(NOTE.id) }));
    await act(() => settle(100));
    first.unmount();
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).toMatchObject({ dead: true, body: { version: 3 } });
    });
    // The device's copy is my text at the version it was typed over. Cached
    // at v4 it reopened as a clean v4 note, and the next save passed.
    expect(server.queryClient.getQueryData(queryKeys.note(NOTE.id))).toMatchObject({
      version: 3,
      body: 'Start. Mine.',
    });

    // Reopened in a tunnel: no read-back, no prompt. One more word.
    server.failing.dark = true;
    const second = server.open();
    await waitFor(() => {
      expect(second.result.current.model.draft.body).toBe('Start. Mine.');
    });
    act(() => {
      second.result.current.edit({ body: 'Start. Mine. More.' });
    });
    await settle(AUTOSAVE_DELAY_MS + 3_500);
    // Still retired, still on its own version, with the new word.
    expect(await queuedEditFor(NOTE.id)).toMatchObject({
      dead: true,
      body: { version: 3, body: 'Start. Mine. More.' },
    });

    delete server.failing.dark;
    const before = server.sent.length;
    await server.flush();
    expect(server.sent, 'a retired entry is not sent').toHaveLength(before);
    expect(server.stored.body).toBe('Start.\n\nFirst dictated paragraph.');
  }, 15_000);
});

describe('an edit the device holds is what the editor shows, whatever copy it was loaded from', () => {
  it('typing with no connection extends a retired edit the device’s copy no longer shows', async () => {
    const server = session({ ...NOTE, version: 4, body: 'Written on the other device.' });
    await server.typeAndLeave('Start. Mine.');
    // Home is visited online: the list row, then the body prefetch, replace
    // the device's copy with the server's. Then a tunnel.
    server.refetched();
    server.failing.dark = true;

    const view = server.open();
    // The held text, not the server's: typed over the server's, the entry
    // became "Written on the other device. Typed offline." and "Mine." was
    // gone without having been shown again.
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start. Mine.');
    });
    act(() => {
      view.result.current.edit({ body: 'Start. Mine. Typed offline.' });
    });
    await act(async () => {
      await view.result.current.saveNow();
    });
    // Still retired, a conflict, on the version the first edit was made
    // over; nothing is sent when the connection returns.
    await waitFor(
      async () => {
        expect(await queuedEditFor(NOTE.id)).toMatchObject({
          dead: true,
          refused: false,
          body: { version: 3, body: 'Start. Mine. Typed offline.' },
        });
      },
      { timeout: 6_000 },
    );
    delete server.failing.dark;
    const before = server.sent.length;
    await server.flush();
    expect(server.sent).toHaveLength(before);
    expect(server.stored).toMatchObject({ version: 4, body: 'Written on the other device.' });
  }, 15_000);

  it('typing with no connection extends a live edit the flush has not sent yet', async () => {
    const server = session({ ...NOTE }, { dark: true });
    await server.typeAndLeave('Start. Mine.');
    expect(await queuedEditFor(NOTE.id)).toMatchObject({ pending: true });
    // Another device writes; this one reads the note again before its flush.
    Object.assign(server.stored, { version: 4, body: 'Written on the other device.' });
    server.refetched();

    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start. Mine.');
    });
    act(() => {
      view.result.current.edit({ body: 'Start. Mine. More.' });
    });
    await act(async () => {
      await view.result.current.saveNow();
    });
    // On the version the first edit was made over: sent, it is refused and
    // comes back as the prompt, with both sentences.
    await waitFor(
      async () => {
        expect(await queuedEditFor(NOTE.id)).toMatchObject({ body: { version: 3, body: 'Start. Mine. More.' } });
      },
      { timeout: 6_000 },
    );
    delete server.failing.dark;
    // A pass begun in the tunnel may still be giving up; the next one sends.
    await waitFor(
      async () => {
        await server.flush();
        expect(await queuedEditFor(NOTE.id)).toMatchObject({ dead: true });
      },
      { timeout: 10_000, interval: 500 },
    );
    expect(server.stored.body).toBe('Written on the other device.');
  }, 25_000);

  it('keeps what is typed while the read-back is fetching the server’s copy', async () => {
    const server = session({ ...NOTE, version: 4, body: 'Written on the other device.' });
    await server.typeAndLeave('Start. Mine.');
    // A slow connection: the reopened note shows my text, editable, for as
    // long as one GET takes. The prompt then wrote the held body over it.
    server.failing.slowGetMs = 600;
    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start. Mine.');
    });
    act(() => {
      view.result.current.edit({ body: 'Start. Mine. Typed at once.' });
    });
    await waitFor(
      () => {
        expect(view.result.current.model.state).toBe('conflict');
      },
      { timeout: 3_000 },
    );
    expect(view.result.current.model.draft.body).toBe('Start. Mine. Typed at once.');
    expect(server.stored.body).toBe('Written on the other device.');
  });

  it('a save from that editor, refused on screen, is judged against what the held edit was made over', async () => {
    const server = session({
      ...NOTE,
      version: 4,
      body: 'Start.\n\nDictated paragraph.',
      captures: [capture('cap-1')],
    });
    await server.typeAndLeave('Start. Mine.');
    server.failing.dark = true;
    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start. Mine.');
    });
    act(() => {
      view.result.current.edit({ body: 'Start. Mine. More.' });
    });
    delete server.failing.dark;
    await act(async () => {
      await view.result.current.saveNow();
    });
    // Against the editor's own last text — the held edit — nothing reads as
    // an addition, and the prompt would not offer to keep the paragraph.
    await waitFor(() => {
      expect(view.result.current.model.theirs).toMatchObject({ addition: 'Dictated paragraph.', recording: true });
    });
    expect(view.result.current.model.draft.body).toBe('Start. Mine. More.');
  });

  it('"Use the newer version" leaves the device’s copy as the one taken, not the dropped draft', async () => {
    const server = session({ ...NOTE, version: 4, body: 'Written on the other device.' });
    await server.typeAndLeave('Start. Mine.');
    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.state).toBe('conflict');
    });
    act(() => {
      view.result.current.takeTheirs();
    });
    await waitFor(() => {
      expect(server.queryClient.getQueryData(queryKeys.note(NOTE.id))).toMatchObject({
        version: 4,
        body: 'Written on the other device.',
      });
    });
    expect(view.result.current.model.draft.body).toBe('Written on the other device.');
    expect(await queuedEditFor(NOTE.id)).toBeNull();
  });
});

describe('a pin from Home on a row newer than the note the device holds', () => {
  it('does not put the row’s version on the old body: the note opens on the server’s copy and a keystroke keeps the recording', async () => {
    const server = session({ ...NOTE });
    // The note was open earlier: its detail is cached at v3. A recording is
    // filed since (v4), and Home's list has been read again.
    Object.assign(server.stored, {
      version: 4,
      body: 'Start.\n\nFirst dictated paragraph.',
      captures: [capture('cap-1')],
    });
    const pin = renderHook(() => usePinNote(), { wrapper: server.wrapper });
    await act(async () => {
      await pin.result.current.mutateAsync({ note: { id: NOTE.id, version: 4 }, pinned: true });
    });
    // The old body stays at its own version; stamped v5, the editor loaded
    // it, ignored the refetch, and one word overwrote the paragraph.
    expect(server.queryClient.getQueryData<NoteDetailWire>(queryKeys.note(NOTE.id))?.version).not.toBe(5);

    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start.\n\nFirst dictated paragraph.');
    });
    act(() => {
      view.result.current.edit({ body: `${view.result.current.model.draft.body} More.` });
    });
    await act(async () => {
      await view.result.current.saveNow();
    });
    expect(server.stored.body).toBe('Start.\n\nFirst dictated paragraph. More.');
  });

  it('a colour from Home does not either', async () => {
    const server = session({ ...NOTE });
    Object.assign(server.stored, { version: 4, body: 'Start.\n\nFirst dictated paragraph.' });
    const color = renderHook(() => useColorNote(NOTE.id), { wrapper: server.wrapper });
    await act(async () => {
      await color.result.current.mutateAsync({ version: 4, color: 'sky' });
    });
    expect(server.stored).toMatchObject({ version: 5, color: 'sky' });
    const cached = server.queryClient.getQueryData<NoteDetailWire>(queryKeys.note(NOTE.id));
    expect(cached?.body === 'Start.' && cached.version === 5, 'the old body at the new version').toBe(false);
  });

  it('still carries the new version onto a copy that was current, so the next pin is not refused', async () => {
    const server = session({ ...NOTE });
    const pin = renderHook(() => usePinNote(), { wrapper: server.wrapper });
    await act(async () => {
      await pin.result.current.mutateAsync({ note: { id: NOTE.id, version: 3 }, pinned: true });
    });
    expect(server.queryClient.getQueryData(queryKeys.note(NOTE.id))).toMatchObject({
      version: 4,
      pinned: true,
      body: 'Start.',
    });
  });
});

describe('a recording filed while typing, then Back', () => {
  it('comes back as the prompt the screen would have shown: the recording named, its paragraph on offer', async () => {
    const server = session({
      ...NOTE,
      version: 4,
      body: 'Start.\n\nDictated paragraph.',
      captures: [
        { id: 'cap-new', status: 'appended', created_at: '2026-08-06T09:20:00.000Z', version: 1, note_id: NOTE.id },
      ],
    });
    await server.typeAndLeave('Start. Mine.');

    server.refetched();
    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.state).toBe('conflict');
    });
    // Computed on reopening, both were lost: the editor is loaded from the
    // copy that has the paragraph, against which nothing reads as added.
    expect(view.result.current.model.theirs).toMatchObject({ addition: 'Dictated paragraph.', recording: true });

    act(() => {
      view.result.current.keepBoth();
    });
    await act(async () => {
      await view.result.current.saveNow();
    });
    expect(server.stored.body).toBe('Start. Mine.\n\nDictated paragraph.');
    expect(await queuedEditFor(NOTE.id)).toBeNull();
  });
});

describe('the note moved again before the parked edit was read back', () => {
  it('keep-both adds everything filed since the edit, not only what the refused save saw', async () => {
    const one = 'Start.\n\nFirst dictated paragraph.';
    const server = session({ ...NOTE, version: 4, body: one, captures: [capture('cap-1')] });
    await server.typeAndLeave('Start. Mine.');
    // A second recording is filed before the note is reopened. The addition
    // stored at the refusal, sent on this newer version, wrote over it.
    Object.assign(server.stored, {
      version: 5,
      body: `${one}\n\nSecond dictated paragraph.`,
      captures: [capture('cap-1'), capture('cap-2')],
    });
    server.refetched();

    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.state).toBe('conflict');
    });
    expect(view.result.current.model.theirs).toMatchObject({
      addition: 'First dictated paragraph.\n\nSecond dictated paragraph.',
      recording: true,
    });
    await act(async () => {
      view.result.current.keepBoth();
      await view.result.current.saveNow();
    });
    expect(server.stored.body).toBe('Start. Mine.\n\nFirst dictated paragraph.\n\nSecond dictated paragraph.');
  });

  it('a save that could not be answered, parked live and then refused by the flush, reads back with the recording named', async () => {
    const server = session(
      { ...NOTE, version: 4, body: 'Start.\n\nDictated paragraph.', captures: [capture('cap-1')] },
      { patch: 503 },
    );
    await onAFakeClock(async () => {
      const first = server.open();
      await waitFor(() => {
        expect(first.result.current.model.draft.body).toBe('Start.');
      });
      act(() => {
        first.result.current.edit({ body: 'Start. Mine.' });
      });
      first.unmount();
      // Through the client's retries, to the queue: live, no prompt known.
      await act(() => vi.advanceTimersByTimeAsync(40_000));
      await waitFor(async () => {
        expect(await queuedEditFor(NOTE.id)).toMatchObject({ pending: true });
      });
      delete server.failing.patch;
      await act(() => server.queryClient.refetchQueries({ queryKey: queryKeys.offlineQueue() }));
      await act(() => vi.advanceTimersByTimeAsync(2_000));
      await waitFor(async () => {
        expect(await queuedEditFor(NOTE.id)).toMatchObject({ dead: true });
      });

      server.refetched();
      const view = server.open();
      await waitFor(() => {
        expect(view.result.current.model.state).toBe('conflict');
      });
      expect(view.result.current.model.theirs).toMatchObject({ addition: 'Dictated paragraph.', recording: true });
    });
  });
});

describe('an edit the server refused on its merits, then Back', () => {
  it('left unanswered, is still an edit the server refused: the same prompt, not a conflict', async () => {
    const server = session({ ...NOTE }, { patch: 413 });
    await server.typeAndLeave('Start. Far too long.');
    const second = server.open();
    await waitFor(() => {
      expect(second.result.current.model.theirs?.refused).toBe(true);
    });
    second.unmount();
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).toMatchObject({ dead: true, refused: true });
    });
    // Re-parked as a conflict, the unmoved note sent it again unasked.
    const before = server.sent.length;
    const third = server.open();
    await waitFor(() => {
      expect(third.result.current.model.theirs?.refused).toBe(true);
    });
    expect(server.sent).toHaveLength(before);
  });

  it('typed over with no connection, is still a refusal', async () => {
    const server = session({ ...NOTE }, { patch: 413 });
    await server.typeAndLeave('Start. Far too long.');
    server.failing.dark = true;
    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start. Far too long.');
    });
    act(() => {
      view.result.current.edit({ body: 'Start. Shorter.' });
    });
    await act(async () => {
      await view.result.current.saveNow();
    });
    // Parked over as a conflict, the unmoved note sent it again unasked.
    await waitFor(
      async () => {
        expect(await queuedEditFor(NOTE.id)).toMatchObject({
          dead: true,
          refused: true,
          body: { body: 'Start. Shorter.' },
        });
      },
      { timeout: 6_000 },
    );
  }, 15_000);

  it('is removed when the note it was made to is gone', async () => {
    const server = session({ ...NOTE }, { patch: 413 });
    await server.typeAndLeave('Start. Far too long.');
    // Deleted elsewhere; this device still has its copy to open.
    server.failing.get = 404;
    server.open();
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).toBeNull();
    });
  });

  it('a later failure of this editor’s own keeps its newer draft when the note is refetched', async () => {
    const server = session({ ...NOTE }, { patch: 413 });
    await server.typeAndLeave('Start. Far too long.');
    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.theirs?.refused).toBe(true);
    });
    // Send it again, refused again; then more typing, refused too.
    await act(async () => {
      view.result.current.keepMine();
      await view.result.current.saveNow();
    });
    act(() => {
      view.result.current.edit({ body: 'Start. Shorter.' });
    });
    await act(async () => {
      await view.result.current.saveNow();
    });
    expect(view.result.current.model.state).toBe('error');

    // The note is refetched — a new object, the entry still retired. The
    // read-back must not write the held text over the live draft.
    act(() => {
      server.refetched();
    });
    await settle(300);
    expect(view.result.current.model).toMatchObject({ state: 'error', draft: { body: 'Start. Shorter.' } });
  });

  it('is asked about on the next visit, not sent again, and can be dropped', async () => {
    const server = session({ ...NOTE }, { patch: 413 });
    await server.typeAndLeave('Start. Far too long.');
    await server.flush();
    expect(server.sent, 'the flush leaves a retired entry alone').toHaveLength(1);

    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.theirs?.refused).toBe(true);
    });
    expect(view.result.current.model.draft.body).toBe('Start. Far too long.');
    expect(server.sent).toHaveLength(1);

    act(() => {
      view.result.current.takeTheirs();
    });
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).toBeNull();
    });
    expect(view.result.current.model.draft.body).toBe('Start.');
  });
});

describe('a save that fails behind a save still on the wire, with the screen gone', () => {
  it('does not leave the first save’s text parked once the second has landed', async () => {
    const server = session({ ...NOTE });
    const view = server.open();
    await waitFor(() => {
      expect(view.result.current.model.draft.body).toBe('Start.');
    });
    // The queue has been read, and holds nothing: a save that lands has no
    // other reason to look in it than the key this editor parked under.
    await waitFor(() => {
      expect(server.queryClient.getQueryState(queryKeys.queuedEdit(NOTE.id))?.status).toBe('success');
    });
    // A blur's save, refused; one more keystroke; Back before it answers.
    server.failing.patch = 413;
    act(() => {
      view.result.current.edit({ body: 'Start. One.' });
    });
    const blur = view.result.current.saveNow();
    act(() => {
      view.result.current.edit({ body: 'Start. One. Two.' });
    });
    delete server.failing.patch;
    view.unmount();
    await blur;

    // The unmount's save ran from the first one's `finally`, closing over a
    // render from before the park: it has to forget the entry all the same.
    await waitFor(() => {
      expect(server.stored.body).toBe('Start. One. Two.');
    });
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).toBeNull();
    });
  });
});

describe('"Try again" under a retired entry whose read-back could not reach the server', () => {
  it('reads it back again, and the edit is sent', async () => {
    // Queued offline, given up on by the flush during an outage.
    await enqueueReplacing({
      id: queuedEditId(NOTE.id),
      kind: 'updateNote',
      payload: { noteId: NOTE.id, body: { version: 3, body: 'Start. One.' } },
      retired: true,
    });
    const server = session({ ...NOTE }, { get: 503 });
    await onAFakeClock(async () => {
      const view = server.open();
      await waitFor(() => {
        expect(view.result.current.model.state).toBe('error');
      });
      // The read-back's own fetch, through the client's retries, gives up.
      await act(() => vi.advanceTimersByTimeAsync(60_000));
      expect(server.sent).toHaveLength(0);

      // The server is back. Nothing else re-runs the read-back: the note in
      // the cache is the same object, and the entry is as dead as it was.
      delete server.failing.get;
      await act(async () => {
        await view.result.current.saveNow();
      });
      await act(() => vi.advanceTimersByTimeAsync(5_000));
    });

    await waitFor(() => {
      expect(server.stored.body).toBe('Start. One.');
    });
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).toBeNull();
    });
  });
});
