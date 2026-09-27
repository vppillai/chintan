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
export async function subscribeThisBrowser(publicKey: string): Promise<PushSubscription> {
  const registration = await navigator.serviceWorker.getRegistration();
  if (!registration) throw new Error('This app has no service worker yet; reload and try again.');
  return registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: applicationServerKey(publicKey),
  });
}
