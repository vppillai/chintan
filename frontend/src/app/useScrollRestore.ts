import { useEffect, useLayoutEffect, useRef, type RefObject } from 'react';
import { useLocation, useNavigationType } from 'react-router';

/** Under the app's prefix, so sign-out's sweep of `sessionStorage` takes these too. */
const PREFIX = 'chintan.scroll.';

/** About a second at 60 Hz: long enough for a lazy screen and cached rows to mount. */
const RESTORE_FRAMES = 60;

function read(key: string): number {
  try {
    return Number(sessionStorage.getItem(PREFIX + key)) || 0;
  } catch {
    // Storage denied: the place is simply not remembered.
    return 0;
  }
}

function write(key: string, top: number): void {
  try {
    sessionStorage.setItem(PREFIX + key, String(Math.round(top)));
  } catch {
    /* Storage denied. */
  }
}

/**
 * Navigation state asking for the offset the path was last left at, for a
 * return that is a new history entry rather than a Back: the capture screen
 * replaces its own entry with the note it recorded into, so the note comes
 * back under a key it has never had (R7-6b).
 */
export const RESTORE_SCROLL = { restoreScroll: true } as const;

function asksToRestore(state: unknown): boolean {
  return typeof state === 'object' && state !== null && 'restoreScroll' in state;
}

/**
 * Puts the scroll region back where it was when the user comes Back to a
 * screen.
 *
 * The list scrolls inside `.app__main`, not the window, so the browser's own
 * scroll restoration never sees it, and Back from a note used to land the
 * library at the top. The position is kept per history entry
 * (`location.key`), which is what Back returns to, in `sessionStorage` so it
 * survives a reload of the tab.
 *
 * The rows are not there on the first frame (the screen may be lazy, and the
 * cached pages mount a frame or two later), so a restore is retried each
 * frame until the region is tall enough to hold the offset or a second has
 * gone by.
 */
export function useScrollRestore(main: RefObject<HTMLElement | null>): void {
  const location = useLocation();
  const navigationType = useNavigationType();
  // The region's offset as of the last scroll event. Read in the layout
  // cleanup below, when the new screen is already committed and the region
  // may have been clamped to its shorter content.
  const position = useRef(0);

  useEffect(() => {
    // On the document in the capture phase, because the shell swaps its
    // `<main>` element between the signed-out and signed-in trees.
    const onScroll = (event: Event) => {
      const region = main.current;
      if (region && event.target === region) position.current = region.scrollTop;
    };
    document.addEventListener('scroll', onScroll, { capture: true, passive: true });
    return () => {
      document.removeEventListener('scroll', onScroll, { capture: true });
    };
  }, [main]);

  useLayoutEffect(() => {
    const key = location.key;
    const path = `path:${location.pathname}`;
    let frame = 0;
    const target =
      navigationType === 'POP' ? read(key) : asksToRestore(location.state) ? read(path) : 0;
    position.current = main.current?.scrollTop ?? 0;

    const restore = (attempt: number) => {
      const region = main.current;
      if (!region) return;
      region.scrollTop = target;
      position.current = region.scrollTop;
      if (Math.abs(region.scrollTop - target) > 1 && attempt < RESTORE_FRAMES) {
        frame = requestAnimationFrame(() => {
          restore(attempt + 1);
        });
      }
    };
    if (target > 0) restore(0);

    return () => {
      cancelAnimationFrame(frame);
      write(key, position.current);
      write(path, position.current);
    };
    // `location.state` belongs to the entry `location.key` names.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.key, navigationType, main]);
}
