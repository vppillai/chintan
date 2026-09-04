import { useEffect, useRef } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { useOnline } from '@/hooks/useOnline.ts';

import { canRetryUpload, type CaptureModel } from './machine.ts';
import { useCaptureStore } from './store.ts';

/**
 * Whether the recording in the machine is one the user already asked to
 * send, and only the network stopped it.
 *
 * `failed` with an `upload-failed` failure that is recoverable: the bytes
 * are on disk and the create or the PUT did not reach the server. Not a
 * recording at review — the user has not said Send — and not a spend cap,
 * which a connection does not fix.
 */
export function awaitsConnection(model: CaptureModel): boolean {
  return canRetryUpload(model) && model.failure?.kind === 'upload-failed';
}

/**
 * Sends a recording again, once, when the connection comes back.
 *
 * A recording made offline and sent failed honestly — "safe on this device",
 * with Retry — and then waited for a tap: nothing happened when the device
 * reconnected, however long it stayed online (QA D11). Edits already
 * resumed on their own through the offline queue; the one artefact that
 * exists nowhere but this device did not.
 *
 * So an offline → online transition retries the store's own `send` once
 * for a recording the user had already asked to send. The filing row shows
 * it uploading, as it does for any send. One attempt per reconnect: if that
 * fails too, the row's Retry is still there, and the next reconnect tries
 * again. A recording still at review waits for the user, as it should —
 * they have not said Send.
 */
export function useResendOnReconnect(): void {
  const api = useApi();
  const online = useOnline();
  const wasOffline = useRef(!online);

  useEffect(() => {
    if (!online) {
      wasOffline.current = true;
      return;
    }
    if (!wasOffline.current) return;
    wasOffline.current = false;

    const { model, send } = useCaptureStore.getState();
    if (awaitsConnection(model)) void send(api);
  }, [online, api]);
}
