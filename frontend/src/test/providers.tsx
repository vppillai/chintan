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
/** A small corpus so shell tests have something real-shaped to render. */
export const TEST_NOTES = [
  {
    id: 'roof-repair',
    title: 'Roof repair',
    snippet: 'Ridge tiles on the south slope have slipped. Two quotes before the rain.',
    updated_at: '2026-08-06T09:14:00.000Z',
    version: 3,
    archived: false,
    tags: ['house'],
  },
  {
    id: 'reading-list',
    title: 'Reading list',
    snippet: 'Seeing Like a State, then the Vitruvius translation from the walk.',
    updated_at: '2026-08-04T18:02:00.000Z',
    version: 11,
    archived: false,
    tags: ['books'],
  },
];

export function defaultTestFetch(): typeof fetch {
  return async (input) => {
    const url = new URL(String(input));
    // One note, as `GET /v1/notes/{id}` answers: the row plus a body. The
    // list envelope used to be returned here too, and the note screen threw
    // the moment it rendered against it.
    const detail = /\/v1\/notes\/([^/]+)$/.exec(url.pathname);
    const note = detail ? TEST_NOTES.find((item) => item.id === detail[1]) : undefined;
    const body = note
      ? { ...note, body: note.snippet, captures: [] }
      : url.pathname.endsWith('/v1/notes')
        ? { items: TEST_NOTES }
        : url.pathname.endsWith('/v1/usage')
          ? { month: '2026-09', cost_micros: 0, calls: 0, ops: {}, days: [] }
          : { items: [] };
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    });
  };
}

export function testApiContext(
  fetchImpl: typeof fetch = defaultTestFetch(),
  /** Pass `null` for a signed-out app, which is what the auth gate renders. */
  tokens: TokenSet | null = TEST_TOKENS,
): ApiContextValue {
  const session = new Session(createMemoryTokenStore(tokens), {
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
