/** TanStack Query bindings for the tenant's settings and its usage figures. */

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useApi } from '../ApiProvider.tsx';
import type { SettingsWire } from '../schema.ts';

import { queryKeys } from './keys.ts';

/**
 * Read once for the session, as the shell's prefetch intends (round-3 T48):
 * with the client's thirty-second default the note screen fetched them again
 * on every open after the first half-minute, and again on each tab focus.
 * Never stale is safe because `useSaveSettings` writes what the server stored
 * into this cache, so this device's own changes are always what is shown; a
 * change made on another device is picked up on the next launch.
 */
export function useSettings() {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.settings(),
    queryFn: () => api.getSettings(),
    staleTime: Infinity,
  });
}

export function useSaveSettings() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: SettingsWire) => api.putSettings(body),
    // The contract returns what was *stored*, not what was sent, so the
    // response replaces the cache rather than the optimistic value.
    onSuccess: (stored) => {
      queryClient.setQueryData(queryKeys.settings(), stored);
    },
  });
}

/* ---------------------------------------------------------------------------
   Usage
   --------------------------------------------------------------------------- */

/**
 * `GET /v1/usage` for one month — the current one when none is given. Stale
 * after a minute: the counters move only when a capture finishes, and a
 * screen someone is looking at while one is filing should catch up.
 */
export function useUsage(month?: string) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.usage(month),
    queryFn: () => api.getUsage(month),
    staleTime: 60_000,
  });
}
