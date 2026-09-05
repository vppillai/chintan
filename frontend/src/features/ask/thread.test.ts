import { afterEach, describe, expect, it } from 'vitest';

import { askAnswered, askFailed, askNotInNotes, askPending } from '@/api/__fixtures__/responses.ts';
import { ASK_POLL_TIMEOUT_MS } from '@/api/queries.ts';

import {
  HISTORY_LIMIT,
  NOT_SENT_MESSAGE,
  THREAD_KEY,
  applyRow,
  failTurn,
  historyFor,
  isBusy,
  loadThread,
  newTurn,
  noteFromThread,
  saveThread,
  sourcesOf,
  type AskTurn,
} from './thread.ts';

function answered(question: string, answer: string, sources = askAnswered.sources): AskTurn {
  return applyRow(newTurn(question, question), { ...askAnswered, question, answer, sources });
}

afterEach(() => {
  sessionStorage.clear();
});

describe('a turn takes the server’s row', () => {
  it('starts as asking, becomes pending on the 202, and settles on the answer', () => {
    const turn = newTurn('k1', 'what did I decide about the roof?', 1_000);
    expect(turn.status).toBe('asking');
    expect(isBusy(turn)).toBe(true);

    const pending = applyRow(turn, askPending);
    expect(pending).toMatchObject({ status: 'pending', askId: 'fixture-ask-id', answer: null });
    expect(isBusy(pending)).toBe(true);

    const done = applyRow(pending, askAnswered);
    expect(done).toMatchObject({
      status: 'answered',
      grounded: true,
      answer: askAnswered.answer,
      error: null,
    });
    expect(done.sources.map((source) => source.title)).toEqual(['Roof repair', 'Kitchen rebuild']);
    expect(isBusy(done)).toBe(false);
  });

  it('keeps the server’s fixed sentence on a failed row, and ours when it sent none', () => {
    const turn = applyRow(newTurn('k1', 'q'), askPending);
    expect(applyRow(turn, askFailed)).toMatchObject({
      status: 'failed',
      error: 'the answer could not be produced; try again',
    });
    expect(applyRow(turn, { ...askFailed, error: null }).error).toMatch(/could not be fetched/);
    expect(failTurn(turn, NOT_SENT_MESSAGE)).toMatchObject({ status: 'failed', error: NOT_SENT_MESSAGE });
  });

  it('is answered, not failed, when the notes do not say', () => {
    const turn = applyRow(newTurn('k1', 'what colour is my car?'), askNotInNotes);
    expect(turn).toMatchObject({ status: 'answered', grounded: false, sources: [] });
  });
});

describe('the history sent with a follow-up', () => {
  it('is the answered turns, oldest first, and skips anything without an answer', () => {
    const turns = [
      answered('one', 'A1'),
      failTurn(newTurn('k', 'two'), NOT_SENT_MESSAGE),
      answered('three', 'A3'),
      applyRow(newTurn('k', 'four'), askPending),
    ];
    expect(historyFor(turns)).toEqual([
      { question: 'one', answer: 'A1' },
      { question: 'three', answer: 'A3' },
    ]);
  });

  it('is capped at the six most recent, which is what the contract accepts', () => {
    const turns = Array.from({ length: 9 }, (_, i) => answered(`q${String(i)}`, `a${String(i)}`));
    const history = historyFor(turns);
    expect(history).toHaveLength(HISTORY_LIMIT);
    expect(history[0]).toEqual({ question: 'q3', answer: 'a3' });
    expect(history.at(-1)).toEqual({ question: 'q8', answer: 'a8' });
  });
});

describe('the thread as a note', () => {
  it('is titled by the first question and holds each exchange, then the sources once each', () => {
    const turns = [
      answered('what did I decide about the roof?', 'Two quotes first.'),
      answered('and when?', 'The **fourteenth**.', [
        { note_id: 'fixture-note-id', title: 'Roof repair' },
        { note_id: 'other', title: 'Calendar' },
      ]),
    ];
    const note = noteFromThread(turns);
    expect(note?.title).toBe('what did I decide about the roof?');
    expect(note?.body).toBe(
      [
        '**Q: what did I decide about the roof?**\n\nTwo quotes first.',
        '**Q: and when?**\n\nThe **fourteenth**.',
        '**Sources**\n- Roof repair\n- Kitchen rebuild\n- Calendar',
      ].join('\n\n'),
    );
    expect(sourcesOf(turns).map((source) => source.note_id)).toEqual([
      'fixture-note-id',
      'fixture-note-id-2',
      'other',
    ]);
  });

  it('leaves out failed turns and the Sources list when nothing was cited', () => {
    const turns = [
      answered('what colour is my car?', 'Your notes do not say.', []),
      failTurn(newTurn('k', 'and the van?'), NOT_SENT_MESSAGE),
    ];
    expect(noteFromThread(turns)?.body).toBe(
      '**Q: what colour is my car?**\n\nYour notes do not say.',
    );
  });

  it('cuts the title to what POST /v1/notes accepts, and is nothing when nothing was answered', () => {
    const long = 'x'.repeat(250);
    expect(noteFromThread([answered(long, 'a')])?.title).toHaveLength(200);
    expect(noteFromThread([])).toBeNull();
    expect(noteFromThread([applyRow(newTurn('k', 'q'), askPending)])).toBeNull();
  });
});

describe('the thread in session storage', () => {
  it('round-trips, and removes itself when cleared', () => {
    const turns = [answered('one', 'A1')];
    saveThread(turns);
    expect(loadThread()).toEqual(turns);
    saveThread([]);
    expect(sessionStorage.getItem(THREAD_KEY)).toBeNull();
    expect(loadThread()).toEqual([]);
  });

  it('is honest about time passed: a POST that never returned failed, a stale poll timed out, a fresh one resumes', () => {
    const now = 1_000_000;
    saveThread([
      newTurn('a', 'never sent', now - 5_000),
      applyRow(newTurn('b', 'stale', now - ASK_POLL_TIMEOUT_MS - 1), askPending),
      { ...applyRow(newTurn('c', 'fresh', now - 5_000), askPending), since: now - 5_000 },
    ]);
    const [neverSent, stale, fresh] = loadThread(now);
    expect(neverSent).toMatchObject({ status: 'failed', error: NOT_SENT_MESSAGE });
    expect(stale?.status).toBe('timeout');
    expect(fresh?.status).toBe('pending');
  });

  it('is empty for anything unreadable', () => {
    sessionStorage.setItem(THREAD_KEY, '{not json');
    expect(loadThread()).toEqual([]);
    sessionStorage.setItem(THREAD_KEY, JSON.stringify([{ nonsense: true }, 42]));
    expect(loadThread()).toEqual([]);
  });
});
