import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it } from 'vitest';

import { askPending } from '@/api/__fixtures__/pending.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NOT_SENT_MESSAGE, THREAD_KEY, loadThread } from './thread.ts';
import { useAskThread } from './useAskThread.ts';

/**
 * The panel's own behaviour is tested through the screen in
 * `AskPanel.test.tsx`. This file is for what the hook has to get right when
 * there is no screen, or a different one: an outcome that lands after the
 * library has unmounted, or after it has been mounted again.
 */

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

interface Post {
  key: string | null;
  body: string;
}

/**
 * A `POST /v1/ask` that records what it was sent and answers as the test
 * says: held until `release()`, and — through `answer` — with something
 * other than the 202 for a given attempt. Every POST's idempotency key and
 * body are kept, because the whole point of a replay is that they match.
 */
function server({
  held = false,
  answer = () => null,
}: {
  held?: boolean;
  answer?: (attempt: number) => Response | Error | null;
} = {}): { fetchImpl: typeof fetch; release: () => void; posts: Post[] } {
  let release: () => void = () => undefined;
  const gate = held
    ? new Promise<void>((resolve) => {
        release = resolve;
      })
    : Promise.resolve();
  const posts: Post[] = [];
  const fetchImpl: typeof fetch = async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/v1/ask') && init?.method === 'POST') {
      posts.push({
        key: new Headers(init.headers).get('Idempotency-Key'),
        body: String(init.body),
      });
      const special = answer(posts.length);
      if (special instanceof Error) throw special;
      if (special) return special;
      await gate;
      return json(askPending, 202);
    }
    return json({ items: [] });
  };
  return { fetchImpl, release, posts };
}

function wrapperFor(fetchImpl: typeof fetch) {
  return ({ children }: { children: ReactNode }) => (
    <TestProviders api={testApiContext(fetchImpl)}>{children}</TestProviders>
  );
}

afterEach(() => {
  sessionStorage.clear();
});

describe('a question whose 202 lands after the screen has gone', () => {
  it('still records its row in the saved thread, so it is polled — not shown as never sent — on return', async () => {
    const { fetchImpl, release } = server({ held: true });
    const wrapper = wrapperFor(fetchImpl);
    const { result, unmount } = renderHook(() => useAskThread(), { wrapper });

    act(() => {
      result.current.ask('what did I decide about the roof?');
    });
    // Read raw: `loadThread` would already call an `asking` turn never sent.
    const saved = JSON.parse(sessionStorage.getItem(THREAD_KEY) ?? '[]') as unknown[];
    expect(saved[0]).toMatchObject({ status: 'asking', askId: null });

    // A source chip tapped while the POST is in flight: the library is gone.
    unmount();
    release();

    await waitFor(() => {
      expect(sessionStorage.getItem(THREAD_KEY)).toContain(askPending.id);
    });
    const [turn] = loadThread();
    expect(turn).toMatchObject({
      status: 'pending',
      askId: askPending.id,
      question: 'what did I decide about the roof?',
    });
  });
});

describe('a question whose 202 lands after the screen has been mounted again', () => {
  /*
   * Round-1 F3 covered a panel that was gone; a panel that has come back is
   * the harder case. The POST belongs to a closure the new instance cannot
   * see, and the new instance used to read the `asking` turn from storage as
   * "did not reach the server" — then Try again minted a new key and paid
   * for the answer twice. Now the new instance sends the same request again
   * under the same key: `POST /v1/ask` is idempotent, so the server answers
   * with the original 202, and the turn is polled as it should be.
   */
  it('sends the same request under the same key, and is polled rather than shown as never sent', async () => {
    const { fetchImpl, release, posts } = server({ held: true });
    const wrapper = wrapperFor(fetchImpl);
    const first = renderHook(() => useAskThread(), { wrapper });

    act(() => {
      first.result.current.ask('what did I decide about the roof?');
    });
    await waitFor(() => {
      expect(posts).toHaveLength(1);
    });
    // Away to a note and back while the POST is still in flight.
    first.unmount();
    const second = renderHook(() => useAskThread(), { wrapper });

    // Not "never sent": the turn is still on its way, and the replay is out.
    expect(second.result.current.turns[0]).toMatchObject({ status: 'asking', error: null });
    await waitFor(() => {
      expect(posts).toHaveLength(2);
    });
    expect(posts[1]?.key).toBe(posts[0]?.key);
    expect(posts[1]?.body).toBe(posts[0]?.body);

    release();
    await waitFor(() => {
      expect(second.result.current.turns[0]?.status).toBe('pending');
    });
    expect(second.result.current.turns[0]?.askId).toBe(askPending.id);
    expect(second.result.current.turns).toHaveLength(1);
  });

  it('waits out a replay the server calls still in flight, and takes the replayed 202', async () => {
    // The original POST is still being handled when the replay arrives; the
    // idempotency claim answers 409 until the record is written. Here the
    // original never returns to anyone, and the record appears for the third
    // attempt.
    const conflict = () =>
      new Response(
        JSON.stringify({
          type: 'about:blank',
          title: 'Conflict',
          status: 409,
          detail: 'an identical request is still in flight',
        }),
        { status: 409, headers: { 'content-type': 'application/problem+json' } },
      );
    const { fetchImpl, posts } = server({
      held: true,
      answer: (attempt) => (attempt === 2 ? conflict() : attempt === 3 ? json(askPending, 202) : null),
    });
    const wrapper = wrapperFor(fetchImpl);
    const first = renderHook(() => useAskThread(), { wrapper });
    act(() => {
      first.result.current.ask('and the gutters?');
    });
    await waitFor(() => {
      expect(posts).toHaveLength(1);
    });
    first.unmount();

    const second = renderHook(() => useAskThread(), { wrapper });
    await waitFor(
      () => {
        expect(second.result.current.turns[0]?.status).toBe('pending');
      },
      { timeout: 4_000 },
    );
    expect(posts).toHaveLength(3);
    expect(new Set(posts.map((post) => post.key)).size).toBe(1);
    expect(new Set(posts.map((post) => post.body)).size).toBe(1);
  });
});

describe('Try again on a question that never reached this side', () => {
  it('sends the same request under the same key, so a POST that did arrive is not paid for twice', async () => {
    /*
     * A dropped connection after the request left: the client cannot know
     * whether the server took it. The client retries a network failure on
     * its own a few times first; once it gives up, the turn reads "did not
     * reach the server", and Try again used to ask afresh under a new key.
     */
    const { fetchImpl, posts } = server({
      answer: (attempt) => (attempt <= 4 ? new TypeError('Failed to fetch') : null),
    });
    const wrapper = wrapperFor(fetchImpl);
    const { result } = renderHook(() => useAskThread(), { wrapper });

    act(() => {
      result.current.ask('what did I decide about the roof?');
    });
    // The client's own retries (three, with backoff under three seconds) run out first.
    await waitFor(
      () => {
        expect(result.current.turns[0]?.status).toBe('failed');
      },
      { timeout: 8_000 },
    );
    expect(result.current.turns[0]?.error).toBe(NOT_SENT_MESSAGE);
    expect(posts).toHaveLength(4);
    const key = result.current.turns[0]?.key;

    act(() => {
      result.current.retry(key ?? '');
    });
    await waitFor(() => {
      expect(result.current.turns[0]?.status).toBe('pending');
    });
    // One more POST, same key, same body — not a new question.
    expect(posts).toHaveLength(5);
    expect(new Set(posts.map((post) => post.key))).toEqual(new Set([key]));
    expect(new Set(posts.map((post) => post.body)).size).toBe(1);
    expect(result.current.turns).toHaveLength(1);
    expect(result.current.turns[0]?.key).toBe(key);
  }, 10_000);
});
