import { useCallback, useEffect, useRef, useState } from 'react';

import { newIdempotencyKey } from '@/api/client.ts';
import { ApiError } from '@/api/problem.ts';
import { ASK_POLL_TIMEOUT_MS, useAsk, useAskQuestion } from '@/api/queries.ts';

import {
  LOST_MESSAGE,
  NOT_SENT_MESSAGE,
  applyRow,
  canResend,
  canResume,
  failTurn,
  historyFor,
  isBusy,
  loadThread,
  newTurn,
  resendTurn,
  resumeTurn,
  saveThread,
  updateStoredTurn,
  type AskTurn,
} from './thread.ts';

/**
 * A replay that reaches the server while the original POST is still being
 * handled there is answered 409 "an identical request is still in flight";
 * the record is written the moment the original finishes, so the same
 * request a second later is the replayed 202. A few tries cover a slow
 * original; after that the question is shown as not sent, and Try again
 * sends the same request once more.
 */
const REPLAY_RETRY_MS = 1_000;
const REPLAY_RETRY_LIMIT = 5;

export interface AskThread {
  turns: readonly AskTurn[];
  /** Waiting on the server for the latest question. */
  busy: boolean;
  ask: (question: string) => void;
  /**
   * Picks a timed-out or lost question back up — polling its row again where
   * there is one to poll, sending the same request under the same key where
   * the POST never came back — or asks it again as a new turn.
   */
  retry: (key: string) => void;
  /** Ends the thread, on screen and in storage — from a panel that is gone too. */
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

  /** The keys this instance has itself sent; see the replay effect below. */
  const sent = useRef(new Set<string>());
  /**
   * A stable handle on `post`, for the replay attempt it schedules for
   * itself: a callback cannot name itself without the hooks linter reading
   * the reference as a mutation. Filled in by the effect after `post` is
   * defined; the placeholder is never reachable before then.
   */
  const postRef = useRef<(turn: AskTurn, attempt: number) => void>(() => {});

  /**
   * One send path for a question, a resend and a replay. The turn's own key
   * is the idempotency key and its `request` the body, so however many times
   * a turn is sent the server sees one question. Chained on the promise
   * rather than given as per-call callbacks, which TanStack drops once the
   * component has unmounted (see `settle`).
   */
  const post = useCallback(
    (turn: AskTurn, attempt = 0): void => {
      if (!turn.request) return;
      sent.current.add(turn.key);
      void send.mutateAsync({ body: turn.request, key: turn.key }).then(
        (created) => {
          // `since` is the moment the row exists, not when the field was
          // submitted, so a slow POST does not eat into the poll's minute.
          settle(turn.key, (current) => ({ ...applyRow(current, created), since: Date.now() }));
        },
        (error: unknown) => {
          if (error instanceof ApiError && error.isConflict && attempt < REPLAY_RETRY_LIMIT) {
            setTimeout(() => {
              postRef.current(turn, attempt + 1);
            }, REPLAY_RETRY_MS);
            return;
          }
          settle(turn.key, (current) => failTurn(current, messageFor(error, NOT_SENT_MESSAGE)));
        },
      );
    },
    [send, settle],
  );

  /*
   * A turn still `asking` that this instance did not send is one the panel
   * was remounted under — the POST belongs to a closure that has gone, and
   * whatever it settles to, this instance would not see it. The same request
   * goes out again under the same key: the server answers with the original
   * 202 if the first attempt reached it, or asks the question once if it did
   * not, and either way the turn is polled here rather than shown as never
   * sent. `sent` is what stops the effect re-sending a turn `ask` just did.
   */
  useEffect(() => {
    postRef.current = post;
  }, [post]);
  useEffect(() => {
    if (last?.status === 'asking' && !sent.current.has(last.key)) post(last);
  }, [last, post]);

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
      const history = historyFor(turns, trimmed);
      const turn: AskTurn = {
        ...newTurn(newIdempotencyKey(), trimmed),
        request: { question: trimmed, ...(history.length > 0 ? { history } : {}) },
      };
      setTurns((prev) => [...prev, turn]);
      post(turn);
    },
    [turns, post],
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
      // A question that never reached this side goes out again as itself —
      // same key, same body — so a POST that did reach the server is not
      // paid for twice. It moves to the end, where the poll is.
      if (canResend(turn)) {
        const resent = resendTurn(turn);
        setTurns((prev) => [...prev.filter((candidate) => candidate.key !== key), resent]);
        post(resent);
        return;
      }
      setTurns((prev) => prev.filter((candidate) => candidate.key !== key));
      ask(turn.question);
    },
    [turns, last, ask, post, replace],
  );

  // A Save as note that lands after the panel has gone still ends the thread
  // the panel comes back to; with no component to hold the state, storage is
  // written directly, as `settle` does.
  const clear = useCallback((): void => {
    if (mounted.current) setTurns([]);
    else saveThread([]);
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
