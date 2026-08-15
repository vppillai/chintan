import { useState } from 'react';
import { useNavigate } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { useBulkPurgeNotes, useBulkRestoreNotes, useNotes } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { Icon } from '@/components/Icon.tsx';
import { describePurge, purgeCountdown } from '@/features/notes/purge.ts';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNotes } from '@/offline/useNotesCache.ts';

/**
 * Archived notes.
 *
 * This screen is one query parameter away from the notes list and was missing
 * entirely: `NotesScreen` hardcoded `state: 'active'`, so everything the user
 * had archived — which was nothing, because nothing could archive — had no
 * surface at all. v1 had an Archive tab.
 *
 * Every row says when it will be purged, because the whole point of a soft
 * delete is that it has a deadline. Where the server sent no date, the row says
 * so rather than doing arithmetic on `undefined` — see `purge.ts`.
 */
export function ArchiveScreen() {
  const navigate = useNavigate();
  const online = useOnline();
  const {
    data,
    isLoading,
    isError,
    error,
    fetchStatus,
    refetch,
    isFetching,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useNotes({ state: 'archived' });
  const cached = useCachedNotes('archived');

  const [selecting, setSelecting] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [confirming, setConfirming] = useState<'restore' | 'purge' | null>(null);
  const bulkRestore = useBulkRestoreNotes();
  const bulkPurge = useBulkPurgeNotes();

  const toggleSelect = (noteId: string): void => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(noteId)) next.delete(noteId);
      else next.add(noteId);
      return next;
    });
  };

  const exitSelecting = (): void => {
    setSelecting(false);
    setSelectedIds(new Set());
  };

  // TanStack pauses rather than fails when the browser reports no connection,
  // so neither isLoading nor isError is ever true offline. Same trap as the
  // notes list; same explicit branch, and the same fallback to what the device
  // has stored.
  const paused = fetchStatus === 'paused';
  const serverNotes = data?.pages.flatMap((page) => page.items);
  const notes = serverNotes ?? cached.data ?? [];
  const nothingToShow = notes.length === 0;

  return (
    <div className="screen">
      <header className="screen__header screen__header--detail">
        <button
          type="button"
          className="icon-button"
          onClick={() => void navigate(ROUTES.notes)}
        >
          <Icon name="back" size={20} />
          <span className="visually-hidden">Back to notes</span>
        </button>
        <h1>Archive</h1>
        {notes.length > 0 && (
          <button
            type="button"
            className="screen__action"
            onClick={() => {
              if (selecting) exitSelecting();
              else setSelecting(true);
            }}
          >
            {selecting ? 'Cancel' : 'Select'}
          </button>
        )}
      </header>

      <p className="screen__count">
        Archived notes stay here until they are deleted. Open one to restore it or
        delete it forever.
      </p>

      {isLoading && !paused && (
        <p className="screen__count" role="status">
          Loading…
        </p>
      )}

      {(!online || paused) && nothingToShow && (
        <p className="screen__empty" role="status">
          You are offline, so the archive could not be loaded.
        </p>
      )}

      {isError && online && !paused && nothingToShow && (
        <div className="screen__empty" role="alert">
          <p>{error instanceof ApiError ? error.userMessage : 'The archive could not be loaded.'}</p>
          <div className="screen__actions">
            <button
              type="button"
              className="screen__action"
              onClick={() => void refetch()}
              disabled={isFetching}
            >
              {isFetching ? 'Trying…' : 'Try again'}
            </button>
          </div>
        </div>
      )}

      {online && !paused && !isLoading && !isError && nothingToShow && (
        <p className="screen__empty">Nothing is archived.</p>
      )}

      <ul className="note-list" role="list">
        {notes.map((note) => (
          <li key={note.id}>
            <ArchivedRow
              note={note}
              selectable={selecting}
              selected={selectedIds.has(note.id)}
              onToggleSelect={toggleSelect}
            />
          </li>
        ))}
      </ul>

      {selecting && (
        <div className="bulk-bar" role="toolbar" aria-label="Bulk actions">
          <button
            type="button"
            className="screen__action"
            onClick={() => {
              setSelectedIds((prev) =>
                prev.size === notes.length ? new Set() : new Set(notes.map((note) => note.id)),
              );
            }}
          >
            {selectedIds.size === notes.length ? 'Deselect all' : 'Select all'}
          </button>
          <p className="screen__count" role="status">
            <span className="numeric">{selectedIds.size}</span> selected
          </p>
          <button
            type="button"
            className="screen__action"
            disabled={selectedIds.size === 0 || bulkRestore.isPending}
            onClick={() => {
              setConfirming('restore');
            }}
          >
            {bulkRestore.isPending ? 'Restoring…' : 'Restore'}
          </button>
          <button
            type="button"
            className="screen__action screen__action--destructive"
            disabled={selectedIds.size === 0 || bulkPurge.isPending}
            onClick={() => {
              setConfirming('purge');
            }}
          >
            {bulkPurge.isPending ? 'Deleting…' : 'Delete forever'}
          </button>
        </div>
      )}

      <ConfirmDialog
        open={confirming === 'restore'}
        title={`Restore ${selectedIds.size} ${selectedIds.size === 1 ? 'note' : 'notes'}?`}
        body="They leave the archive and return to your notes."
        confirmLabel="Restore them"
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          bulkRestore.mutate(Array.from(selectedIds), { onSuccess: exitSelecting });
        }}
      />

      {/*
        Bulk purge is the "empty the archive" control: select all, then this.
        Typing a fixed word rather than each note's own title — the single-note
        purge's gate — because requiring every selected title typed in one
        dialog does not scale past a couple of notes and would just get pasted
        anyway; this is still a deliberate second step, not a bare "OK".
      */}
      <ConfirmDialog
        open={confirming === 'purge'}
        title={`Delete ${selectedIds.size} ${selectedIds.size === 1 ? 'note' : 'notes'} forever?`}
        body="Their recordings and transcripts are destroyed. This cannot be undone, and there is no copy on the server or on any other device you have signed in on."
        confirmLabel="Delete them forever"
        requireText="delete"
        requireLabel='Type "delete" to confirm'
        destructive
        onCancel={() => {
          setConfirming(null);
        }}
        onConfirm={() => {
          setConfirming(null);
          bulkPurge.mutate(Array.from(selectedIds), { onSuccess: exitSelecting });
        }}
      />

      {hasNextPage && (
        <button
          type="button"
          className="load-more"
          onClick={() => void fetchNextPage()}
          disabled={isFetchingNextPage}
        >
          {isFetchingNextPage ? 'Loading…' : 'Load more'}
        </button>
      )}
    </div>
  );
}

/**
 * Deliberately not `NoteRow`. An archived row's second line is its deadline,
 * not its tags, and reusing the row would mean threading a variant flag through
 * the component the whole library renders.
 */
function ArchivedRow({
  note,
  selectable = false,
  selected = false,
  onToggleSelect,
}: {
  note: NoteWire;
  selectable?: boolean;
  selected?: boolean;
  onToggleSelect?: (noteId: string) => void;
}) {
  const navigate = useNavigate();
  const countdown = purgeCountdown(note.purge_after);

  const body = (
    <>
      <span className="note-row__title">{note.title}</span>
      {note.snippet && <span className="note-row__snippet">{note.snippet}</span>}
      <span className="note-row__meta" data-purge={countdown.kind}>
        {describePurge(countdown)}
      </span>
    </>
  );

  if (selectable) {
    return (
      <label className="note-row note-row--selectable">
        <input
          type="checkbox"
          className="note-row__checkbox"
          checked={selected}
          onChange={() => {
            onToggleSelect?.(note.id);
          }}
        />
        <span className="note-row__body">{body}</span>
      </label>
    );
  }

  return (
    <button
      type="button"
      className="note-row"
      onClick={() => {
        void navigate(ROUTES.note(note.id));
      }}
    >
      {body}
    </button>
  );
}
