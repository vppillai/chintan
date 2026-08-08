import { useEffect, useRef, type RefObject } from 'react';
import { useLocation } from 'react-router';

/**
 * Moves focus to the routed region on navigation (spec §5.7).
 *
 * Without this a screen reader stays parked wherever the user tapped and a
 * keyboard user's next Tab resumes at the top of the document, which in a
 * bottom-navigation app means tabbing through the whole shell to reach the
 * content that just appeared.
 *
 * The first render is skipped: stealing focus on load is its own bug.
 */
export function useRouteFocus(target: RefObject<HTMLElement | null>): void {
  const { pathname } = useLocation();
  const firstRender = useRef(true);

  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false;
      return;
    }
    target.current?.focus({ preventScroll: true });
  }, [pathname, target]);
}
