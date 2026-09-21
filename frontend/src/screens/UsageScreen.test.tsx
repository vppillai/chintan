import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, RouterProvider, createMemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

import { usageRich as USAGE } from '@/api/__fixtures__/pending.ts';
import { usage as USAGE_TODAY } from '@/api/__fixtures__/responses.ts';
import type { UsageWire } from '@/api/schema.ts';
import { routes } from '@/app/router.tsx';
import { TestProviders, testApiContext } from '@/test/providers.tsx';

import { UsageScreen } from './UsageScreen.tsx';

const NO_USAGE: UsageWire = { month: '2026-09', cost_micros: 0, calls: 0, ops: {}, days: [] };

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });
}

/** The usage screen over a stub API answering `GET /v1/usage`. */
function mountUsage(overrides: { usage?: UsageWire } = {}) {
  const fetchImpl = vi.fn<typeof fetch>(async (input) => {
    const url = new URL(String(input));
    if (url.pathname.endsWith('/v1/usage')) return json(overrides.usage ?? NO_USAGE);
    return json({ items: [] });
  });
  render(
    <TestProviders api={testApiContext(fetchImpl)}>
      <MemoryRouter initialEntries={['/usage']}>
        <UsageScreen />
      </MemoryRouter>
    </TestProviders>,
  );
}

/**
 * The Usage card was a third of You and buried Passkeys and About beneath it
 * (round-3 T20); it is a screen of its own now, reached from one row on You,
 * and everything it says is tested here exactly as it was there.
 */
describe('getting there and back', () => {
  it('is a real route, reached from the Usage row on You, and Back returns to You', async () => {
    const router = createMemoryRouter(routes, { initialEntries: ['/settings'] });
    render(
      <TestProviders>
        <RouterProvider router={router} />
      </TestProviders>,
    );

    await userEvent.click(await screen.findByRole('link', { name: /usage this month/i }));

    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/usage');
    });
    expect(await screen.findByRole('heading', { name: 'Usage', level: 1 })).toBeInTheDocument();
    expect(screen.getByRole('main')).toHaveAttribute('aria-label', 'Usage');

    await userEvent.click(screen.getByRole('link', { name: /back to\s*you/i }));
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/settings');
    });
  });
});


/**
 * "Usage this month" replaces the read-only spend-cap sentence (backlog U13):
 * the cap is one number for the whole instance and said nothing about the
 * person; what their own recordings cost does.
 */
describe('the usage screen', () => {
  it('shows the month’s providers figure in dollars, the split by stage, and the calls and minutes', async () => {
    mountUsage({ usage: USAGE });

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
    mountUsage({ usage: USAGE });
    await screen.findByText('Providers');

    const labels = Array.from(document.querySelectorAll('.usage__providers dt'), (t) => t.textContent);
    expect(labels).toEqual(['Language model (MiniMax)', 'Groq']);
    const minimax = screen.getByText('Language model (MiniMax)').closest('.usage__provider');
    expect(minimax).toHaveTextContent('$0.002');
    expect(minimax).toHaveTextContent('4 calls');
    // The rows add up to the figure, so there is nothing unattributed to show.
    expect(screen.queryByText(/earlier this month/i)).toBeNull();
  });

  it('shows what the provider rows do not account for, when the split began part-way through the month', async () => {
    // The per-provider counters were added after the month's total had been
    // accumulating; for that month the rows sum to less than the figure, and
    // the difference is a line of its own rather than a puzzle. Derived from
    // the data, so it disappears once every row carries a provider.
    mountUsage({
      usage: { ...USAGE, providers: { minimax: { calls: 2, cost_micros: 1200 } } },
    });
    await screen.findByText('Providers');

    const rest = screen.getByText(/earlier this month/i).closest('.usage__provider');
    // 2,721 − 1,200 microdollars.
    expect(rest).toHaveTextContent('$0.002');
    expect(rest).toHaveClass('usage__provider--unattributed');
  });

  it('labels the day strip in print as well as for a screen reader: the ends and a caption, no "Tallest bar"', async () => {
    mountUsage({ usage: USAGE });
    const chart = (await screen.findByRole('img', { name: /spend by day/i })).closest('.usage__chart');

    // The peak is in the figure's description and on the bar's hover; the
    // printed "Tallest bar" line was operator-speak (round-3 T20).
    expect(chart?.querySelector('.usage__chart-scale')).toBeNull();
    expect(chart?.textContent).not.toMatch(/tallest bar/i);
    const caption = chart?.querySelector('.usage__chart-caption');
    expect(caption).toHaveTextContent(/^(1 Jan|Jan 1)/);
    expect(caption).toHaveTextContent(/(31 Jan|Jan 31)$/);
    expect(caption).toHaveTextContent(/spend by day, today in colour · dots mark API requests/i);
  });

  it('lists the month’s API requests and what is stored, as one row of facts', async () => {
    mountUsage({ usage: USAGE });
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
    mountUsage({ usage: { ...USAGE, storage: { ...USAGE.storage!, approximate: true } } });
    const facts = await screen.findByRole('group', { name: 'This month' });
    expect(facts).toHaveTextContent(/41 recordings · 23.2 min · 9.1 MB · approx\./);
    expect(facts).toHaveTextContent(/12 notes · approx\./);
  });

  it('shows the user’s estimated share of AWS and totals with that, saying so', async () => {
    mountUsage({ usage: USAGE });

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
    mountUsage({ usage: USAGE });
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
    mountUsage({ usage: legacy });

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
    mountUsage({ usage: USAGE });

    const figure = await screen.findByRole('img', { name: /spend by day in January 2026/i });
    expect(figure).toHaveAccessibleName(/2 days with recordings/i);
    // The day is rendered in the runtime's locale ("4 Jan" or "Jan 4"), so only its parts are pinned.
    expect(figure).toHaveAccessibleName(/the most on (4 Jan|Jan 4) at \$0\.002/i);
    // Two rows, two bars — the empty days are not drawn as marks.
    expect(figure.querySelectorAll('.usage__bar')).toHaveLength(2);
  });

  it('says plainly when nothing has been processed yet, instead of an empty chart', async () => {
    mountUsage();
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
      mountUsage({
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
    mountUsage({
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
    mountUsage({ usage: { ...USAGE, aws: null } });

    const row = (await screen.findByText('AWS')).closest('.usage__cell');
    expect(row).toHaveTextContent(/not recorded yet/i);
    expect(screen.queryByText('Total')).toBeNull();
    // The providers' figure is still the one on top.
    expect(screen.getByText('$0.003', { selector: '.usage__figure' })).toBeInTheDocument();
  });
});
