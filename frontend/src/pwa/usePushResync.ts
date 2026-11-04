import { useEffect, useRef } from 'react';

import { usePushKey, usePushSubscriptions, useSubscribePush } from '@/api/queries.ts';
import {
  currentPushSubscription,
  enrolThisBrowser,
  notificationPermission,
  pushSubscribeBody,
  pushSupport,
  pushWanted,
  setPushWanted,
} from '@/features/settings/push.ts';

/**
 * Re-enrols this browser for notifications on launch when the server has
 * lost it (docs/design/push.md, "Staying enrolled").
 *
 * The worker deletes a subscription the push service calls gone (404 or
 * 410), and a browser may replace or drop its subscription on its own; the
 * card alone noticed neither, and only if someone opened You. So once per
 * session, with permission already granted, a browser that was turned on —
 * it holds a subscription, or remembers being turned on — and is not in the
 * server's list gets a fresh subscription and registers it. Permission
 * granted means `pushManager.subscribe` needs no tap. Nothing runs, and
 * nothing is fetched, for a browser that was never turned on or has no
 * permission; the card says what is left to do.
 */
export function usePushResync(enabled: boolean): void {
  const eligible = enabled && pushSupport() === 'supported' && notificationPermission() === 'granted';
  const key = usePushKey(eligible);
  const subscriptions = usePushSubscriptions(eligible && Boolean(key.data));
  const { mutateAsync: register } = useSubscribePush();
  const tried = useRef(false);

  useEffect(() => {
    const publicKey = key.data?.public_key;
    const items = subscriptions.data?.items;
    if (tried.current || !publicKey || !items) return;
    tried.current = true;
    void (async () => {
      const held = await currentPushSubscription();
      if (!held && !pushWanted()) return;
      await enrolThisBrowser(
        publicKey,
        items.map((item) => item.id),
        (fresh) => register(pushSubscribeBody(fresh)),
      );
      setPushWanted(true);
    })().catch(() => {
      // The card reads "Not reaching this device" and its switch retries.
    });
  }, [key.data, subscriptions.data, register]);
}
