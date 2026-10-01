import { useEffect, useId, useState } from 'react';

import { ApiError } from '@/api/problem.ts';
import { usePushKey, usePushSubscriptions, useSubscribePush, useUnsubscribePush } from '@/api/queries.ts';
import type { PushSubscribeWire } from '@/api/schema.ts';
import { config } from '@/config/env.ts';

import { SettingsCard, SettingsRow } from './SettingsCard.tsx';
import {
  browserLabel,
  currentPushSubscription,
  notificationPermission,
  pushSubscriptionId,
  pushSupport,
  subscribeThisBrowser,
} from './push.ts';

/** The one sentence for anything that did not go through; the server's own words when it has them. */
function failureText(error: unknown): string {
  if (error instanceof ApiError) return error.userMessage;
  return 'That did not go through. Try again.';
}

/**
 * "Notifications", on You (R5-RC-D1/D2, docs/design/push.md).
 *
 * The worker knows the moment a recording files; Web Push is how that reaches
 * a phone in a pocket while a ring records. This card holds the one switch:
 * on asks the browser's permission — which must follow a tap, so the switch
 * is where the question is asked — subscribes this browser with the
 * instance's key and registers the subscription; off unsubscribes and
 * removes it. The switch reads on only when this browser holds a
 * subscription AND the server lists it: a row the worker pruned after a 410
 * reads off, which is the truth.
 *
 * Four states say why the switch is not there. Not set up on this instance
 * (the key answers 404) comes first, because on an instance without a key
 * nothing else about the browser matters; then iOS outside an installed app,
 * where the sentence is the fix; then a browser without the API; then
 * permission blocked, which only the browser's own settings undo.
 */
export function NotificationsCard() {
  const key = usePushKey();
  const configured = key.data !== null && key.data !== undefined;
  const support = pushSupport();
  const subscriptions = usePushSubscriptions(configured && support === 'supported');
  const subscribe = useSubscribePush();
  const unsubscribe = useUnsubscribePush();
  const labelId = useId();

  /** This browser's subscription and its server id; `undefined` until read. */
  const [local, setLocal] = useState<PushSubscription | null | undefined>(undefined);
  const [localId, setLocalId] = useState<string | null>(null);
  const [permission, setPermission] = useState<NotificationPermission>(() => notificationPermission());
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  useEffect(() => {
    if (support !== 'supported') return;
    let cancelled = false;
    currentPushSubscription()
      .then(async (subscription) => {
        const id = subscription ? await pushSubscriptionId(subscription.endpoint) : null;
        if (cancelled) return;
        setLocal(subscription);
        setLocalId(id);
      })
      .catch(() => {
        if (!cancelled) setLocal(null);
      });
    return () => {
      cancelled = true;
    };
  }, [support]);

  const enrolled = subscriptions.data?.items ?? [];
  const on = local !== null && local !== undefined && localId !== null && enrolled.some((item) => item.id === localId);
  const ready = local !== undefined && !subscriptions.isLoading;

  const turnOn = async (): Promise<void> => {
    if (!key.data) return;
    const granted = await Notification.requestPermission();
    setPermission(granted);
    if (granted !== 'granted') return;
    const subscription = await subscribeThisBrowser(key.data.public_key);
    const json = subscription.toJSON();
    const body: PushSubscribeWire = {
      endpoint: subscription.endpoint,
      expirationTime: subscription.expirationTime,
      keys: { p256dh: json.keys?.['p256dh'] ?? '', auth: json.keys?.['auth'] ?? '' },
      label: browserLabel(),
    };
    await subscribe.mutateAsync(body);
    setLocal(subscription);
    setLocalId(await pushSubscriptionId(subscription.endpoint));
  };

  const turnOff = async (): Promise<void> => {
    // The server's row goes first: a browser still subscribed to a row the
    // worker no longer has gets nothing, while a row without a browser
    // would be sent to until the push service says 410.
    if (localId) {
      try {
        await unsubscribe.mutateAsync(localId);
      } catch (error) {
        if (!(error instanceof ApiError && error.status === 404)) throw error;
      }
    }
    if (local) await local.unsubscribe();
    setLocal(null);
    setLocalId(null);
  };

  const toggle = (): void => {
    setBusy(true);
    setProblem(null);
    (on ? turnOff() : turnOn())
      .catch((error: unknown) => {
        setProblem(failureText(error));
      })
      .finally(() => {
        setBusy(false);
      });
  };

  return (
    <SettingsCard
      title="Notifications"
      lead="A note on this device when a recording has been filed, even with the app closed."
      foot={
        configured && support === 'supported' ? (
          <p>
            {enrolled.length === 0
              ? 'No device is enrolled yet. '
              : `Notifications reach ${String(enrolled.length)} ${enrolled.length === 1 ? 'device' : 'devices'}${on ? ', this one included' : ''}. `}
            Only the note&rsquo;s title is sent, never what was said.
          </p>
        ) : undefined
      }
    >
      {key.isLoading ? (
        <p className="you-card__note">Checking whether notifications are set up…</p>
      ) : key.isError ? (
        <p className="you-card__note" role="alert">
          Couldn&rsquo;t check whether notifications are set up.{' '}
          <button
            type="button"
            className="text-link"
            onClick={() => {
              void key.refetch();
            }}
          >
            Try again
          </button>
        </p>
      ) : !configured ? (
        <p className="you-card__note" role="note">
          Not set up on this instance yet. The owner installs the key pair with{' '}
          <code>scripts/vapid-keys.sh --apply</code> (setup does it unless <code>web_push</code> is
          off); the switch appears here after the next deploy.
        </p>
      ) : support === 'ios-not-installed' ? (
        <p className="you-card__note" role="note">
          Add {config.appName} to your Home Screen first. iOS shows notifications only for an
          installed app (16.4 or later).
        </p>
      ) : support === 'unsupported' ? (
        <p className="you-card__note" role="note">
          This browser cannot show notifications from {config.appName}.
        </p>
      ) : (
        <>
          <SettingsRow
            label="Notify me when a recording files"
            hint={
              permission === 'denied'
                ? 'Blocked in the browser settings. Allow notifications for this site there, then turn this on.'
                : 'Also when one needs a note chosen, or did not finish'
            }
            labelId={labelId}
          >
            <button
              type="button"
              role="switch"
              aria-checked={on}
              aria-labelledby={labelId}
              aria-busy={busy || undefined}
              className="switch"
              disabled={!ready || busy || permission === 'denied'}
              onClick={toggle}
            >
              <span className="switch__track" aria-hidden="true">
                <span className="switch__thumb" />
              </span>
            </button>
          </SettingsRow>
          {problem && (
            <p className="you-card__note" role="alert">
              {problem}
            </p>
          )}
        </>
      )}
    </SettingsCard>
  );
}
