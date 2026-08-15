import { useNavigate } from 'react-router';

import type { NoteWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';

export interface NoteRowProps {
  note: NoteWire;
  /**
   * Bulk-select mode. A real `<input type="checkbox">` inside a `<label>`,
   * not a styled div with a click handler — the row's own doc comment already
   * makes that argument for the plain case, and a checkbox is the one control
   * every screen reader and every keyboard already knows how to operate.
   */
  selectable?: boolean;
  selected?: boolean;
  onToggleSelect?: (noteId: string) => void;
  /** Date-only by default; see NotesScreen's "Show time" toggle. */
  timestamped?: boolean;
}

/**
 * A note row is a real <button> (spec §5.7), not a clickable div. v1 shipped
 * divs, which made the entire library unreachable by keyboard and invisible to
 * assistive technology as an actionable thing.
 */
export function NoteRow({
  note,
  selectable = false,
  selected = false,
  onToggleSelect,
  timestamped = false,
}: NoteRowProps) {
  const navigate = useNavigate();
  const tags = note.tags ?? [];

  const body = (
    <>
      <span className="note-row__title">{note.title}</span>
      {note.snippet && <span className="note-row__snippet">{note.snippet}</span>}
      <span className="note-row__meta">
        <time className="note-row__date numeric" dateTime={note.updated_at}>
          {formatUpdated(note.updated_at, timestamped)}
        </time>
        {tags.length > 0 && (
          <>
            <span aria-hidden="true">·</span>
            <span className="note-row__tags">{tags.join(', ')}</span>
          </>
        )}
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

function formatUpdated(iso: string, timestamped: boolean): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return new Intl.DateTimeFormat(
    undefined,
    timestamped
      ? { day: 'numeric', month: 'short', hour: 'numeric', minute: '2-digit' }
      : { day: 'numeric', month: 'short' },
  ).format(date);
}
