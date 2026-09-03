import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createContext, useContext, useMemo, useState, type ReactNode } from 'react';

import { ApiClient } from './client.ts';
import { ChintanApi } from './endpoints.ts';
import { ApiError } from './problem.ts';
import { createSession, type Session } from './session.ts';

export interface ApiContextValue {
  api: ChintanApi;
  session: Session;
}

const ApiContext = createContext<ApiContextValue | null>(null);

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // The HTTP client already retries network failures and 5xx with
        // backoff. Retrying again here would multiply the two budgets.
        retry: false,
        staleTime: 30_000,
        // A note edited on another device should be visible the moment the
        // app returns to the foreground. The capture list opts out of this
        // (`usePendingCaptures`): it polls on its own while anything moves.
        refetchOnWindowFocus: true,
        refetchOnReconnect: true,
      },
      mutations: { retry: false },
    },
  });
}

export interface ApiProviderProps {
  children: ReactNode;
  /** Injected by tests. Production builds the real stack. */
  value?: ApiContextValue;
  queryClient?: QueryClient;
}

export function ApiProvider({ children, value, queryClient }: ApiProviderProps) {
  const [client] = useState(() => queryClient ?? createQueryClient());
  const contextValue = useMemo<ApiContextValue>(() => {
    if (value) return value;
    const session = createSession();
    return { session, api: new ChintanApi(new ApiClient(session)) };
  }, [value]);

  return (
    <QueryClientProvider client={client}>
      <ApiContext.Provider value={contextValue}>{children}</ApiContext.Provider>
    </QueryClientProvider>
  );
}

export function useApi(): ChintanApi {
  return useApiContext().api;
}

export function useSession(): Session {
  return useApiContext().session;
}

function useApiContext(): ApiContextValue {
  const context = useContext(ApiContext);
  if (!context) throw new Error('useApi must be used inside an ApiProvider');
  return context;
}

export { ApiError };
