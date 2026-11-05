import type { PushSubscribeWire } from '@/api/schema.ts';

/**
 * The browser half of Web Push (docs/design/push.md): whether this browser
 * can subscribe at all, the subscription it holds, and the id the server
 * files that subscription under. Pure where it can be, so the card's states
 * are testable without a service worker.
 */

/**
 * `supported` needs the Push API, the Notification API and a service worker
 * container. iOS has all three only inside an app installed to the Home
 * Screen (16.4 or later), so a browser that lacks them on an iPhone or iPad
 * is told that rather than "cannot".
 */
export type PushSupport = 'supported' | 'unsupported' | 'ios-not-installed';

export function pushSupport(
  win: Window | undefined = globalThis.window,
  nav: Navigator | undefined = globalThis.navigator,
): PushSupport {
  if (!win || !nav) return 'unsupported';
  if ('PushManager' in win && 'Notification' in win && 'serviceWorker' in nav) return 'supported';
  return isIOS(nav) ? 'ios-not-installed' : 'unsupported';
}

/** iPadOS calls itself a Mac; the touch points give it away. */
function isIOS(nav: Navigator): boolean {
  return /iPhone|iPad|iPod/.test(nav.userAgent) || (nav.platform === 'MacIntel' && nav.maxTouchPoints > 1);
}

/** The permission as the browser has it, or `default` where there is no Notification API to ask. */
export function notificationPermission(win: Window | undefined = globalThis.window): NotificationPermission {
  if (!win || !('Notification' in win)) return 'default';
  return (win as Window & typeof globalThis).Notification.permission;
}

/**
 * The server's id for an endpoint — the first sixteen hex characters of its
 * SHA-256, exactly as `service.PushSubscriptionID` computes it — so this
 * browser can find and remove its own row without reading the list's
 * endpoints, which the list never carries.
 */
export async function pushSubscriptionId(endpoint: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(endpoint));
  return Array.from(new Uint8Array(digest).slice(0, 8), (byte) => byte.toString(16).padStart(2, '0')).join('');
}

/** `applicationServerKey` takes the raw P-256 point; the API hands it out base64url. */
export function applicationServerKey(base64url: string): Uint8Array<ArrayBuffer> {
  const padded = base64url.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(base64url.length / 4) * 4, '=');
  const raw = atob(padded);
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i += 1) bytes[i] = raw.charCodeAt(i);
  return bytes;
}

/**
 * What this browser calls itself, for the Notifications card's count and
 * the operator's list: "Safari on iPhone", "Chrome on Android". A family
 * and a platform, never a version — it is a label, not an identity.
 */
export function browserLabel(ua: string = globalThis.navigator?.userAgent ?? ''): string {
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /Firefox\//.test(ua)
      ? 'Firefox'
      : /Chrome\//.test(ua) && !/Chromium/.test(ua)
        ? 'Chrome'
        : /Safari\//.test(ua)
          ? 'Safari'
          : 'A browser';
  const platform = /iPhone/.test(ua)
    ? 'iPhone'
    : /iPad/.test(ua)
      ? 'iPad'
      : /Android/.test(ua)
        ? 'Android'
        : /Mac OS X/.test(ua)
          ? 'Mac'
          : /Windows/.test(ua)
            ? 'Windows'
            : /Linux/.test(ua)
              ? 'Linux'
              : 'this device';
  return `${browser} on ${platform}`;
}

/**
 * The subscription this browser holds, or null. `getRegistration` rather than
 * `ready`: the dev server registers no worker, and `ready` would wait for
 * one for ever.
 */
export async function currentPushSubscription(): Promise<PushSubscription | null> {
  const registration = await navigator.serviceWorker.getRegistration();
  return registration ? registration.pushManager.getSubscription() : null;
}

/** Subscribes this browser with the instance's key. The same key again returns the subscription it already has. */
async function subscribeThisBrowser(publicKey: string): Promise<PushSubscription> {
  const registration = await navigator.serviceWorker.getRegistration();
  if (!registration) throw new Error('This app has no service worker yet; reload and try again.');
  return registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: applicationServerKey(publicKey),
  });
}

/**
 * Whether the person turned notifications on in this browser and has not
 * turned them off: remembered per device ('1' on, '0' off), so a
 * subscription the browser or the push service dropped is noticed even when
 * nothing of it is left here. A browser with no record but with permission
 * granted counts as on: only the card's switch asks for permission, so it
 * was turned on here, before this record existed.
 */
export const PUSH_WANTED_KEY = 'chintan.push.wanted';

export function pushWanted(): boolean {
  try {
    const wanted = localStorage.getItem(PUSH_WANTED_KEY);
    if (wanted !== null) return wanted === '1';
    return notificationPermission() === 'granted';
  } catch {
    // Storage blocked: only a subscription still held says it was wanted.
    return false;
  }
}

export function setPushWanted(wanted: boolean): void {
  try {
    localStorage.setItem(PUSH_WANTED_KEY, wanted ? '1' : '0');
  } catch {
    // Quota or private mode; the held subscription still counts as wanted.
  }
}

/** The POST body for a subscription: the browser's own object plus its label. */
export function pushSubscribeBody(subscription: PushSubscription): PushSubscribeWire {
  const json = subscription.toJSON();
  return {
    endpoint: subscription.endpoint,
    expirationTime: subscription.expirationTime,
    keys: { p256dh: json.keys?.['p256dh'] ?? '', auth: json.keys?.['auth'] ?? '' },
    label: browserLabel(),
  };
}

/**
 * Makes this browser's subscription one the server lists, and returns it.
 *
 * A subscription the browser holds but the server does not list is one the
 * worker pruned because the push service answered 404 or 410 — or one whose
 * POST never landed. Either way it is not re-registered as it is:
 * `pushManager.subscribe` hands back the subscription already held, so a
 * second POST of a dead endpoint would be pruned again on the next send.
 * It is dropped, and a fresh one is asked for and registered.
 *
 * The list is read here, not handed in: "not listed" must come from a list
 * the server just gave. A failed read throws before anything is dropped —
 * an empty list standing in for a failed one would unsubscribe a working
 * subscription, and offline nothing would replace it. The read and what
 * follows hold a lock (Web Locks, where the browser has them), so the card
 * and the launch check, or two tabs, cannot each drop the other's fresh
 * subscription as unlisted.
 */
export async function enrolThisBrowser(
  publicKey: string,
  enrolledIds: () => Promise<readonly string[]>,
  register: (subscription: PushSubscription) => Promise<unknown>,
): Promise<PushSubscription> {
  const enrol = async (): Promise<PushSubscription> => {
    const ids = await enrolledIds();
    const held = await currentPushSubscription();
    if (held && ids.includes(await pushSubscriptionId(held.endpoint))) return held;
    if (held) await held.unsubscribe();
    const fresh = await subscribeThisBrowser(publicKey);
    await register(fresh);
    return fresh;
  };
  const locks = (globalThis.navigator as Navigator | undefined)?.locks;
  return locks ? locks.request('chintan-push-enrol', enrol) : enrol();
}
