import { Link } from 'react-router';

import { useNotes } from '@/api/queries.ts';
import { ApiError } from '@/api/problem.ts';
import { ROUTES } from '@/app/routes.ts';
import { NoteRow } from '@/components/NoteRow.tsx';
import { useOnline } from '@/hooks/useOnline.ts';

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

  const notes = data?.pages.flatMap((page) => page.items) ?? [];

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
      </header>

      {isLoading && !paused && (
        <p className="screen__count" role="status">
          Loading…
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
            <NoteRow note={note} />
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

      {/*
        The way into the archive, and the only one. Placed after the list rather
        than in the header so it never competes with the notes themselves — v1
        made Archive a tab beside All, which gave a rarely-wanted filter the same
        weight as the library.
      */}
      <div className="screen__actions">
        <Link className="screen__action" to={ROUTES.archive}>
          Archive
        </Link>
      </div>
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
