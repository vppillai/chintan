import { useNavigate } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import type { PlaceholderNote } from '@/data/placeholderNotes.ts';

/**
 * A note row is a real <button> (spec §5.7), not a clickable div. v1 shipped
 * divs, which made the entire library unreachable by keyboard and invisible to
 * assistive technology as an actionable thing.
 */
export function NoteRow({ note }: { note: PlaceholderNote }) {
  const navigate = useNavigate();

  return (
    <button
      type="button"
      className="note-row"
      onClick={() => {
        void navigate(ROUTES.note(note.id));
      }}
    >
      <span className="note-row__title">{note.title}</span>
      <span className="note-row__snippet">{note.snippet}</span>
      <span className="note-row__meta">
        <time className="numeric" dateTime={note.updatedAt}>
          {formatUpdated(note.updatedAt)}
        </time>
        <span aria-hidden="true">·</span>
        <span className="numeric">{note.captureCount}</span>
        <span>{note.captureCount === 1 ? 'capture' : 'captures'}</span>
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
