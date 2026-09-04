import { useId } from 'react';

import { useUsage } from '@/api/queries.ts';
import type { UsageWire } from '@/api/schema.ts';

import {
  dayBars,
  dayLabel,
  formatAudioMinutes,
  formatCalls,
  formatDollars,
  monthLabel,
  opRows,
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
 * The cap survives as one quiet line beneath, only when it is non-zero: it is
 * still the reason a capture can stop with `spend_capped`, so it belongs next
 * to the numbers it is compared against.
 */
export function UsageSection({ dailyCapMicros = 0 }: { dailyCapMicros?: number }) {
  const headingId = useId();
  const { data, isLoading, isError } = useUsage();

  return (
    <section className="settings-group" aria-labelledby={headingId}>
      <h2 id={headingId} className="settings-group__title">
        Usage this month
      </h2>

      {isLoading && (
        <p className="settings-group__note" role="status">
          Adding up this month…
        </p>
      )}

      {isError && (
        <p className="settings-group__note" role="alert">
          Couldn&rsquo;t load this month&rsquo;s usage.
        </p>
      )}

      {data && <Usage usage={data} />}

      <p className="settings-group__note">
        Provider cost only — what transcription and the language model charged for this
        account&rsquo;s recordings. It does not include what AWS charges to run the instance.
      </p>

      {dailyCapMicros > 0 && (
        <p className="settings-group__note">
          This instance stops taking recordings after{' '}
          <span className="numeric">{formatDollars(dailyCapMicros)}</span> of provider spend in a
          day, across everyone using it, and says so rather than failing vaguely.
        </p>
      )}
    </section>
  );
}

function Usage({ usage }: { usage: UsageWire }) {
  const rows = opRows(usage.ops);

  return (
    <div className="usage">
      <p className="usage__total">
        <span className="numeric usage__figure">{formatDollars(usage.cost_micros)}</span>{' '}
        <span className="usage__month">in {monthLabel(usage.month)}</span>
      </p>

      {usage.calls === 0 ? (
        <p className="settings-group__note">No recordings have been processed this month yet.</p>
      ) : (
        <>
          <p className="usage__summary">
            <span className="numeric">{formatCalls(usage.calls)}</span>
            {' · '}
            <span className="numeric">{formatAudioMinutes(usage.audio_seconds)}</span> of audio
          </p>

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
    </div>
  );
}

/** Drawing constants, in SVG user units: a thin bar, a two-unit gap, a low strip. */
const BAR = 6;
const GAP = 2;
const HEIGHT = 32;

/**
 * A bar per day of the month, cost as height, today in the accent.
 *
 * Inline SVG with theme tokens for fills, so it is one colour system with the
 * rest of the screen in both themes. Every bar carries a `<title>` for hover,
 * and the whole figure is described in words for anyone who cannot see it —
 * the numbers themselves are already in the text above, so the description
 * says what the picture adds: which day was the biggest.
 */
function Sparkline({ usage }: { usage: UsageWire }) {
  const bars = dayBars(usage);
  if (bars.length === 0) return null;
  const max = Math.max(...bars.map((bar) => bar.costMicros), 1);
  const width = bars.length * (BAR + GAP) - GAP;
  const peak = bars.reduce((best, bar) => (bar.costMicros > best.costMicros ? bar : best), bars[0]!);
  const active = bars.filter((bar) => bar.costMicros > 0).length;

  return (
    <svg
      className="usage__spark"
      viewBox={`0 0 ${String(width)} ${String(HEIGHT)}`}
      role="img"
      aria-label={`Spend by day in ${monthLabel(usage.month)}: ${String(active)} ${
        active === 1 ? 'day' : 'days'
      } with recordings, the most on ${dayLabel(peak.date)} at ${formatDollars(peak.costMicros)}.`}
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
            <title>{`${dayLabel(bar.date)} · ${formatDollars(bar.costMicros)} · ${formatCalls(bar.calls)}`}</title>
          </rect>
        );
      })}
    </svg>
  );
}
