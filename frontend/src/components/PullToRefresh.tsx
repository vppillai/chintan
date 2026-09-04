import { pullLabel, usePullToRefresh, type PullToRefreshOptions } from '@/hooks/usePullToRefresh.ts';

/**
 * Pull-to-refresh for the screen this sits at the top of.
 *
 * Renders the small serif line that appears under a pull — "Pull to refresh",
 * "Release to refresh", "Refreshing…" — and owns the gesture: it listens on
 * the shell's scroll container it finds itself inside, and grows with the
 * finger. Zero-height when idle, so it costs the list nothing until asked.
 *
 * `onRefresh` should settle when the data has been asked for again; the
 * indicator lets go when it does.
 */
export function PullToRefresh({
  onRefresh,
  enabled,
}: { onRefresh: () => Promise<unknown> } & PullToRefreshOptions) {
  const { ref, phase } = usePullToRefresh(onRefresh, enabled === undefined ? {} : { enabled });

  return (
    <div ref={ref} className="pull-refresh" data-phase={phase} role="status" aria-live="polite">
      {/* Only announced once it is actually refreshing: the two pulling
          states are visual feedback for a finger already on the screen. */}
      <span className="pull-refresh__label" aria-hidden={phase !== 'refreshing'}>
        {pullLabel(phase)}
      </span>
    </div>
  );
}
