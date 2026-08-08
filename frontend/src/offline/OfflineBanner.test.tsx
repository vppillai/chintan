import { onlineManager } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { act } from 'react';
import { afterEach, describe, expect, it } from 'vitest';

import { queryKeys } from '@/api/queries.ts';
import { TEST_NOTES, TestProviders, testQueryClient } from '@/test/providers.tsx';

import { OfflineBanner } from './OfflineBanner.tsx';

function goOffline(): void {
  Object.defineProperty(navigator, 'onLine', { value: false, configurable: true });
  onlineManager.setOnline(false);
  window.dispatchEvent(new Event('offline'));
}

afterEach(() => {
  Object.defineProperty(navigator, 'onLine', { value: true, configurable: true });
  onlineManager.setOnline(true);
});

describe('the offline banner only claims data that is really there', () => {
  it('does not say "showing saved notes" on an offline cold start', async () => {
    /*
     * There is no query persister, so an offline cold start has an empty cache.
     * The banner said "Offline — showing saved notes." unconditionally, which
     * on the notes screen put it directly above "You are offline and no notes
     * are cached on this device yet." Its own doc comment criticises v1 for
     * presenting stale data as live data; this presented absent data as saved
     * data.
     */
    goOffline();
    render(
      <TestProviders queryClient={testQueryClient()}>
        <OfflineBanner />
      </TestProviders>,
    );

    expect(await screen.findByText(/offline/i)).toBeInTheDocument();
    expect(screen.queryByText(/showing saved notes/i)).toBeNull();
    expect(screen.getByText(/nothing is saved on this device/i)).toBeInTheDocument();
  });

  it('says it is showing saved notes once the library really is cached', async () => {
    goOffline();
    const queryClient = testQueryClient();
    queryClient.setQueryData(queryKeys.notes({ state: 'active' }), {
      pages: [{ items: TEST_NOTES }],
      pageParams: [undefined],
    });

    render(
      <TestProviders queryClient={queryClient}>
        <OfflineBanner />
      </TestProviders>,
    );

    expect(await screen.findByText(/showing saved notes/i)).toBeInTheDocument();
  });

  it('updates when the library arrives while the banner is on screen', async () => {
    goOffline();
    const queryClient = testQueryClient();
    render(
      <TestProviders queryClient={queryClient}>
        <OfflineBanner />
      </TestProviders>,
    );

    expect(await screen.findByText(/nothing is saved on this device/i)).toBeInTheDocument();

    act(() => {
      queryClient.setQueryData(queryKeys.notes({ state: 'active' }), {
        pages: [{ items: TEST_NOTES }],
        pageParams: [undefined],
      });
    });

    await waitFor(() => {
      expect(screen.getByText(/showing saved notes/i)).toBeInTheDocument();
    });
  });
});
