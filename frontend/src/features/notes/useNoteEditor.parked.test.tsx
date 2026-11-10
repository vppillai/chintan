import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { describe, expect, it, vi } from 'vitest';

import { queryKeys, useNote } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { enqueueReplacing } from '@/offline/queue.ts';
import { queuedEditFor, queuedEditId } from '@/offline/queuedEdits.ts';
import { useOfflineQueue } from '@/offline/useOfflineQueue.ts';
import { onAFakeClock } from '@/test/clock.ts';
import { TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

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
function session(stored: NoteDetailWire, refuse: { patch?: number; get?: number } = {}) {
  const sent: Record<string, unknown>[] = [];
  const failing = { ...refuse };
  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), {
      status,
      headers: { 'content-type': status >= 400 ? 'application/problem+json' : 'application/json' },
    });
  const fetchImpl = vi.fn<typeof fetch>(async (_input, init) => {
    if ((init?.method ?? 'GET') !== 'PATCH') {
      return failing.get ? json({ type: 'about:blank', title: 'Refused', status: failing.get }, failing.get) : json(stored);
    }
    const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
    sent.push(body);
    if (failing.patch) return json({ type: 'about:blank', title: 'Refused', status: failing.patch }, failing.patch);
    if (body['version'] !== stored.version) return json({ type: 'about:blank', title: 'Conflict', status: 409 }, 409);
    if (typeof body['body'] === 'string') stored.body = body['body'];
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
    await waitFor(async () => {
      expect(await queuedEditFor(NOTE.id)).not.toBeNull();
    });
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
  return { open, typeAndLeave, flush, refetched, sent, failing, stored, queryClient };
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
