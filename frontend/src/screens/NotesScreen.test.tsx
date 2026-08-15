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
  window.localStorage.clear();
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

describe('archiving several notes at once, instead of one at a time from inside each one', () => {
  it('archives every selected note and leaves selection mode', async () => {
    const user = userEvent.setup();
    const archived: string[] = [];
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      if (method === 'DELETE' && url.includes('/v1/notes/')) {
        archived.push(decodeURIComponent(url.split('/v1/notes/')[1] ?? ''));
        return json({});
      }
      return json({ items: TEST_NOTES });
    });
    mount(fetchImpl);

    await user.click(await screen.findByRole('button', { name: 'Select' }));
    const checkboxes = await screen.findAllByRole('checkbox');
    expect(checkboxes).toHaveLength(TEST_NOTES.length);
    await user.click(checkboxes[0] as HTMLElement);

    expect(
      await screen.findByText((_content, el) => el?.textContent === '1 selected'),
    ).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Archive' }));
    await user.click(await screen.findByRole('button', { name: 'Archive them' }));

    await waitFor(() => {
      expect(archived).toEqual([TEST_NOTES[0]?.id]);
    });
    // Back to the plain list: the Select control reads "Select" again, not
    // "Cancel", and the checkboxes are gone.
    expect(await screen.findByRole('button', { name: 'Select' })).toBeInTheDocument();
    expect(screen.queryByRole('checkbox')).toBeNull();
  });

  it('selects and deselects everything with one control', async () => {
    const user = userEvent.setup();
    mount(vi.fn<typeof fetch>(async () => json({ items: TEST_NOTES })));

    await user.click(await screen.findByRole('button', { name: 'Select' }));
    await user.click(screen.getByRole('button', { name: 'Select all' }));
    expect(
      await screen.findByText(
        (_content, el) => el?.textContent === `${TEST_NOTES.length} selected`,
      ),
    ).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Deselect all' }));
    expect(
      await screen.findByText((_content, el) => el?.textContent === '0 selected'),
    ).toBeInTheDocument();
  });

  it('leaves the plain list untouched when not selecting', async () => {
    // The default path — a real <button> that navigates — must still be what
    // renders until Select is pressed.
    mount(vi.fn<typeof fetch>(async () => json({ items: TEST_NOTES })));
    await screen.findByText(TEST_NOTES[0]?.title ?? '');
    expect(screen.queryByRole('checkbox')).toBeNull();
    expect(screen.getAllByRole('button').some((el) => el.className.includes('note-row'))).toBe(
      true,
    );
  });
});

describe('a row shows a date by default, and a time on request', () => {
  it('adds the time once "Show time" is pressed, and can hide it again', async () => {
    const user = userEvent.setup();
    mount(vi.fn<typeof fetch>(async () => json({ items: TEST_NOTES })));

    await screen.findByText(TEST_NOTES[0]?.title ?? '');
    const dateEl = document.querySelector('time.note-row__date');
    const dateOnly = dateEl?.textContent;

    await user.click(screen.getByRole('button', { name: 'Show time' }));
    await waitFor(() => {
      expect(document.querySelector('time.note-row__date')?.textContent).not.toBe(dateOnly);
    });
    const withTime = document.querySelector('time.note-row__date')?.textContent;
    expect(await screen.findByRole('button', { name: 'Hide time' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Hide time' }));
    await waitFor(() => {
      expect(document.querySelector('time.note-row__date')?.textContent).toBe(dateOnly);
    });
    expect(document.querySelector('time.note-row__date')?.textContent).not.toBe(withTime);
  });

  it('remembers the choice across a remount, the way the theme preference does', async () => {
    const user = userEvent.setup();
    const { unmount } = mount(vi.fn<typeof fetch>(async () => json({ items: TEST_NOTES })));

    await user.click(await screen.findByRole('button', { name: 'Show time' }));
    await screen.findByRole('button', { name: 'Hide time' });
    unmount();

    mount(vi.fn<typeof fetch>(async () => json({ items: TEST_NOTES })));
    expect(await screen.findByRole('button', { name: 'Hide time' })).toBeInTheDocument();
  });
});
