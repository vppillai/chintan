import { QueryClient } from '@tanstack/react-query';
import type { ReactNode } from 'react';

import { ApiProvider, type ApiContextValue } from '@/api/ApiProvider.tsx';
import { ApiClient } from '@/api/client.ts';
import { ChintanApi } from '@/api/endpoints.ts';
import { Session } from '@/api/session.ts';
import { createMemoryTokenStore, type TokenSet } from '@/api/tokens.ts';
import { ThemeProvider } from '@/theme/ThemeProvider.tsx';

export const TEST_TOKENS: TokenSet = {
  idToken: 'id-test',
  accessToken: 'access-test',
  refreshToken: 'refresh-test',
  expiresAt: Date.now() + 3_600_000,
  tokenType: 'Bearer',
};

/**
 * An API stack backed by a caller-supplied `fetch`.
 *
 * Tests stub at the fetch boundary rather than mocking `ChintanApi`, so the
 * client's own behaviour — bearer, idempotency, problem parsing — is exercised
 * rather than bypassed.
 */
export function testApiContext(
  fetchImpl: typeof fetch = async () =>
    new Response(JSON.stringify({ items: [] }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }),
): ApiContextValue {
  const session = new Session(createMemoryTokenStore(TEST_TOKENS), {
    async refresh(current) {
      return current;
    },
  });
  return { session, api: new ChintanApi(new ApiClient(session, 'https://api.test', fetchImpl)) };
}

export function testQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      // No retries and no background refetching: a test should fail on the
      // first wrong answer, not eventually.
      queries: { retry: false, refetchOnWindowFocus: false, refetchOnReconnect: false },
      mutations: { retry: false },
    },
  });
}

export interface ProvidersProps {
  children: ReactNode;
  api?: ApiContextValue;
  queryClient?: QueryClient;
}

/** Every provider the shell requires, in the order the app mounts them. */
export function TestProviders({ children, api, queryClient }: ProvidersProps) {
  return (
    <ThemeProvider>
      <ApiProvider
        value={api ?? testApiContext()}
        queryClient={queryClient ?? testQueryClient()}
      >
        {children}
      </ApiProvider>
    </ThemeProvider>
  );
}
