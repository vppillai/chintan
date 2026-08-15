import { useState } from 'react';
import { Link } from 'react-router';

import { useBulkArchiveNotes, useNotes } from '@/api/queries.ts';
import { ApiError } from '@/api/problem.ts';
import { ROUTES } from '@/app/routes.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { NoteRow } from '@/components/NoteRow.tsx';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNotes } from '@/offline/useNotesCache.ts';

/**
 * Whether a row's date also shows a time. Local to this device, like the
 * theme preference — this is a display choice, not something that needs to
 * agree across sessions the way an edit does, so it does not go through
 * `PUT /v1/settings`.
 */
const SHOW_TIME_KEY = 'chintan.notes.showTime';

function readShowTime(): boolean {
  try {
    return window.localStorage.getItem(SHOW_TIME_KEY) === 'true';
  } catch {
    // Storage denied (private mode, quota). Date-only is the default either way.
    return false;
  }
}

function writeShowTime(value: boolean): void {
  try {
    window.localStorage.setItem(SHOW_TIME_KEY, String(value));
  } catch {
    // Not persisted this session, but the toggle still works for it.
  }
}

export function NotesScreen() {
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
  } = useNotes({ state: 'active' });
  const online = useOnline();
  const cached = useCachedNotes('active');

  /*
   * Multi-select, for archiving several notes at once rather than one at a
   * time from inside each note's own screen. Confined to this component's
   * state rather than the URL: leaving the screen is exactly when "which
   * notes were selected" should stop mattering.
   */
  const [selecting, setSelecting] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [confirmingBulkArchive, setConfirmingBulkArchive] = useState(false);
  const bulkArchive = useBulkArchiveNotes();
  const [timestamped, setTimestamped] = useState(readShowTime);

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

  /*
   * TanStack *pauses* a query when the browser reports no connection: it does
   * not run and it does not fail, so neither `isLoading` nor `isError` is ever
   * true. The screen used to fall through both and render the brand-new-user
   * empty state — "Nothing here yet. Tap the record button and say something." —
   * directly under a banner saying "Offline — showing saved notes.". To someone
   * with a full library walking into a tunnel, their whole library had been
   * deleted.
   */
  const paused = fetchStatus === 'paused';

  /*
   * The device's copy, shown only when the server has answered nothing at all.
   *
   * The condition is `data === undefined`, not "offline": a server that has
   * answered is the authority even when it answered with an empty list, and
   * falling back on an empty *response* would resurrect notes the user had just
   * archived on another device.
   */
  const serverNotes = data?.pages.flatMap((page) => page.items);
  const notes = serverNotes ?? cached.data ?? [];
  const fromCache = serverNotes === undefined && notes.length > 0;
  /*
   * Labelled only once it is clear the server is not going to answer. While a
   * fetch is still in flight the cached list is simply *shown* — instantly,
   * which is the whole point of holding it — and saying "saved on this device"
   * over a list about to be replaced would be noise.
   */
  const showingCached = fromCache && (!online || paused || isError);
  const nothingToShow = notes.length === 0;

  return (
    <div className="screen">
      <header className="screen__header">
        <h1>Notes</h1>
        {notes.length > 0 && (
          <p className="screen__count">
            <span className="numeric">{notes.length}</span>{' '}
            {notes.length === 1 ? 'note' : 'notes'}
          </p>
        )}
        {notes.length > 0 && (
          <button
            type="button"
            className="screen__action"
            onClick={() => {
              setTimestamped((prev) => {
                writeShowTime(!prev);
                return !prev;
              });
            }}
          >
            {timestamped ? 'Hide time' : 'Show time'}
          </button>
        )}
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

      {isLoading && !paused && !fromCache && (
        <p className="screen__count" role="status">
          Loading…
        </p>
      )}

      {showingCached && (
        <p className="screen__count" role="status">
          Saved on this device. Recordings and transcripts need a connection.
        </p>
      )}

      {(!online || paused) && nothingToShow && (
        <p className="screen__empty" role="status">
          You are offline and no notes are cached on this device yet. They will appear when
          you reconnect.
        </p>
      )}

      {/*
        A real control, not an instruction for a gesture the app does not
        implement. The previous copy said "Pull down to try again." — there is
        no pull-to-refresh anywhere in the codebase, and this region rendered
        zero buttons, so the only escapes were switching tabs to force a
        remount or reloading.
      */}
      {isError && online && !paused && nothingToShow && (
        <div className="screen__empty" role="alert">
          <p>{failureMessage(error)}</p>
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
        <p className="screen__empty">
          Nothing here yet. Tap the record button and say something.
        </p>
      )}

      <ul className="note-list" role="list">
        {notes.map((note) => (
          <li key={note.id}>
            <NoteRow
              note={note}
              selectable={selecting}
              selected={selectedIds.has(note.id)}
              onToggleSelect={toggleSelect}
              timestamped={timestamped}
            />
          </li>
        ))}
      </ul>

      {/*
        Cursor pagination is on every list endpoint by contract, so the library
        loads a page at a time rather than assuming the corpus is small.
      */}
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

      {selecting ? (
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
            disabled={selectedIds.size === 0 || bulkArchive.isPending}
            onClick={() => {
              setConfirmingBulkArchive(true);
            }}
          >
            {bulkArchive.isPending ? 'Archiving…' : 'Archive'}
          </button>
        </div>
      ) : (
        /*
          The way into the archive, and the only one. Placed after the list
          rather than in the header so it never competes with the notes
          themselves — v1 made Archive a tab beside All, which gave a
          rarely-wanted filter the same weight as the library.
        */
        <div className="screen__actions">
          <Link className="screen__action" to={ROUTES.archive}>
            Archive
          </Link>
        </div>
      )}

      <ConfirmDialog
        open={confirmingBulkArchive}
        title={`Archive ${selectedIds.size} ${selectedIds.size === 1 ? 'note' : 'notes'}?`}
        body="They leave your notes and move to the archive, where you can restore them until they are deleted."
        confirmLabel="Archive them"
        destructive
        onCancel={() => {
          setConfirmingBulkArchive(false);
        }}
        onConfirm={() => {
          setConfirmingBulkArchive(false);
          bulkArchive.mutate(Array.from(selectedIds), {
            onSuccess: exitSelecting,
          });
        }}
      />
    </div>
  );
}

/**
 * The server's own wording where there is one, so a 401 reads as "sign in
 * again" rather than as a generic fault the user cannot act on.
 */
function failureMessage(error: unknown): string {
  if (error instanceof ApiError) return error.userMessage;
  return 'Your notes could not be loaded.';
}
