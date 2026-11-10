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
  return { open, typeAndLeave, flush, refetched, sent, failing, stored };
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
    expect(await queuedEditFor(NOTE.id)).toEqual(parked);
    const third = server.open();
    await waitFor(() => {
      expect(third.result.current.model.state).toBe('conflict');
    });
    expect(third.result.current.model.draft.body).toBe('Start. Mine.');
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
        prompt: { refused: false },
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

describe('an edit the server refused on its merits, then Back', () => {
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
