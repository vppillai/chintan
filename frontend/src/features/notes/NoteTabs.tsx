import { useCallback, useRef, useState, type KeyboardEvent } from 'react';
import { useSearchParams } from 'react-router';

/**
 * The note's segments: Text · Recordings (N).
 *
 * The note used to be one long page — title, body, then every recording with
 * its player and transcript — and reaching the recordings of a note with a
 * few thousand words meant scrolling past all of them. Now the body and the
 * recordings are panels under one strip, and the strip sticks under the
 * banner while whichever panel is open scrolls.
 *
 * The chosen tab is remembered per note for the session and is a real query
 * parameter (`?tab=recordings`), so a link can open a note on its recordings
 * and a reload lands where the user was. Text is the default: the note is
 * the document; the recordings are its sources.
 */

export const NOTE_TABS = ['text', 'recordings'] as const;
export type NoteTab = (typeof NOTE_TABS)[number];

export const NOTE_TAB_PARAM = 'tab';

/** Where the last-used tab for a note is kept for the rest of the session. */
export function noteTabStorageKey(noteId: string): string {
  return `chintan.note-tab.${noteId}`;
}

function isNoteTab(value: string | null | undefined): value is NoteTab {
  return NOTE_TABS.includes(value as NoteTab);
}

function readRemembered(noteId: string): NoteTab | null {
  try {
    const stored = sessionStorage.getItem(noteTabStorageKey(noteId));
    return isNoteTab(stored) ? stored : null;
  } catch {
    // Storage denied: the tab simply is not remembered.
    return null;
  }
}

function remember(noteId: string, tab: NoteTab): void {
  try {
    sessionStorage.setItem(noteTabStorageKey(noteId), tab);
  } catch {
    /* Storage denied. */
  }
}

/**
 * The tab to open a note on: the URL's `?tab=` first — a deep link is an
 * explicit request — then the one this session last used for the note, then
 * Text. Pure, so the precedence is testable without a router.
 */
export function initialNoteTab(fromUrl: string | null, remembered: NoteTab | null): NoteTab {
  if (isNoteTab(fromUrl)) return fromUrl;
  return remembered ?? 'text';
}

/**
 * The current tab and a way to change it. Changing it writes the session
 * memory and the URL — replacing the entry, so Back still leaves the note
 * rather than stepping back through tabs. Nothing is written on arrival: the
 * back guard is rewriting history under a deep link at the same moment, and
 * a second navigation racing it is how a URL ends up pointing at the wrong
 * entry. A remembered tab the URL does not name is simply shown; the URL
 * names it from the first change on.
 */
export function useNoteTab(noteId: string): [NoteTab, (tab: NoteTab) => void] {
  const [params, setParams] = useSearchParams();
  const fromUrl = params.get(NOTE_TAB_PARAM);
  // The memory is read once, to seed the first render; from then on it is
  // only written, so a change from another browser tab cannot yank this one.
  const [chosen, setChosen] = useState(() => initialNoteTab(fromUrl, readRemembered(noteId)));
  // A URL that names a tab wins for as long as it names one — a link followed
  // while the note is open lands where it asked. Choosing a tab rewrites the
  // URL as well, so the two never disagree.
  const tab = isNoteTab(fromUrl) ? fromUrl : chosen;

  const select = useCallback(
    (next: NoteTab) => {
      remember(noteId, next);
      setChosen(next);
      setParams(
        (current) => {
          const search = new URLSearchParams(current);
          if (next === 'text') search.delete(NOTE_TAB_PARAM);
          else search.set(NOTE_TAB_PARAM, next);
          return search;
        },
        { replace: true },
      );
    },
    [noteId, setParams],
  );

  return [tab, select];
}

export interface NoteTabDescriptor {
  id: NoteTab;
  label: string;
  /** A count shown after the label — the recordings tab's "(3)". */
  count?: number;
}

export function noteTabId(noteId: string, tab: NoteTab): string {
  return `note-tab-${noteId}-${tab}`;
}

export function noteTabPanelId(noteId: string, tab: NoteTab): string {
  return `note-tabpanel-${noteId}-${tab}`;
}

/**
 * The strip itself: a WAI-ARIA tablist with automatic activation. Left and
 * Right move between the segments and select as they go; Home and End jump.
 * Only the selected tab is in the Tab order, as the pattern prescribes, so a
 * keyboard user reaches the panel's content in one press rather than three.
 */
export function NoteTabList({
  noteId,
  tabs,
  value,
  onChange,
}: {
  noteId: string;
  tabs: readonly NoteTabDescriptor[];
  value: NoteTab;
  onChange: (tab: NoteTab) => void;
}) {
  const buttons = useRef(new Map<NoteTab, HTMLButtonElement>());

  const moveTo = (tab: NoteTab): void => {
    onChange(tab);
    buttons.current.get(tab)?.focus();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    const index = tabs.findIndex((tab) => tab.id === value);
    if (index === -1) return;
    let next: number | null = null;
    switch (event.key) {
      case 'ArrowRight':
        next = (index + 1) % tabs.length;
        break;
      case 'ArrowLeft':
        next = (index - 1 + tabs.length) % tabs.length;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = tabs.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    const target = tabs[next];
    if (target) moveTo(target.id);
  };

  return (
    <div className="note-tabs">
      <div className="note-tabs__list" role="tablist" aria-label="Note views" onKeyDown={onKeyDown}>
        {tabs.map((tab) => {
          const selected = tab.id === value;
          return (
            <button
              key={tab.id}
              ref={(element) => {
                if (element) buttons.current.set(tab.id, element);
                else buttons.current.delete(tab.id);
              }}
              type="button"
              role="tab"
              id={noteTabId(noteId, tab.id)}
              className="note-tabs__tab"
              aria-selected={selected}
              aria-controls={noteTabPanelId(noteId, tab.id)}
              tabIndex={selected ? 0 : -1}
              onClick={() => {
                onChange(tab.id);
              }}
            >
              {tab.label}
              {typeof tab.count === 'number' && (
                <>
                  {' '}
                  <span className="note-tabs__count numeric">({tab.count})</span>
                </>
              )}
            </button>
          );
        })}
      </div>
    </div>
  );
}
