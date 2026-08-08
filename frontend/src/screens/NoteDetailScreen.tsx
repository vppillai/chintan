import { useNavigate, useParams } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { findPlaceholderNote } from '@/data/placeholderNotes.ts';

export function NoteDetailScreen() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const note = id ? findPlaceholderNote(id) : undefined;

  return (
    <div className="screen">
      <header className="screen__header screen__header--detail">
        <button
          type="button"
          className="icon-button"
          onClick={() => {
            void navigate(ROUTES.notes);
          }}
        >
          <Icon name="back" size={20} />
          <span className="visually-hidden">Back to notes</span>
        </button>
        <h1>{note ? note.title : 'Note not found'}</h1>
      </header>

      {note ? (
        <>
          <p className="screen__meta">
            <span className="numeric">{note.captureCount}</span>{' '}
            {note.captureCount === 1 ? 'capture' : 'captures'} ·{' '}
            <time className="numeric" dateTime={note.updatedAt}>
              {note.updatedAt.slice(0, 10)}
            </time>
          </p>
          <article className="prose">
            <p>{note.snippet}</p>
            <p className="prose__placeholder">
              Inline playback, the transcript panel, and the cleaned note body arrive with
              the API client. This screen exists so the shell&rsquo;s navigation and focus
              behaviour can be exercised end to end.
            </p>
          </article>
          {note.tags.length > 0 && (
            <ul className="tag-list" role="list" aria-label="Tags">
              {note.tags.map((tag) => (
                <li key={tag} className="tag">
                  {tag}
                </li>
              ))}
            </ul>
          )}
        </>
      ) : (
        <p className="screen__empty">
          No note with that identifier. It may have been archived or purged.
        </p>
      )}
    </div>
  );
}
