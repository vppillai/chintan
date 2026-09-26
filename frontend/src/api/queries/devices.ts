/** TanStack Query bindings for the device keys that may drop captures into the inbox. */

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { useApi } from '../ApiProvider.tsx';
import type { DeviceCreateWire } from '../schema.ts';

import { queryKeys } from './keys.ts';

/**
 * `GET /v1/devices`: the keys that may drop captures into the inbox, never
 * the keys themselves. Read by the You card and by a note's recordings, which
 * name the device a row came from; the latter asks only when a row needs it
 * (`enabled`), so a note recorded entirely in the app costs no request.
 */
export function useDevices(enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.devices(),
    queryFn: () => api.listDevices(),
    enabled,
    staleTime: 5 * 60_000,
  });
}

export function useCreateDevice() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: DeviceCreateWire) => api.createDevice(body),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.devices() });
    },
  });
}

export function useDeleteDevice() {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (deviceId: string) => api.deleteDevice(deviceId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.devices() });
    },
  });
}
