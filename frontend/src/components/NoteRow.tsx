import { useNavigate } from 'react-router';

import type { NoteWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { describeRecordings, formatRowTime } from '@/features/notes/groups.ts';
import { describePurge, purgeCountdown } from '@/features/notes/purge.ts';

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
  /**
   * A search hit: the excerpt around the match stands in for the snippet, and
   * the matched term is marked in it. Absent on the plain library.
   */
  excerpt?: string;
  highlight?: string;
}

/**
 * A note row is a real <button> (spec §5.7), not a clickable div. v1 shipped
 * divs, which made the entire library unreachable by keyboard and invisible to
 * assistive technology as an actionable thing.
 *
 * Serif title with the time on the right in tabular serif numerals, two lines
 * of the note, then a meta line: the purge countdown for an archived note, the
 * tags, and — when the payload carries them — how many recordings are behind
 * it and how long they run.
 */
export function NoteRow({
  note,
  selectable = false,
  selected = false,
  onToggleSelect,
  excerpt,
  highlight,
}: NoteRowProps) {
  const navigate = useNavigate();
  const tags = note.tags ?? [];
  const time = formatRowTime(note.updated_at);
  const recordings = describeRecordings(note);
  const countdown = note.archived ? purgeCountdown(note.purge_after) : null;
  const snippet = excerpt ?? note.snippet;
  const hasMeta = countdown !== null || tags.length > 0 || recordings !== null;

  const body = (
    <>
      <span className="note-row__head">
        <span className="note-row__title">{note.title}</span>
        {time && (
          <time className="note-row__time numeric" dateTime={note.updated_at}>
            {time}
          </time>
        )}
      </span>
      {snippet && (
        <span className="note-row__snippet">
          <Marked text={snippet} term={highlight ?? ''} />
        </span>
      )}
      {hasMeta && (
        <span className="note-row__meta">
          {countdown && (
            <span className="note-row__purge" data-purge={countdown.kind}>
              {describePurge(countdown)}
            </span>
          )}
          {tags.length > 0 && (
            <span className="note-row__tags">
              {tags.map((tag) => (
                <span key={tag} className="note-row__tag">
                  {tag}
                </span>
              ))}
            </span>
          )}
          {recordings && <span className="note-row__recordings numeric">{recordings}</span>}
        </span>
      )}
    </>
  );

  if (selectable) {
    return (
      <label className="note-row note-row--selectable">
        {/*
          A 24 px box inside a 44 px one: the control itself meets the WCAG
          2.5.8 minimum (it was 20 px), and the wrapper is the thumb's target.
          The whole row is the label, so a tap anywhere toggles it regardless.
        */}
        <span className="note-row__check">
          <input
            type="checkbox"
            className="note-row__checkbox"
            checked={selected}
            onChange={() => {
              onToggleSelect?.(note.id);
            }}
          />
        </span>
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

/** Renders the match in situ, with the hit marked rather than stripped out. */
function Marked({ text, term }: { text: string; term: string }) {
  const index = term ? text.toLowerCase().indexOf(term.toLowerCase()) : -1;
  if (index === -1) return <>{text}</>;

  return (
    <>
      {text.slice(0, index)}
      <mark className="search-hit">{text.slice(index, index + term.length)}</mark>
      {text.slice(index + term.length)}
    </>
  );
}
