import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';

import { deviceCreated, devicesPage } from '@/api/__fixtures__/pending.ts';
import { DEFAULT_TIMEOUT_MS } from '@/api/client.ts';
import type { DeviceWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { DevicesCard, MAX_DEVICES, UNCONFIRMED_TEXT, curlRecipe, inboxAudioUrl } from './DevicesCard.tsx';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      'content-type': status >= 400 ? 'application/problem+json' : 'application/json',
    },
  });
}

/**
 * A small stateful server: the list, a POST that mints a key and adds the
 * row (without the key, as the real list answers), a DELETE that drops it.
 */
function mount(
  initial: readonly DeviceWire[] = devicesPage.items,
  overrides: { create?: (init?: RequestInit) => Response | Promise<Response> } = {},
) {
  let items = [...initial];
  const calls: { method: string; path: string; body?: unknown }[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? 'GET';
    calls.push({
      method,
      path: url.pathname,
      ...(typeof init?.body === 'string' ? { body: JSON.parse(init.body) as unknown } : {}),
    });
    if (url.pathname === '/v1/devices' && method === 'POST') {
      if (overrides.create) return overrides.create(init);
      const body = JSON.parse(String(init?.body)) as { name: string };
      const created: DeviceWire = {
        ...deviceCreated,
        id: `dev_${String(items.length + 1)}`,
        name: body.name,
      };
      const { key: _key, ...listed } = created;
      items.push(listed);
      return json(created, 201);
    }
    const id = /\/v1\/devices\/([^/]+)$/.exec(url.pathname)?.[1];
    if (id && method === 'DELETE') {
      items = items.filter((device) => device.id !== id);
      return new Response(null, { status: 204 });
    }
    if (url.pathname === '/v1/devices') return json({ items });
    return json({ items: [] });
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <DevicesCard />
    </TestProviders>,
  );
  return { calls };
}

/** The device rows alone, once the list has answered: the recipes under them are lists too. */
async function deviceRows(): Promise<HTMLElement[]> {
  return within(await screen.findByRole('list', { name: 'Your devices' })).getAllByRole('listitem');
}

describe('the devices card on You', () => {
  it('lists each device with when it was added and when it last sent something', async () => {
    mount();
    const card = await screen.findByRole('region', { name: 'Devices & shortcuts' });
    const rows = await deviceRows();
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('Watch');
    expect(rows[0]).toHaveTextContent(/Added .+ · Last used on/);
    expect(rows[1]).toHaveTextContent('Shortcut on the phone');
    expect(rows[1]).toHaveTextContent('Never used');
    // No key anywhere on the list: the server never sends one here.
    expect(card).not.toHaveTextContent('ck_');
  });

  it('says so when there are none, and stops adding at ten', async () => {
    mount([]);
    expect(await screen.findByText(/no devices yet/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /add a device/i })).toBeEnabled();
  });

  it('is full at ten devices', async () => {
    mount(
      Array.from({ length: MAX_DEVICES }, (_, index) => ({
        id: `dev_${String(index)}`,
        name: `Device ${String(index)}`,
        created_at: '2026-01-01T00:00:00Z',
        last_used_at: null,
      })),
    );
    expect(await deviceRows()).toHaveLength(MAX_DEVICES);
    const add = screen.getByRole('button', { name: /add a device/i });
    expect(add).toBeDisabled();
    expect(add).toHaveTextContent(/remove one first/i);
  });

  it('mints a key once: the name is posted, the key is shown with its warning, Done hides it for good', async () => {
    const user = userEvent.setup();
    const { calls } = mount();
    await user.click(await screen.findByRole('button', { name: /add a device/i }));

    const field = screen.getByRole('textbox', { name: /what is this device/i });
    expect(field).toHaveFocus();
    // Nothing to create until there is a name.
    expect(screen.getByRole('button', { name: 'Create key' })).toBeDisabled();
    await user.type(field, '  Ring  ');
    await user.click(screen.getByRole('button', { name: 'Create key' }));

    const shown = await screen.findByRole('status');
    expect(shown).toHaveTextContent('The key for Ring');
    expect(shown).toHaveTextContent(deviceCreated.key ?? '');
    expect(shown).toHaveTextContent(/will not be shown again/i);
    expect(within(shown).getByRole('button', { name: 'Copy key' })).toBeInTheDocument();
    // Focus lands on the box so the key is read out and the next Tab is Copy
    // key (R4-25); the effect that moves it runs a tick after the box appears.
    await waitFor(() => {
      expect(shown).toHaveFocus();
    });
    // Trimmed, and posted exactly once: the server does not replay this route,
    // so the client must not retry it (R4-5).
    expect(calls.filter((call) => call.method === 'POST')).toHaveLength(1);
    expect(calls.find((call) => call.method === 'POST')).toMatchObject({
      path: '/v1/devices',
      body: { name: 'Ring' },
    });
    // The form is gone; the list has the new row, without the key.
    expect(screen.queryByRole('textbox')).toBeNull();
    const card = screen.getByRole('region', { name: 'Devices & shortcuts' });
    await waitFor(async () => {
      expect(await deviceRows()).toHaveLength(3);
    });
    const [, , ring] = await deviceRows();
    expect(ring).toHaveTextContent('Ring');
    expect(ring).not.toHaveTextContent('ck_');

    await user.click(screen.getByRole('button', { name: 'Done' }));
    expect(screen.queryByRole('status')).toBeNull();
    expect(card).not.toHaveTextContent('ck_');
  });

  it('says why when the server refuses, in the server’s own words', async () => {
    const user = userEvent.setup();
    mount(devicesPage.items, {
      create: () =>
        json(
          {
            type: 'about:blank',
            title: 'Conflict',
            status: 409,
            detail: 'you already have ten devices; remove one first',
          },
          409,
        ),
    });
    await user.click(await screen.findByRole('button', { name: /add a device/i }));
    await user.type(screen.getByRole('textbox'), 'One more');
    await user.click(screen.getByRole('button', { name: 'Create key' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'you already have ten devices; remove one first',
    );
    // The form stays, with the name, so it can be tried again after a removal.
    expect(screen.getByRole('textbox')).toHaveValue('One more');
  });

  it('sends a create once even when the server fails, since a retry would mint a key nobody sees (R4-5)', async () => {
    const user = userEvent.setup();
    const { calls } = mount(devicesPage.items, {
      create: () =>
        json({ type: 'about:blank', title: 'Service Unavailable', status: 503, detail: 'try later' }, 503),
    });
    await user.click(await screen.findByRole('button', { name: /add a device/i }));
    await user.type(screen.getByRole('textbox'), 'Ring');
    await user.click(screen.getByRole('button', { name: 'Create key' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('try later');
    // A 5xx is what the default policy retries; this route gets one attempt.
    expect(calls.filter((call) => call.method === 'POST')).toHaveLength(1);
  });

  it('says to check the list when a create times out, and asks for the list again (R4-5)', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      const { calls } = mount(devicesPage.items, {
        // Never answers; the client's own timer aborts it as a dead network would.
        create: (init) =>
          new Promise((_, reject) => {
            init?.signal?.addEventListener('abort', () => {
              reject(init.signal?.reason as Error);
            });
          }),
      });
      await user.click(await screen.findByRole('button', { name: /add a device/i }));
      await user.type(screen.getByRole('textbox'), 'Ring');
      await user.click(screen.getByRole('button', { name: 'Create key' }));
      await waitFor(() => {
        expect(calls.filter((call) => call.method === 'POST')).toHaveLength(1);
      });
      const listGets = () => calls.filter((call) => call.method === 'GET' && call.path === '/v1/devices').length;
      const before = listGets();

      act(() => {
        vi.advanceTimersByTime(DEFAULT_TIMEOUT_MS + 1);
      });

      // Not "it will be retried": nothing will retry it, and the server may
      // have minted the key, so the person is sent to look.
      expect(await screen.findByRole('alert')).toHaveTextContent(UNCONFIRMED_TEXT);
      expect(calls.filter((call) => call.method === 'POST')).toHaveLength(1);
      await waitFor(() => {
        expect(listGets()).toBeGreaterThan(before);
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it('removes a device behind a confirmation, which revokes it on the server', async () => {
    const user = userEvent.setup();
    const { calls } = mount();
    await user.click(await screen.findByRole('button', { name: 'Remove Watch' }));

    const dialog = screen.getByRole('dialog', { name: 'Remove Watch?' });
    expect(dialog).toHaveTextContent(/stops working at once/i);
    await user.click(within(dialog).getByRole('button', { name: 'Remove it' }));

    await waitFor(() => {
      expect(calls).toContainEqual({ method: 'DELETE', path: '/v1/devices/dev_fixture' });
    });
    await waitFor(async () => {
      expect(await deviceRows()).toHaveLength(1);
    });
    expect(screen.getByRole('list', { name: 'Your devices' })).not.toHaveTextContent('Watch');
  });

  it('cancelling the confirmation removes nothing', async () => {
    const user = userEvent.setup();
    const { calls } = mount();
    await user.click(await screen.findByRole('button', { name: 'Remove Watch' }));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(calls.some((call) => call.method === 'DELETE')).toBe(false);
  });

  it('points every recipe at this instance’s inbox, with copy buttons', async () => {
    mount();
    const card = await screen.findByRole('region', { name: 'Devices & shortcuts' });
    expect(within(card).getByText('From a terminal (curl)')).toBeInTheDocument();
    expect(within(card).getByText(/iPhone or Apple Watch/)).toBeInTheDocument();
    expect(within(card).getByText(/Android/)).toBeInTheDocument();
    expect(card).toHaveTextContent(inboxAudioUrl());
    expect(card).toHaveTextContent(/anything that can POST a file with one header/i);
    expect(within(card).getByRole('button', { name: 'Copy command' })).toBeInTheDocument();
    expect(within(card).getAllByRole('button', { name: 'Copy address' })).toHaveLength(2);
  });

  it('writes the curl recipe for the configured API', () => {
    const recipe = curlRecipe('https://api.example');
    expect(recipe).toContain('POST "https://api.example/v1/inbox/audio"');
    expect(recipe).toContain('Authorization: Bearer YOUR_KEY');
    expect(recipe).toContain('Content-Type: audio/m4a');
    expect(recipe).toContain('--data-binary @recording.m4a');
  });
});
