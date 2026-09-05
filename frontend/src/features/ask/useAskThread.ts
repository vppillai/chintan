import { useCallback, useEffect, useRef, useState } from 'react';

import { newIdempotencyKey } from '@/api/client.ts';
import { ApiError } from '@/api/problem.ts';
import { ASK_POLL_TIMEOUT_MS, useAsk, useAskQuestion } from '@/api/queries.ts';

import {
  LOST_MESSAGE,
  NOT_SENT_MESSAGE,
  applyRow,
  canResume,
  failTurn,
  historyFor,
  isBusy,
  loadThread,
  newTurn,
  resumeTurn,
  saveThread,
  updateStoredTurn,
  type AskTurn,
} from './thread.ts';

export interface AskThread {
  turns: readonly AskTurn[];
  /** Waiting on the server for the latest question. */
  busy: boolean;
  ask: (question: string) => void;
  /**
   * Picks a timed-out or lost question back up — polling its row again where
   * there is one to poll — or sends it again, in place.
   */
  retry: (key: string) => void;
  clear: () => void;
}

/**
 * The conversation behind the Ask panel.
 *
 * One turn at a time is ever in flight — the follow-up field is held while
 * the last question is unanswered — so the poll is always for the last turn.
 * `POST /v1/ask` answers 202 with the row; `useAsk` then asks for it on the
 * cadence in `queries.ts` until it settles, and the settled row is folded
 * into the turn here, where it outlives the query cache (in `sessionStorage`,
 * see `thread.ts`) and becomes the history the next question is sent with.
 *
 * The settled row is applied during render, the way the cleaned panel does
 * it, so the first paint after the answer lands is the answered one — and so
 * nothing here sets state from inside an effect.
 */
export function useAskThread(): AskThread {
  const [turns, setTurns] = useState<AskTurn[]>(() => loadThread());
  const send = useAskQuestion();

  useEffect(() => {
    saveThread(turns);
  }, [turns]);

  const last = turns.at(-1);
  const polling = last?.status === 'pending' && last.askId !== null ? last : null;
  const poll = useAsk(polling?.askId ?? null, polling?.since ?? 0);

  const replace = useCallback((key: string, update: (turn: AskTurn) => AskTurn): void => {
    setTurns((prev) => prev.map((turn) => (turn.key === key ? update(turn) : turn)));
  }, []);

  /*
   * The POST's outcome can land after this screen has gone — a source chip
   * tapped while it was in flight — when setting state goes nowhere and the
   * effect that saves the thread never runs. The saved thread is what the
   * panel comes back from, so the outcome is written there directly when
   * there is no component left to hold it; left as `asking`, `loadThread`
   * would show the question as never sent, though it was answered and billed.
   */
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const settle = useCallback(
    (key: string, update: (turn: AskTurn) => AskTurn): void => {
      if (mounted.current) replace(key, update);
      else updateStoredTurn(key, update);
    },
    [replace],
  );

  /*
   * Settled? Decided from the poll as it is now. The id is checked as well as
   * the status, because the query for a previous question can still hold its
   * answered row for a render after the next question is sent.
   */
  const row = poll.data;
  if (polling && row && row.id === polling.askId && row.status !== 'pending') {
    replace(polling.key, (turn) => applyRow(turn, row));
  }
  // A poll that itself failed — the row gone (404 after its day), or the
  // connection — is the question failing, said in the client's own words.
  if (polling && poll.isError) {
    replace(polling.key, (turn) => failTurn(turn, messageFor(poll.error, LOST_MESSAGE)));
  }

  /*
   * The wait has a ceiling the poll shares (`askPollInterval` stops at the
   * same moment); this is what says so on screen. Armed from `since`, so a
   * thread restored from storage is given only what is left of its minute.
   */
  useEffect(() => {
    if (!polling) return;
    const remaining = Math.max(0, ASK_POLL_TIMEOUT_MS - (Date.now() - polling.since));
    const giveUp = setTimeout(() => {
      replace(polling.key, (turn) =>
        turn.status === 'pending' ? { ...turn, status: 'timeout' } : turn,
      );
    }, remaining);
    return () => {
      clearTimeout(giveUp);
    };
  }, [polling, replace]);

  const ask = useCallback(
    (question: string): void => {
      const trimmed = question.trim();
      if (trimmed.length === 0) return;
      const turn = newTurn(newIdempotencyKey(), trimmed);
      const history = historyFor(turns, trimmed);
      setTurns((prev) => [...prev, turn]);
      // Chained on the promise rather than given as per-call callbacks, which
      // TanStack drops once the component has unmounted (see `settle`).
      void send
        .mutateAsync({
          body: { question: trimmed, ...(history.length > 0 ? { history } : {}) },
          key: turn.key,
        })
        .then(
          (created) => {
            // `since` is the moment the row exists, not when the field was
            // submitted, so a slow POST does not eat into the poll's minute.
            settle(turn.key, (current) => ({ ...applyRow(current, created), since: Date.now() }));
          },
          (error: unknown) => {
            settle(turn.key, (current) => failTurn(current, messageFor(error, NOT_SENT_MESSAGE)));
          },
        );
    },
    [turns, send, settle],
  );

  const retry = useCallback(
    (key: string): void => {
      const turn = turns.find((candidate) => candidate.key === key);
      // One turn in flight at a time holds for a retry as for a question.
      if (!turn || isBusy(turn) || isBusy(last)) return;
      // The worker may have answered since the client gave up: the row is
      // polled again rather than a second answer paid for. Only the last turn
      // is ever polled, so an older one is asked again as a new turn.
      if (turn === last && canResume(turn)) {
        replace(key, (current) => resumeTurn(current));
        return;
      }
      setTurns((prev) => prev.filter((candidate) => candidate.key !== key));
      ask(turn.question);
    },
    [turns, last, ask, replace],
  );

  const clear = useCallback((): void => {
    setTurns([]);
  }, []);

  return { turns, busy: isBusy(last), ask, retry, clear };
}

/**
 * The server's own sentence where there is one — "daily provider spend cap
 * reached" is the server's to say — and a fixed one of ours otherwise.
 */
function messageFor(error: unknown, fallback: string): string {
  if (error instanceof ApiError && error.kind === 'http') return error.userMessage;
  return fallback;
}
