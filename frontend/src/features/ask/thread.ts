/**
 * The Ask thread: what the panel knows about each question, and how that is
 * kept, sent back and written down. Pure, so the shapes are testable without
 * a network or a DOM.
 *
 * A thread is short-lived on purpose. It lives in `sessionStorage`, so
 * switching to a note and back — or to another tab and back — keeps the
 * conversation on screen, while a reload starts clean: a question is not a
 * document, and the one way to keep an answer is "Save as note", which makes
 * it one.
 */

import { ASK_POLL_TIMEOUT_MS } from '@/api/queries.ts';
import type {
  AskRequestWire,
  AskSourceWire,
  AskTurnWire,
  AskWire,
  NoteCreateWire,
} from '@/api/schema.ts';

export const THREAD_KEY = 'chintan.ask.thread';

/** The contract admits at most six earlier turns as context. */
export const HISTORY_LIMIT = 6;

/**
 * What one earlier turn may carry, in code points — the server counts runes,
 * not UTF-16 units. The worker stores an answer of up to 8,000, and the
 * contract admits half of that as history and says the client is the one to
 * cut; a question is 1,000 either way (`AskTurn` in `docs/api/openapi.yaml`).
 */
export const HISTORY_ANSWER_LIMIT = 4000;
export const HISTORY_QUESTION_LIMIT = 1000;

/**
 * What the whole request may weigh. The server refuses a body over 16 KiB,
 * which six cut turns can still exceed; the budget sits under the cap by
 * enough that the JSON around the turns never tips it over.
 */
export const HISTORY_BYTES_BUDGET = 14 * 1024;

/**
 * How long after a question was sent its row is still worth polling again.
 * The worker is done, one way or the other, well inside this; a row still
 * pending after it is one nobody is coming back to, and Try again should ask
 * afresh rather than watch it for another minute.
 */
export const RESUME_WINDOW_MS = 5 * 60 * 1000;

/** `POST /v1/notes` caps the title; a question can run longer than that. */
const TITLE_LIMIT = 200;

/** Where text was cut, so the model can tell it was. One code point. */
const ELLIPSIS = '…';

const ENCODER = new TextEncoder();

/**
 * Where one question is. `asking` is the POST in flight; `pending` is the
 * worker's turn, polled; `timeout` is the client giving up on the poll, which
 * the server never reports — it may still answer, but nobody is waiting.
 */
export type TurnStatus = 'asking' | 'pending' | 'answered' | 'failed' | 'timeout';

export interface AskTurn {
  /** Client-side identity, also the request's idempotency key. */
  key: string;
  question: string;
  status: TurnStatus;
  askId: string | null;
  /**
   * When the current wait began, epoch ms — the poll's clock. Set when the
   * question is sent, again when the row exists, and again when the poll is
   * resumed, so each wait gets its whole minute.
   */
  since: number;
  /**
   * When the question was first sent, epoch ms. Bounds how long Try again
   * keeps polling the same row rather than asking afresh. Absent on a thread
   * saved before it was recorded, which is read as `since`.
   */
  sentAt?: number;
  answer: string | null;
  grounded: boolean;
  sources: AskSourceWire[];
  error: string | null;
}

/** Fixed sentences for what the client itself knows went wrong. */
export const NOT_SENT_MESSAGE = 'That question did not reach the server. Try again.';
export const LOST_MESSAGE = 'The answer could not be fetched. Try again.';
export const TIMEOUT_MESSAGE = 'This is taking too long.';

export function newTurn(key: string, question: string, now: number = Date.now()): AskTurn {
  return {
    key,
    question,
    status: 'asking',
    askId: null,
    since: now,
    sentAt: now,
    answer: null,
    grounded: false,
    sources: [],
    error: null,
  };
}

/** The turn with the server's row folded in — pending, answered or failed. */
export function applyRow(turn: AskTurn, row: AskWire): AskTurn {
  return {
    ...turn,
    askId: row.id,
    status: row.status,
    answer: row.answer,
    grounded: row.grounded,
    sources: row.sources,
    error: row.status === 'failed' ? (row.error ?? LOST_MESSAGE) : null,
  };
}

/** The turn as failed on this side, with one of the fixed sentences. */
export function failTurn(turn: AskTurn, message: string): AskTurn {
  return { ...turn, status: 'failed', error: message };
}

/** Still waiting on the server, one way or the other. */
export function isBusy(turn: AskTurn | undefined): boolean {
  return turn?.status === 'asking' || turn?.status === 'pending';
}

/**
 * Whether Try again can pick the row back up instead of asking afresh. A turn
 * the client stopped waiting for, or lost the connection to, has a row the
 * server may well have answered since — rows live a day — so it is polled
 * again, for as long as the question is young enough that the worker could
 * still be at it. A turn the server itself failed is not resumed: its row is
 * terminal, and only a new question can do better. (A failed row that arrived
 * without its sentence reads as lost here and is polled once more, which lands
 * on the same failure within a second — harmless, and outside the contract.)
 */
export function canResume(turn: AskTurn, now: number = Date.now()): boolean {
  if (turn.askId === null) return false;
  if (now - (turn.sentAt ?? turn.since) >= RESUME_WINDOW_MS) return false;
  return turn.status === 'timeout' || (turn.status === 'failed' && turn.error === LOST_MESSAGE);
}

/** The turn back in the poll, with a fresh minute on its clock. */
export function resumeTurn(turn: AskTurn, now: number = Date.now()): AskTurn {
  return { ...turn, status: 'pending', error: null, since: now };
}

/**
 * The context for the next question: the most recent answered turns, oldest
 * first, within what the contract accepts. A failed or unanswered turn
 * carries nothing worth repeating.
 *
 * The server refuses, rather than trims, a turn over its bounds or a body
 * over the cap, and "Try again" would only send the same history back — so
 * the cutting is done here, where the conversation is. Each turn is cut to
 * its limits first; then, with `question` (the one about to be asked) in the
 * body, the oldest turns go until the whole request fits. The turn a
 * follow-up most often refers to is the last one, which is what survives.
 */
export function historyFor(turns: readonly AskTurn[], question = ''): AskTurnWire[] {
  const history = turns
    .filter((turn) => turn.status === 'answered' && turn.answer !== null)
    .slice(-HISTORY_LIMIT)
    .map((turn) => ({
      question: cut(turn.question, HISTORY_QUESTION_LIMIT),
      answer: cut(turn.answer ?? '', HISTORY_ANSWER_LIMIT),
    }));
  while (history.length > 0 && bodyBytes({ question, history }) > HISTORY_BYTES_BUDGET) {
    history.shift();
  }
  return history;
}

/** `text` within `limit` code points, ending in the ellipsis where it was cut. */
function cut(text: string, limit: number): string {
  const points = Array.from(text);
  if (points.length <= limit) return text;
  return points.slice(0, limit - 1).join('') + ELLIPSIS;
}

/** The request as the client will send it, in the bytes the server counts. */
function bodyBytes(body: AskRequestWire): number {
  return ENCODER.encode(JSON.stringify(body)).length;
}

/** Every cited note, once, in the order it was first cited. */
export function sourcesOf(turns: readonly AskTurn[]): AskSourceWire[] {
  const seen = new Set<string>();
  const sources: AskSourceWire[] = [];
  for (const turn of turns) {
    for (const source of turn.sources) {
      if (seen.has(source.note_id)) continue;
      seen.add(source.note_id);
      sources.push(source);
    }
  }
  return sources;
}

/** The tag a saved thread carries, so retrieval can tell an answer from a note. */
export const ASK_NOTE_TAG = 'ask';

/**
 * The thread as a note: the first question is the title, the body is each
 * exchange as `Q:` and `A:` paragraphs, and the notes the answers drew on are
 * listed at the end so the reader can go and check. Plain text throughout —
 * a note's body is what the Text tab and the library snippet show verbatim,
 * where Markdown's asterisks and dashes are just asterisks and dashes — so
 * the bold the answers arrive with is unwrapped too. Turns without an answer
 * are left out: a failed question is not worth keeping. `null` when nothing
 * was answered, so there is nothing to save.
 *
 * Tagged `ask`, because an answer saved as a note is note text the next
 * question would otherwise retrieve as if it were a source, and one answer
 * feeding the next is a loop. The tag is what retrieval can exclude on.
 */
export function noteFromThread(turns: readonly AskTurn[]): NoteCreateWire | null {
  const answered = turns.filter((turn) => turn.status === 'answered' && turn.answer !== null);
  const first = turns[0];
  if (!first || answered.length === 0) return null;

  const exchanges = answered.map(
    (turn) => `Q: ${turn.question}\n\nA: ${plainText(turn.answer ?? '')}`,
  );
  const sources = sourcesOf(answered);
  const parts = [...exchanges];
  if (sources.length > 0) {
    parts.push(`Sources:\n${sources.map((source) => source.title).join('\n')}`);
  }
  return {
    title: first.question.slice(0, TITLE_LIMIT),
    body: parts.join('\n\n'),
    tags: [ASK_NOTE_TAG],
  };
}

/** An answer's light Markdown as text: `**bold**` unwrapped, lists left as lines. */
function plainText(answer: string): string {
  return answer.replace(/\*\*(.+?)\*\*/g, '$1');
}

/* ---------------------------------------------------------------------------
   Session storage
   --------------------------------------------------------------------------- */

function isTurn(value: unknown): value is AskTurn {
  if (typeof value !== 'object' || value === null) return false;
  const turn = value as Record<string, unknown>;
  return (
    typeof turn.key === 'string' &&
    typeof turn.question === 'string' &&
    typeof turn.status === 'string' &&
    typeof turn.since === 'number' &&
    Array.isArray(turn.sources)
  );
}

/**
 * The thread as last saved, made honest about time passed. A question whose
 * POST never came back cannot be resumed — the panel has no id to poll — so
 * it is shown failed; one still pending resumes polling if there is any wait
 * left, and is timed out if not. Storage blocked or corrupt is an empty
 * thread, never a failure.
 */
export function loadThread(now: number = Date.now()): AskTurn[] {
  try {
    const raw = sessionStorage.getItem(THREAD_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(isTurn).map((turn) => {
      if (turn.status === 'asking' || (turn.status === 'pending' && turn.askId === null)) {
        return failTurn(turn, NOT_SENT_MESSAGE);
      }
      if (turn.status === 'pending' && now - turn.since >= ASK_POLL_TIMEOUT_MS) {
        return { ...turn, status: 'timeout' as const };
      }
      return turn;
    });
  } catch {
    return [];
  }
}

export function saveThread(turns: readonly AskTurn[]): void {
  try {
    if (turns.length === 0) sessionStorage.removeItem(THREAD_KEY);
    else sessionStorage.setItem(THREAD_KEY, JSON.stringify(turns));
  } catch {
    // Quota or private mode: the thread lives for this screen only.
  }
}

/**
 * One turn rewritten in the saved thread, for an outcome that lands after
 * the panel has gone (see `useAskThread`). Read raw rather than through
 * `loadThread`, whose reading of time passed is for a panel about to show
 * the thread, not for a write-through.
 */
export function updateStoredTurn(key: string, update: (turn: AskTurn) => AskTurn): void {
  try {
    const raw = sessionStorage.getItem(THREAD_KEY);
    if (!raw) return;
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return;
    saveThread(parsed.filter(isTurn).map((turn) => (turn.key === key ? update(turn) : turn)));
  } catch {
    // Unreadable or blocked: the panel shows the question as not sent.
  }
}
