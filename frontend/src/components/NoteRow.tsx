import { useId, useState } from 'react';
import { useNavigate } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import {
  useArchiveNote,
  useDeleteNoteForever,
  usePinNote,
  useRestoreNote,
} from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import {
  describeProgress,
  openItemsText,
  parseChecklist,
  progressOf,
  snippetIsCut,
} from '@/features/notes/checklist.ts';
import { describeRecordings, formatRowTime } from '@/features/notes/groups.ts';
import { describePurge, purgeCountdown } from '@/features/notes/purge.ts';
import { useLongPress } from '@/hooks/useLongPress.ts';

import { ConfirmDialog } from './ConfirmDialog.tsx';
import { Icon } from './Icon.tsx';
import { OverflowMenu, type OverflowMenuItem } from './OverflowMenu.tsx';
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
   * Whether pressing and holding the row starts selection. Off for a pinned
   * row under a finger, where the same hold lifts the row to reorder it
   * (`PinnedGroup`); Select is still in the row's ⋮ menu there.
   */
  holdToSelect?: boolean;
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
 * A checklist wears a glyph before its title, shows its open items as the two
 * lines rather than raw `- [ ]` syntax, and says how far along it is in the
 * meta line — counted from the snippet, which is the body's first 500 runes,
 * so the total is a floor ("3 of 7+ done") when the snippet was cut.
 *
 * Two ways into selection, the same on every pointer (backlog U2; owner,
 * 2026-09-24: "hover to get a checkbox is not clean UX"). Press and hold the
 * row — a finger or a mouse, `useLongPress` takes both — or pick Select from
 * the row's ⋮ menu. The menu sits at the row's right: revealed on hover and
 * on focus for a pointer that can hover, always there at low emphasis under
 * a finger, where nothing can hover. It holds Pin (or Unpin), Archive (or
 * Restore), Delete and Select, so every action the swipe tray offers is a
 * click away on the desktop too. The checkbox that slid in at the row's left
 * edge on hover is gone; the "Select" button that sat in the header before it
 * went for the same reason.
 *
 * And a third gesture, for a finger only: swipe the row aside for its actions
 * (backlog N8). In the library that is Pin, Archive and Delete; in the
 * archive, Restore and Delete. The row carries these itself — its own
 * mutations, its own confirmation — so the screen that lists it need know
 * nothing about them. Pinning keeps the note at the top of Home in a group of
 * its own (2026-09-24, B); the glyph before the title says so. Archive and
 * delete keep the disciplines the note's own menu sets: archiving is
 * reversible and happens on the tap; deleting is not, so it names what goes
 * and asks for "delete" to be typed — the word every delete in the app asks
 * for, not the title, which dictated is 27 characters of digits on a phone
 * keyboard (review 2026-09-21, T18). From the library, delete is the two
 * server operations the archive would otherwise require — archive, then
 * purge — because the server refuses to purge a note that is still active,
 * and rightly (see `useBulkDeleteNotes`).
 */
export function NoteRow({
  note,
  selectable = false,
  selected = false,
  onToggleSelect,
  holdToSelect = true,
  excerpt,
  highlight,
}: NoteRowProps) {
  const navigate = useNavigate();
  const titleId = useId();
  const longPress = useLongPress(
    onToggleSelect && holdToSelect && !selectable
      ? () => {
          onToggleSelect(note.id, { range: false });
        }
      : null,
  );
  const archive = useArchiveNote();
  const restore = useRestoreNote();
  const purge = useDeleteNoteForever();
  const pin = usePinNote();
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const busy = archive.isPending || restore.isPending || purge.isPending;
  const failure = archive.error ?? restore.error ?? purge.error ?? pin.error;

  const tags = note.tags ?? [];
  const time = formatRowTime(note.updated_at);
  const recordings = describeRecordings(note);
  const countdown = note.archived ? purgeCountdown(note.purge_after) : null;
  const checklist = note.kind === 'checklist';
  const items = checklist ? parseChecklist(note.snippet ?? '') : [];
  // A search hit's excerpt wins either way: it is where the match was.
  const snippet = excerpt ?? (checklist ? openItemsText(items) : note.snippet);
  const progress = checklist
    ? describeProgress(progressOf(items), snippetIsCut(note.snippet ?? ''))
    : null;
  const hasMeta =
    countdown !== null || progress !== null || tags.length > 0 || recordings !== null;

  const body = (
    <>
      <span className="note-row__head">
        <span id={titleId} className="note-row__title">
          {/* Decorative: the row sits under the "Pinned" heading, which says it. */}
          {note.pinned && <Icon name="pin" size={16} className="note-row__kind note-row__pin" />}
          {checklist && <Icon name="checklist" size={16} className="note-row__kind" />}
          {note.title}
        </span>
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
          {progress && <span className="note-row__progress numeric">{progress}</span>}
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
        data-pinned={note.pinned || undefined}
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

  const togglePin = (): void => {
    pin.mutate({ note, pinned: !note.pinned });
  };
  const pinLabel = note.pinned ? 'Unpin' : 'Pin';
  const remove = (): void => {
    setConfirmingDelete(true);
  };

  // The tray and the menu offer the same actions, in the same words: the
  // tray's nearest button is its last, so Delete sits at the edge of both.
  const actions: SwipeAction[] = note.archived
    ? [
        { id: 'restore', label: 'Restore', icon: 'restore', onSelect: () => restore.mutate(note.id) },
        { id: 'delete', label: 'Delete', icon: 'trash', destructive: true, onSelect: remove },
      ]
    : [
        { id: 'pin', label: pinLabel, icon: 'pin', onSelect: togglePin },
        { id: 'archive', label: 'Archive', icon: 'archive', onSelect: () => archive.mutate(note.id) },
        { id: 'delete', label: 'Delete', icon: 'trash', destructive: true, onSelect: remove },
      ];
  const menu: OverflowMenuItem[] = [
    ...(note.archived
      ? [{ label: 'Restore', disabled: busy, onSelect: () => restore.mutate(note.id) }]
      : [
          { label: pinLabel, disabled: busy, onSelect: togglePin },
          { label: 'Archive', disabled: busy, onSelect: () => archive.mutate(note.id) },
        ]),
    { label: 'Delete', destructive: true, disabled: busy, onSelect: remove },
    ...(onToggleSelect
      ? [{ label: 'Select', onSelect: () => onToggleSelect(note.id, { range: false }) }]
      : []),
  ];

  return (
    <>
      <SwipeRow
        actions={actions}
        disabled={busy}
        label={`Actions for ${note.title}`}
        className="note-swipe"
      >
        <div className="note-row-wrap note-row-wrap--menu">
          <button
            type="button"
            className="note-row"
            data-pinned={note.pinned || undefined}
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
            After the row in the DOM so Tab reaches it from the row it acts on,
            and the row's focus is what reveals it (`:focus-within` on the wrap)
            where hover does the same for a mouse. Named "More" and described
            by the title, so the title answers to the row alone.
          */}
          <span className="note-row__menu">
            <OverflowMenu label="More" describedBy={titleId} items={menu} />
          </span>
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
        requireText="delete"
        requireLabel='Type "delete" to confirm'
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
