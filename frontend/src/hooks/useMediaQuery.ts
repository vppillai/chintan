import { useCallback, useSyncExternalStore } from 'react';

/**
 * Whether a CSS media query matches, kept current as it changes.
 *
 * For the one decision CSS alone cannot make: whether to *render* a control.
 * A pinned row's grip is drawn for a pointer that is fine and not otherwise —
 * a phone user has a press-and-hold instead, and a grip on every pinned row
 * that CSS merely hid would still be in the tab order and the accessibility
 * tree of a device that never shows it; the swipe tray is the same decision
 * the other way round.
 *
 * `useSyncExternalStore`, because a `MediaQueryList` is exactly an external
 * store: subscribe to its `change`, read `matches` for the snapshot. On the
 * server, or anywhere without `matchMedia`, it does not match.
 */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
        return () => {};
      }
      const list = window.matchMedia(query);
      list.addEventListener('change', onChange);
      return () => {
        list.removeEventListener('change', onChange);
      };
    },
    [query],
  );
  const read = useCallback(() => matchNow(query), [query]);
  return useSyncExternalStore(subscribe, read, () => false);
}

function matchNow(query: string): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false;
  return window.matchMedia(query).matches;
}
