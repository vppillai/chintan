import { useEffect, useRef, useState } from 'react';

/**
 * The next page, fetched as the reader approaches the end of this one.
 *
 * An `IntersectionObserver` on a zero-height sentinel after the last row,
 * rooted on the shell's scroll container and given a bottom margin of one
 * viewport, so the request goes out while there is still a screen of rows to
 * read rather than when the list has visibly run out. The observer fires
 * again on its own once the new page has rendered and the sentinel is still
 * within reach, which is what makes a short page chain into the next without
 * a scroll event of its own.
 *
 * Rooted on `.app__main` deliberately: with the default (viewport) root a
 * sentinel below the scroll container's bottom edge is clipped by it and
 * never intersects, however large the root margin.
 *
 * The button is still there for a keyboard or a screen reader — a list that
 * only grows when it is scrolled is a list a screen reader cannot finish —
 * visually hidden until it takes focus, and shown outright where there is no
 * observer to do the work.
 */
export function LoadMore({
  hasMore,
  loading,
  onLoad,
}: {
  hasMore: boolean;
  loading: boolean;
  onLoad: () => void;
}) {
  const sentinelRef = useRef<HTMLDivElement>(null);
  const [observed] = useState(() => typeof IntersectionObserver !== 'undefined');

  useEffect(() => {
    const sentinel = sentinelRef.current;
    if (!sentinel || !hasMore || loading || !observed) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) onLoad();
      },
      { root: sentinel.closest('.app__main'), rootMargin: '0px 0px 100% 0px' },
    );
    observer.observe(sentinel);
    return () => {
      observer.disconnect();
    };
  }, [hasMore, loading, onLoad, observed]);

  if (!hasMore) return null;

  return (
    <>
      <div ref={sentinelRef} className="load-more-sentinel" aria-hidden="true" />
      <button
        type="button"
        className={observed ? 'load-more visually-hidden' : 'load-more'}
        onClick={onLoad}
        disabled={loading}
      >
        {loading ? 'Loading…' : 'Load more'}
      </button>
    </>
  );
}
