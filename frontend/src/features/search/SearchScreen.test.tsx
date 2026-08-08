import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { TEST_NOTES, TestProviders, testApiContext } from '@/test/providers.tsx';

import { SearchScreen } from './SearchScreen.tsx';

function mount(fetchImpl: typeof fetch, query = 'roof') {
  return render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter initialEntries={[`/search?q=${query}`]}>
        <SearchScreen />
      </MemoryRouter>
    </TestProviders>,
  );
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

/** Online, but the API is unreachable: a captive portal, or a dead gateway. */
function serverDown(): typeof fetch {
  return vi.fn<typeof fetch>(async (input) => {
    const url = String(input);
    if (url.includes('/v1/search')) throw new TypeError('Failed to fetch');
    return json({ items: TEST_NOTES });
  });
}

describe('a search that never ran does not say the note does not exist', () => {
  it('says the server search failed, even with nothing cached to show', async () => {
    /*
     * `usingCachedOnly` required `merged.length > 0`, so the one case where the
     * user most needs to know — the API dead while `navigator.onLine` is still
     * true, and nothing cached matching — showed a bare "Nothing matches …" and
     * no notice at all. The user was told authoritatively that a note they own
     * does not exist.
     */
    mount(serverDown(), 'chimney');

    // The client retries a network failure three times with jittered backoff,
    // so the notice is up to ~3s away. That delay is the app's, not the test's.
    expect(
      await screen.findByText(/server search did not respond/i, undefined, { timeout: 8_000 }),
    ).toBeInTheDocument();
  });

  it('still says so when cached results did come back', async () => {
    mount(serverDown(), 'roof');

    // The client retries a network failure three times with jittered backoff,
    // so the notice is up to ~3s away. That delay is the app's, not the test's.
    expect(
      await screen.findByText(/server search did not respond/i, undefined, { timeout: 8_000 }),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /roof repair/i })).toBeInTheDocument();
  });

  it('says nothing when the server answered', async () => {
    mount(
      vi.fn<typeof fetch>(async (input) =>
        json(String(input).includes('/v1/search') ? { items: [] } : { items: TEST_NOTES }),
      ),
      'chimney',
    );

    expect(await screen.findByText(/nothing matches/i)).toBeInTheDocument();
    expect(screen.queryByText(/did not respond/i)).toBeNull();
  });
});
