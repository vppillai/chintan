import { useId } from 'react';
import { useSearchParams } from 'react-router';

import { NoteRow } from '@/components/NoteRow.tsx';
import { searchPlaceholderNotes } from '@/data/placeholderNotes.ts';

/**
 * The query lives in the URL (`/search?q=…`), not in component state, so a
 * search is shareable, restorable on reload, and reachable by Back — the same
 * rule the rest of the shell follows (§5.2).
 */
export function SearchScreen() {
  const [params, setParams] = useSearchParams();
  const query = params.get('q') ?? '';
  const results = searchPlaceholderNotes(query);
  const inputId = useId();
  const resultsId = useId();

  return (
    <div className="screen">
      <header className="screen__header">
        <h1>Search</h1>
      </header>

      <form
        className="search-form"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
        }}
      >
        <label className="visually-hidden" htmlFor={inputId}>
          Search notes
        </label>
        <input
          id={inputId}
          className="search-input"
          type="search"
          value={query}
          placeholder="Search notes"
          autoComplete="off"
          aria-controls={resultsId}
          onChange={(event) => {
            const next = event.target.value;
            setParams(
              next ? { q: next } : {},
              // Typing must not push a history entry per keystroke, or Back
              // becomes a character-by-character undo instead of leaving search.
              { replace: true },
            );
          }}
        />
      </form>

      <p className="screen__count" aria-live="polite">
        {query
          ? `${results.length} ${results.length === 1 ? 'result' : 'results'}`
          : 'Type to search your notes.'}
      </p>

      <ul id={resultsId} className="note-list" role="list">
        {results.map((note) => (
          <li key={note.id}>
            <NoteRow note={note} />
          </li>
        ))}
      </ul>
    </div>
  );
}
