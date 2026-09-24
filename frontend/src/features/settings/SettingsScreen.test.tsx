import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { usageRich as USAGE } from '@/api/__fixtures__/pending.ts';
import type { SettingsWire, UsageWire } from '@/api/schema.ts';
import { config } from '@/config/env.ts';
import { TEST_TOKENS, TestProviders, testApiContext } from '@/test/providers.tsx';

import { SettingsScreen } from './SettingsScreen.tsx';

const STORED: SettingsWire = {
  cleanup_mode: 'faithful',
  retention_days: 0,
  theme: 'ink',
  default_language: 'en',
  daily_spend_cap_micros: 0,
};

const NO_USAGE: UsageWire = { month: '2026-09', cost_micros: 0, calls: 0, ops: {}, days: [] };

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

/** You, over a stub API that stores what PUT sends and answers GET with it. */
export function mountSettings(
  overrides: { settings?: SettingsWire; usage?: UsageWire; idToken?: string } = {},
) {
  let stored: SettingsWire = overrides.settings ?? STORED;
  const puts: SettingsWire[] = [];
  const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/v1/usage')) return json(overrides.usage ?? NO_USAGE);
    if (url.pathname.endsWith('/v1/settings')) {
      if ((init?.method ?? 'GET') === 'PUT') {
        const body = JSON.parse(String(init?.body)) as SettingsWire;
        puts.push(body);
        stored = { ...body, daily_spend_cap_micros: stored.daily_spend_cap_micros ?? 0 };
      }
      return json(stored);
    }
    return json({ items: [] });
  });

  const tokens = overrides.idToken ? { ...TEST_TOKENS, idToken: overrides.idToken } : TEST_TOKENS;
  render(
    <TestProviders api={testApiContext(fetchImpl, tokens)}>
      <MemoryRouter initialEntries={['/settings']}>
        <SettingsScreen />
      </MemoryRouter>
    </TestProviders>,
  );
  return { puts, fetchImpl };
}

/** An id token as Cognito mints one, unverified: the header reads its claims. */
function idTokenWith(claims: Record<string, unknown>): string {
  const encode = (value: unknown): string =>
    btoa(JSON.stringify(value)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  return `${encode({ alg: 'RS256' })}.${encode(claims)}.sig`;
}

describe('every control saves itself when it is changed', () => {
  /*
   * QA D9: "Keep recordings for" set to 7 read "Unsaved changes"; tapping the
   * Home tab and coming back showed 0, with no prompt — only `beforeunload`
   * was guarded, and an in-app navigation is not an unload. There is no
   * draft to lose now: a change is a PUT.
   */
  it('saves a language change as soon as it is chosen, with no Save button to find', async () => {
    const { puts } = mountSettings();

    const select = (await screen.findByRole('combobox', {
      name: /transcription language/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select.value).toBe('en');
    });
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
    expect(screen.queryByText(/unsaved/i)).toBeNull();

    await userEvent.selectOptions(select, 'ml');

    await waitFor(() => {
      expect(puts).toHaveLength(1);
    });
    expect(puts[0]?.default_language).toBe('ml');
    // The rest of the record rides along unchanged: PUT replaces the whole thing.
    expect(puts[0]?.cleanup_mode).toBe('faithful');
    expect(screen.getByText(/transcribed as Malayalam/i)).toBeInTheDocument();
    // A brief tick, not a permanent claim.
    expect(await screen.findByText('Saved')).toBeInTheDocument();
  });

  it('saves a cleanup mode on tap', async () => {
    const { puts } = mountSettings();
    await screen.findByRole('combobox', { name: /transcription language/i });
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /polished/i })).toBeEnabled();
    });

    await userEvent.click(screen.getByRole('button', { name: /polished/i }));

    await waitFor(() => {
      expect(puts).toHaveLength(1);
    });
    expect(puts[0]?.cleanup_mode).toBe('polished');
    expect(screen.getByRole('button', { name: /polished/i })).toHaveAttribute('aria-pressed', 'true');
  });

  it('applies a theme on the device at once and saves it like the rest', async () => {
    /*
     * QA D13: Nocturne applied and persisted on the device, the status read
     * "Unsaved changes", and after a reload "All changes saved" while the
     * server still said `ink` — no PUT had ever gone out.
     */
    const { puts } = mountSettings();
    await screen.findByRole('combobox', { name: /transcription language/i });
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /polished/i })).toBeEnabled();
    });

    await userEvent.click(screen.getByRole('button', { name: 'Nocturne' }));

    expect(document.documentElement).toHaveAttribute('data-theme', 'nocturne');
    await waitFor(() => {
      expect(puts).toHaveLength(1);
    });
    expect(puts[0]?.theme).toBe('nocturne');
    expect(screen.queryByText(/unsaved/i)).toBeNull();
  });

  it('offers the five retention tiers the server stores, and saves the chosen one at once', async () => {
    /*
     * Round-3 T5: the control was a free number 0–3650, but the server
     * rounds down to 0/7/30/90/365, so "45 days" was promised while the audio
     * went on day 30. The choice is one of the tiers now, and "forever" is a
     * word rather than "0 days".
     */
    const { puts } = mountSettings();
    const select = (await screen.findByRole('combobox', {
      name: /keep recordings for/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select).toBeEnabled();
    });
    expect(Array.from(select.options, (option) => option.textContent)).toEqual([
      'Keep forever',
      '7 days',
      '30 days',
      '90 days',
      '365 days',
    ]);
    expect(select.value).toBe('0');

    await userEvent.selectOptions(select, '30');

    await waitFor(() => {
      expect(puts).toHaveLength(1);
    });
    expect(puts[0]?.retention_days).toBe(30);
    expect(screen.getByText(/deleted after 30 days/i)).toBeInTheDocument();
    expect(screen.getByText(/applies to recordings made from now on/i)).toBeInTheDocument();
  });

  it('shows what the server stored, not what was sent, once the PUT answers', async () => {
    /*
     * The screen used to copy the stored record into its draft exactly once,
     * so a value the server coerced stayed on screen as typed (round-3 T5).
     * The stub here stores 30 whatever is sent, standing in for the tiers.
     */
    const puts: SettingsWire[] = [];
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(String(input));
      if (url.pathname.endsWith('/v1/usage')) return json(NO_USAGE);
      if (url.pathname.endsWith('/v1/settings')) {
        if ((init?.method ?? 'GET') === 'PUT') {
          puts.push(JSON.parse(String(init?.body)) as SettingsWire);
          return json({ ...STORED, retention_days: 30 });
        }
        return json(STORED);
      }
      return json({ items: [] });
    });
    render(
      <TestProviders api={testApiContext(fetchImpl)}>
        <MemoryRouter initialEntries={['/settings']}>
          <SettingsScreen />
        </MemoryRouter>
      </TestProviders>,
    );
    const select = (await screen.findByRole('combobox', {
      name: /keep recordings for/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select).toBeEnabled();
    });

    await userEvent.selectOptions(select, '90');

    await waitFor(() => {
      expect(puts[0]?.retention_days).toBe(90);
    });
    await waitFor(() => {
      expect(select.value).toBe('30');
    });
    expect(screen.getByText(/deleted after 30 days/i)).toBeInTheDocument();
  });

  it('says when a save failed and offers to try again, keeping the choice on screen', async () => {
    let refuse = true;
    const puts: SettingsWire[] = [];
    const fetchImpl = vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(String(input));
      if (url.pathname.endsWith('/v1/usage')) return json(NO_USAGE);
      if (url.pathname.endsWith('/v1/settings')) {
        if ((init?.method ?? 'GET') === 'PUT') {
          const body = JSON.parse(String(init?.body)) as SettingsWire;
          puts.push(body);
          // 403 rather than 500: the client retries 5xx on its own.
          if (refuse) {
            return new Response(
              JSON.stringify({ type: 'about:blank', title: 'Not permitted', status: 403 }),
              { status: 403, headers: { 'content-type': 'application/problem+json' } },
            );
          }
          return json(body);
        }
        return json(STORED);
      }
      return json({ items: [] });
    });
    render(
      <TestProviders api={testApiContext(fetchImpl)}>
        <MemoryRouter initialEntries={['/settings']}>
          <SettingsScreen />
        </MemoryRouter>
      </TestProviders>,
    );
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /polished/i })).toBeEnabled();
    });

    await userEvent.click(screen.getByRole('button', { name: /polished/i }));

    expect(await screen.findByText(/couldn.t save your settings/i)).toBeInTheDocument();
    // The choice stays where the user put it.
    expect(screen.getByRole('button', { name: /polished/i })).toHaveAttribute('aria-pressed', 'true');

    refuse = false;
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await waitFor(() => {
      expect(puts).toHaveLength(2);
    });
    expect(puts[1]?.cleanup_mode).toBe('polished');
    expect(await screen.findByText('Saved')).toBeInTheDocument();
  });
});

describe('the default transcription language', () => {
  it('explains auto-detect honestly, including what it cannot do', async () => {
    mountSettings({ settings: { ...STORED, default_language: 'auto' } });

    const select = (await screen.findByRole('combobox', {
      name: /transcription language/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select.value).toBe('auto');
    });
    expect(screen.getByText(/mixes two is detected as one of them/i)).toBeInTheDocument();
  });

  it('sits above the retention field and says a note can choose its own under Details', async () => {
    mountSettings();
    const language = await screen.findByRole('combobox', { name: /transcription language/i });
    const retention = screen.getByRole('combobox', { name: /keep recordings for/i });

    // The same card, the language row first.
    expect(language.closest('.you-card')).toBe(retention.closest('.you-card'));
    expect(language.compareDocumentPosition(retention) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(
      screen.getByText(/Applies to every recording; a note can choose its own under Details\./),
    ).toBeInTheDocument();
    // Named in the speaker's own script as well as in English.
    expect(screen.getByRole('option', { name: 'മലയാളം · Malayalam' })).toBeInTheDocument();
  });

  it('says under the language row what to choose for a recording that mixes Malayalam and English', async () => {
    // Round-3 T3: Auto-detect, the owner's live default, re-scripted real
    // Malayalam as Tamil and dropped the Malayalam sentence from a mixed clip.
    mountSettings({ settings: { ...STORED, default_language: 'auto' } });
    const language = await screen.findByRole('combobox', { name: /transcription language/i });
    const hint = screen.getByText(/mix malayalam and english in one recording\? choose malayalam\./i);
    expect(hint).toHaveTextContent(/auto-detect picks one language per recording/i);
    expect(hint.closest('.you-card')).toBe(language.closest('.you-card'));
  });

  it('reads a record from before the field existed as English, and saves nothing for it', async () => {
    const { default_language: _absent, ...legacy } = STORED;
    const { puts } = mountSettings({ settings: legacy });

    const select = (await screen.findByRole('combobox', {
      name: /transcription language/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select.value).toBe('en');
    });
    expect(puts).toHaveLength(0);
  });
});

/**
 * The screen is called You, and it used to open on a theme picker with the
 * person nowhere on it. The account is the header now, and Sign out is a text
 * action on it rather than an accent-bordered card among the settings.
 */
describe('the account header', () => {
  it('names the signed-in account from the id token, with its initial in the roundel', async () => {
    const authTime = Math.floor(Date.now() / 1000) - 3 * 86_400;
    mountSettings({ idToken: idTokenWith({ email: 'vpillai@example.com', auth_time: authTime }) });

    const account = await screen.findByRole('region', { name: 'Account' });
    expect(account).toHaveTextContent('vpillai@example.com');
    expect(account).toHaveTextContent(/signed in 3 days ago/i);
    expect(account.querySelector('.account__roundel')).toHaveTextContent('V');
  });

  it('still says it is signed in when the token carries no claims it can read', async () => {
    // The e2e stub's token, and any session whose token is not a JWT.
    mountSettings();
    const account = await screen.findByRole('region', { name: 'Account' });
    expect(account).toHaveTextContent(/^Signed in/);
    expect(account).not.toHaveTextContent('@');
  });

  it('offers Sign out as a quiet text action that still asks first', async () => {
    mountSettings();
    const signOut = await screen.findByRole('button', { name: 'Sign out' });
    expect(signOut).toHaveClass('text-link');
    expect(document.querySelector('.option--destructive')).toBeNull();

    await userEvent.click(signOut);

    // Round-3 T64: two sentences for the common case, not four.
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(new RegExp(`Sign out of ${config.appName} on this device\\?`));
    expect(dialog).toHaveTextContent(/your notes stay on the server\./i);
    expect(dialog).not.toHaveTextContent(/identity provider/i);
  });
});

/**
 * The screen is five cards, each a landmark named by its title, so a screen
 * reader's landmark list is the screen's table of contents; the version is a
 * row in the last one rather than a footnote under the whole screen.
 */
describe('the cards', () => {
  it('never names the instance’s daily cap, even when there is one (U13b)', async () => {
    mountSettings({ settings: { ...STORED, daily_spend_cap_micros: 5_000_000 } });
    await screen.findByRole('combobox', { name: /transcription language/i });
    expect(screen.queryByText(/stops taking recordings/i)).toBeNull();
    expect(screen.queryByText(/\bcap\b/i)).toBeNull();
    expect(screen.queryByText('$5.00')).toBeNull();
  });

  it('reserves no blank band for the status line while it has nothing to say (QA 11)', async () => {
    mountSettings();
    await screen.findByRole('combobox', { name: /transcription language/i });
    await waitFor(() => {
      expect(screen.queryByText(/loading your settings/i)).toBeNull();
    });
    // Empty, so `:empty` collapses it; the live region itself stays for the
    // next Saved to be announced.
    const status = screen.getByRole('status', { name: '' });
    expect(status).toHaveClass('settings-status');
    expect(status).toBeEmptyDOMElement();
  });

  it('are six labelled sections in the order a person needs them', async () => {
    mountSettings();
    await screen.findByRole('combobox', { name: /transcription language/i });

    const titles = screen.getAllByRole('heading', { level: 2 }).map((heading) => heading.textContent);
    expect(titles).toEqual([
      'Recording & transcription',
      'Appearance',
      'Passkeys',
      'Devices & shortcuts',
      'Your data',
      'About & support',
    ]);
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1);
  });

  it('puts Usage behind one row in About & support, carrying the month’s figure, that opens /usage', async () => {
    // Round-3 T20: the Usage card was a third of a three-screen You.
    mountSettings({ usage: USAGE });
    const row = await screen.findByRole('link', { name: /usage this month/i });
    expect(row).toHaveAttribute('href', '/usage');
    await waitFor(() => {
      expect(row).toHaveTextContent('$0.003');
    });
    expect(row.closest('.you-card')).toBe(
      screen.getByRole('heading', { name: 'About & support' }).closest('.you-card'),
    );
    expect(screen.queryByRole('heading', { name: 'Usage this month' })).toBeNull();
  });

  it('keeps one sentence of each footnote in view and the rest behind a native More', async () => {
    mountSettings();
    const card = (await screen.findByRole('heading', { name: 'Recording & transcription' })).closest(
      '.you-card',
    );
    const foot = card?.querySelector('.you-card__foot');
    expect(foot?.querySelector(':scope > p')).toHaveTextContent(/transcribed as English/);
    const more = foot?.querySelector('details');
    expect(more).not.toBeNull();
    expect(more?.open).toBe(false);
    expect(more?.querySelector('summary')).toHaveTextContent('More');
    expect(more).toHaveTextContent(/a note can choose its own under Details/);
    expect(more).toHaveTextContent(/kept indefinitely/);
  });

  it('puts the build in the About & support card, as selectable text, with the links beside it', async () => {
    mountSettings();
    const card = (await screen.findByRole('heading', { name: 'About & support' })).closest('.you-card');
    expect(card).not.toBeNull();
    expect(card).toHaveTextContent('Version');
    expect(card?.querySelector('.version-footnote code')).toHaveTextContent(config.version);
    expect(screen.getByRole('link', { name: /about chintan/i })).toHaveAttribute('href', '/about');
    expect(screen.getByRole('link', { name: /source on github/i })).toHaveAttribute(
      'href',
      'https://github.com/vppillai/chintan',
    );
    // Nothing under the last card: the footnote it used to be is gone.
    expect(document.querySelector('.screen > .version-footnote')).toBeNull();
  });

  it('shows the theme as one three-way control with a swatch each', async () => {
    mountSettings();
    const theme = await screen.findByRole('group', { name: 'Theme' });
    const options = Array.from(theme.querySelectorAll('button'), (button) => button.textContent);
    expect(options).toEqual(['Ink & Paper', 'Nocturne', 'System']);
    expect(theme.querySelectorAll('.theme-swatch')).toHaveLength(3);
    expect(screen.getByRole('button', { name: 'Ink & Paper' })).toHaveAttribute('aria-pressed', 'true');
  });
});
