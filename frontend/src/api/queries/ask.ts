/** TanStack Query bindings for Ask: the question, and the poll for its answer. */

import { useMutation, useQuery } from '@tanstack/react-query';

import { useApi } from '../ApiProvider.tsx';
import type { AskRequestWire, AskWire } from '../schema.ts';

import { queryKeys } from './keys.ts';

/**
 * How often to ask after a question. The worker's answer is one model call
 * over a handful of notes — typically a few seconds — so the first ten
 * seconds are asked every second, when the answer is most likely to land;
 * after that every two, because a slow answer is being retried by the worker
 * and there is nothing to gain from asking faster than it can change. After a
 * minute the panel gives up and says so.
 */
const ASK_POLL_FAST_MS = 1_000;
const ASK_POLL_FAST_WINDOW_MS = 10_000;
const ASK_POLL_SLOW_MS = 2_000;
export const ASK_POLL_TIMEOUT_MS = 60_000;

function isAskSettled(ask: AskWire | undefined): boolean {
  return ask !== undefined && ask.status !== 'pending';
}

/**
 * The interval for the next poll, or `false` to stop: once the row has
 * settled, or once it has been pending for longer than anyone should wait.
 * `since` is when the client sent the question, not the row's `created_at`,
 * so a clock skew between device and server cannot cut the wait short.
 */
export function askPollInterval(
  ask: AskWire | undefined,
  since: number,
  now: number = Date.now(),
): number | false {
  if (isAskSettled(ask)) return false;
  const waited = now - since;
  if (waited >= ASK_POLL_TIMEOUT_MS) return false;
  return waited < ASK_POLL_FAST_WINDOW_MS ? ASK_POLL_FAST_MS : ASK_POLL_SLOW_MS;
}

/**
 * `POST /v1/ask`: the 202 with the pending row. `key` is the turn's own
 * identity reused as the idempotency key, so a retry of the same question
 * from the same turn replays the row rather than paying for a second answer.
 */
export function useAskQuestion() {
  const api = useApi();
  return useMutation({
    mutationFn: ({ body, key }: { body: AskRequestWire; key: string }) => api.ask(body, key),
  });
}

/**
 * `GET /v1/ask/{id}`, polled on the cadence above while the row is pending.
 * Nothing else reads these rows, and they expire in a day, so there is no
 * stale time to speak of: a settled row is simply never asked for again.
 */
export function useAsk(askId: string | null, since: number) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.ask(askId ?? ''),
    queryFn: () => api.getAsk(askId ?? ''),
    enabled: askId !== null,
    refetchInterval: (query) => askPollInterval(query.state.data, since),
    refetchIntervalInBackground: true,
  });
}
