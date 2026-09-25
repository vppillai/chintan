import { useId, useMemo, useRef, useState } from 'react';
import { flushSync } from 'react-dom';

import { useNotes } from '@/api/queries.ts';
import type { CaptureMoveWire, NoteWire } from '@/api/schema.ts';
import { useModalFocus } from '@/components/useModalFocus.ts';

import { openItemsText, parseChecklist } from './checklist.ts';
import { formatRowTime } from './groups.ts';

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
 * "New note…" heads the list. The sheet first shipped without it — moving
 * was to be re-filing among notes that exist, and the capture screen's
 * target prompt the one place a recording could start a note — but the
 * moment a misfiled recording is found is as often the moment it becomes
 * clear it belongs to a note nobody has made yet, and the owner asked to
 * make it from here (2026-09-24). The row unfolds into a title field; Enter
 * or "Create and move" sends the move with `new_note_title`, Escape or Back
 * returns to the list. The paragraph the recording dictated goes with it
 * either way: that is what the server's move does.
 *
 * The same modal discipline as `ConfirmDialog` — `useModalFocus` — so a
 * keyboard user is trapped inside it, Escape leaves, and focus returns to the
 * control that opened it.
 *
 * Each option carries a meta line under its title — when the note was last
 * touched, and the start of its text — because a list of dictated titles
 * has identical entries ("Voice note …", five of them) with nothing to tell
 * them apart (review 2026-09-21, T42).
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
  /** The move to make, and the title of the note it goes to, for the notice. */
  onChoose: (target: CaptureMoveWire, title: string) => void;
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

/** The server's limit on a title, so the field cannot ask for a refusal. */
const TITLE_MAX = 200;

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
  onChoose: (target: CaptureMoveWire, title: string) => void;
  onCancel: () => void;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  const newNoteRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const searchId = useId();
  const nameId = useId();
  const [query, setQuery] = useState('');
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');

  /*
   * Escape steps back one level: out of the title field to the list, out of
   * the list to the recordings. Back puts focus on the row that opened the
   * field, synchronously, because the field it was in has just unmounted and
   * focus would otherwise fall to the body — from where the next Tab leaves
   * the dialog, since the trap only redirects from the panel's own edges.
   */
  const backToList = (): void => {
    flushSync(() => {
      setCreating(false);
    });
    newNoteRef.current?.focus();
  };
  useModalFocus(panelRef, creating ? backToList : onCancel);

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
  const trimmedName = name.trim();

  const problem = error && (
    <p className="target-picker__error" role="alert">
      {error}
    </p>
  );

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

        {creating ? (
          <form
            className="dialog__gate"
            onSubmit={(event) => {
              event.preventDefault();
              // The button is disabled with nothing to name the note, and
              // Enter in the field lands here too.
              if (!trimmedName || pending) return;
              onChoose({ new_note_title: trimmedName }, trimmedName);
            }}
          >
            <label className="dialog__gate-label" htmlFor={nameId}>
              Name the new note
            </label>
            <input
              id={nameId}
              className="dialog__gate-input"
              type="text"
              value={name}
              maxLength={TITLE_MAX}
              autoComplete="off"
              autoFocus
              onChange={(event) => {
                setName(event.target.value);
              }}
            />
            {problem}
            <div className="dialog__actions">
              <button
                type="button"
                className="dialog__action"
                disabled={pending}
                onClick={backToList}
              >
                Back
              </button>
              <button
                type="submit"
                className="dialog__action dialog__action--primary"
                disabled={pending || !trimmedName}
              >
                {pending ? 'Moving…' : 'Create and move'}
              </button>
            </div>
          </form>
        ) : (
          <>
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

            {/* Whatever the search says: a term no note matches is the
                likeliest reason to want a new one, so it becomes the name
                to start from. */}
            <button
              ref={newNoteRef}
              type="button"
              className="move-sheet__option"
              disabled={pending}
              onClick={() => {
                setName((current) => current || query.trim());
                setCreating(true);
              }}
            >
              <span className="move-sheet__option-title">New note…</span>
            </button>

            {list.isLoading ? (
              <p className="screen__count" role="status">
                Loading your notes…
              </p>
            ) : notes.length === 0 ? (
              <p className="screen__count">
                {query.trim()
                  ? `No note matches “${query.trim()}”.`
                  : 'No other note to move into.'}
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
                        onChoose({ note_id: note.id }, note.title);
                      }}
                    >
                      <span className="move-sheet__option-title">{note.title}</span>
                      <span className="move-sheet__option-meta">{optionMeta(note)}</span>
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

            {problem}

            <div className="dialog__actions">
              <button
                type="button"
                className="dialog__action"
                onClick={onCancel}
                disabled={pending}
              >
                {pending ? 'Moving…' : 'Cancel'}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

/**
 * "6 Aug · Ridge tiles on the south slope have slip… · house" — the row's own
 * time, the first 40 characters of the note's text (a checklist's as its open
 * items rather than raw `- [ ]` syntax), then the tags the line showed before
 * T42, which still tell notes apart. Exported for the test.
 */
export function optionMeta(
  note: Pick<NoteWire, 'updated_at' | 'snippet' | 'kind' | 'tags'>,
): string {
  const snippet = note.snippet ?? '';
  const text = note.kind === 'checklist' ? openItemsText(parseChecklist(snippet)) : snippet;
  const cut = text.trim().replace(/\s+/g, ' ');
  return [
    formatRowTime(note.updated_at),
    cut.length > 40 ? `${cut.slice(0, 40).trimEnd()}…` : cut,
    ...(note.tags ?? []),
  ]
    .filter(Boolean)
    .join(' · ');
}
