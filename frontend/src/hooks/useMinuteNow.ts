import { useEffect, useState } from 'react';

/**
 * The clock a row's age ("· 2 min", "after 12 min") is read against, re-read
 * once a minute while `active`. A row's data does not change just because
 * time passes — a poll that answers the same row twice does not re-render it
 * — so without a tick the age would stand still until something else
 * changed. Off, it is the time of the last tick, or of the mount.
 */
export function useMinuteNow(active: boolean): number {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => setNow(Date.now()), 60_000);
    return () => {
      clearInterval(timer);
    };
  }, [active]);
  return now;
}
