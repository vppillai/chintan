import { useId, useState } from 'react';
import { useNavigate } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import {
  useArchiveNote,
  useDeleteNoteForever,
  usePinNote,
  useRestoreNote,
  useUndoDelete,
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
import { useOnline } from '@/hooks/useOnline.ts';

import { ConfirmDialog } from './ConfirmDialog.tsx';
import { DeleteConfirm } from './DeleteConfirm.tsx';
import { showDeleted } from './Toast.tsx';
import { Icon } from './Icon.tsx';
import { OverflowMenu, type OverflowMenuItem } from './OverflowMenu.tsx';
import { SwipeRow, type SwipeAction } from './SwipeRow.tsx';

export interface NoteRowProps {
  note: NoteWire;
  /**
   * A pinned row's gesture-free way to move one step (`PinnedGroup`), as
   * "Move up" and "Move down" in the ⋮: on a phone nothing announces the
   * hold-then-drag, and a switch or a screen reader cannot drag at all (WCAG
   * 2.5.7; review 2026-09-24, R4-4). Absent at the group's ends.
   */
  onMoveUp?: () => void;
  onMoveDown?: () => void;
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
 * There is no selection mode (owner, 2026-09-29: with the row's own actions
 * and Undo, doing things to several notes at once was not needed), so a hold
 * on the row does nothing and its release opens the note like a tap. Every
 * action is on the row. The ⋮ at its right is revealed on hover and on focus
 * for a pointer that can hover, always there at low emphasis under a finger,
 * where nothing can hover. It holds Pin (or Unpin) and Delete — or Restore
 * and Delete forever, in the archive — and, on a pinned row, Move up and
 * Move down, so every action the swipe tray offers is a click away on the
 * desktop too, and the pinned order can be changed without a drag.
 *
 * And a gesture, for a finger only: swipe the row aside for its actions
 * (backlog N8). In the library that is Pin and Delete; in the archive,
 * Restore and Delete forever. The row carries these itself — its own
 * mutations, its own confirmation — so the screen that lists it need know
 * nothing about them. Pinning keeps the note at the top of Home in a group of
 * its own (2026-09-24, B); the glyph before the title says so.
 *
 * Delete on Home is the archive (owner, 2026-09-26: "I have to type delete. I
 * don't like that UX"). It asks first — "Delete “<title>”?", no field, focus
 * on Cancel (`DeleteConfirm`; owner, 2026-09-27: "ask are you sure, don't
 * directly archive") — and the toast that follows offers Undo for a few
 * seconds: the note is in the Archive for thirty days either way, so neither
 * tap is the last word. There is no separate Archive item any more: the
 * Archive is where deleted notes wait. Delete forever, in the archive, is the
 * one thing here that cannot be undone, so it names what goes with the note
 * and asks in its own words.
 */
export function NoteRow({
  note,
  onMoveUp,
  onMoveDown,
  excerpt,
  highlight,
}: NoteRowProps) {
  const navigate = useNavigate();
  const titleId = useId();
  // A pin made offline would sit paused until the network returned and then
  // fire with whatever version the cache held; the row does not move meanwhile,
  // so the tap looks lost (review 2026-09-24, R4-11). The pin waits for the network.
  const online = useOnline();
  const archive = useArchiveNote();
  const restore = useRestoreNote();
  const undo = useUndoDelete();
  const purge = useDeleteNoteForever();
  const pin = usePinNote();
  const [confirming, setConfirming] = useState<'delete' | 'purge' | null>(null);
  const busy =
    archive.isPending || restore.isPending || undo.isPending || purge.isPending || pin.isPending;
  const failure = archive.error ?? restore.error ?? undo.error ?? purge.error ?? pin.error;

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

  const togglePin = (): void => {
    pin.mutate({ note, pinned: !note.pinned });
  };
  const pinLabel = note.pinned ? 'Unpin' : 'Pin';
  /*
   * Delete on Home, once confirmed: archive, then offer Undo. Chained on the
   * promise rather than in a per-call `onSuccess`, which TanStack drops once
   * the row has unmounted — and the archive's own refetch is about to remove
   * this row from the list it is in. Undo calls the undo hook of this same
   * row for the same reason: its hook-level `onSuccess` refetches the lists
   * whether or not the row is still mounted. It is handed the note as it
   * was, so a pinned note comes back pinned — the server's restore alone
   * would not (`useUndoDelete`). A failure lands on the mutation's state and
   * is shown beneath the row, with no toast claiming otherwise.
   */
  const archiveNow = (): void => {
    void archive
      .mutateAsync(note.id)
      .then(() => {
        showDeleted(() => {
          undo.mutate(note);
        });
      })
      .catch(() => undefined);
  };
  const remove = (): void => {
    setConfirming('delete');
  };
  const removeForever = (): void => {
    setConfirming('purge');
  };

  // The tray and the menu offer the same actions, in the same words: the
  // tray's nearest button is its last, so Delete sits at the edge of both.
  const actions: SwipeAction[] = note.archived
    ? [
        { id: 'restore', label: 'Restore', icon: 'restore', onSelect: () => restore.mutate(note.id) },
        { id: 'delete', label: 'Delete forever', icon: 'trash', destructive: true, onSelect: removeForever },
      ]
    : [
        // The tray has no disabled state, so offline the pin is simply not offered.
        ...(online ? [{ id: 'pin', label: pinLabel, icon: 'pin' as const, onSelect: togglePin }] : []),
        { id: 'delete', label: 'Delete', icon: 'trash', destructive: true, onSelect: remove },
      ];
  const menu: OverflowMenuItem[] = [
    ...(note.archived
      ? [
          { label: 'Restore', disabled: busy, onSelect: () => restore.mutate(note.id) },
          { label: 'Delete forever', destructive: true, disabled: busy, onSelect: removeForever },
        ]
      : [
          { label: pinLabel, disabled: busy || !online, onSelect: togglePin },
          { label: 'Delete', destructive: true, disabled: busy, onSelect: remove },
        ]),
    ...(onMoveUp ? [{ label: 'Move up', disabled: !online, onSelect: onMoveUp }] : []),
    ...(onMoveDown ? [{ label: 'Move down', disabled: !online, onSelect: onMoveDown }] : []),
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
              void navigate(ROUTES.note(note.id));
            }}
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

      <DeleteConfirm
        open={confirming === 'delete'}
        title={note.title}
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          archiveNow();
        }}
      />

      <ConfirmDialog
        open={confirming === 'purge'}
        title="Delete this note forever?"
        body={`“${note.title}” and its recordings and transcripts are destroyed. This cannot be undone, and there is no copy on the server or on any other device you have signed in on.`}
        confirmLabel="Delete forever"
        destructive
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          purge.mutate(note.id);
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
