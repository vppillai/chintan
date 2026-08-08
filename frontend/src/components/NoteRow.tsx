import { useNavigate } from 'react-router';

import type { NoteWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';

/**
 * A note row is a real <button> (spec §5.7), not a clickable div. v1 shipped
 * divs, which made the entire library unreachable by keyboard and invisible to
 * assistive technology as an actionable thing.
 */
export function NoteRow({ note }: { note: NoteWire }) {
  const navigate = useNavigate();
  const tags = note.tags ?? [];

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
      <span className="note-row__meta">
        <time className="note-row__date numeric" dateTime={note.updated_at}>
          {formatUpdated(note.updated_at)}
        </time>
        {tags.length > 0 && (
          <>
            <span aria-hidden="true">·</span>
            <span className="note-row__tags">{tags.join(', ')}</span>
          </>
        )}
      </span>
    </button>
  );
}

function formatUpdated(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return new Intl.DateTimeFormat(undefined, {
    day: 'numeric',
    month: 'short',
  }).format(date);
}
