import { useId, useMemo, useRef, useState } from 'react';

import { useNotes } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { useModalFocus } from '@/components/useModalFocus.ts';

/**
 * "Move to…": which note should these recordings go into?
 *
 * The active notes, most recently touched first, with a field that narrows
 * them as you type — the same corpus the library shows, in the same order,
 * because "the note I was in an hour ago" is where a misfiled recording
 * usually belongs. The note being moved out of is left out, and so is the
 * archive: the server refuses an archived target (409), and offering a row
 * that can only fail is not a choice.
 *
 * Deliberately no "New note". Moving is re-filing what exists; the capture
 * screen's target prompt is where a recording can start a note, because that
 * is the moment nobody yet knows where it goes. Offering creation here would
 * make every move a fork in the road.
 *
 * The same modal discipline as `ConfirmDialog` — `useModalFocus` — so a
 * keyboard user is trapped inside it, Escape leaves, and focus returns to the
 * control that opened it.
 */
export function MoveSheet({
  open,
  count,
  excludeNoteId,
  pending,
  error,
  onChoose,
  onCancel,
}: {
  open: boolean;
  count: number;
  excludeNoteId: string;
  pending: boolean;
  error: string | null;
  onChoose: (note: NoteWire) => void;
  onCancel: () => void;
}) {
  if (!open) return null;
  return (
    <SheetPanel
      count={count}
      excludeNoteId={excludeNoteId}
      pending={pending}
      error={error}
      onChoose={onChoose}
      onCancel={onCancel}
    />
  );
}

function SheetPanel({
  count,
  excludeNoteId,
  pending,
  error,
  onChoose,
  onCancel,
}: {
  count: number;
  excludeNoteId: string;
  pending: boolean;
  error: string | null;
  onChoose: (note: NoteWire) => void;
  onCancel: () => void;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const searchId = useId();
  const [query, setQuery] = useState('');
  useModalFocus(panelRef, onCancel);

  const list = useNotes({ state: 'active' });
  const notes = useMemo(() => {
    const all = list.data?.pages.flatMap((page) => page.items) ?? [];
    const term = query.trim().toLowerCase();
    return all
      .filter((note) => note.id !== excludeNoteId)
      .filter(
        (note) =>
          !term ||
          note.title.toLowerCase().includes(term) ||
          (note.aliases ?? []).some((alias) => alias.toLowerCase().includes(term)) ||
          (note.tags ?? []).some((tag) => tag.toLowerCase().includes(term)),
      )
      .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
  }, [list.data, query, excludeNoteId]);

  const title = `Move ${count === 1 ? 'this recording' : `${String(count)} recordings`} to…`;

  return (
    <div className="dialog-layer">
      <div className="dialog-scrim" aria-hidden="true" />
      <div
        ref={panelRef}
        className="dialog move-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
      >
        <h2 id={titleId} className="dialog__title">
          {title}
        </h2>
        <p className="dialog__body">
          The text it dictated goes with it, in order among that note&rsquo;s own recordings.
        </p>

        <label className="visually-hidden" htmlFor={searchId}>
          Search notes
        </label>
        <input
          id={searchId}
          className="move-sheet__search"
          type="search"
          value={query}
          placeholder="Search notes"
          autoComplete="off"
          onChange={(event) => {
            setQuery(event.target.value);
          }}
        />

        {list.isLoading ? (
          <p className="screen__count" role="status">
            Loading your notes…
          </p>
        ) : notes.length === 0 ? (
          <p className="screen__count">
            {query.trim() ? `No note matches “${query.trim()}”.` : 'No other note to move into.'}
          </p>
        ) : (
          <ul className="move-sheet__list" role="list">
            {notes.map((note) => (
              <li key={note.id}>
                <button
                  type="button"
                  className="move-sheet__option"
                  disabled={pending}
                  onClick={() => {
                    onChoose(note);
                  }}
                >
                  <span className="move-sheet__option-title">{note.title}</span>
                  {(note.tags ?? []).length > 0 && (
                    <span className="move-sheet__option-meta">{(note.tags ?? []).join(' · ')}</span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        )}

        {list.hasNextPage && !query.trim() && (
          <button
            type="button"
            className="move-sheet__more"
            disabled={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
          >
            {list.isFetchingNextPage ? 'Loading…' : 'Older notes'}
          </button>
        )}

        {error && (
          <p className="target-picker__error" role="alert">
            {error}
          </p>
        )}

        <div className="dialog__actions">
          <button type="button" className="dialog__action" onClick={onCancel} disabled={pending}>
            {pending ? 'Moving…' : 'Cancel'}
          </button>
        </div>
      </div>
    </div>
  );
}
