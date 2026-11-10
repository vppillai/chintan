/*
 * A model check of the note editor and its queue: random sequences of user
 * and server actions against a server with the real version rule (a PATCH is
 * applied only when its version equals the note's), with the shell's flusher
 * mounted, the real hook and the real IndexedDB queue.
 *
 * Two things must hold after every step:
 *   S  the server's body never loses text another writer put there, unless
 *      the user tapped a prompt button that says so;
 *   U  text the user typed is on the server, in the open editor or in the
 *      note's queue entry, unless the user tapped it away.
 *
 * The seeds are fixed, each kept for a defect it found (review of PR 281):
 * 16 and 80 lost a recording with no tap while a retired entry could go
 * live, which more than one rule now stops; 97 and 185 lose typed text when
 * the editor does not show a held edit; 135, run with pins, loses another
 * device's text when a pin from Home puts the row's version on a body the
 * device never fetched. 7 and 42 run with faults — a server that at times
 * refuses a PATCH on its merits, answers 503, or fails reads while writes
 * get through, and a tag saved at once — so refused entries are inside the
 * model; no random sequence tried has yet found a defect there (the one
 * known, a tag's save forgetting a retired edit unsent, has its own test in
 * `useNoteEditor.parked.test.tsx`). `MODEL_SEEDS`, `MODEL_FROM`
 * and `MODEL_MODE` (`pins`, `faults`) run a wider sweep by hand; it is about a second a seed.
 */
import { act, renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { queryKeys, useNote, usePinNote } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { clearAllLocalData } from '@/offline/db.ts';
import { queuedEditFor } from '@/offline/queuedEdits.ts';
import { useOfflineQueue } from '@/offline/useOfflineQueue.ts';
import { TestProviders, testApiContext, testQueryClient } from '@/test/providers.tsx';

import { useNoteEditor } from './useNoteEditor.ts';

const STEPS = 20;
type Mode = 'plain' | 'pins' | 'faults';
const FIXED: [number, Mode][] = [
  [16, 'plain'],
  [80, 'plain'],
  [97, 'plain'],
  [185, 'plain'],
  [135, 'pins'],
  [7, 'faults'],
  [42, 'faults'],
];
const sweep = Number(process.env['MODEL_SEEDS'] ?? 0);
const RUNS: [number, Mode][] = sweep
  ? Array.from({ length: sweep }, (_unused, index) => [
      Number(process.env['MODEL_FROM'] ?? 1) + index,
      (process.env['MODEL_MODE'] ?? 'plain') as Mode,
    ])
  : FIXED;

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

function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

afterEach(async () => {
  vi.useRealTimers();
  await clearAllLocalData().catch(() => undefined);
});

async function run(seed: number, mode: Mode): Promise<string | null> {
  const pins = mode === 'pins';
  const faults = mode === 'faults';
  const random = mulberry32(seed);
  const pick = <T,>(items: T[]): T => items[Math.floor(random() * items.length)] as T;
  const trace: string[] = [];

  const server = {
    stored: { ...NOTE, captures: [] as NonNullable<NoteDetailWire['captures']> },
    dark: false,
    /** Every request answered 503. */
    down: false,
    /** Every PATCH with a body refused on its merits. */
    strict: false,
    /** Reads answered 503 while writes get through: the weather a read-back fails in. */
    blind: false,
  };
  const fetchImpl = vi.fn<typeof fetch>(async (_input, init) => {
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), {
        status,
        headers: { 'content-type': status >= 400 ? 'application/problem+json' : 'application/json' },
      });
    if (server.dark) throw new TypeError('Failed to fetch');
    if (server.down) return json({ type: 'about:blank', title: 'Unavailable', status: 503 }, 503);
    if ((init?.method ?? 'GET') !== 'PATCH') {
      return server.blind ? json({ type: 'about:blank', title: 'Unavailable', status: 503 }, 503) : json(server.stored);
    }
    const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
    if (server.strict && typeof body['body'] === 'string') {
      return json({ type: 'about:blank', title: 'Too large', status: 413 }, 413);
    }
    if (body['version'] !== server.stored.version) {
      return json({ type: 'about:blank', title: 'Conflict', status: 409 }, 409);
    }
    if (typeof body['body'] === 'string') server.stored = { ...server.stored, body: body['body'] };
    if (typeof body['pinned'] === 'boolean') server.stored = { ...server.stored, pinned: body['pinned'] };
    if (Array.isArray(body['tags'])) server.stored = { ...server.stored, tags: body['tags'] as string[] };
    server.stored = { ...server.stored, version: server.stored.version + 1 };
    const { body: _b, captures: _c, ...row } = server.stored;
    return json(row);
  });
  const queryClient = testQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false, staleTime: 30_000 } });
  queryClient.setQueryData(queryKeys.note(NOTE.id), NOTE);
  const wrapper = ({ children }: { children: ReactNode }) => (
    <TestProviders api={testApiContext(fetchImpl)} queryClient={queryClient}>
      {children}
    </TestProviders>
  );
  const shell = renderHook(
    () => {
      useOfflineQueue();
      return usePinNote();
    },
    { wrapper },
  );
  const open = () =>
    renderHook(
      () => {
        const { data } = useNote(NOTE.id);
        return useNoteEditor(data);
      },
      { wrapper },
    );
  let view: ReturnType<typeof open> | null = null;

  /** Text each side wrote and has not been tapped away. */
  const mine = new Set<string>();
  const theirs = new Set<string>();
  let counter = 0;

  // Long enough for the client's own retries of an unreachable server and for
  // every IndexedDB hop behind them.
  const step = async (): Promise<void> => {
    await act(() => vi.advanceTimersByTimeAsync(20_000));
    await act(() => vi.advanceTimersByTimeAsync(2_000));
  };
  // Inside the autosave's debounce: what was typed is still only in the editor.
  const moment = async (): Promise<void> => {
    await act(() => vi.advanceTimersByTimeAsync(150));
  };

  const check = async (): Promise<string | null> => {
    const body = server.stored.body;
    for (const token of theirs) {
      if (!body.includes(token)) return `S: the server lost ${token} with no tap (server: ${JSON.stringify(body)})`;
    }
    const entry = await queuedEditFor(NOTE.id);
    const draft = view ? view.result.current.model.draft.body : '';
    const held = entry?.body?.body ?? '';
    for (const token of mine) {
      if (!body.includes(token) && !draft.includes(token) && !held.includes(token)) {
        return `U: ${token} is nowhere (server: ${JSON.stringify(body)}; editor: ${JSON.stringify(draft)}; entry: ${JSON.stringify(entry?.body ?? null)}, dead ${String(entry?.dead)})`;
      }
    }
    return null;
  };

  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    view = open();
    await step();
    for (let index = 0; index < STEPS; index += 1) {
      const model = view?.result.current.model;
      const loaded = model !== undefined && model.version > 0;
      const actions: string[] = ['rec', 'other', 'flush', server.dark ? 'up' : 'dark'];
      if (view) actions.push('back');
      else actions.push('open', 'open');
      if (view && loaded) actions.push('type', 'type', 'type', 'blur');
      if (view && model?.state === 'conflict') actions.push('answer', 'answer', 'answer');
      if (!server.dark && !server.blind && !server.down) actions.push('reread');
      // From Home, whose list has just been read: the row is the server's.
      if (pins && !view && !server.dark) actions.push('pin', 'pin');
      if (faults) {
        actions.push(
          server.strict ? 'lenient' : 'strict',
          server.down ? 'recover' : 'down',
          server.blind ? 'sight' : 'blind',
        );
        // A tag saves at once, with whatever else the editor holds.
        if (view && loaded) actions.push('tag', 'tag');
      }
      const action = pick(actions);
      let detail = '';

      if (action === 'type' && view) {
        counter += 1;
        const token = `[u${String(counter)}]`;
        mine.add(token);
        const current = view.result.current;
        act(() => {
          current.edit({ body: `${current.model.draft.body} ${token}` });
        });
        detail = token;
      } else if (action === 'blur' && view) {
        const current = view.result.current;
        await act(async () => {
          void current.saveNow();
          await Promise.resolve();
        });
      } else if (action === 'back' && view) {
        view.unmount();
        view = null;
        // The save made on leaving, and whatever it parks, has settled before anything is judged.
      } else if (action === 'open') {
        view = open();
      } else if (action === 'dark') {
        server.dark = true;
      } else if (action === 'up') {
        server.dark = false;
      } else if (action === 'rec') {
        counter += 1;
        const token = `[r${String(counter)}]`;
        theirs.add(token);
        server.stored = {
          ...server.stored,
          version: server.stored.version + 1,
          body: `${server.stored.body}\n\n${token}`,
          captures: [
            ...server.stored.captures,
            { id: `cap-${String(counter)}`, status: 'appended', created_at: NOTE.updated_at, version: 1, note_id: NOTE.id } as NonNullable<NoteDetailWire['captures']>[number],
          ],
        };
        detail = token;
      } else if (action === 'other') {
        counter += 1;
        const token = `[o${String(counter)}]`;
        theirs.add(token);
        server.stored = { ...server.stored, version: server.stored.version + 1, body: `${token} ${server.stored.body}` };
        detail = token;
      } else if (action === 'flush') {
        await act(() => queryClient.refetchQueries({ queryKey: queryKeys.offlineQueue() }));
      } else if (action === 'reread') {
        // The screen's poll, a pull to refresh, or Home's list and body prefetch.
        if (view) await act(() => queryClient.refetchQueries({ queryKey: queryKeys.note(NOTE.id) }));
        else queryClient.setQueryData(queryKeys.note(NOTE.id), { ...server.stored });
      } else if (action === 'strict' || action === 'lenient') {
        server.strict = action === 'strict';
      } else if (action === 'down' || action === 'recover') {
        server.down = action === 'down';
      } else if (action === 'blind' || action === 'sight') {
        server.blind = action === 'blind';
      } else if (action === 'tag' && view) {
        counter += 1;
        const current = view.result.current;
        await act(async () => {
          current.edit({ tags: [`t${String(counter)}`] });
          void current.saveNow();
          await Promise.resolve();
        });
      } else if (action === 'pin') {
        const row = { id: NOTE.id, version: server.stored.version };
        await act(() =>
          shell.result.current.mutateAsync({ note: row, pinned: !server.stored.pinned }).catch(() => undefined),
        );
      } else if (action === 'answer' && view) {
        const current = view.result.current;
        const prompt = current.model.theirs;
        const options = ['takeTheirs', 'keepMine'];
        if (prompt?.addition) options.push('keepBoth', 'keepBoth');
        const choice = pick(options);
        detail = choice;
        if (prompt) {
          const shown = prompt.draft.body;
          const draft = current.model.draft.body;
          if (choice === 'takeTheirs') {
            // The user dropped what the newer version does not have.
            for (const token of mine) if (draft.includes(token) && !shown.includes(token)) mine.delete(token);
          } else if (choice === 'keepMine') {
            // The user chose their text over the version shown.
            for (const token of theirs) if (shown.includes(token) && !draft.includes(token)) theirs.delete(token);
          }
          await act(async () => {
            if (choice === 'takeTheirs') current.takeTheirs();
            else {
              if (choice === 'keepMine') current.keepMine();
              else current.keepBoth();
              void current.saveNow();
            }
            await Promise.resolve();
          });
        }
      }
      // Two times in three the next action follows a keystroke, or a move on
      // the server, at once: Back inside the debounce, a save crossing a recording.
      const quick = (action === 'type' || action === 'rec' || action === 'other') && random() < 0.66;
      trace.push(`${detail ? `${action}(${detail})` : action}${quick ? '' : ' …'}`);
      if (quick) await moment();
      else await step();
      const broken = await check();
      if (broken) return `seed ${String(seed)} after ${trace.join(' → ')}: ${broken}`;
    }
    return null;
  } finally {
    view?.unmount();
    shell.unmount();
    await act(() => vi.advanceTimersByTimeAsync(25_000));
    queryClient.clear();
  }
}

describe('the editor and its queue, under random sequences', () => {
  for (const [seed, mode] of RUNS) {
    it(`seed ${String(seed)}, ${mode}`, async () => {
      expect(await run(seed, mode)).toBeNull();
    }, 60_000);
  }
});
