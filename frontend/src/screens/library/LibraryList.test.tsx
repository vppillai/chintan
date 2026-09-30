import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { ApiError } from '@/api/problem.ts';
import type { useNotes } from '@/api/queries.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { LibraryList } from './LibraryList.tsx';

/** The device's copy on screen because the list failed with `error`. */
function renderCached(error: unknown, refetch = vi.fn()) {
  const list = { isError: true, error, isLoading: false, isFetching: false, refetch } as unknown as ReturnType<
    typeof useNotes
  >;
  render(
    <TestProviders api={testApiContext(vi.fn<typeof fetch>())}>
      <MemoryRouter>
        <LibraryList
          id="list"
          view="active"
          tag={null}
          kind={null}
          list={list}
          online
          paused={false}
          fromCache
          showingCached
          notes={[]}
          pinned={[]}
          groups={[]}
          searching={false}
          trimmed=""
          hits={[]}
          serverPending={false}
          serverUnavailable={false}
          loadMore={() => {}}
          archived={{ count: 0, more: false }}
        />
      </MemoryRouter>
    </TestProviders>,
  );
  return refetch;
}

describe('the cached list says why it is the cached list', () => {
  it('says the server did not answer, and offers Retry, on a 5xx while online (R7-12)', async () => {
    const refetch = renderCached(new ApiError({ kind: 'http', status: 500, title: 'Internal Server Error' }));

    expect(screen.getByText(/server didn.t answer — showing notes saved on this device/i)).toBeInTheDocument();
    expect(screen.queryByText(/need a connection/i)).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(refetch).toHaveBeenCalled();
  });

  it.each([
    ['a timeout', new ApiError({ kind: 'timeout', status: 0, title: 'Timed out' })],
    ['a 429', new ApiError({ kind: 'http', status: 429, title: 'Too Many Requests' })],
  ])('says the server did not answer for %s too, not that the device is offline', (_, error) => {
    renderCached(error);

    expect(screen.getByText(/server didn.t answer/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument();
  });

  it('keeps the offline sentence for a network failure', () => {
    renderCached(new ApiError({ kind: 'network', status: 0, title: 'Network error' }));

    expect(screen.getByText(/recordings and transcripts need a connection/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull();
  });
});
