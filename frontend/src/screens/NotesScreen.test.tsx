import { onlineManager } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';

import { NotesScreen } from './NotesScreen.tsx';

function mount(fetchImpl: typeof fetch) {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter>
        <NotesScreen />
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

function goOffline(): void {
  Object.defineProperty(navigator, 'onLine', { value: false, configurable: true });
  onlineManager.setOnline(false);
  window.dispatchEvent(new Event('offline'));
}

afterEach(() => {
  Object.defineProperty(navigator, 'onLine', { value: true, configurable: true });
  onlineManager.setOnline(true);
});

describe('the library never claims an empty library it cannot see', () => {
  it('says it is offline rather than "Nothing here yet"', async () => {
    /*
     * TanStack *pauses* an offline query rather than failing it, so neither
     * `isLoading` nor `isError` was ever true and the brand-new-user empty
     * state rendered — directly under a banner reading "Offline — showing saved
     * notes.". To a user with a full library walking into a tunnel, their
     * entire library appeared to have been deleted.
     */
    goOffline();
    const fetchImpl = vi.fn<typeof fetch>(async () => json({ items: TEST_NOTES }));
    mount(fetchImpl);

    expect(
      await screen.findByText(/offline and no notes are cached/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/nothing here yet/i)).toBeNull();
    expect(fetchImpl, 'a paused query must not reach the network').not.toHaveBeenCalled();
  });

  it('keeps showing the empty state for a genuinely empty library', async () => {
    mount(vi.fn<typeof fetch>(async () => json({ items: [] })));
    expect(await screen.findByText(/nothing here yet/i)).toBeInTheDocument();
    expect(screen.queryByText(/offline/i)).toBeNull();
  });
});

describe('a failed load offers a control, not a gesture the app lacks', () => {
  it('renders a Try again button that refetches', async () => {
    // The copy was "Your notes could not be loaded. Pull down to try again."
    // There is no pull-to-refresh anywhere in the codebase, and this region
    // rendered zero buttons: a 300px pull-down did nothing, and the only
    // escapes were switching tabs to force a remount or reloading.
    const user = userEvent.setup();
    let calls = 0;
    // 403 rather than 500 on purpose: the client retries 5xx with backoff, and
    // this test is about the button, not the client's retry policy.
    const fetchImpl = vi.fn<typeof fetch>(async () => {
      calls += 1;
      return json({ type: 'about:blank', title: 'Not permitted', status: 403 }, 403);
    });

    mount(fetchImpl);

    const retry = await screen.findByRole('button', { name: /try again/i });
    const before = calls;
    await user.click(retry);

    await waitFor(() => {
      expect(calls).toBeGreaterThan(before);
    });
  });

  it('surfaces the problem detail so a 401 reads as "sign in again"', async () => {
    mount(
      vi.fn<typeof fetch>(async () =>
        new Response(
          JSON.stringify({
            type: 'about:blank',
            title: 'Your session has expired',
            status: 401,
            detail: 'Sign in again to see your notes.',
          }),
          { status: 401, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    );

    expect(await screen.findByText('Sign in again to see your notes.')).toBeInTheDocument();
  });
});
