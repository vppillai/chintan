import { useEffect, useId, useRef, useState, type ReactNode } from 'react';

import { ASK_MODE, ROUTES } from '@/app/routes.ts';
import { historyIndex, useTabNavigation } from '@/app/useTabNavigation.ts';
import { Icon } from '@/components/Icon.tsx';
import type { AskThread } from '@/features/ask/useAskThread.ts';
import { useMediaQuery } from '@/hooks/useMediaQuery.ts';

import type { LibraryParams } from './useLibraryParams.ts';

/** Below this the search field no longer fits the long placeholder beside its Ask glyph. */
const NARROW_FIELD_QUERY = '(max-width: 26rem)';

/** A count for a chip, and whether there is more behind it than has been loaded. */
export interface ChipCount {
  count: number | undefined;
  more: boolean;
}

interface LibraryFieldProps extends LibraryParams {
  /** The thread the field submits to; the field is its follow-up once a thread is open. */
  thread: AskThread;
  /** What the field controls, for `aria-controls`: the Ask panel in Ask mode, the list otherwise. */
  askPanelId: string;
  listId: string;
  tagNames: readonly string[];
  checklists: ChipCount;
  archived: ChipCount;
}

/**
 * One field, two modes, and the chips row under it.
 *
 * Searching filters on every keystroke through the URL's `q`; asking holds
 * the question until Enter, because a question is a request that costs a
 * model call, not a filter to narrow. The glyph inside the field's trailing
 * edge is the switch, a pressed button rather than the Search | Ask segment
 * that took a third of the row (round-3 T17); switching to Ask drops the
 * filter so coming back to Search shows the whole library. With a thread
 * open the field is the follow-up (round-3 T21): one field for one
 * conversation. A second question typed while the first is unanswered stays
 * in the field until Enter can send it: `useAskThread.ask` drops it, so the
 * field must not be cleared on its account, and says it is waiting
 * (`aria-busy`) for the reader who cannot see the panel's status line.
 *
 * The chips — All, Checklists, one per tag, Archived — are the list's other
 * filters; they hide while asking, since the panel stands in for the list.
 */
export function LibraryField({
  view,
  tag,
  kind,
  asking,
  query,
  setFilter,
  thread,
  askPanelId,
  listId,
  tagNames,
  checklists,
  archived,
}: LibraryFieldProps) {
  /*
   * The question being typed. Component state rather than the URL: `q` is a
   * filter and belongs there, but a question is sent once, on Enter, and
   * should be in nobody's history or shared link.
   */
  const [question, setQuestion] = useState('');
  const { goHome, goTab } = useTabNavigation();
  const inputRef = useRef<HTMLInputElement>(null);
  const chipsRef = useRef<HTMLDivElement>(null);
  /** A thread is open, so the field is its follow-up rather than a first question. */
  const following = thread.turns.length > 0;
  // On a 320 px phone the field, less its Ask glyph, clips the long
  // placeholder mid-word ("…tags, tran"). A placeholder cannot be changed
  // from CSS, so the width is read here.
  const narrowField = useMediaQuery(NARROW_FIELD_QUERY);
  const inputId = useId();

  /*
   * The pressed chip is where the reader is, so the row is scrolled to show
   * it: the Archived chip sits at the row's far end and was off a phone's
   * screen whether you arrived by the Archive row or `?view=archived` (QA
   * 2026-09-21, finding 6). Keyed on what lays the row out, not on the chip
   * becoming pressed: on a cold `?view=archived` the tag chips land later
   * from IndexedDB and the counts from the network, and they pushed a chip
   * that had scrolled itself in straight back off the screen. The row's own
   * scrollLeft, not scrollIntoView: Chromium moves the sequential focus
   * navigation starting point to the element it scrolls to, so the first Tab
   * landed after the pressed chip instead of on the skip link. In jsdom every
   * box is empty and nothing moves, which is the right thing there.
   */
  useEffect(() => {
    const row = chipsRef.current;
    const chip = row?.querySelector<HTMLElement>('[aria-pressed="true"]');
    if (!row || !chip) return;
    const gutter = Number.parseFloat(getComputedStyle(row).paddingInlineEnd) || 0;
    const by = chipScrollBy(row.getBoundingClientRect(), chip.getBoundingClientRect(), gutter);
    if (by !== 0) row.scrollLeft += by;
  }, [asking, view, tag, kind, tagNames, checklists.count, archived.count]);

  return (
    <>
      <form
        className="search-form"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          if (!asking || thread.busy) return;
          thread.ask(question);
          setQuestion('');
        }}
      >
        <label className="visually-hidden" htmlFor={inputId}>
          {asking ? (following ? 'Ask a follow-up' : 'Ask your notes') : 'Search notes'}
        </label>
        <div className="search-field">
          <input
            ref={inputRef}
            id={inputId}
            className="search-input"
            type="search"
            value={asking ? question : query}
            placeholder={
              asking
                ? following
                  ? 'Ask a follow-up…'
                  : 'Ask your notes…'
                : narrowField
                  ? 'Search notes'
                  : 'Search titles, tags, transcripts'
            }
            autoComplete="off"
            enterKeyHint={asking ? 'send' : 'search'}
            maxLength={asking ? 1000 : undefined}
            aria-controls={asking ? askPanelId : listId}
            aria-busy={asking && thread.busy}
            onChange={(event) => {
              if (asking) setQuestion(event.target.value);
              else setFilter({ q: event.target.value });
            }}
          />
          {/* `type="button"`: inside the form so it can sit inside the field, never its submit. */}
          <button
            type="button"
            className="ask-toggle"
            aria-pressed={asking}
            aria-label="Ask"
            title={asking ? 'Back to search' : 'Ask your notes'}
            onClick={() => {
              setFilter(asking ? { mode: null } : { mode: ASK_MODE, q: null });
              inputRef.current?.focus();
            }}
          >
            <Icon name="sparkle" size={20} />
          </button>
        </div>
      </form>

      {!asking && (
        <div ref={chipsRef} className="chips" role="group" aria-label="Filter notes">
          <Chip
            label="All"
            pressed={view === 'active' && !tag && !kind}
            onClick={() => {
              setFilter({ view: null, tag: null, kind: null });
            }}
          />
          {/*
            On the same terms as Archived below: not while there is nothing
            behind it. Checklists are learnt about where they are made, on a
            note's Details; a chip reading "Checklists · 0" taught nothing.
          */}
          {(kind === 'checklist' || (checklists.count ?? 0) > 0) && (
            <Chip
              label={<CountedLabel word="Checklists" {...checklists} />}
              name={
                checklists.count === undefined
                  ? 'Checklists'
                  : `Checklists · ${String(checklists.count)}`
              }
              pressed={kind === 'checklist'}
              onClick={() => {
                setFilter({ kind: kind ? null : 'checklist' });
              }}
            />
          )}
          {tagNames.map((name) => (
            <Chip
              key={name}
              label={name}
              pressed={tag === name}
              onClick={() => {
                setFilter({ tag: tag === name ? null : name });
              }}
            />
          ))}
          {/*
            Not while there is nothing behind it (round-3 T17): "Archived · 0"
            offered a filter of nothing. The archive is still one tap away,
            as a row at the list's end below; the chip returns with the
            first archived note, and is always there in the archive itself.
          */}
          {(view === 'archived' || (archived.count ?? 0) > 0) && (
            <Chip
              label={<CountedLabel word="Archived" {...archived} />}
              name={archived.count === undefined ? 'Archived' : `Archived · ${String(archived.count)}`}
              pressed={view === 'archived'}
              onClick={() => {
                // Not a filter like the others: the archive is a top-level
                // screen (R8, F2), pushed above Home so Back from it is Home,
                // and left by going back down to that Home.
                if (view !== 'archived') goTab(ROUTES.archive);
                else if (historyIndex() > 0) goHome();
                else setFilter({ view: null });
              }}
            />
          )}
        </div>
      )}
    </>
  );
}

/** "Checklists · 12+": the word, and the count once it is known, with "+" while there is more. */
function CountedLabel({ word, count, more }: { word: string } & ChipCount) {
  return (
    <>
      {word}
      {count !== undefined && (
        <>
          {' · '}
          <span className="numeric">
            {count}
            {more ? '+' : ''}
          </span>
        </>
      )}
    </>
  );
}

function Chip({
  label,
  name,
  pressed,
  onClick,
}: {
  label: ReactNode;
  /** The accessible name, when the visible label is more than plain text. */
  name?: string;
  pressed: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className="chip"
      aria-pressed={pressed}
      aria-label={name}
      onClick={onClick}
    >
      {label}
    </button>
  );
}

/**
 * How far a chip row must scroll sideways for `chip` to sit inside it, a
 * gutter in from the edge: positive to the right, negative to the left, zero
 * when it already does. Exported for its test.
 */
export function chipScrollBy(
  row: Pick<DOMRect, 'left' | 'right'>,
  chip: Pick<DOMRect, 'left' | 'right'>,
  gutter: number,
): number {
  if (chip.right > row.right - gutter) return chip.right - (row.right - gutter);
  if (chip.left < row.left + gutter) return chip.left - (row.left + gutter);
  return 0;
}
