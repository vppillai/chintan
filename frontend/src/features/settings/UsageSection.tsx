import { useUsage } from '@/api/queries.ts';
import type { UsageWire } from '@/api/schema.ts';

import { SettingsCard } from './SettingsCard.tsx';
import {
  asOfLabel,
  combinedMicros,
  dayBars,
  dayLabel,
  formatAudioMinutes,
  formatCalls,
  formatCount,
  formatDollars,
  formatMegabytes,
  formatRequests,
  monthLabel,
  opRows,
  providerRows,
  totalBasis,
} from './usage.ts';

/**
 * "Usage this month", on You.
 *
 * This replaces the read-only spend-cap sentence (backlog U13): the cap is one
 * number for the whole instance, set in the deploy config, and telling a
 * person about a ceiling they cannot move told them nothing about themselves.
 * What they can act on is what their own recordings cost — the month's total,
 * the split by pipeline stage, and a bar per day — from `GET /v1/usage`, the
 * per-tenant accounting the worker writes on every priced provider call.
 *
 * The card reads top to bottom the way a bill does: the month as an eyebrow,
 * the providers' figure large in the serif numerals, the calls and minutes it
 * bought; then three cells — Providers (split by provider), AWS, Total — as
 * one compact grid rather than three stacked headlines, which is what made
 * the section a page long; then the per-stage rows as a quiet table, the
 * day-by-day strip, and the facts about what is stored.
 *
 * Beneath the providers' figure sits what AWS charges to run the instance
 * (D6b): the whole instance's month to date, read from the stack's Budget by
 * a daily worker task, so it carries an as-of and, when the Budget has a
 * limit, how much of it this is. Since N11 the backend also apportions that
 * bill by each user's share of the instance's provider spend, and when it
 * does, the Total is providers plus *that* share — a number about this
 * person — rather than providers plus everyone's AWS. The cell says which.
 *
 * N11 adds the rest of the picture: the split by provider under Providers,
 * the two hand-asked calls (Clean note, Ask) among the stages, and the facts
 * — API requests this month, what is stored — so "what does this cost" and
 * "what am I keeping" are answered on one screen. Every one of those is
 * optional on the wire here, because this screen may ship before the backend
 * that sends them; each simply does not appear until it does, and a fact the
 * backend learns to send later is one more row in the same list.
 *
 * The instance's daily spend cap is not named here at all (U13b): it is an
 * operator's runaway guard, set far above any real day, and a sentence about
 * it beneath someone's own cents read as a warning. About says, in one line,
 * that a cap exists and what happens if it is ever reached.
 */
export function UsageSection() {
  const { data, isLoading, isError, refetch } = useUsage();

  return (
    <SettingsCard
      title="Usage this month"
      className="you-card--usage"
      foot={
        <p>
          Providers are what transcription and the language model charged for this
          account&rsquo;s recordings and questions. AWS is what it costs to run the instance for
          everyone using it, updated once a day; your share of it is estimated from your part of
          the provider spend.
        </p>
      }
    >
      {isLoading && (
        <p className="usage__state" role="status">
          Adding up this month…
        </p>
      )}

      {isError && (
        <p className="usage__state" role="alert">
          Couldn&rsquo;t load this month&rsquo;s usage.{' '}
          <button
            type="button"
            className="text-link"
            onClick={() => {
              void refetch();
            }}
          >
            Try again
          </button>
        </p>
      )}

      {data && <Usage usage={data} />}
    </SettingsCard>
  );
}

function Usage({ usage }: { usage: UsageWire }) {
  const rows = opRows(usage.ops);
  const providers = providerRows(usage.providers);
  const aws = usage.aws ?? null;
  const basis = totalBasis(aws);
  const total = combinedMicros(usage, aws);
  const share = aws?.share_micros ?? null;
  const storage = usage.storage;
  const requests = usage.api?.requests;
  const hasFacts = requests !== undefined || storage !== undefined;

  return (
    <div className="usage">
      <div className="usage__head">
        <p className="usage__month eyebrow">{monthLabel(usage.month)}</p>
        <p className="usage__figure numeric">{formatDollars(usage.cost_micros)}</p>
        {usage.calls === 0 ? (
          <p className="usage__summary">No recordings have been processed this month yet.</p>
        ) : (
          <p className="usage__summary">
            <span className="numeric">{formatCalls(usage.calls)}</span>
            {' · '}
            <span className="numeric">{formatAudioMinutes(usage.audio_seconds)}</span> of audio
          </p>
        )}
      </div>

      <dl className="usage__cells">
        <div className="usage__cell">
          <dt className="usage__cell-label">Providers</dt>
          <dd className="usage__cell-figure numeric">{formatDollars(usage.cost_micros)}</dd>
          {providers.length > 0 && (
            <dd className="usage__cell-detail">
              <dl className="usage__providers">
                {providers.map(({ key, label, totals }) => (
                  <div key={key} className="usage__provider">
                    <dt className="usage__provider-label">{label}</dt>
                    <dd className="usage__provider-figures">
                      <span className="numeric">{formatDollars(totals.cost_micros)}</span>
                      {' · '}
                      <span className="numeric">{formatCalls(totals.calls)}</span>
                    </dd>
                  </div>
                ))}
              </dl>
            </dd>
          )}
        </div>

        <div className="usage__cell">
          <dt className="usage__cell-label">AWS</dt>
          {aws ? (
            <>
              <dd className="usage__cell-figure numeric">{formatDollars(aws.month_micros)}</dd>
              <dd className="usage__cell-detail">
                {asOfLabel(aws.as_of)}
                {aws.budget_micros !== null && (
                  <>
                    {' · '}of <span className="numeric">{formatDollars(aws.budget_micros)}</span>{' '}
                    budget
                  </>
                )}
                {share !== null && (
                  <>
                    <br />
                    Your estimated share: <span className="numeric">{formatDollars(share)}</span>{' '}
                    (by provider spend)
                  </>
                )}
              </dd>
            </>
          ) : (
            <dd className="usage__cell-detail">Not recorded yet</dd>
          )}
        </div>

        {total !== null && (
          <div className="usage__cell usage__cell--total">
            <dt className="usage__cell-label">Total</dt>
            <dd className="usage__cell-figure numeric">{formatDollars(total)}</dd>
            <dd className="usage__cell-detail">
              {basis === 'share' ? 'providers + your AWS share' : 'providers + instance AWS'}
            </dd>
          </div>
        )}
      </dl>

      {usage.calls > 0 && (
        <>
          <dl className="usage__ops">
            {rows.map(({ key, label, totals }) => (
              <div key={key} className="usage__op">
                <dt className="usage__op-label">{label}</dt>
                <dd className="usage__op-figures">
                  <span className="numeric">{formatDollars(totals.cost_micros)}</span>
                  {' · '}
                  <span className="numeric">{formatCalls(totals.calls)}</span>
                  {totals.audio_seconds !== undefined && (
                    <>
                      {' · '}
                      <span className="numeric">{formatAudioMinutes(totals.audio_seconds)}</span>
                    </>
                  )}
                </dd>
              </div>
            ))}
          </dl>

          <Sparkline usage={usage} />
        </>
      )}

      {hasFacts && (
        <dl className="usage__facts" role="group" aria-label="This month">
          {requests !== undefined && (
            <div className="usage__fact">
              <dt className="usage__fact-label">API requests</dt>
              <dd className="usage__fact-figures">
                <span className="numeric">{formatRequests(requests)}</span>
              </dd>
            </div>
          )}
          {storage && (
            <>
              <div className="usage__fact">
                <dt className="usage__fact-label">Recordings stored</dt>
                <dd className="usage__fact-figures">
                  <span className="numeric">{formatCount(storage.recordings, 'recording')}</span>
                  {' · '}
                  <span className="numeric">{formatAudioMinutes(storage.audio_seconds)}</span>
                  {' · '}
                  <span className="numeric">{formatMegabytes(storage.audio_bytes)}</span>
                  {storage.approximate && ' · approx.'}
                </dd>
              </div>
              <div className="usage__fact">
                <dt className="usage__fact-label">Notes</dt>
                <dd className="usage__fact-figures">
                  <span className="numeric">{formatCount(storage.notes, 'note')}</span>
                  {storage.approximate && ' · approx.'}
                </dd>
              </div>
            </>
          )}
        </dl>
      )}
    </div>
  );
}

/** Drawing constants, in SVG user units: a thin bar, a two-unit gap, a low strip. */
const BAR = 6;
const GAP = 2;
const HEIGHT = 32;
/** The request dot's radius; it sits in the strip's top band, above the bars' floor. */
const DOT = 1.2;

/**
 * A bar per day of the month, cost as height, today in the accent — and,
 * where the backend counts them, a dot per day for that day's API requests,
 * on its own scale above the bars, so a busy day of reading (many requests,
 * no cost) still leaves a mark.
 *
 * Inline SVG with theme tokens for fills, so it is one colour system with the
 * rest of the screen in both themes. Every bar carries a `<title>` for hover
 * that names both figures, and the whole figure is described in words for
 * anyone who cannot see it — the numbers themselves are already in the text
 * above, so the description says what the picture adds: which day was the
 * biggest, and how many requests the month took.
 */
function Sparkline({ usage }: { usage: UsageWire }) {
  const bars = dayBars(usage);
  if (bars.length === 0) return null;
  const max = Math.max(...bars.map((bar) => bar.costMicros), 1);
  const maxRequests = Math.max(...bars.map((bar) => bar.apiRequests), 0);
  const width = bars.length * (BAR + GAP) - GAP;
  const peak = bars.reduce((best, bar) => (bar.costMicros > best.costMicros ? bar : best), bars[0]!);
  const active = bars.filter((bar) => bar.costMicros > 0).length;
  const requests = bars.reduce((sum, bar) => sum + bar.apiRequests, 0);

  return (
    <figure className="usage__chart">
      <svg
        className="usage__spark"
        viewBox={`0 0 ${String(width)} ${String(HEIGHT)}`}
        role="img"
        aria-label={`Spend by day in ${monthLabel(usage.month)}: ${String(active)} ${
          active === 1 ? 'day' : 'days'
        } with recordings, the most on ${dayLabel(peak.date)} at ${formatDollars(peak.costMicros)}.${
          maxRequests > 0 ? ` ${formatRequests(requests)} to the API, shown as dots.` : ''
        }`}
        preserveAspectRatio="none"
      >
        <line className="usage__baseline" x1={0} x2={width} y1={HEIGHT - 0.5} y2={HEIGHT - 0.5} />
        {bars.map((bar, index) => {
          if (bar.costMicros === 0) return null;
          // Never shorter than two units, so a day that cost a tenth of a cent
          // is still a mark rather than a rumour.
          const height = Math.max(2, Math.round((bar.costMicros / max) * (HEIGHT - 2)));
          return (
            <rect
              key={bar.date}
              className="usage__bar"
              data-today={bar.today || undefined}
              x={index * (BAR + GAP)}
              y={HEIGHT - height}
              width={BAR}
              height={height}
              rx={1}
            >
              <title>{`${dayLabel(bar.date)} · ${formatDollars(bar.costMicros)} · ${formatCalls(
                bar.calls,
              )}${bar.apiRequests > 0 ? ` · ${formatRequests(bar.apiRequests)}` : ''}`}</title>
            </rect>
          );
        })}
        {maxRequests > 0 &&
          bars.map((bar, index) => {
            if (bar.apiRequests === 0) return null;
            // The busiest day sits at the top of the strip, the quietest just
            // above the baseline; the dot never touches the floor so it cannot
            // be read as a very small bar.
            const y = HEIGHT - 2 - DOT - (bar.apiRequests / maxRequests) * (HEIGHT - 4 - 2 * DOT);
            return (
              <circle
                key={bar.date}
                className="usage__api-dot"
                cx={index * (BAR + GAP) + BAR / 2}
                cy={y}
                r={DOT}
              >
                <title>{`${dayLabel(bar.date)} · ${formatRequests(bar.apiRequests)}`}</title>
              </circle>
            );
          })}
      </svg>
      <figcaption className="usage__chart-caption">
        <span>{dayLabel(bars[0]!.date)}</span>
        <span>Spend by day{maxRequests > 0 ? ' · dots are API requests' : ''}</span>
        <span>{dayLabel(bars[bars.length - 1]!.date)}</span>
      </figcaption>
    </figure>
  );
}
