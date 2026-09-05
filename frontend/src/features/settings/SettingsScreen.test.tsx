import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { usageRich as USAGE } from '@/api/__fixtures__/pending.ts';
import { usage as USAGE_TODAY } from '@/api/__fixtures__/responses.ts';
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

  it('saves the retention once the typing pauses, as one PUT', async () => {
    const user = userEvent.setup({ delay: null });
    const { puts } = mountSettings();
    const input = await screen.findByRole('spinbutton', { name: /keep recordings for/i });
    await waitFor(() => {
      expect(input).toBeEnabled();
    });

    await user.clear(input);
    await user.type(input, '30');
    expect(input).toHaveValue(30);
    // Not yet: "3" on the way to "30" must not be stored.
    expect(puts).toHaveLength(0);

    await waitFor(() => {
      expect(puts).toHaveLength(1);
    });
    expect(puts[0]?.retention_days).toBe(30);
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
    const retention = screen.getByRole('spinbutton', { name: /keep recordings for/i });

    // The same card, the language row first.
    expect(language.closest('.you-card')).toBe(retention.closest('.you-card'));
    expect(language.compareDocumentPosition(retention) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(
      screen.getByText(/Applies to every recording; a note can choose its own under Details\./),
    ).toBeInTheDocument();
    // Named in the speaker's own script as well as in English.
    expect(screen.getByRole('option', { name: 'മലയാളം · Malayalam' })).toBeInTheDocument();
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
 * "Usage this month" replaces the read-only spend-cap sentence (backlog U13):
 * the cap is one number for the whole instance and said nothing about the
 * person; what their own recordings cost does.
 */
describe('usage this month', () => {
  it('shows the month’s providers figure in dollars, the split by stage, and the calls and minutes', async () => {
    mountSettings({ usage: USAGE });

    // 2721 microdollars, three decimals under a dollar.
    expect(await screen.findByText('$0.003', { selector: '.usage__figure' })).toBeInTheDocument();
    // The month as the card's eyebrow; the figure is the one large number.
    expect(screen.getByText('January 2026')).toHaveClass('usage__month');
    expect(screen.getByText('Providers')).toBeInTheDocument();
    expect(screen.getByText('5 calls', { selector: '.usage__summary .numeric' })).toBeInTheDocument();
    expect(screen.getByText('0.5 min', { selector: '.usage__summary .numeric' })).toBeInTheDocument();

    // The stages, in the order a recording meets them, then the two a person asks for.
    const labels = Array.from(document.querySelectorAll('.usage__ops dt'), (term) => term.textContent);
    expect(labels).toEqual(['Transcribe', 'Route', 'Clean up', 'Clean note', 'Ask']);
    // Transcribe (311), route (420) and clean note (250) round to nothing; cleanup (640) and ask (1100) do not.
    expect(screen.getAllByText('$0.000', { selector: '.usage__op-figures .numeric' })).toHaveLength(3);
    expect(screen.getAllByText('$0.001', { selector: '.usage__op-figures .numeric' })).toHaveLength(2);
  });

  /**
   * N11: the richer view. The split by provider under Providers, the facts
   * row, and the user's share of AWS — each from a field the contract makes
   * required and this screen treats as optional, so it can ship first.
   */
  it('splits the providers’ figure by provider, the biggest bill first', async () => {
    mountSettings({ usage: USAGE });
    await screen.findByText('Providers');

    const labels = Array.from(document.querySelectorAll('.usage__providers dt'), (t) => t.textContent);
    expect(labels).toEqual(['Language model (MiniMax)', 'Groq']);
    const minimax = screen.getByText('Language model (MiniMax)').closest('.usage__provider');
    expect(minimax).toHaveTextContent('$0.002');
    expect(minimax).toHaveTextContent('4 calls');
    // The rows add up to the figure, so there is nothing unattributed to show.
    expect(screen.queryByText(/before the split began/i)).toBeNull();
  });

  it('shows what the provider rows do not account for, when the split began part-way through the month', async () => {
    // The per-provider counters were added after the month's total had been
    // accumulating; for that month the rows sum to less than the figure, and
    // the difference is a line of its own rather than a puzzle. Derived from
    // the data, so it disappears once every row carries a provider.
    mountSettings({
      usage: { ...USAGE, providers: { minimax: { calls: 2, cost_micros: 1200 } } },
    });
    await screen.findByText('Providers');

    const rest = screen.getByText(/before the split began/i).closest('.usage__provider');
    // 2,721 − 1,200 microdollars.
    expect(rest).toHaveTextContent('$0.002');
    expect(rest).toHaveClass('usage__provider--unattributed');
  });

  it('labels the day strip in print as well as for a screen reader: the scale, the ends, a caption', async () => {
    mountSettings({ usage: USAGE });
    const chart = (await screen.findByRole('img', { name: /spend by day/i })).closest('.usage__chart');

    expect(chart?.querySelector('.usage__chart-scale')).toHaveTextContent(/Tallest bar \$0\.002 on (4 Jan|Jan 4)/);
    const caption = chart?.querySelector('.usage__chart-caption');
    expect(caption).toHaveTextContent(/^(1 Jan|Jan 1)/);
    expect(caption).toHaveTextContent(/(31 Jan|Jan 31)$/);
    expect(caption).toHaveTextContent(/spend by day, today in colour · dots mark API requests/i);
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

  it('lists the month’s API requests and what is stored, as one row of facts', async () => {
    mountSettings({ usage: USAGE });
    const facts = await screen.findByRole('group', { name: 'This month' });

    expect(facts).toHaveTextContent(/API requests\s*312 requests/);
    expect(facts).toHaveTextContent(/Recordings stored\s*41 recordings · 23.2 min · 9.1 MB/);
    expect(facts).toHaveTextContent(/Notes\s*12 notes/);
    // The month's storage-days, priced here as an estimate at a named rate;
    // 18 MB·days is real but rounds below a tenth of a cent.
    expect(facts).toHaveTextContent(
      /Stored this month\s*0\.02 GB·days \(under \$0\.001 at S3 standard \$0\.023\/GB-month\)/,
    );
    expect(facts).not.toHaveTextContent(/approx/);
  });

  it('marks the stored figures approximate when the backend stopped counting at its cap', async () => {
    mountSettings({ usage: { ...USAGE, storage: { ...USAGE.storage!, approximate: true } } });
    const facts = await screen.findByRole('group', { name: 'This month' });
    expect(facts).toHaveTextContent(/41 recordings · 23.2 min · 9.1 MB · approx\./);
    expect(facts).toHaveTextContent(/12 notes · approx\./);
  });

  it('shows the user’s estimated share of AWS and totals with that, saying so', async () => {
    mountSettings({ usage: USAGE });

    const aws = (await screen.findByText('AWS')).closest('.usage__cell');
    // The instance figure stays; the share sits beneath it.
    expect(aws).toHaveTextContent('$2.35');
    expect(aws).toHaveTextContent(/Your estimated share: \$0\.123 \(by provider spend\)/);

    // 2,721 + 123,456 microdollars, not 2,721 + 2,345,678.
    const total = screen.getByText('Total').closest('.usage__cell');
    expect(total).toHaveTextContent('$0.126');
    expect(total).toHaveTextContent(/providers \+ your AWS share/);
  });

  it('draws a dot per day for API requests, and says how many the month took', async () => {
    mountSettings({ usage: USAGE });
    const figure = await screen.findByRole('img', { name: /spend by day in January 2026/i });
    expect(figure).toHaveAccessibleName(/21 requests to the API, shown as dots/i);
    expect(figure.querySelectorAll('.usage__api-dot')).toHaveLength(2);
  });

  it('renders a response from a backend that predates providers, api, storage and share, with nothing invented', async () => {
    // The generated fixture now carries every member; this is the shape an
    // instance running the previous release answers, and the screen must keep
    // rendering it while a frontend deploy is ahead of a backend deploy.
    const { providers: _providers, api: _api, storage: _storage, aws, days, ...rest } = USAGE_TODAY;
    const legacy: UsageWire = {
      ...rest,
      days: days.map(({ api_requests: _requests, ...day }) => day),
      aws: aws
        ? { month_micros: aws.month_micros, as_of: aws.as_of, budget_micros: aws.budget_micros }
        : null,
    };
    mountSettings({ usage: legacy });

    expect(await screen.findByText('$0.001', { selector: '.usage__figure' })).toBeInTheDocument();
    expect(document.querySelector('.usage__providers')).toBeNull();
    expect(screen.queryByRole('group', { name: 'This month' })).toBeNull();
    expect(screen.queryByText(/estimated share/)).toBeNull();
    // The Total falls back to providers plus the instance figure, and says so.
    const total = screen.getByText('Total').closest('.usage__cell');
    expect(total).toHaveTextContent('$2.35');
    expect(total).toHaveTextContent(/providers \+ instance AWS/);
    const figure = screen.getByRole('img', { name: /spend by day/i });
    expect(figure).not.toHaveAccessibleName(/API/);
    expect(figure.querySelectorAll('.usage__api-dot')).toHaveLength(0);
  });

  it('draws a bar per day with usage, described in words for whoever cannot see it', async () => {
    mountSettings({ usage: USAGE });

    const figure = await screen.findByRole('img', { name: /spend by day in January 2026/i });
    expect(figure).toHaveAccessibleName(/2 days with recordings/i);
    // The day is rendered in the runtime's locale ("4 Jan" or "Jan 4"), so only its parts are pinned.
    expect(figure).toHaveAccessibleName(/the most on (4 Jan|Jan 4) at \$0\.002/i);
    // Two rows, two bars — the empty days are not drawn as marks.
    expect(figure.querySelectorAll('.usage__bar')).toHaveLength(2);
  });

  it('says plainly when nothing has been processed yet, instead of an empty chart', async () => {
    mountSettings();
    expect(await screen.findByText(/no recordings have been processed this month yet/i)).toBeInTheDocument();
    expect(screen.queryByRole('img', { name: /spend by day/i })).toBeNull();
  });

  /**
   * "AWS this month" (D6b): the instance's month to date from the stack's
   * Budget, read once a day by the worker, so it carries how old it is.
   */
  it('shows the AWS figure with how old it is, and adds it to the providers in a Total', async () => {
    vi.useFakeTimers({ now: new Date('2026-01-04T12:00:00Z'), toFake: ['Date'] });
    try {
      mountSettings({
        usage: {
          ...USAGE,
          aws: { month_micros: 3_120_000, as_of: '2026-01-04T09:00:00Z', budget_micros: null },
        },
      });

      const aws = await screen.findByText('AWS');
      const row = aws.closest('.usage__cell');
      expect(row).toHaveTextContent('$3.12');
      expect(row).toHaveTextContent(/as of 3 hours ago/);
      expect(row).not.toHaveTextContent(/budget/);

      // 3,120,000 + 2,721 microdollars: no share on this reading, so the instance figure.
      const total = screen.getByText('Total').closest('.usage__cell');
      expect(total).toHaveTextContent('$3.12');
      expect(total).toHaveTextContent(/instance AWS/);
      expect(screen.queryByText(/not recorded yet/i)).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('says quietly how much of the budget this is, when the Budget has a limit', async () => {
    mountSettings({
      usage: {
        ...USAGE,
        aws: { month_micros: 3_120_000, as_of: new Date().toISOString(), budget_micros: 10_000_000 },
      },
    });

    const row = (await screen.findByText('AWS')).closest('.usage__cell');
    expect(row).toHaveTextContent(/of \$10\.00 budget/);
    expect(row).toHaveTextContent(/as of a moment ago/);
  });

  it('says the AWS cost is not recorded yet, and leaves the Total out, when the API sends null', async () => {
    mountSettings({ usage: { ...USAGE, aws: null } });

    const row = (await screen.findByText('AWS')).closest('.usage__cell');
    expect(row).toHaveTextContent(/not recorded yet/i);
    expect(screen.queryByText('Total')).toBeNull();
    // The providers' figure is still the one on top.
    expect(screen.getByText('$0.003', { selector: '.usage__figure' })).toBeInTheDocument();
  });

  it('never names the instance’s daily cap, even when there is one (U13b)', async () => {
    mountSettings({ settings: { ...STORED, daily_spend_cap_micros: 5_000_000 } });
    await screen.findByText(/no recordings have been processed/i);
    expect(screen.queryByText(/stops taking recordings/i)).toBeNull();
    expect(screen.queryByText(/\bcap\b/i)).toBeNull();
    expect(screen.queryByText('$5.00')).toBeNull();
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

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(/nothing is waiting to sync/i);
    expect(dialog).toHaveTextContent(/ends the session with the identity provider/i);
  });
});

/**
 * The screen is five cards, each a landmark named by its title, so a screen
 * reader's landmark list is the screen's table of contents; the version is a
 * row in the last one rather than a footnote under the whole screen.
 */
describe('the cards', () => {
  it('are five labelled sections in the order a person needs them', async () => {
    mountSettings();
    await screen.findByRole('combobox', { name: /transcription language/i });

    const titles = screen.getAllByRole('heading', { level: 2 }).map((heading) => heading.textContent);
    expect(titles).toEqual([
      'Recording & transcription',
      'Appearance',
      'Usage this month',
      'Passkeys',
      'About & support',
    ]);
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1);
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
