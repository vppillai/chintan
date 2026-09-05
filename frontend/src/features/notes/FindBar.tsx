import {
  Children,
  cloneElement,
  createElement,
  isValidElement,
  useEffect,
  useLayoutEffect,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from 'react';

import { Icon } from '@/components/Icon.tsx';

import {
  FIND_MATCH_CAP,
  describeMatches,
  findMatches,
  splitRuns,
  type FindMatch,
} from './find.ts';

/**
 * Find in this note.
 *
 * A bar under the tab strip — query, "3 of 12", previous, next, close — that
 * searches whichever panel is open. The bar owns none of the text: the panel
 * beneath it finds the matches in what it shows, highlights them, and reports
 * how many there are through `FindTarget.onTotal`; the bar counts from that
 * and the screen's reducer (`find.ts`) moves the active match. The same bar
 * therefore works over the body, which is a textarea, and over the cleaned
 * view, which is a tree of elements, without knowing which it is looking at.
 *
 * Enter is next and Shift+Enter previous, as in every browser's find; Escape
 * closes. The input is a search field, autofocused when the bar opens, so
 * opening it is already typing.
 */
export function FindBar({
  id,
  query,
  active,
  total,
  onQueryChange,
  onNext,
  onPrevious,
  onClose,
  inputRef,
  disabled = false,
  hint,
}: {
  id: string;
  query: string;
  active: number;
  total: number;
  onQueryChange: (query: string) => void;
  onNext: () => void;
  onPrevious: () => void;
  onClose: () => void;
  /** So the screen can put the caret back in the bar on Ctrl/⌘+F while it is open. */
  inputRef?: RefObject<HTMLInputElement | null> | undefined;
  /** The open panel has nothing to search; the bar stays, greyed, and says why. */
  disabled?: boolean;
  hint?: string | undefined;
}) {
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>): void => {
    if (event.key === 'Enter') {
      event.preventDefault();
      if (event.shiftKey) onPrevious();
      else onNext();
    } else if (event.key === 'Escape') {
      // WebKit clears a search field on Escape; closing is the whole gesture.
      event.preventDefault();
      onClose();
    }
  };

  const count = disabled ? '' : describeMatches(query, active, total);
  const canStep = !disabled && total > 0;

  return (
    <div id={id} className="find-bar" role="search" aria-label="Find in note">
      <div className="find-bar__row">
        <input
          ref={inputRef}
          type="search"
          className="find-bar__input"
          aria-label="Find in note"
          placeholder="Find in note"
          value={query}
          disabled={disabled}
          autoFocus
          autoComplete="off"
          spellCheck={false}
          enterKeyHint="search"
          onChange={(event) => {
            onQueryChange(event.target.value);
          }}
          onKeyDown={onKeyDown}
        />
        {/* Read out as it changes — a screen reader hears "3 of 12" after Enter. */}
        <span className="find-bar__count numeric" role="status" aria-live="polite">
          {count}
        </span>
        <button
          type="button"
          className="find-bar__button"
          aria-label="Previous match"
          disabled={!canStep}
          onClick={onPrevious}
        >
          <Icon name="chevron-up" size={18} />
        </button>
        <button
          type="button"
          className="find-bar__button"
          aria-label="Next match"
          disabled={!canStep}
          onClick={onNext}
        >
          <Icon name="chevron-down" size={18} />
        </button>
        <button type="button" className="find-bar__button" aria-label="Close find" onClick={onClose}>
          <Icon name="close" size={18} />
        </button>
      </div>
      {hint && <p className="find-bar__hint">{hint}</p>}
    </div>
  );
}

/**
 * What a panel is asked to find. `null` when the bar is closed or empty, so a
 * panel renders exactly as it did before the bar existed.
 */
export interface FindTarget {
  query: string;
  /** The match to show as current, counting every match in the panel from 0. */
  active: number;
  /** Told how many matches the panel's text holds, whenever that changes. */
  onTotal: (total: number) => void;
}

/**
 * Tells the bar how many matches there are. In an effect rather than during
 * render because the count is the bar's state and this is the panel's render;
 * `onTotal` is stable and the reducer ignores an unchanged total, so this
 * settles in one pass.
 */
export function useReportTotal(find: FindTarget | null, total: number): void {
  const onTotal = find?.onTotal;
  useEffect(() => {
    onTotal?.(total);
  }, [onTotal, total]);
}

/**
 * Brings the active match into the middle of the view whenever it changes.
 * Looked up rather than held in a ref because the cleaned view's marks are
 * produced by walking a tree, where a ref has nowhere to live. Instant, not
 * smooth: the browser's own find jumps, and a jump honours reduced motion by
 * construction.
 */
export function useScrollToActiveMatch(
  container: RefObject<HTMLElement | null>,
  active: number | null,
  total: number,
): void {
  useLayoutEffect(() => {
    if (active === null || total === 0) return;
    const mark = container.current?.querySelector<HTMLElement>('mark[data-active]');
    // jsdom has no scrollIntoView; nothing to do there is the right thing.
    if (mark && typeof mark.scrollIntoView === 'function') {
      mark.scrollIntoView({ block: 'center', inline: 'nearest' });
    }
  }, [container, active, total]);
}

/**
 * A text with its matches marked: `<mark>` on every match and `data-active`
 * on the one the bar is on. `first` numbers this text's matches after those
 * of the texts before it in the same document.
 */
export function markMatches(
  text: string,
  matches: readonly FindMatch[],
  active: number,
  first = 0,
): ReactNode {
  if (matches.length === 0) return text;
  return splitRuns(text, matches, first).map((run, index) =>
    run.match === null
      ? run.text
      : createElement(
          'mark',
          {
            key: index,
            className: 'find-match',
            'data-active': run.match === active ? true : undefined,
          },
          run.text,
        ),
  );
}

/**
 * The cleaned view's rendered Markdown with its matches marked.
 *
 * Walks the element tree and searches each string where it stands, so a
 * match inside `<strong>` or a list item is marked in place and nothing
 * about the document's shape changes. Each text node is searched on its
 * own: a query that spans the boundary between "nine" in bold and " hundred"
 * after it is not found, which is the honest limit of marking a tree rather
 * than flattening it and guessing the elements back. Matches are numbered
 * in document order, so `active` means the same thing here as in the bar.
 */
export function markTree(
  nodes: ReactNode,
  query: string,
  active: number,
): { nodes: ReactNode; total: number } {
  let total = 0;

  const walk = (node: ReactNode): ReactNode => {
    if (typeof node === 'string') {
      const matches = findMatches(node, query, FIND_MATCH_CAP - total);
      if (matches.length === 0) return node;
      const first = total;
      total += matches.length;
      return markMatches(node, matches, active, first);
    }
    if (Array.isArray(node)) {
      // `Children.map` keeps the children's keys, so React sees the same list.
      return Children.map(node, walk);
    }
    if (isValidElement<{ children?: ReactNode }>(node)) {
      const { children } = node.props;
      if (children === undefined || children === null) return node;
      return cloneElement(node, undefined, walk(children));
    }
    return node;
  };

  const marked = query === '' ? nodes : walk(nodes);
  return { nodes: marked, total };
}
