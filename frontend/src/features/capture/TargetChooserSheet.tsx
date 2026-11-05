import { useId, useMemo, useState } from 'react';

import { useCreateNote } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { openItemsText, parseChecklist } from '@/features/notes/checklist.ts';
import { formatRowTime } from '@/features/notes/groups.ts';
import { useOnline } from '@/hooks/useOnline.ts';

import { DEFAULT_TARGET_LABEL } from './TargetChooser.tsx';

/**
 * The chooser's unfolded sheet, a chunk of its own: most recordings never
 * open it, and the launch carries every byte against a fixed budget. A field
 * finds any note the device holds and names a new one; "Chintan decides";
 * then the recent notes, or what the field found.
 */
export function TargetChooserSheet({
  id,
  noteId,
  everything,
  recent,
  paused,
  onChoose,
}: {
  id: string;
  noteId: string | null;
  everything: readonly NoteWire[];
  recent: readonly NoteWire[];
  paused: boolean;
  onChoose: (id: string | null, made?: NoteWire) => void;
}) {
  const searchId = useId();
  const [query, setQuery] = useState('');
  const term = query.trim();
  const online = useOnline();
  const create = useCreateNote();
  const [createFailed, setCreateFailed] = useState(false);
  const found = useMemo(() => {
    const folded = term.toLowerCase();
    if (!folded) return recent;
    return everything.filter(
      (note) =>
        note.title.toLowerCase().includes(folded) ||
        (note.aliases ?? []).some((alias) => alias.toLowerCase().includes(folded)) ||
        (note.tags ?? []).some((tag) => tag.toLowerCase().includes(folded)),
    );
  }, [term, recent, everything]);
  // Offered while the name is not already a note's title: two "Roof"s is
  // the routing mistake this exists to avoid.
  const offerNew =
    term !== '' && !everything.some((note) => note.title.toLowerCase() === term.toLowerCase());
  const choose = (next: string | null, made?: NoteWire): void => {
    onChoose(next, made);
  };
  const startNote = (): void => {
    setCreateFailed(false);
    create.mutate(
      { title: term, kind: 'note' },
      {
        onSuccess: (created) => {
          choose(created.id, created);
        },
        onError: () => {
          setCreateFailed(true);
        },
      },
    );
  };
  return (
    <div
      id={id}
      className="target-chooser__sheet"
      role="group"
      aria-label="Where this recording goes"
    >
      <label className="visually-hidden" htmlFor={searchId}>
        Find a note, or name a new one
      </label>
      <input
        id={searchId}
        className="move-sheet__search"
        type="search"
        value={query}
        placeholder="Find or name a note"
        autoComplete="off"
        maxLength={200}
        onChange={(event) => {
          setCreateFailed(false);
          setQuery(event.target.value);
        }}
      />
      <ul className="target-chooser__list" role="list">
        {offerNew && online && (
          <li>
            <button
              type="button"
              className="target-chooser__option"
              disabled={create.isPending}
              onClick={startNote}
            >
              New note &ldquo;{term}&rdquo;
            </button>
          </li>
        )}
        <li>
          <button
            type="button"
            className="target-chooser__option"
            aria-pressed={noteId === null}
            onClick={() => {
              choose(null);
            }}
          >
            {DEFAULT_TARGET_LABEL}
          </button>
        </li>
        {found.map((note) => (
          <li key={note.id}>
            <button
              type="button"
              className="target-chooser__option"
              aria-pressed={note.id === noteId}
              onClick={() => {
                choose(note.id);
              }}
            >
              <span className="target-chooser__option-title">{note.title}</span>
              {/* Part of the name on purpose: two "Roof repair"s are as
                  alike to a screen reader as to the eye. */}
              <span className="target-chooser__option-meta">{optionMeta(note)}</span>
            </button>
          </li>
        ))}
      </ul>
      {offerNew && !online && (
        <p className="target-chooser__empty">New note needs a connection.</p>
      )}
      {createFailed && (
        <p className="target-chooser__empty" role="alert">
          The note could not be created. Try again.
        </p>
      )}
      {term === '' && recent.length === 0 && (
        <p className="target-chooser__empty">
          {paused
            ? 'No notes are on this device yet. The router will file it once you reconnect.'
            : 'No notes yet — this will start the first one.'}
        </p>
      )}
    </div>
  );
}

/**
 * What tells two notes with the same title apart: when it was last touched
 * and how it begins, cut at forty characters so the row stays one line.
 *
 * Characters as a reader counts them, not code points. In Malayalam, Tamil or
 * Hindi the fortieth code point is a vowel sign or a virama as often as a
 * letter, and a cut there strands the sign — a dotted circle on screen, a
 * broken syllable in the option's name — or takes it off its consonant. The
 * cut is marked, because the name is read where the CSS ellipsis is not.
 */
const GRAPHEMES = new Intl.Segmenter(undefined, { granularity: 'grapheme' });
const META_SNIPPET_GRAPHEMES = 40;

export function optionMeta(note: NoteWire): string {
  const when = formatRowTime(note.updated_at);
  // A checklist's snippet is its raw `- [ ]` lines; the row and the Move
  // sheet show the open items as words (QA 2026-09-21, finding 9).
  const text =
    note.kind === 'checklist'
      ? openItemsText(parseChecklist(note.snippet ?? ''))
      : (note.snippet ?? '');
  const graphemes = Array.from(GRAPHEMES.segment(text.trim()), (s) => s.segment);
  const snippet =
    graphemes.length > META_SNIPPET_GRAPHEMES
      ? `${graphemes.slice(0, META_SNIPPET_GRAPHEMES).join('')}…`
      : graphemes.join('');
  return [when, snippet].filter(Boolean).join(' · ');
}

