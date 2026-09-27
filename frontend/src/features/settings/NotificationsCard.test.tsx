import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { webcrypto } from 'node:crypto';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { pushKey, pushSubscriptionCreated } from '@/api/__fixtures__/responses.ts';
import type { PushSubscriptionWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { NotificationsCard } from './NotificationsCard.tsx';
import { applicationServerKey, browserLabel, pushSubscriptionId } from './push.ts';

const ENDPOINT = 'https://web.push.apple.com/send/this-browser';

/** A browser subscription as `pushManager` hands one out, `toJSON` and `unsubscribe` included. */
function fakeSubscription(endpoint = ENDPOINT) {
  return {
    endpoint,
    expirationTime: null,
    toJSON: () => ({ endpoint, expirationTime: null, keys: { p256dh: 'BPUB', auth: 'AUTH' } }),
    unsubscribe: vi.fn(async () => true),
  } as unknown as PushSubscription;
}

/**
 * The Push, Notification and service-worker APIs jsdom lacks, set by hand
 * and removed after each test so a state does not leak into the next.
 */
function installPushApis(options: {
  permission?: NotificationPermission;
  grant?: NotificationPermission;
  existing?: PushSubscription | null;
  userAgent?: string;
} = {}) {
  const requestPermission = vi.fn(async () => options.grant ?? 'granted');
  const subscribe = vi.fn(async () => fakeSubscription());
  const getSubscription = vi.fn(async () => options.existing ?? null);
  const globals = globalThis as unknown as Record<string, unknown>;
  globals['PushManager'] = class PushManager {};
  globals['Notification'] = { permission: options.permission ?? 'default', requestPermission };
  Object.defineProperty(navigator, 'serviceWorker', {
    value: { getRegistration: async () => ({ pushManager: { getSubscription, subscribe } }) },
    configurable: true,
  });
  if (options.userAgent) {
    Object.defineProperty(navigator, 'userAgent', { value: options.userAgent, configurable: true });
  }
  // jsdom's `crypto` has no `subtle`; the id is a SHA-256.
  vi.stubGlobal('crypto', webcrypto);
  return { requestPermission, subscribe, getSubscription };
}

function uninstallPushApis() {
  const globals = globalThis as unknown as Record<string, unknown>;
  delete globals['PushManager'];
  delete globals['Notification'];
  Reflect.deleteProperty(navigator, 'serviceWorker');
  Reflect.deleteProperty(navigator, 'userAgent');
  vi.unstubAllGlobals();
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'content-type': status >= 400 ? 'application/problem+json' : 'application/json',
    },
  });
}

/** A stub API: the key (or its 404), the list, a POST that adds a row, a DELETE that drops it. */
function mount(options: { configured?: boolean; enrolled?: PushSubscriptionWire[] } = {}) {
  let items = [...(options.enrolled ?? [])];
  const calls: { method: string; path: string; body?: unknown }[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    calls.push({
      method,
      path: url.pathname,
      ...(typeof init?.body === 'string' ? { body: JSON.parse(init.body) as unknown } : {}),
    });
    if (url.pathname === '/v1/push/key') {
      return options.configured === false
        ? json(
            {
              type: 'about:blank',
              title: 'Not Found',
              status: 404,
              detail: 'notifications are not configured on this instance',
            },
            404,
          )
        : json(pushKey);
    }
    if (url.pathname === '/v1/push/subscriptions' && method === 'POST') {
      const body = JSON.parse(String(init?.body)) as { endpoint: string; label: string };
      const created: PushSubscriptionWire = {
        ...pushSubscriptionCreated,
        id: await pushSubscriptionId(body.endpoint),
        label: body.label,
      };
      items.push(created);
      return json(created, 201);
    }
    const id = /\/v1\/push\/subscriptions\/([^/]+)$/.exec(url.pathname)?.[1];
    if (id && method === 'DELETE') {
      items = items.filter((item) => item.id !== id);
      return new Response(null, { status: 204 });
    }
    if (url.pathname === '/v1/push/subscriptions') return json({ items });
    return json({ items: [] });
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <NotificationsCard />
    </TestProviders>,
  );
  return { calls, items: () => items };
}

afterEach(uninstallPushApis);

describe('the notifications card on You', () => {
  it('says the instance is not set up when the key answers 404, whatever the browser can do', async () => {
    installPushApis();
    mount({ configured: false });
    const card = await screen.findByRole('region', { name: 'Notifications' });
    await waitFor(() => {
      expect(card).toHaveTextContent(/not set up on this instance yet/i);
    });
    expect(card).toHaveTextContent('scripts/vapid-keys.sh');
    expect(screen.queryByRole('switch')).toBeNull();
  });

  it('tells an iPhone outside the installed app to add it to the Home Screen first', async () => {
    installPushApis({
      userAgent:
        'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
    });
    // Safari in the browser proper has no PushManager; only the installed app does.
    delete (globalThis as unknown as Record<string, unknown>)['PushManager'];
    mount();
    const card = await screen.findByRole('region', { name: 'Notifications' });
    await waitFor(() => {
      expect(card).toHaveTextContent(/add .* to your home screen first/i);
    });
    expect(screen.queryByRole('switch')).toBeNull();
  });

  it('says notifications are blocked in the browser settings and holds the switch off', async () => {
    installPushApis({ permission: 'denied' });
    mount();
    const toggle = await screen.findByRole('switch', { name: /notify me when a recording files/i });
    await waitFor(() => {
      expect(toggle).toBeDisabled();
    });
    expect(toggle).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByRole('region', { name: 'Notifications' })).toHaveTextContent(
      /blocked in the browser settings/i,
    );
  });

  it('asks permission, subscribes with the key and registers the browser when turned on', async () => {
    const apis = installPushApis({ grant: 'granted' });
    const { calls } = mount();
    const user = userEvent.setup();

    const toggle = await screen.findByRole('switch', { name: /notify me when a recording files/i });
    await waitFor(() => {
      expect(toggle).toBeEnabled();
    });
    expect(toggle).toHaveAttribute('aria-checked', 'false');
    expect(screen.getByRole('region', { name: 'Notifications' })).toHaveTextContent(
      /no device is enrolled yet/i,
    );

    await user.click(toggle);

    await waitFor(() => {
      expect(toggle).toHaveAttribute('aria-checked', 'true');
    });
    expect(apis.requestPermission).toHaveBeenCalledTimes(1);
    // The instance's key, as the raw P-256 point `applicationServerKey` takes.
    const [subscribeOptions] = apis.subscribe.mock.calls[0] as unknown as [PushSubscriptionOptionsInit];
    expect(subscribeOptions.userVisibleOnly).toBe(true);
    expect(subscribeOptions.applicationServerKey).toEqual(applicationServerKey(pushKey.public_key));
    // The browser's own object went over the wire, with the label.
    const post = calls.find((call) => call.method === 'POST');
    expect(post?.body).toEqual({
      endpoint: ENDPOINT,
      expirationTime: null,
      keys: { p256dh: 'BPUB', auth: 'AUTH' },
      label: browserLabel(),
    });
    expect(screen.getByRole('region', { name: 'Notifications' })).toHaveTextContent(
      /reach 1 device, this one included/i,
    );
  });

  it('leaves the switch off and says nothing was registered when permission is refused', async () => {
    const apis = installPushApis({ grant: 'denied' });
    const { calls } = mount();
    const user = userEvent.setup();
    const toggle = await screen.findByRole('switch', { name: /notify me when a recording files/i });
    await waitFor(() => {
      expect(toggle).toBeEnabled();
    });
    await user.click(toggle);
    await waitFor(() => {
      expect(toggle).toBeDisabled();
    });
    expect(toggle).toHaveAttribute('aria-checked', 'false');
    expect(apis.subscribe).not.toHaveBeenCalled();
    expect(calls.some((call) => call.method === 'POST')).toBe(false);
    expect(screen.getByRole('region', { name: 'Notifications' })).toHaveTextContent(
      /blocked in the browser settings/i,
    );
  });

  it('removes the row and unsubscribes the browser when turned off', async () => {
    const existing = fakeSubscription();
    const id = await pushSubscriptionId(ENDPOINT);
    installPushApis({ permission: 'granted', existing });
    const { calls, items } = mount({
      enrolled: [{ ...pushSubscriptionCreated, id, label: 'This browser' }],
    });
    const user = userEvent.setup();

    const toggle = await screen.findByRole('switch', { name: /notify me when a recording files/i });
    await waitFor(() => {
      expect(toggle).toHaveAttribute('aria-checked', 'true');
    });
    await user.click(toggle);
    await waitFor(() => {
      expect(toggle).toHaveAttribute('aria-checked', 'false');
    });
    expect(calls.some((call) => call.method === 'DELETE' && call.path.endsWith(`/${id}`))).toBe(true);
    expect(existing.unsubscribe).toHaveBeenCalledTimes(1);
    expect(items()).toHaveLength(0);
  });

  it('reads off when the browser holds a subscription the server no longer lists', async () => {
    // The worker pruned the row after a 410; the switch says what the
    // server will do, which is nothing.
    installPushApis({ permission: 'granted', existing: fakeSubscription() });
    mount({ enrolled: [] });
    const toggle = await screen.findByRole('switch', { name: /notify me when a recording files/i });
    await waitFor(() => {
      expect(toggle).toBeEnabled();
    });
    expect(toggle).toHaveAttribute('aria-checked', 'false');
  });
});

describe('the push helpers', () => {
  it('names the browser and the platform, never a version', () => {
    expect(
      browserLabel(
        'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1',
      ),
    ).toBe('Safari on iPhone');
    expect(
      browserLabel(
        'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36',
      ),
    ).toBe('Chrome on Android');
    expect(browserLabel('')).toBe('A browser on this device');
  });

  it('decodes the base64url key to the 65-byte P-256 point', () => {
    const bytes = applicationServerKey(pushKey.public_key);
    expect(bytes).toHaveLength(65);
    expect(bytes[0]).toBe(0x04);
  });

  it('computes the id the server files an endpoint under', async () => {
    vi.stubGlobal('crypto', webcrypto);
    // SHA-256("https://push.example/a") — the first sixteen hex characters.
    const id = await pushSubscriptionId('https://push.example/a');
    expect(id).toMatch(/^[0-9a-f]{16}$/);
    expect(await pushSubscriptionId('https://push.example/b')).not.toBe(id);
  });
});
