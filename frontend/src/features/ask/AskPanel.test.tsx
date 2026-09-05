import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { askAnswered, askFailed, askNotInNotes, askPending } from '@/api/__fixtures__/pending.ts';
import { noteCreated } from '@/api/__fixtures__/responses.ts';
import { ASK_POLL_TIMEOUT_MS } from '@/api/queries.ts';
import type { AskWire, NoteCreateWire } from '@/api/schema.ts';
import { NotesScreen } from '@/screens/NotesScreen.tsx';
import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';

import { COST_NOTE_KEY } from './costNote.ts';
import { applyRow, newTurn, saveThread } from './thread.ts';

/**
 * The Ask panel is only reachable through the library's field, so these
 * tests mount the library and switch it, the way a person does.
 */

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}

function mount(fetchImpl: typeof fetch, path = '/') {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter initialEntries={[path]}>
        <NotesScreen />
        <LocationProbe />
      </MemoryRouter>
    </TestProviders>,
  );
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

/**
 * A library plus the Ask endpoints: the POST answers 202 with the pending
 * row, and the poll answers `pending` for the first `pendingPolls` reads of
 * each question and then `final`. What was posted, and how many polls there
 * were in all, are recorded.
 */
function server({ pendingPolls = 1, final = askAnswered }: { pendingPolls?: number; final?: AskWire } = {}) {
  const posts: { question: string; history?: unknown }[] = [];
  const creates: NoteCreateWire[] = [];
  let polls = 0;
  let pollsThisQuestion = 0;
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    // Each question is its own row with its own id, as on the real API.
    if (url.pathname.endsWith('/v1/ask') && method === 'POST') {
      posts.push(JSON.parse(String(init?.body)) as { question: string; history?: unknown });
      pollsThisQuestion = 0;
      return json({ ...askPending, id: `ask-${String(posts.length)}` }, 202);
    }
    if (url.pathname.includes('/v1/ask/')) {
      polls += 1;
      pollsThisQuestion += 1;
      const id = url.pathname.slice(url.pathname.lastIndexOf('/') + 1);
      return json({ ...(pollsThisQuestion <= pendingPolls ? askPending : final), id });
    }
    if (url.pathname.endsWith('/v1/notes') && method === 'POST') {
      creates.push(JSON.parse(String(init?.body)) as NoteCreateWire);
      return json({ ...noteCreated, id: 'saved-thread' }, 201);
    }
    if (url.pathname.endsWith('/v1/tags')) return json({ items: [{ name: 'house', count: 1 }] });
    if (url.pathname.endsWith('/v1/notes')) return json({ items: TEST_NOTES });
    return json({ items: [] });
  });
  return { fetchImpl, posts, creates, polls: () => polls };
}

function location(): string {
  return screen.getByTestId('location').textContent ?? '';
}

/**
 * Deterministic time for the poll. Everything that schedules is faked,
 * including Date, so `advanceTimersByTime` moves the poll's clock too;
 * `shouldAdvanceTime` keeps Testing Library's own waiting working.
 */
function fakeTime() {
  vi.useFakeTimers({
    shouldAdvanceTime: true,
    toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'],
  });
  return userEvent.setup({ advanceTimers: vi.advanceTimersByTime.bind(vi) });
}

/** Switches the field to Ask and returns it. */
async function switchToAsk(user: ReturnType<typeof userEvent.setup>): Promise<HTMLElement> {
  await screen.findByRole('button', { name: /roof repair/i });
  await user.click(screen.getByRole('radio', { name: 'Ask' }));
  return screen.getByRole('searchbox', { name: /ask your notes/i });
}

async function askAndAnswer(
  user: ReturnType<typeof userEvent.setup>,
  question = 'what did I decide about the roof?',
): Promise<void> {
  const field = await switchToAsk(user);
  await user.type(field, `${question}{Enter}`);
  expect(await screen.findByText('Reading your notes…')).toBeInTheDocument();
  // The first read comes back pending; the second, a second later, answers.
  await waitFor(() => {
    expect(screen.queryByText('Reading your notes…')).toBeInTheDocument();
  });
  act(() => {
    vi.advanceTimersByTime(1_000);
  });
  await screen.findByText(/The tiler can start on the fourteenth/);
}

afterEach(() => {
  vi.useRealTimers();
  sessionStorage.clear();
  localStorage.clear();
});

describe('the field has two modes', () => {
  it('switches to Ask: the placeholder changes, the list and its chips step aside, and the URL says so', async () => {
    const user = userEvent.setup();
    mount(server().fetchImpl);

    const before = await screen.findByRole('searchbox', { name: /search notes/i });
    expect(before).toHaveAttribute('placeholder', 'Search titles, tags, transcripts');
    expect(screen.getByRole('radio', { name: 'Search' })).toBeChecked();

    const field = await switchToAsk(user);
    expect(field).toHaveAttribute('placeholder', 'Ask your notes…');
    expect(screen.getByRole('radio', { name: 'Ask' })).toBeChecked();
    expect(screen.queryByRole('button', { name: /roof repair/i })).toBeNull();
    expect(screen.queryByRole('group', { name: 'Filter notes' })).toBeNull();
    expect(screen.getByRole('region', { name: 'Ask your notes' })).toBeInTheDocument();
    expect(location()).toBe('/?mode=ask');

    // And back, with the whole library where it was.
    await user.click(screen.getByRole('radio', { name: 'Search' }));
    expect(await screen.findByRole('button', { name: /roof repair/i })).toBeInTheDocument();
    expect(screen.getByRole('searchbox', { name: /search notes/i })).toBeInTheDocument();
    expect(location()).toBe('/');
  });

  it('is deep-linkable: `?mode=ask` opens in Ask, and switching drops a filter that was typed', async () => {
    const user = userEvent.setup();
    mount(server().fetchImpl, '/?q=roof');
    await screen.findByRole('button', { name: /roof repair/i });
    await user.click(screen.getByRole('radio', { name: 'Ask' }));
    expect(location()).toBe('/?mode=ask');

    mount(server().fetchImpl, '/?mode=ask');
    expect(screen.getAllByRole('radio', { name: 'Ask' }).at(-1)).toBeChecked();
  });

  it('says once what a question costs, and remembers being told', async () => {
    const user = userEvent.setup();
    const { unmount } = mount(server().fetchImpl);
    await switchToAsk(user);

    expect(screen.getByText(/one model call, about a cent/i)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Got it' }));
    expect(screen.queryByText(/about a cent/i)).toBeNull();
    expect(localStorage.getItem(COST_NOTE_KEY)).toBe('1');

    unmount();
    mount(server().fetchImpl, '/?mode=ask');
    await screen.findByRole('region', { name: 'Ask your notes' });
    expect(screen.queryByText(/about a cent/i)).toBeNull();
  });
});

describe('a question is sent on Enter and its answer polled for', () => {
  it('posts the question, says it is reading, polls until answered, and renders the answer with its sources', async () => {
    const user = fakeTime();
    const api = server();
    mount(api.fetchImpl);

    const field = await switchToAsk(user);
    await user.type(field, 'what did I decide about the roof?');
    // Nothing is sent per keystroke — a question is a request, not a filter.
    expect(api.posts).toHaveLength(0);
    await user.keyboard('{Enter}');

    await waitFor(() => {
      expect(api.posts).toEqual([{ question: 'what did I decide about the roof?' }]);
    });
    expect(field).toHaveValue('');
    expect(await screen.findByText('Reading your notes…')).toBeInTheDocument();
    const thread = screen.getByRole('list', { name: 'Questions and answers' });
    expect(thread).toHaveAttribute('aria-live', 'polite');
    expect(within(thread).getByText('what did I decide about the roof?')).toBeInTheDocument();

    // The first poll is immediate and pending; the next comes a second later.
    await waitFor(() => {
      expect(api.polls()).toBe(1);
    });
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    await waitFor(() => {
      expect(api.polls()).toBe(2);
    });

    // The light Markdown, as elements and never as HTML.
    expect(await screen.findByText('ridge tiles', { selector: 'strong' })).toBeInTheDocument();
    expect(screen.getByText('The tiler can start on the fourteenth.', { selector: 'li' })).toBeInTheDocument();
    expect(screen.queryByText('Reading your notes…')).toBeNull();
    expect(screen.queryByText('Not in your notes')).toBeNull();

    // A settled row is not polled again.
    act(() => {
      vi.advanceTimersByTime(5_000);
    });
    expect(api.polls()).toBe(2);

    // The sources, in the order the server ranked them, and one opens its note.
    const sources = screen.getByRole('list', { name: 'Sources' });
    expect(within(sources).getAllByRole('button').map((chip) => chip.textContent)).toEqual([
      'Roof repair',
      'Kitchen rebuild',
    ]);
    await user.click(within(sources).getByRole('button', { name: 'Kitchen rebuild' }));
    expect(location()).toBe('/notes/fixture-note-id-2');
  });

  it('labels an answer the notes could not give, and lists no sources for it', async () => {
    const user = fakeTime();
    mount(server({ final: askNotInNotes }).fetchImpl);

    const field = await switchToAsk(user);
    await user.type(field, 'what colour is my car?{Enter}');
    await screen.findByText('Reading your notes…');
    act(() => {
      vi.advanceTimersByTime(1_000);
    });

    expect(await screen.findByText('Not in your notes')).toBeInTheDocument();
    expect(screen.getByText('Your notes do not say anything about a car.')).toBeInTheDocument();
    expect(screen.queryByRole('list', { name: 'Sources' })).toBeNull();
  });

  it('shows the server’s fixed sentence when the answer failed, and Try again asks again', async () => {
    const user = fakeTime();
    const api = server({ final: askFailed });
    mount(api.fetchImpl);

    const field = await switchToAsk(user);
    await user.type(field, 'what did I decide about the roof?{Enter}');
    await screen.findByText('Reading your notes…');
    act(() => {
      vi.advanceTimersByTime(1_000);
    });

    expect(await screen.findByText('the answer could not be produced; try again')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    await waitFor(() => {
      expect(api.posts).toHaveLength(2);
    });
    expect(api.posts[1]).toEqual({ question: 'what did I decide about the roof?' });
  });

  it('stops waiting after a minute and says so', async () => {
    const user = fakeTime();
    const api = server({ pendingPolls: Number.POSITIVE_INFINITY });
    mount(api.fetchImpl);

    const field = await switchToAsk(user);
    await user.type(field, 'what did I decide about the roof?{Enter}');
    await screen.findByText('Reading your notes…');
    await waitFor(() => {
      expect(api.polls()).toBe(1);
    });

    // Every second for the first ten seconds. Stepped a second at a time,
    // because a poll in flight is not doubled up: the next tick waits on it.
    for (let second = 1; second <= 10; second += 1) {
      act(() => {
        vi.advanceTimersByTime(1_000);
      });
      await waitFor(() => {
        expect(api.polls()).toBe(1 + second);
      });
    }
    // Then every two: a second passes with no poll, the next brings one.
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(api.polls()).toBe(11);
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    await waitFor(() => {
      expect(api.polls()).toBe(12);
    });

    // And at the minute, the panel gives up and the polling stops with it.
    act(() => {
      vi.advanceTimersByTime(ASK_POLL_TIMEOUT_MS - 12_000);
    });
    expect(await screen.findByText('This is taking too long.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument();
    expect(screen.queryByText('Reading your notes…')).toBeNull();
    const settled = api.polls();
    act(() => {
      vi.advanceTimersByTime(10_000);
    });
    expect(api.polls()).toBe(settled);
  });
});

describe('the thread', () => {
  it('sends a follow-up with the earlier exchange as history', async () => {
    const user = fakeTime();
    const api = server();
    mount(api.fetchImpl);
    await askAndAnswer(user);

    await user.type(screen.getByRole('textbox', { name: 'Ask a follow-up' }), 'and when?{Enter}');
    await waitFor(() => {
      expect(api.posts).toHaveLength(2);
    });
    expect(api.posts[1]).toEqual({
      question: 'and when?',
      history: [{ question: 'what did I decide about the roof?', answer: askAnswered.answer }],
    });
    // Both questions stay on screen; the follow-up field is held while it is unanswered.
    expect(screen.getByText('and when?')).toBeInTheDocument();
    expect(await screen.findByText('Reading your notes…')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Ask a follow-up' })).toBeDisabled();
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    await waitFor(() => {
      expect(screen.getByRole('textbox', { name: 'Ask a follow-up' })).toBeEnabled();
    });
    expect(screen.getAllByText(/The tiler can start on the fourteenth/)).toHaveLength(2);
  });

  it('is saved as a note titled by the first question, with the exchanges and the sources', async () => {
    const user = fakeTime();
    const api = server();
    mount(api.fetchImpl);
    await askAndAnswer(user);

    await user.click(screen.getByRole('button', { name: 'Save as note' }));
    await waitFor(() => {
      expect(api.creates).toHaveLength(1);
    });
    expect(api.creates[0]).toEqual({
      title: 'what did I decide about the roof?',
      body: [
        `**Q: what did I decide about the roof?**\n\n${askAnswered.answer ?? ''}`,
        '**Sources**\n- Roof repair\n- Kitchen rebuild',
      ].join('\n\n'),
    });
    // And the reader is taken to it; the thread is finished with.
    await waitFor(() => {
      expect(location()).toBe('/notes/saved-thread');
    });
    expect(sessionStorage.getItem('chintan.ask.thread')).toBeNull();
  });

  it('is cleared on Clear', async () => {
    const user = fakeTime();
    mount(server().fetchImpl);
    await askAndAnswer(user);

    await user.click(screen.getByRole('button', { name: 'Clear' }));
    expect(screen.queryByRole('list', { name: 'Questions and answers' })).toBeNull();
    expect(screen.getByText(/ask a question and the answer is drawn from your notes/i)).toBeInTheDocument();
  });

  it('comes back from session storage when the screen is remounted, without asking again', async () => {
    const turn = applyRow(newTurn('k1', 'what did I decide about the roof?'), askAnswered);
    saveThread([turn]);
    const api = server();
    mount(api.fetchImpl, '/?mode=ask');

    expect(await screen.findByText('what did I decide about the roof?')).toBeInTheDocument();
    expect(screen.getByText('ridge tiles', { selector: 'strong' })).toBeInTheDocument();
    expect(api.posts).toHaveLength(0);
    expect(api.polls()).toBe(0);
  });
});
