import { useNavigate } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { useNotes } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
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
            <ArchivedRow note={note} />
          </li>
        ))}
      </ul>

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
function ArchivedRow({ note }: { note: NoteWire }) {
  const navigate = useNavigate();
  const countdown = purgeCountdown(note.purge_after);

  return (
    <button
      type="button"
      className="note-row"
      onClick={() => {
        void navigate(ROUTES.note(note.id));
      }}
    >
      <span className="note-row__title">{note.title}</span>
      {note.snippet && <span className="note-row__snippet">{note.snippet}</span>}
      <span className="note-row__meta" data-purge={countdown.kind}>
        {describePurge(countdown)}
      </span>
    </button>
  );
}
