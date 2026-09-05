import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import process from 'node:process';

import { afterEach, describe, expect, it } from 'vitest';

import { askAnswered, askFailed, askNotInNotes, askPending } from '@/api/__fixtures__/pending.ts';
import { ASK_POLL_TIMEOUT_MS } from '@/api/queries.ts';

import {
  ASK_NOTE_TAG,
  HISTORY_ANSWER_LIMIT,
  HISTORY_BYTES_BUDGET,
  HISTORY_LIMIT,
  HISTORY_QUESTION_LIMIT,
  LOST_MESSAGE,
  NOT_SENT_MESSAGE,
  RESUME_WINDOW_MS,
  THREAD_KEY,
  applyRow,
  canResend,
  canResume,
  failTurn,
  historyFor,
  isBusy,
  loadThread,
  newTurn,
  noteFromThread,
  resendTurn,
  resumeTurn,
  saveThread,
  sourceLabels,
  sourcesOf,
  type AskTurn,
} from './thread.ts';

function answered(question: string, answer: string, sources = askAnswered.sources): AskTurn {
  return applyRow(newTurn(question, question), { ...askAnswered, question, answer, sources });
}

/**
 * The bounds `POST /v1/ask` declares, read from the contract itself so that a
 * limit moved there fails here rather than in production with a 400.
 */
function contractBounds(): { question: number; answer: number; turns: number; bytes: number } {
  // Vitest runs from `frontend/`, as the request-contract test relies on too.
  const yaml = readFileSync(resolve(process.cwd(), '..', 'docs', 'api', 'openapi.yaml'), 'utf8');
  const turn =
    /AskTurn:[\s\S]*?question: \{ type: string, maxLength: (\d+) \}\s*answer: \{ type: string, maxLength: (\d+) \}/.exec(
      yaml,
    );
  const turns = /history:\s*type: array\s*maxItems: (\d+)/.exec(yaml);
  const bytes = /The request body is capped at (\d+) KB\./.exec(yaml);
  if (!turn || !turns || !bytes) {
    throw new Error('openapi.yaml no longer declares the Ask bounds where this test reads them');
  }
  return {
    question: Number(turn[1]),
    answer: Number(turn[2]),
    turns: Number(turns[1]),
    bytes: Number(bytes[1]) * 1024,
  };
}

function utf8Bytes(value: unknown): number {
  return new TextEncoder().encode(JSON.stringify(value)).length;
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

  it('is bounded by the numbers the contract declares', () => {
    const bounds = contractBounds();
    expect(HISTORY_LIMIT).toBe(bounds.turns);
    expect(HISTORY_ANSWER_LIMIT).toBe(bounds.answer);
    expect(HISTORY_QUESTION_LIMIT).toBe(bounds.question);
    expect(HISTORY_BYTES_BUDGET).toBeLessThan(bounds.bytes);
  });

  it('cuts an answer the worker stored whole to what history admits, counting code points, and says so', () => {
    const bounds = contractBounds();
    // 8,000 code points is what the worker may store; two bytes each in UTF-8.
    const stored = 'é'.repeat(8_000);
    const [turn] = historyFor([answered('q', stored)]);
    expect(Array.from(turn?.answer ?? '')).toHaveLength(bounds.answer);
    expect(turn?.answer.endsWith('…')).toBe(true);
    expect(turn?.answer.startsWith('éééé')).toBe(true);

    // Astral characters are two UTF-16 units but one rune to the server:
    // three thousand of them — 6,000 units — are within the limit and are
    // left alone (and 12 KiB is within the body budget).
    const astral = '😀'.repeat(3_000);
    expect(astral.length).toBeGreaterThan(bounds.answer);
    expect(historyFor([answered('q', astral)])[0]?.answer).toBe(astral);

    const question = 'q'.repeat(bounds.question + 500);
    const [cutQuestion] = historyFor([answered(question, 'a')]);
    expect(Array.from(cutQuestion?.question ?? '')).toHaveLength(bounds.question);
    expect(cutQuestion?.question.endsWith('…')).toBe(true);
  });

  it('drops the oldest turns until the request fits under the body cap, keeping the latest', () => {
    const bounds = contractBounds();
    const turns = Array.from({ length: 20 }, (_, i) =>
      answered(`q${String(i)}`, 'a'.repeat(bounds.answer - 100)),
    );
    const question = 'x'.repeat(bounds.question);
    const history = historyFor(turns, question);

    // Six such turns are ~23 KiB; three fit beside the question.
    expect(history).toHaveLength(3);
    expect(history.map((turn) => turn.question)).toEqual(['q17', 'q18', 'q19']);
    expect(utf8Bytes({ question, history })).toBeLessThanOrEqual(HISTORY_BYTES_BUDGET);
    expect(utf8Bytes({ question, history })).toBeLessThan(bounds.bytes);
    for (const turn of history) {
      expect(Array.from(turn.answer).length).toBeLessThanOrEqual(bounds.answer);
      expect(Array.from(turn.question).length).toBeLessThanOrEqual(bounds.question);
    }
  });

  it('sends no history at all rather than a body the server would refuse', () => {
    // One answer of 4,000 four-byte code points is 16 KiB on its own.
    const heavy = answered('q', '😀'.repeat(HISTORY_ANSWER_LIMIT));
    expect(historyFor([heavy], 'and?')).toEqual([]);
  });
});

describe('Try again on a question the client gave up on', () => {
  const now = 1_000_000;

  it('resumes the poll for a timed-out or lost row, with a fresh minute', () => {
    const pending = { ...applyRow(newTurn('k', 'q', now), askPending), since: now + 500 };
    const timedOut: AskTurn = { ...pending, status: 'timeout' };
    expect(canResume(timedOut, now + 90_000)).toBe(true);
    expect(canResume(failTurn(pending, LOST_MESSAGE), now + 90_000)).toBe(true);

    const resumed = resumeTurn(timedOut, now + 90_000);
    expect(resumed).toMatchObject({ status: 'pending', error: null, since: now + 90_000, askId: 'fixture-ask-id' });
    expect(resumed.sentAt).toBe(now);
  });

  it('asks afresh when the server failed the row, when there is no row, or when the question is old', () => {
    const pending = applyRow(newTurn('k', 'q', now), askPending);
    expect(canResume(applyRow(pending, askFailed), now + 5_000)).toBe(false);
    expect(canResume(failTurn(newTurn('k', 'q', now), NOT_SENT_MESSAGE), now + 5_000)).toBe(false);
    expect(canResume({ ...pending, status: 'timeout' }, now + RESUME_WINDOW_MS)).toBe(false);
    // Still pending is not a retry at all.
    expect(canResume(pending, now + 5_000)).toBe(false);
  });

  it('judges the age of a thread saved before `sentAt` existed by its poll clock', () => {
    const { sentAt: _dropped, ...older } = applyRow(newTurn('k', 'q', now), askPending);
    expect(canResume({ ...older, status: 'timeout' }, now + 90_000)).toBe(true);
    expect(canResume({ ...older, status: 'timeout' }, now + RESUME_WINDOW_MS)).toBe(false);
  });
});

describe('what a source chip says', () => {
  const dated = (dates: Record<string, string>) => (noteId: string) => dates[noteId] ?? null;

  it('is the title alone while titles are distinct', () => {
    expect(sourceLabels(askAnswered.sources, dated({}))).toEqual(['Roof repair', 'Kitchen rebuild']);
  });

  it('adds the note’s date where two sources share a title, and numbers the ones it has no date for', () => {
    const sources = [
      { note_id: 'a', title: 'Gutter leak' },
      { note_id: 'b', title: 'Gutter leak' },
      { note_id: 'c', title: 'Roof repair' },
      { note_id: 'd', title: 'Gutter leak' },
    ];
    expect(sourceLabels(sources, dated({ a: '4 Sept', b: '28 Aug' }))).toEqual([
      'Gutter leak · 4 Sept',
      'Gutter leak · 28 Aug',
      'Roof repair',
      'Gutter leak (1)',
    ]);
    expect(sourceLabels(sources, dated({}))).toEqual([
      'Gutter leak (1)',
      'Gutter leak (2)',
      'Roof repair',
      'Gutter leak (3)',
    ]);
  });

  it('numbers the repeats when even the dates collide', () => {
    const sources = [
      { note_id: 'a', title: 'Gutter leak' },
      { note_id: 'b', title: 'Gutter leak' },
      { note_id: 'c', title: 'Gutter leak' },
    ];
    expect(sourceLabels(sources, dated({ a: '4 Sept', b: '4 Sept', c: '4 Sept' }))).toEqual([
      'Gutter leak · 4 Sept',
      'Gutter leak · 4 Sept (2)',
      'Gutter leak · 4 Sept (3)',
    ]);
  });
});

describe('the thread as a note', () => {
  it('is titled by the first question and holds each exchange as plain text, then the sources once each, tagged ask', () => {
    const turns = [
      answered('what did I decide about the roof?', 'Two quotes first.'),
      answered('and when?', 'The **fourteenth**.\n\n- Call the tiler\n- Then the **gutter**', [
        { note_id: 'fixture-note-id', title: 'Roof repair' },
        { note_id: 'other', title: 'Calendar' },
      ]),
    ];
    const note = noteFromThread(turns);
    expect(note?.title).toBe('what did I decide about the roof?');
    // No Markdown: the Text tab and the library snippet show the body verbatim.
    expect(note?.body).toBe(
      [
        'Q: what did I decide about the roof?\n\nA: Two quotes first.',
        'Q: and when?\n\nA: The fourteenth.\n\n- Call the tiler\n- Then the gutter',
        'Sources:\nRoof repair\nKitchen rebuild\nCalendar',
      ].join('\n\n'),
    );
    expect(note?.body).not.toMatch(/\*/);
    expect(note?.tags).toEqual([ASK_NOTE_TAG]);
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
    expect(noteFromThread(turns)?.body).toBe('Q: what colour is my car?\n\nA: Your notes do not say.');
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

  it('keeps a young question whose POST was in flight as asking, so it is sent again under its key', () => {
    // The panel was remounted mid-send. With the request held, the hook
    // replays it and the server answers with the original 202; without it,
    // or once too old to be worth it, the question is shown as never sent.
    const now = 1_000_000;
    const request = { question: 'in flight' };
    saveThread([
      { ...newTurn('a', 'in flight', now - 5_000), request },
      { ...newTurn('b', 'too old', now - RESUME_WINDOW_MS), request },
      newTurn('c', 'saved without its request', now - 5_000),
    ]);
    const [young, old, bare] = loadThread(now);
    expect(young).toMatchObject({ status: 'asking', request });
    expect(old).toMatchObject({ status: 'failed', error: NOT_SENT_MESSAGE });
    expect(bare).toMatchObject({ status: 'failed', error: NOT_SENT_MESSAGE });
  });

  it('can resend only a question that never reached this side and still has its body', () => {
    const request = { question: 'q' };
    const lost = failTurn({ ...newTurn('k', 'q'), request }, NOT_SENT_MESSAGE);
    expect(canResend(lost)).toBe(true);
    expect(canResend(failTurn(newTurn('k', 'q'), NOT_SENT_MESSAGE))).toBe(false);
    // The server's own failure is bound to the key; only a new question can do better.
    expect(canResend({ ...applyRow({ ...newTurn('k', 'q'), request }, askFailed) })).toBe(false);
    expect(canResend(failTurn({ ...newTurn('k', 'q'), request }, LOST_MESSAGE))).toBe(false);
    expect(resendTurn(lost, 7)).toMatchObject({ status: 'asking', askId: null, error: null, since: 7 });
  });

  it('is empty for anything unreadable', () => {
    sessionStorage.setItem(THREAD_KEY, '{not json');
    expect(loadThread()).toEqual([]);
    sessionStorage.setItem(THREAD_KEY, JSON.stringify([{ nonsense: true }, 42]));
    expect(loadThread()).toEqual([]);
  });
});
