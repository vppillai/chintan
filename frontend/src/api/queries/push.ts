/** TanStack Query bindings for Web Push: the instance's key and this tenant's subscriptions. */

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useApi } from '../ApiProvider.tsx';
import { ApiError } from '../problem.ts';
import type { PushKeyWire, PushSubscribeWire } from '../schema.ts';

import { queryKeys } from './keys.ts';

/**
 * `GET /v1/push/key`, once per session: the key does not change while the
 * app is open. `null` is the contract's 404 — the owner has not made a key
 * pair — which the Notifications card explains rather than retries; any
 * other failure is an error like the rest.
 */
export function usePushKey() {
  const api = useApi();
  return useQuery<PushKeyWire | null>({
    queryKey: queryKeys.pushKey(),
    queryFn: async () => {
      try {
        return await api.getPushKey();
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null;
        throw error;
      }
    },
    staleTime: Infinity,
  });
}

/** `GET /v1/push/subscriptions`: every browser enrolled, this one included, by its id. */
export function usePushSubscriptions(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.pushSubscriptions(),
    queryFn: () => api.listPushSubscriptions(),
    enabled,
    staleTime: 5 * 60_000,
  });
}

export function useSubscribePush() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: PushSubscribeWire) => api.createPushSubscription(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.pushSubscriptions() });
    },
  });
}

export function useUnsubscribePush() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (subscriptionId: string) => api.deletePushSubscription(subscriptionId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.pushSubscriptions() });
    },
  });
}
