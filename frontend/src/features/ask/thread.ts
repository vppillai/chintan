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
import type { AskSourceWire, AskTurnWire, AskWire, NoteCreateWire } from '@/api/schema.ts';

export const THREAD_KEY = 'chintan.ask.thread';

/** The contract admits at most six earlier turns as context. */
export const HISTORY_LIMIT = 6;

/** `POST /v1/notes` caps the title; a question can run longer than that. */
const TITLE_LIMIT = 200;

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
  /** When the question was sent, epoch ms — the poll's clock. */
  since: number;
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
 * The context for the next question: the most recent answered turns, oldest
 * first, capped at what the contract accepts. A failed or unanswered turn
 * carries nothing worth repeating.
 */
export function historyFor(turns: readonly AskTurn[]): AskTurnWire[] {
  return turns
    .filter((turn) => turn.status === 'answered' && turn.answer !== null)
    .slice(-HISTORY_LIMIT)
    .map((turn) => ({ question: turn.question, answer: turn.answer ?? '' }));
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

/**
 * The thread as a note: the first question is the title, the body is each
 * exchange in the same light Markdown the answers arrive in, and the notes
 * the answers drew on are listed at the end so the reader can go and check.
 * Turns without an answer are left out — a failed question is not worth
 * keeping. `null` when nothing was answered, so there is nothing to save.
 */
export function noteFromThread(turns: readonly AskTurn[]): NoteCreateWire | null {
  const answered = turns.filter((turn) => turn.status === 'answered' && turn.answer !== null);
  const first = turns[0];
  if (!first || answered.length === 0) return null;

  const exchanges = answered.map((turn) => `**Q: ${turn.question}**\n\n${turn.answer ?? ''}`);
  const sources = sourcesOf(answered);
  const parts = [...exchanges];
  if (sources.length > 0) {
    parts.push(`**Sources**\n${sources.map((source) => `- ${source.title}`).join('\n')}`);
  }
  return { title: first.question.slice(0, TITLE_LIMIT), body: parts.join('\n\n') };
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
