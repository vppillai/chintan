import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { deviceCreated, devicesPage } from '@/api/__fixtures__/responses.ts';
import { DEFAULT_TIMEOUT_MS } from '@/api/client.ts';
import type { DeviceCreatedWire, DeviceWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import {
  CONNECT_DOCS_URL,
  DevicesCard,
  MAX_DEVICES,
  UNCONFIRMED_TEXT,
  curlRecipe,
  deviceHint,
  devicesStatus,
  inboxAudioUrl,
  isExpired,
} from './DevicesCard.tsx';

const DAY_MS = 86_400_000;
/** A perpetual, never-used device, for the tests that vary one field. */
const idle: DeviceWire = {
  id: 'dev_1',
  name: 'Watch',
  created_at: '2026-01-01T00:00:00Z',
  last_used_at: null,
  last_used_from: null,
  expires_at: null,
  usage_month: null,
};

/**
 * The generated list with its ids made distinct: the fixture generator
 * collapses every id to one stand-in, and the fake server below removes by id.
 */
const listed: DeviceWire[] = devicesPage.items.map((device, index) => ({
  ...device,
  id: `dev_${String(index)}`,
}));

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
  initial: readonly DeviceWire[] = listed,
  overrides: {
    create?: (init?: RequestInit) => Response | Promise<Response>;
    list?: () => Response;
    /** Where You was opened: `/settings#devices` is About's link. */
    entry?: string;
  } = {},
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
      const created: DeviceCreatedWire = {
        ...deviceCreated,
        id: `dev_${String(items.length + 1)}`,
        name: body.name,
      };
      const { key: _key, ...row } = created;
      items.push(row);
      return json(created, 201);
    }
    const id = /\/v1\/devices\/([^/]+)$/.exec(url.pathname)?.[1];
    if (id && method === 'DELETE') {
      items = items.filter((device) => device.id !== id);
      return new Response(null, { status: 204 });
    }
    if (url.pathname === '/v1/devices') return overrides.list ? overrides.list() : json({ items });
    return json({ items: [] });
  });
  render(
    <MemoryRouter initialEntries={[overrides.entry ?? '/settings']}>
      <TestProviders api={testApiContext(fetchImpl)}>
        <DevicesCard />
      </TestProviders>
    </MemoryRouter>,
  );
  return { calls };
}

/** The device rows alone, once the list has answered: the recipes under them are lists too. */
async function deviceRows(): Promise<HTMLElement[]> {
  return within(await screen.findByRole('list', { name: 'Your devices' })).getAllByRole('listitem');
}

/** The card's own disclosure: the one whose summary holds the title. */
async function fold(): Promise<HTMLDetailsElement> {
  const card = await screen.findByRole('region', { name: 'Devices & shortcuts' });
  return card.querySelector(':scope > details') as HTMLDetailsElement;
}

describe('the devices card folds behind its title (owner, round 8)', () => {
  it('is closed by default, its summary the title, a status and a chevron', async () => {
    mount([idle, { ...idle, id: 'dev_2', name: 'Ring' }]);
    const details = await fold();
    expect(details.open).toBe(false);
    const summary = details.querySelector(':scope > summary') as HTMLElement;
    expect(within(summary).getByRole('heading', { name: 'Devices & shortcuts' })).toBeInTheDocument();
    expect(summary.querySelector('.you-card__fold-chevron')).not.toBeNull();
    await waitFor(() => {
      expect(summary).toHaveTextContent('2 devices');
    });
    // The rows are in the DOM, behind the fold.
    expect(screen.getByText('Ring')).not.toBeVisible();
  });

  it('opens and closes on its summary', async () => {
    const user = userEvent.setup();
    mount([idle]);
    const details = await fold();
    await user.click(details.querySelector(':scope > summary') as HTMLElement);
    expect(details.open).toBe(true);
    expect(await screen.findByRole('button', { name: /add a device/i })).toBeVisible();
  });

  it('counts the keys, and says first what needs attention', () => {
    const now = Date.parse('2026-09-30T00:00:00Z');
    const inDays = (days: number): string => new Date(now + days * DAY_MS).toISOString();
    expect(devicesStatus([], now)).toBe('No devices yet');
    expect(devicesStatus([idle], now)).toBe('1 device');
    expect(devicesStatus([idle, idle, idle], now)).toBe('3 devices');
    expect(devicesStatus([idle, { ...idle, expires_at: inDays(3) }], now)).toBe('1 expiring soon');
    expect(devicesStatus([idle, { ...idle, expires_at: inDays(30) }], now)).toBe('2 devices');
    expect(
      devicesStatus([{ ...idle, expires_at: inDays(-1) }, { ...idle, expires_at: inDays(3) }], now),
    ).toBe('1 expired');
  });

  it('says it could not load in the summary, and stays closed', async () => {
    mount([], { list: () => json({ title: 'Nope', status: 400 }, 400) });
    const details = await fold();
    await waitFor(() => {
      expect(details.querySelector(':scope > summary')).toHaveTextContent('Couldn’t load');
    });
    expect(details.open).toBe(false);
  });

  it('opens when About links to it', async () => {
    mount([idle], { entry: '/settings#devices' });
    const details = await fold();
    await waitFor(() => {
      expect(details.open).toBe(true);
    });
    expect(details.closest('section')).toHaveAttribute('id', 'devices');
  });

  it('stays open while a minted key is on screen, through re-renders', async () => {
    const user = userEvent.setup();
    mount([idle]);
    const details = await fold();
    await user.click(details.querySelector(':scope > summary') as HTMLElement);
    await user.click(await screen.findByRole('button', { name: /add a device/i }));
    await user.type(screen.getByRole('textbox', { name: /what is this device/i }), 'Ring');
    await user.click(screen.getByRole('button', { name: 'Create key' }));
    const shown = await screen.findByRole('status');
    // The list refetches after the create and the summary changes (the
    // fixture's key is dated in the past): a re-render, which must not fold
    // the key away.
    await waitFor(() => {
      expect(details.querySelector(':scope > summary')).toHaveTextContent('1 expired');
    });
    expect(details.open).toBe(true);
    expect(shown).toBeVisible();
  });
});

describe('the devices card on You', () => {
  it('lists each device with when it was added and when it last sent something', async () => {
    mount();
    const card = await screen.findByRole('region', { name: 'Devices & shortcuts' });
    const rows = await deviceRows();
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('Watch');
    // The device that has sent this month says how much, then when last.
    // …and from which neighbourhood, never a full address (WH-B).
    expect(rows[0]).toHaveTextContent(/1 sent this month · .+ MB · Last used on .+ from 203\.0\.113\.x/);
    expect(rows[0]).not.toHaveTextContent(/Added/);
    expect(rows[0]).not.toHaveTextContent(/Expire/);
    expect(rows[1]).toHaveTextContent('Shortcut on the phone');
    expect(rows[1]).toHaveTextContent(/Added .+ · Never used/);
    expect(rows[1]).not.toHaveTextContent(/sent this month/);
    // The fixture's thirty-day key carries a date the generator pins in the
    // past, so the row reads as expired and still offers Remove (WH-A).
    expect(rows[1]).toHaveTextContent(/Never used · Expired/);
    expect(screen.getByRole('button', { name: 'Remove Shortcut on the phone' })).toBeEnabled();
    // No key anywhere on the list: the server never sends one here.
    expect(card).not.toHaveTextContent('ck_');
  });

  it('says when it was added and last used for a device that sent nothing this month', () => {
    expect(deviceHint({ ...idle, last_used_at: '2026-02-01T00:00:00Z' })).toMatch(/^Added .+ · Last used /);
    expect(
      deviceHint({
        ...idle,
        last_used_at: '2026-02-01T00:00:00Z',
        usage_month: { requests: 12, bytes: 8_700_000, month: '2026-02' },
      }),
    ).toMatch(/^12 sent this month · 8\.7 MB · Last used /);
  });

  it('says where the key was last used from, as a neighbourhood, and when it expires (WH-A, WH-B)', () => {
    const now = Date.parse('2026-09-26T12:00:00Z');
    const used = { ...idle, last_used_at: '2026-09-26T10:00:00Z', last_used_from: '203.0.113.x' };
    expect(deviceHint(used, now)).toMatch(/^Added .+ · Last used 2 hours ago from 203\.0\.113\.x$/);
    expect(deviceHint({ ...used, last_used_from: null }, now)).toMatch(/2 hours ago$/);
    // Counting up to the date: a key that ends in eleven and a half days has twelve left.
    const twelve = { ...idle, expires_at: new Date(now + 11.5 * DAY_MS).toISOString() };
    expect(deviceHint(twelve, now)).toMatch(/^Added .+ · Never used · Expires in 12 days$/);
    expect(isExpired(twelve, now)).toBe(false);
    expect(deviceHint({ ...idle, expires_at: new Date(now + 3_600_000).toISOString() }, now)).toMatch(/Expires in 1 day$/);
    const gone = { ...idle, expires_at: new Date(now - 1000).toISOString() };
    expect(deviceHint(gone, now)).toMatch(/^Added .+ · Never used · Expired$/);
    expect(isExpired(gone, now)).toBe(true);
    expect(isExpired(idle, now)).toBe(false);
  });

  it('offers an expiry when creating: Never by default and not sent, thirty days as expires_in_days', async () => {
    const user = userEvent.setup();
    const { calls } = mount();
    await user.click(await screen.findByRole('button', { name: /add a device/i }));
    const expires = screen.getByRole('combobox', { name: 'Expires after' });
    expect(expires).toHaveValue('');
    expect(within(expires).getAllByRole('option').map((option) => option.textContent)).toEqual([
      'Never',
      '30 days',
      '90 days',
      '1 year',
    ]);
    await user.type(screen.getByRole('textbox', { name: /what is this device/i }), 'Ring');
    await user.click(screen.getByRole('button', { name: 'Create key' }));
    await screen.findByRole('status');
    expect(calls.find((call) => call.method === 'POST')?.body).toEqual({ name: 'Ring' });
    await user.click(screen.getByRole('button', { name: 'Done' }));

    await user.click(screen.getByRole('button', { name: /add a device/i }));
    await user.selectOptions(screen.getByRole('combobox', { name: 'Expires after' }), '30');
    await user.type(screen.getByRole('textbox', { name: /what is this device/i }), 'Script');
    await user.click(screen.getByRole('button', { name: 'Create key' }));
    await screen.findByRole('status');
    expect(calls.filter((call) => call.method === 'POST').at(-1)?.body).toEqual({
      name: 'Script',
      expires_in_days: 30,
    });
  });

  it('says so when there are none, and stops adding at ten', async () => {
    mount([]);
    expect(await screen.findByText(/no devices yet\. add one/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /add a device/i })).toBeEnabled();
  });

  it('is full at ten devices, for Add and for Rotate alike', async () => {
    mount(
      Array.from({ length: MAX_DEVICES }, (_, index) => ({
        ...idle,
        id: `dev_${String(index)}`,
        name: `Device ${String(index)}`,
      })),
    );
    expect(await deviceRows()).toHaveLength(MAX_DEVICES);
    const add = screen.getByRole('button', { name: /add a device/i });
    expect(add).toBeDisabled();
    expect(add).toHaveTextContent(/remove one first/i);
    // A rotation briefly needs an eleventh row, so it is held too, with the same reason.
    const rotate = screen.getByRole('button', { name: 'Rotate key for Device 0' });
    expect(rotate).toBeDisabled();
    expect(rotate).toHaveAttribute('title', 'Ten is the limit; remove one first');
    expect(screen.getAllByRole('button', { name: /^Rotate key for/ }).every((b) => (b as HTMLButtonElement).disabled)).toBe(true);
  });

  it('rotates a key: posts a create with the same name, shows the new key, and revokes the old device only on Done', async () => {
    const user = userEvent.setup();
    const { calls } = mount();
    await user.click(await screen.findByRole('button', { name: 'Rotate key for Watch' }));

    const shown = await screen.findByRole('status');
    expect(shown).toHaveTextContent('The new key for Watch');
    expect(shown).toHaveTextContent(deviceCreated.key);
    expect(shown).toHaveTextContent(/old key keeps working until you tap Done/i);
    // The key on screen is the only copy; no row may rotate over it until Done.
    expect(screen.getAllByRole('button', { name: /^Rotate key for/ }).every((b) => (b as HTMLButtonElement).disabled)).toBe(true);
    // One create, with the old device's name, and no revoke yet: the device
    // still has a working key while the new one is being pasted in.
    expect(calls.filter((call) => call.method === 'POST')).toEqual([{ method: 'POST', path: '/v1/devices', body: { name: 'Watch' } }]);
    expect(calls.some((call) => call.method === 'DELETE')).toBe(false);
    await waitFor(async () => {
      expect(await deviceRows()).toHaveLength(3);
    });

    await user.click(screen.getByRole('button', { name: 'Done' }));
    await waitFor(() => {
      expect(calls).toContainEqual({ method: 'DELETE', path: '/v1/devices/dev_0' });
    });
    expect(calls.filter((call) => call.method !== 'GET').map((call) => call.method)).toEqual(['POST', 'DELETE']);
    expect(screen.queryByRole('status')).toBeNull();
    await waitFor(async () => {
      expect(await deviceRows()).toHaveLength(2);
    });
    const card = screen.getByRole('region', { name: 'Devices & shortcuts' });
    expect(card).not.toHaveTextContent('ck_');
  });

  it('a failed create rotates nothing', async () => {
    const user = userEvent.setup();
    const { calls } = mount(listed, {
      create: () =>
        json({ type: 'about:blank', title: 'Service Unavailable', status: 503, detail: 'try later' }, 503),
    });
    await user.click(await screen.findByRole('button', { name: 'Rotate key for Watch' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('try later');
    expect(screen.queryByRole('status')).toBeNull();
    expect(calls.filter((call) => call.method === 'POST')).toHaveLength(1);
    expect(calls.some((call) => call.method === 'DELETE')).toBe(false);
    expect(await deviceRows()).toHaveLength(2);
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
    expect(shown).toHaveTextContent(deviceCreated.key);
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
    mount(listed, {
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
    const { calls } = mount(listed, {
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
      const { calls } = mount(listed, {
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
      expect(calls).toContainEqual({ method: 'DELETE', path: '/v1/devices/dev_0' });
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
    expect(within(card).getAllByRole('button', { name: 'Copy address' })).toHaveLength(3);
    // Each recipe is a disclosure with its own chevron, not an underlined link (R4-29).
    expect(card.querySelectorAll('details.recipe > summary > .recipe__chevron')).toHaveLength(5);
  });

  it('folds the raw addresses, the note-id header and the README link behind a fifth disclosure, closed until opened', async () => {
    const user = userEvent.setup();
    mount();
    const card = await screen.findByRole('region', { name: 'Devices & shortcuts' });
    // Nothing outside a fold names an address any more (owner, 2026-09-27:
    // the paragraph under the recipes was a wall of URLs).
    const outside = Array.from(card.querySelectorAll('.recipes > p'), (p) => p.textContent).join(' ');
    expect(outside).not.toContain('/v1/inbox');
    expect(outside).not.toContain('X-Chintan-Note-Id');

    const other = within(card).getByText('Other apps, and filing into a specific note').closest('details') as HTMLDetailsElement;
    expect(other.open).toBe(false);
    expect(other).toHaveTextContent(/anything that can POST a file with one header/i);
    expect(other).toHaveTextContent(inboxAudioUrl());
    expect(other).toHaveTextContent(/add the header X-Chintan-Note-Id with the note’s id/);
    const readme = within(other).getByRole('link', { name: /README on github\.com/ });
    expect(readme).toHaveAttribute('href', CONNECT_DOCS_URL);
    expect(readme).toHaveAttribute('target', '_blank');
    expect(readme).toHaveAttribute('rel', 'noopener noreferrer');

    await user.click(within(other).getByText('Other apps, and filing into a specific note'));
    expect(other.open).toBe(true);
  });

  it('walks the Pebble Index ring’s webhook to the inbox: the address, the header, Recording (OF-RING)', async () => {
    mount();
    const card = await screen.findByRole('region', { name: 'Devices & shortcuts' });
    const ring = within(card).getByText('Pebble Index 01 ring').closest('details');
    expect(ring).not.toBeNull();
    expect(ring).toHaveTextContent(/Index → Webhook/);
    expect(ring).toHaveTextContent(inboxAudioUrl());
    expect(ring).toHaveTextContent(/Authorization = Bearer YOUR_KEY/);
    // The ring's header field is a bare value; the inbox takes the key without the scheme.
    expect(ring).toHaveTextContent(/bare key, without “Bearer”, works too/);
    expect(ring).toHaveTextContent(/Send the Recording \(or Both\)/);
    expect(ring).toHaveTextContent(/lands like any recording/);
    expect(within(ring as HTMLElement).getByRole('button', { name: 'Copy address' })).toBeInTheDocument();
  });

  it('counts requests, not recordings, and says how to aim a device at one note (R4-30)', async () => {
    mount();
    const card = await screen.findByRole('region', { name: 'Devices & shortcuts' });
    expect(card).toHaveTextContent(/two hundred requests a day \(recordings or text\), up to 4 MiB each/);
    expect(card).not.toHaveTextContent(/recordings a day/);
    expect(card).toHaveTextContent(/add the header X-Chintan-Note-Id with the note’s id/);
  });

  it('writes the curl recipe for the configured API', () => {
    const recipe = curlRecipe('https://api.example');
    expect(recipe).toContain('POST "https://api.example/v1/inbox/audio"');
    expect(recipe).toContain('Authorization: Bearer YOUR_KEY');
    expect(recipe).toContain('Content-Type: audio/m4a');
    expect(recipe).toContain('--data-binary @recording.m4a');
  });
});
