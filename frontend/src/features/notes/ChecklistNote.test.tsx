import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { RouterProvider, createMemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { CAPTURE_POLL_FAST_MS } from '@/api/queries/captures.ts';
import type { CaptureWire, CleanedWire, NoteDetailWire } from '@/api/schema.ts';
import { Toast, dismissToast } from '@/components/Toast.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NoteDetailScreen } from './NoteDetailScreen.tsx';
import { CLEAN_POLL_MS } from './cleaned.ts';
import { resetTidies } from './useTidyList.ts';

/**
 * The note screen for a checklist, against a small server that stores what
 * PATCH sends and answers `POST …/clean` with 202 and a `tasks` view a beat
 * later. What these prove is the 2026-09-21 contract's frontend half: the
 * meta line counts items, the Details switch converts the body and sends
 * `kind` with it — and round 8's (F8): the tabs are Items and Recordings, and
 * Tidy up list in the ⋮ writes the `tasks` view into the body with Undo,
 * only while the list is unchanged, and runs by itself on conversion.
 */

const SHOPPING: NoteDetailWire = {
  id: 'shopping',
  kind: 'checklist',
  title: 'Shopping',
  body: '- [ ] Milk\n- [x] Eggs\n- [ ] Bread',
  aliases: [],
  tags: [],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 3,
  archived: false,
  captures: [],
  cleaned: null,
  auto_clean: false,
};

const ROOF: NoteDetailWire = {
  id: 'roof-repair',
  kind: 'note',
  title: 'Roof repair',
  body: 'Ridge tiles have slipped.\n\nGet two\nquotes.',
  aliases: [],
  tags: [],
  updated_at: '2026-08-06T09:14:00.000Z',
  version: 1,
  archived: false,
  captures: [],
  cleaned: null,
  auto_clean: false,
};

/** What the cleanup model writes for the shopping list in `tasks` mode. */
const SPLIT: CleanedWire = {
  body: '- [ ] Milk\n- [x] Eggs\n- [ ] Bread\n- [ ] Butter',
  mode: 'tasks',
  generated_at: '2026-08-06T09:20:00.000Z',
  stale: false,
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function server(
  initial: NoteDetailWire,
  {
    split = SPLIT.body,
    error,
    failures = Infinity,
    path = `/notes/${initial.id}`,
  }: { split?: string; error?: string; failures?: number; path?: string } = {},
) {
  const state = {
    note: structuredClone(initial),
    patches: [] as Record<string, unknown>[],
    cleans: [] as (Record<string, unknown> | null)[],
    /** PATCHes refused for a stale version, which `patches` leaves out. */
    conflicts: 0,
  };
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    if (url.pathname.endsWith('/clean') && method === 'POST') {
      state.cleans.push(init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : null);
      const failing = error !== undefined && state.cleans.length <= failures;
      // As the server: accepting a clean stamps the row, and the worker's
      // answer bumps it again, though neither touches the words.
      state.note = { ...state.note, version: state.note.version + 1 };
      setTimeout(() => {
        state.note = {
          ...state.note,
          version: state.note.version + 1,
          cleaned: {
            ...SPLIT,
            body: failing ? '' : split,
            generated_at: new Date().toISOString(),
            ...(failing ? { error } : {}),
          },
        };
      }, 50);
      return json({ status: 'queued', mode: 'tasks' }, 202);
    }
    if (method === 'PATCH') {
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      // Only accepted writes are counted as patches: an edit made while a
      // clean is in flight crosses its version stamp, which the editor's
      // 409 rebase answers, and that is not what these tests are about.
      if (body['version'] !== state.note.version) {
        state.conflicts += 1;
        return new Response(
          JSON.stringify({ type: 'about:blank', title: 'Conflict', status: 409, current_version: state.note.version }),
          { status: 409, headers: { 'content-type': 'application/problem+json' } },
        );
      }
      state.patches.push(body);
      const nextBody = typeof body['body'] === 'string' ? body['body'] : state.note.body;
      const cleaned = state.note.cleaned;
      state.note = {
        ...state.note,
        version: state.note.version + 1,
        body: nextBody,
        ...(body['kind'] === 'note' || body['kind'] === 'checklist' ? { kind: body['kind'] } : {}),
        // As the server (`service/notes.go`): a body that is the view,
        // trailing whitespace aside, is current; any other leaves it stale.
        ...(cleaned ? { cleaned: { ...cleaned, stale: nextBody.trimEnd() !== cleaned.body.trimEnd() } } : {}),
      };
      // The answer is a list row — no body, captures or cleaned view — so
      // the client keeps the view it holds until the note refetches.
      const { body: _body, captures: _captures, cleaned: _cleaned, ...row } = state.note;
      return json(row);
    }
    if (url.pathname.endsWith('/v1/settings')) {
      return json({ retention_days: 0, theme: 'ink' });
    }
    if (url.pathname.endsWith(`/v1/notes/${state.note.id}`)) return json(state.note);
    // The open note's filing poll asks after each moving capture on its own.
    const capture = /\/v1\/captures\/([^/]+)$/.exec(url.pathname);
    const held = capture && state.note.captures?.find((item) => item.id === capture[1]);
    if (held) return json(held);
    return json({ items: [] });
  });
  const router = createMemoryRouter([{ path: '/notes/:id', Component: NoteDetailScreen }], {
    initialEntries: [path],
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <RouterProvider router={router} />
      {/* The shell's, in the app; here so the Undo that adopting offers can be pressed. */}
      <Toast />
    </TestProviders>,
  );
  return {
    router,
    get note() { return state.note; },
    /** The note changing on the server by another hand: a recording filed in. */
    set note(next: NoteDetailWire) { state.note = next; },
    get patches() { return state.patches; },
    get cleans() { return state.cleans; },
    get conflicts() { return state.conflicts; },
  };
}

/** A recording still being transcribed, which keeps the note polling, as in the app. */
function transcribing(): CaptureWire {
  return {
    id: 'cap-9',
    status: 'transcribing',
    created_at: new Date().toISOString(),
    version: 1,
    note_id: 'shopping',
    duration_ms: 3_000,
    has_peaks: false,
    has_segments: false,
  };
}

/** The recording filed in: its item at the end, a version up, the capture appended. */
function landed(api: { note: NoteDetailWire }, capture: CaptureWire, body: string): void {
  api.note = {
    ...api.note,
    body,
    version: api.note.version + 1,
    captures: [{ ...capture, status: 'appended' }],
  };
}

function tabNames(): string[] {
  return within(screen.getByRole('tablist', { name: 'Note views' }))
    .getAllByRole('tab')
    .map((tab) => tab.textContent ?? '');
}

// The tidy's poll on a fake clock that still moves with real time, as
// CleanedPanel.test.tsx: a test advances CLEAN_POLL_MS rather than waiting it.
beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});

afterEach(() => {
  dismissToast();
  resetTidies();
  sessionStorage.clear();
  vi.useRealTimers();
});

async function tidyFromMenu(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(screen.getByRole('button', { name: 'Note actions' }));
  await user.click(screen.getByRole('menuitem', { name: 'Tidy up list' }));
}

function toastText(): string | null {
  return document.querySelector('.toast__text')?.textContent ?? null;
}

describe('a checklist note', () => {
  it('has Items and Recordings for tabs, the editor for a body, and the count in the meta line', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING);
    await screen.findByRole('textbox', { name: 'Item 1' });
    expect(tabNames()).toEqual(['Items', 'Recordings (0)']);
    expect(screen.queryByRole('textbox', { name: 'Note body' })).toBeNull();
    expect(screen.getByText(/1 of 3 done/)).toBeInTheDocument();
    expect(screen.queryByText(/\d+ words/)).toBeNull();

    await user.click(screen.getByRole('checkbox', { name: 'Milk' }));
    expect(screen.getByText(/2 of 3 done/)).toBeInTheDocument();
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    // The item's own line flipped, the order kept; the kind did not change,
    // so it is not sent.
    expect(api.patches[0]).toEqual(
      expect.objectContaining({ version: 3, body: '- [x] Milk\n- [x] Eggs\n- [ ] Bread' }),
    );
    expect(api.patches[0]).not.toHaveProperty('kind');
  });

  it('the Details switch converts prose to items and back, sending kind with the body', async () => {
    const user = userEvent.setup();
    const api = server(ROOF);
    await screen.findByRole('textbox', { name: 'Note body' });
    expect(tabNames()[0]).toBe('Text');

    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Details' }));
    const toggle = screen.getByRole('checkbox', { name: 'This note is a checklist' });
    expect(toggle).not.toBeChecked();
    await user.click(toggle);

    // The screen is a checklist's at once, before the save lands.
    expect(tabNames()).toEqual(['Items', 'Recordings (0)']);
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toHaveValue('Ridge tiles have slipped.');
    expect(screen.getByRole('textbox', { name: 'Item 2' })).toHaveValue('Get two quotes.');
    expect(screen.getByText(/0 of 2 done/)).toBeInTheDocument();
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(
      expect.objectContaining({
        kind: 'checklist',
        body: '- [ ] Ridge tiles have slipped.\n- [ ] Get two quotes.',
      }),
    );

    await user.click(screen.getByRole('checkbox', { name: 'This note is a checklist' }));
    expect(tabNames()[0]).toBe('Text');
    expect(await screen.findByRole('textbox', { name: 'Note body' })).toHaveValue(
      'Ridge tiles have slipped.\n\nGet two quotes.',
    );
    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches[1]).toEqual(
      expect.objectContaining({ kind: 'note', body: 'Ridge tiles have slipped.\n\nGet two quotes.' }),
    );
  });

  it('a remembered or linked Cleaned tab lands on Items, since a checklist has none', async () => {
    server(SHOPPING, { path: '/notes/shopping?tab=cleaned' });
    await screen.findByRole('textbox', { name: 'Item 1' });
    expect(screen.getByRole('tab', { name: 'Items' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByRole('region', { name: 'Cleaned view' })).toBeNull();
  });

  it('Tidy up list writes the tidied list into the body with Undo, and Undo puts the list back', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING);
    await screen.findByRole('textbox', { name: 'Item 1' });
    await tidyFromMenu(user);

    // No mode in the request: the server picks tasks for a checklist.
    await waitFor(() => {
      expect(api.cleans).toEqual([null]);
    });
    // Seen above the rows, and said by the status region that stays mounted.
    expect(screen.getByText('Tidying the list…', { selector: '.checklist-editor__status' })).toBeInTheDocument();
    expect(screen.getByText('Tidying the list…', { selector: '[role="status"]' })).toBeInTheDocument();
    // The rows stay editable under the status line.
    expect(screen.getByRole('textbox', { name: 'Item 1' })).toBeEnabled();

    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    // On the version the clean and its answer moved the note to (3 → 5): the
    // write is not sent stale and rescued by a 409 round trip (R8-P1).
    expect(api.patches[0]).toEqual(expect.objectContaining({ body: SPLIT.body, version: 5 }));
    expect(api.conflicts).toBe(0);
    expect(screen.getByRole('textbox', { name: 'Item 3' })).toHaveValue('Butter');
    expect(toastText()).toBe('List tidied: 3 lines → 4 items.');
    expect(screen.queryByText('Tidying the list…', { selector: '.checklist-editor__status' })).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Undo' }));
    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches[1]).toEqual(expect.objectContaining({ body: SHOPPING.body }));
    expect(screen.queryByRole('textbox', { name: 'Item 3' })).toBeNull();
    expect(api.conflicts).toBe(0);
  });

  it('the tidy’s Undo refuses once a recording has landed in the list meanwhile', async () => {
    const user = userEvent.setup();
    const filing = transcribing();
    const api = server({ ...SHOPPING, captures: [filing] });
    await screen.findByRole('textbox', { name: 'Item 1' });
    await tidyFromMenu(user);
    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });

    const withJam = `${SPLIT.body}\n- [ ] Jam`;
    landed(api, filing, withJam);
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS);
    await waitFor(() => {
      expect(screen.getByText(/1 of 5 done/)).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(toastText()).toBe('The list changed since — nothing undone.');
    expect(api.patches).toHaveLength(1);
    expect(api.note.body).toBe(withJam);
  });

  it('a list changed while tidying is not replaced, and the toast offers Tidy again', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING);
    await screen.findByRole('textbox', { name: 'Item 1' });
    await tidyFromMenu(user);
    // A tick before the answer lands: the view is now older than the list.
    await user.click(screen.getByRole('checkbox', { name: 'Bread' }));
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });

    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(toastText()).toBe('The list changed while tidying — nothing replaced.');
    });
    expect(api.patches).toHaveLength(1);
    expect(api.note.body).toBe('- [ ] Milk\n- [x] Eggs\n- [x] Bread');

    await user.click(screen.getByRole('button', { name: 'Tidy again' }));
    await waitFor(() => {
      expect(api.cleans).toHaveLength(2);
    });
  });

  it('a list that comes back the same is left alone: Already tidy', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING, { split: SHOPPING.body });
    await screen.findByRole('textbox', { name: 'Item 1' });
    await tidyFromMenu(user);
    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(toastText()).toBe('Already tidy.');
    });
    expect(api.patches).toHaveLength(0);
  });

  it('a tidy that failed says so and offers Try again, with nothing written', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING, { error: 'The model did not answer.' });
    await screen.findByRole('textbox', { name: 'Item 1' });
    await tidyFromMenu(user);
    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(toastText()).toBe('Couldn’t tidy the list.');
    });
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument();
    expect(api.patches).toHaveLength(0);
  });

  it('Try again after a failed tidy waits for its own answer rather than the old error, and lands', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING, { error: 'The model did not answer.', failures: 1 });
    await screen.findByRole('textbox', { name: 'Item 1' });
    await tidyFromMenu(user);
    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(toastText()).toBe('Couldn’t tidy the list.');
    });

    // The note still carries the first run's error: that is not this run's answer.
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    await waitFor(() => {
      expect(api.cleans).toHaveLength(2);
    });
    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(expect.objectContaining({ body: SPLIT.body }));
    expect(toastText()).toBe('List tidied: 3 lines → 4 items.');
  });

  it('a tidy saves unsaved typing first, so the answer is for the list as typed', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING, { split: '- [ ] Oat milk\n- [x] Eggs\n- [ ] Bread' });
    const first = await screen.findByRole('textbox', { name: 'Item 1' });
    await user.type(first, 'Oat ', { initialSelectionStart: 0, initialSelectionEnd: 0 });
    expect(api.patches).toHaveLength(0);
    await tidyFromMenu(user);
    await waitFor(() => {
      expect(api.cleans).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(expect.objectContaining({ body: '- [ ] Oat Milk\n- [x] Eggs\n- [ ] Bread' }));

    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    expect(api.patches[1]).toEqual(expect.objectContaining({ body: '- [ ] Oat milk\n- [x] Eggs\n- [ ] Bread' }));
  });

  it('a tidy under way survives the Items editor unmounting: it lands from the Recordings tab', async () => {
    const user = userEvent.setup();
    const api = server(SHOPPING);
    await screen.findByRole('textbox', { name: 'Item 1' });
    await tidyFromMenu(user);
    await user.click(screen.getByRole('tab', { name: /Recordings/ }));
    expect(screen.queryByRole('textbox', { name: 'Item 1' })).toBeNull();

    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(expect.objectContaining({ body: SPLIT.body }));
    await user.click(screen.getByRole('tab', { name: 'Items' }));
    expect(screen.getByRole('textbox', { name: 'Item 3' })).toHaveValue('Butter');
  });

  it('Share offers no cleaned view for a checklist, even one carrying an old tasks view', async () => {
    const user = userEvent.setup();
    server({ ...SHOPPING, cleaned: SPLIT });
    await screen.findByRole('textbox', { name: 'Item 1' });
    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Share' }));
    expect(screen.getByRole('button', { name: 'Copy note' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy cleaned view' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Download cleaned view' })).toBeNull();
  });

  it('the menu offers no tidy for a list with nothing open', async () => {
    const user = userEvent.setup();
    server({ ...SHOPPING, body: '- [x] Milk' });
    await screen.findByRole('checkbox', { name: 'Milk' });
    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    expect(screen.getByRole('menuitem', { name: 'Pin' })).toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: 'Tidy up list' })).toBeNull();
  });

  it('converting prose to a checklist tidies it, and Undo gives back the paragraph items', async () => {
    const user = userEvent.setup();
    const split = '- [ ] Fix the ridge tiles\n- [ ] Get two quotes';
    const api = server(ROOF, { split });
    await screen.findByRole('textbox', { name: 'Note body' });
    await user.click(screen.getByRole('button', { name: 'Note actions' }));
    await user.click(screen.getByRole('menuitem', { name: 'Details' }));
    await user.click(screen.getByRole('checkbox', { name: 'This note is a checklist' }));

    // One PATCH with the deterministic conversion first, then the tidy.
    await waitFor(() => {
      expect(api.cleans).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(
      expect.objectContaining({ kind: 'checklist', body: '- [ ] Ridge tiles have slipped.\n- [ ] Get two quotes.' }),
    );
    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    await waitFor(() => {
      expect(api.patches).toHaveLength(2);
    });
    // Conversion 1 → 2, the clean and its answer 2 → 4: no 409 on the way (R8-P1).
    expect(api.patches[1]).toEqual(expect.objectContaining({ body: split, version: 4 }));
    expect(api.conflicts).toBe(0);
    expect(toastText()).toBe('Made a checklist: 2 paragraphs → 2 items.');

    await user.click(screen.getByRole('button', { name: 'Undo' }));
    await waitFor(() => {
      expect(api.patches).toHaveLength(3);
    });
    expect(api.patches[2]).toEqual(
      expect.objectContaining({ body: '- [ ] Ridge tiles have slipped.\n- [ ] Get two quotes.' }),
    );
  });

  it('Delete done’s Undo on Items refuses from the Recordings tab too, once a recording has landed', async () => {
    const user = userEvent.setup();
    const filing = transcribing();
    const api = server({
      ...SHOPPING,
      cleaned: { ...SPLIT, generated_at: '2026-08-06T09:24:00.000Z' },
      captures: [filing],
    });
    await screen.findByRole('textbox', { name: 'Item 1' });
    await user.click(screen.getByRole('button', { name: 'Delete done' }));
    await waitFor(() => {
      expect(api.patches).toHaveLength(1);
    });
    expect(api.patches[0]).toEqual(expect.objectContaining({ body: '- [ ] Milk\n- [ ] Bread' }));
    expect(screen.getByText('1 done item deleted', { selector: '.toast__text' })).toBeInTheDocument();

    // Over to Recordings, which unmounts the editor that wrote; the recording
    // lands. The meta line counts the body's items whichever tab is open.
    await user.click(screen.getByRole('tab', { name: /Recordings/ }));
    const withJam = '- [ ] Milk\n- [ ] Bread\n- [ ] Jam';
    landed(api, filing, withJam);
    await vi.advanceTimersByTimeAsync(CAPTURE_POLL_FAST_MS);
    await waitFor(() => {
      expect(screen.getByText(/0 of 3 done/)).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(screen.getByText('The list changed since — nothing undone.', { selector: '.toast__text' })).toBeInTheDocument();
    await vi.advanceTimersByTimeAsync(CLEAN_POLL_MS);
    expect(api.patches).toHaveLength(1);
    expect(api.note.body).toBe(withJam);
  });
});
