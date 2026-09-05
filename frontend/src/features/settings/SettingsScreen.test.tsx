import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { usage as USAGE } from '@/api/__fixtures__/responses.ts';
import type { SettingsWire, UsageWire } from '@/api/schema.ts';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

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
  overrides: { settings?: SettingsWire; usage?: UsageWire } = {},
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

  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter initialEntries={['/settings']}>
        <SettingsScreen />
      </MemoryRouter>
    </TestProviders>,
  );
  return { puts, fetchImpl };
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
      name: /default transcription language/i,
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
    await screen.findByRole('combobox', { name: /default transcription language/i });
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
    await screen.findByRole('combobox', { name: /default transcription language/i });
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
    const input = await screen.findByRole('spinbutton', { name: /days to keep source audio/i });
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
      name: /default transcription language/i,
    })) as HTMLSelectElement;
    await waitFor(() => {
      expect(select.value).toBe('auto');
    });
    expect(screen.getByText(/mixes two is detected as one of them/i)).toBeInTheDocument();
  });

  it('reads a record from before the field existed as English, and saves nothing for it', async () => {
    const { default_language: _absent, ...legacy } = STORED;
    const { puts } = mountSettings({ settings: legacy });

    const select = (await screen.findByRole('combobox', {
      name: /default transcription language/i,
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

    // 1371 microdollars, three decimals under a dollar.
    expect(await screen.findByText('$0.001', { selector: '.usage__figure' })).toBeInTheDocument();
    expect(screen.getByText('Providers this month')).toBeInTheDocument();
    expect(screen.getByText(/in January 2026/)).toBeInTheDocument();
    expect(screen.getByText('3 calls', { selector: '.usage__summary .numeric' })).toBeInTheDocument();
    expect(screen.getByText('0.5 min', { selector: '.usage__summary .numeric' })).toBeInTheDocument();

    // The stages, in the order a recording meets them.
    const labels = Array.from(document.querySelectorAll('.usage__ops dt'), (term) => term.textContent);
    expect(labels).toEqual(['Transcribe', 'Route', 'Clean up']);
    // Transcribe (311) and route (420) both round to nothing; cleanup (640) does not.
    expect(screen.getAllByText('$0.000', { selector: '.usage__op-figures .numeric' })).toHaveLength(2);
    expect(screen.getAllByText('$0.001', { selector: '.usage__op-figures .numeric' })).toHaveLength(1);
  });

  it('draws a bar per day with usage, described in words for whoever cannot see it', async () => {
    mountSettings({ usage: USAGE });

    const figure = await screen.findByRole('img', { name: /spend by day in January 2026/i });
    expect(figure).toHaveAccessibleName(/2 days with recordings/i);
    // The day is rendered in the runtime's locale ("3 Jan" or "Jan 3"), so only its parts are pinned.
    expect(figure).toHaveAccessibleName(/the most on (3 Jan|Jan 3) at \$0\.001/i);
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

      const aws = await screen.findByText('AWS this month');
      const row = aws.closest('.usage__headline');
      expect(row).toHaveTextContent('$3.12');
      expect(row).toHaveTextContent(/as of 3 hours ago/);
      expect(row).not.toHaveTextContent(/budget/);

      // 3,120,000 + 1,371 microdollars.
      const total = screen.getByText('Total').closest('.usage__headline');
      expect(total).toHaveTextContent('$3.12');
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

    const row = (await screen.findByText('AWS this month')).closest('.usage__headline');
    expect(row).toHaveTextContent(/of \$10\.00 budget/);
    expect(row).toHaveTextContent(/as of a moment ago/);
  });

  it('says the AWS cost is not recorded yet, and leaves the Total out, when the API sends null', async () => {
    mountSettings({ usage: { ...USAGE, aws: null } });

    const row = (await screen.findByText('AWS this month')).closest('.usage__headline');
    expect(row).toHaveTextContent(/AWS cost not recorded yet/);
    expect(screen.queryByText('Total')).toBeNull();
    // The providers' figure is still the one on top.
    expect(screen.getByText('$0.001', { selector: '.usage__figure' })).toBeInTheDocument();
  });

  it('never names the instance’s daily cap, even when there is one (U13b)', async () => {
    mountSettings({ settings: { ...STORED, daily_spend_cap_micros: 5_000_000 } });
    await screen.findByText(/no recordings have been processed/i);
    expect(screen.queryByText(/stops taking recordings/i)).toBeNull();
    expect(screen.queryByText(/\bcap\b/i)).toBeNull();
    expect(screen.queryByText('$5.00')).toBeNull();
  });
});
