import { expect, test } from './fixtures.ts';

/**
 * Staying enrolled (docs/design/push.md): the worker deletes a subscription
 * the push service calls gone, and nothing after that fails, so a phone goes
 * quiet. On launch the shell notices a browser that holds a subscription the
 * server no longer lists and, with permission already granted, replaces it
 * and registers the new one — no tap, no visit to You.
 *
 * Headless Chromium has no push service to subscribe against and no
 * notification permission to grant, so both are stand-ins. The stand-in
 * `pushManager` behaves as a browser's does: `subscribe` returns the
 * subscription already held until it is unsubscribed.
 */
test('a subscription the server pruned is replaced and registered on launch', async ({
  page,
  api,
}) => {
  api.pushKey = 'BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM';
  await page.addInitScript(() => {
    // Headless Chromium reads `denied` whatever was granted; the phone's
    // permission is what is being stood in for.
    Object.defineProperty(Notification, 'permission', { get: () => 'granted' });
    const make = (endpoint: string) => {
      const sub = {
        endpoint,
        expirationTime: null,
        toJSON: () => ({ endpoint, expirationTime: null, keys: { p256dh: 'BPUB', auth: 'AUTH' } }),
        unsubscribe: async () => {
          held = null;
          return true;
        },
      };
      return sub;
    };
    let held: ReturnType<typeof make> | null = make('https://fcm.googleapis.com/fcm/send/pruned');
    const pushManager = {
      getSubscription: async () => held,
      subscribe: async () => (held ??= make('https://fcm.googleapis.com/fcm/send/fresh')),
    };
    Object.defineProperty(navigator.serviceWorker, 'getRegistration', {
      value: async () => ({ pushManager }),
    });
  });

  await page.goto('/');

  await expect.poll(() => api.pushPosts.map((post) => post.endpoint)).toEqual([
    'https://fcm.googleapis.com/fcm/send/fresh',
  ]);
  expect(api.pushPosts[0]?.keys).toEqual({ p256dh: 'BPUB', auth: 'AUTH' });
});
