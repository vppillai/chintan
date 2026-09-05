import { useState } from 'react';
import { useNavigate } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { useArchiveNote, useDeleteNoteForever, useRestoreNote } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { describeRecordings, formatRowTime } from '@/features/notes/groups.ts';
import { describePurge, purgeCountdown } from '@/features/notes/purge.ts';
import { useLongPress } from '@/hooks/useLongPress.ts';
import { HOVER_QUERY, useMediaQuery } from '@/hooks/useMediaQuery.ts';

import { ConfirmDialog } from './ConfirmDialog.tsx';
import { SwipeRow, type SwipeAction } from './SwipeRow.tsx';

export interface SelectOptions {
  /** Shift was held: select the range from the last toggled row to this one. */
  range: boolean;
}

export interface NoteRowProps {
  note: NoteWire;
  /**
   * Bulk-select mode. A real `<input type="checkbox">` inside a `<label>`,
   * not a styled div with a click handler — the row's own doc comment already
   * makes that argument for the plain case, and a checkbox is the one control
   * every screen reader and every keyboard already knows how to operate.
   */
  selectable?: boolean;
  selected?: boolean;
  onToggleSelect?: (noteId: string, options: SelectOptions) => void;
  /**
   * A search hit: the excerpt around the match stands in for the snippet, and
   * the matched term is marked in it. Absent on the plain library.
   */
  excerpt?: string;
  highlight?: string;
}

/**
 * A note row is a real <button>, not a clickable div. Divs would make the
 * entire library unreachable by keyboard and invisible to assistive technology
 * as an actionable thing.
 *
 * Serif title with the time on the right in tabular serif numerals, two lines
 * of the note, then a meta line: the purge countdown for an archived note, the
 * tags, and — when the payload carries them — how many recordings are behind
 * it and how long they run.
 *
 * Two ways into selection, one per kind of pointer (backlog U2). A finger
 * presses and holds the row; a mouse gets a checkbox that slides in at the
 * row's left edge on hover (and on focus, for the keyboard), and Shift-click
 * on it selects the range since the last one. The "Select" button that sat in
 * the header is gone: it was a desktop idiom on a phone screen, and on the
 * desktop it was a second step before the first click.
 *
 * And a third gesture, for a finger only: swipe the row aside for its actions
 * (backlog N8). In the library that is Archive and Delete; in the archive,
 * Restore and Delete. The row carries these itself — its own mutations, its
 * own confirmation — so the screen that lists it need know nothing about
 * them. The two keep the disciplines the note's action bar sets: archiving is
 * reversible and happens on the tap; deleting is not, so it names what goes
 * and asks for the title to be typed. From the library, delete is the two
 * server operations the archive would otherwise require — archive, then
 * purge — because the server refuses to purge a note that is still active,
 * and rightly (see `useBulkDeleteNotes`).
 */
export function NoteRow({
  note,
  selectable = false,
  selected = false,
  onToggleSelect,
  excerpt,
  highlight,
}: NoteRowProps) {
  const navigate = useNavigate();
  const canHover = useMediaQuery(HOVER_QUERY);
  const longPress = useLongPress(
    onToggleSelect && !selectable
      ? () => {
          onToggleSelect(note.id, { range: false });
        }
      : null,
  );
  const archive = useArchiveNote();
  const restore = useRestoreNote();
  const purge = useDeleteNoteForever();
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const busy = archive.isPending || restore.isPending || purge.isPending;
  const failure = archive.error ?? restore.error ?? purge.error;

  const tags = note.tags ?? [];
  const time = formatRowTime(note.updated_at);
  const recordings = describeRecordings(note);
  const countdown = note.archived ? purgeCountdown(note.purge_after) : null;
  const snippet = excerpt ?? note.snippet;
  const hasMeta = countdown !== null || tags.length > 0 || recordings !== null;

  const body = (
    <>
      <span className="note-row__head">
        <span className="note-row__title">{note.title}</span>
        {time && (
          <time className="note-row__time numeric" dateTime={note.updated_at}>
            {time}
          </time>
        )}
      </span>
      {snippet && (
        <span className="note-row__snippet">
          <Marked text={snippet} term={highlight ?? ''} />
        </span>
      )}
      {hasMeta && (
        <span className="note-row__meta">
          {countdown && (
            <span className="note-row__purge" data-purge={countdown.kind}>
              {describePurge(countdown)}
            </span>
          )}
          {tags.length > 0 && (
            <span className="note-row__tags">
              {tags.map((tag) => (
                <span key={tag} className="note-row__tag">
                  {tag}
                </span>
              ))}
            </span>
          )}
          {recordings && <span className="note-row__recordings numeric">{recordings}</span>}
        </span>
      )}
    </>
  );

  if (selectable) {
    return (
      <label
        className="note-row note-row--selectable"
        data-selected={selected || undefined}
        onClick={(event) => {
          /*
           * The finger lifting after the long press that started this mode
           * lands its click here — the row was a button when the press began
           * and is this label by the time the click arrives — and a label's
           * click toggles its checkbox, which would deselect the row that was
           * just selected. The hook survives the swap, so it knows.
           */
          if (longPress.consumeClick()) event.preventDefault();
        }}
      >
        {/*
          A 24 px box inside a 44 px one: the control itself meets the WCAG
          2.5.8 minimum (it was 20 px), and the wrapper is the thumb's target.
          The whole row is the label, so a tap anywhere toggles it regardless.
        */}
        <span className="note-row__check">
          <input
            type="checkbox"
            className="note-row__checkbox"
            checked={selected}
            onClick={(event) => {
              // `onChange` carries no modifier keys; the click does.
              onToggleSelect?.(note.id, { range: event.shiftKey });
            }}
            onChange={() => {
              /* Handled on click, above, where Shift is known. */
            }}
          />
        </span>
        <span className="note-row__body">{body}</span>
      </label>
    );
  }

  const actions: SwipeAction[] = note.archived
    ? [
        {
          id: 'restore',
          label: 'Restore',
          icon: 'restore',
          onSelect: () => {
            restore.mutate(note.id);
          },
        },
        {
          id: 'delete',
          label: 'Delete',
          icon: 'trash',
          destructive: true,
          onSelect: () => {
            setConfirmingDelete(true);
          },
        },
      ]
    : [
        {
          id: 'archive',
          label: 'Archive',
          icon: 'archive',
          onSelect: () => {
            archive.mutate(note.id);
          },
        },
        {
          id: 'delete',
          label: 'Delete',
          icon: 'trash',
          destructive: true,
          onSelect: () => {
            setConfirmingDelete(true);
          },
        },
      ];

  return (
    <>
      <SwipeRow
        actions={actions}
        disabled={busy}
        label={`Actions for ${note.title}`}
        className="note-swipe"
      >
        <div className="note-row-wrap">
          <button
            type="button"
            className="note-row"
            onClick={() => {
              // The click that follows a long press is the finger lifting, not a tap.
              if (longPress.consumeClick()) return;
              void navigate(ROUTES.note(note.id));
            }}
            {...longPress.handlers}
          >
            {body}
          </button>

          {/*
            After the row in the DOM so Tab reaches it from the row it selects, and
            the row's focus is what reveals it (`:focus-within` on the wrap). Drawn
            only for a pointer that can hover; a finger has the long press.
          */}
          {canHover && onToggleSelect && (
            <span className="note-row__hover-check">
              <input
                type="checkbox"
                className="note-row__checkbox"
                checked={false}
                aria-label={`Select ${note.title}`}
                onClick={(event) => {
                  onToggleSelect(note.id, { range: event.shiftKey });
                }}
                onChange={() => {
                  /* Handled on click, above, where Shift is known. */
                }}
              />
            </span>
          )}
        </div>
      </SwipeRow>

      {failure && (
        <p className="note-row__error" role="alert">
          {failure instanceof ApiError ? failure.userMessage : 'That did not go through.'}
        </p>
      )}

      <ConfirmDialog
        open={confirmingDelete}
        title="Delete this note forever?"
        body={`“${note.title}” and its recordings and transcripts are destroyed. This cannot be undone, and there is no copy on the server or on any other device you have signed in on.`}
        confirmLabel="Delete forever"
        requireText={note.title}
        requireLabel={`Type the note's title to confirm: ${note.title}`}
        destructive
        onCancel={() => {
          setConfirmingDelete(false);
        }}
        onConfirm={() => {
          setConfirmingDelete(false);
          if (note.archived) {
            purge.mutate(note.id);
            return;
          }
          // Chained on the promise rather than in a per-call `onSuccess`, which
          // TanStack drops once the row has unmounted — and the archive's own
          // refetch is about to remove this row from the list it is in. Either
          // failure lands on its mutation's state and is shown beneath the row.
          void archive
            .mutateAsync(note.id)
            .then(() => purge.mutateAsync(note.id))
            .catch(() => undefined);
        }}
      />
    </>
  );
}

/** Renders the match in situ, with the hit marked rather than stripped out. */
function Marked({ text, term }: { text: string; term: string }) {
  const index = term ? text.toLowerCase().indexOf(term.toLowerCase()) : -1;
  if (index === -1) return <>{text}</>;

  return (
    <>
      {text.slice(0, index)}
      <mark className="search-hit">{text.slice(index, index + term.length)}</mark>
      {text.slice(index + term.length)}
    </>
  );
}
