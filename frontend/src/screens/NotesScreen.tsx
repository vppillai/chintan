import { useNotes } from '@/api/queries.ts';
import { NoteRow } from '@/components/NoteRow.tsx';
import { useOnline } from '@/hooks/useOnline.ts';

export function NotesScreen() {
  const { data, isLoading, isError, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useNotes({ state: 'active' });
  const online = useOnline();

  const notes = data?.pages.flatMap((page) => page.items) ?? [];

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

      {isLoading && (
        <p className="screen__count" role="status">
          Loading…
        </p>
      )}

      {isError && notes.length === 0 && (
        <p className="screen__empty" role="status">
          {online
            ? 'Your notes could not be loaded. Pull down to try again.'
            : 'You are offline and no notes are cached on this device yet.'}
        </p>
      )}

      {!isLoading && !isError && notes.length === 0 && (
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
    </div>
  );
}
