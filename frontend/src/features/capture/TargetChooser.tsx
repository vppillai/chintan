import { useId, useState } from 'react';

import { useNotes } from '@/api/queries.ts';
import type { NoteWire } from '@/api/schema.ts';
import { useCachedNote, useCachedNotes } from '@/offline/useNotesCache.ts';

/**
 * Where this recording will be filed, shown and changeable before you speak.
 *
 * "Into · New note" by default, or the note's title when the screen was opened
 * from one ("Record into this") or with `?note=`. Tapping it unfolds a short
 * list — New note first, then the most recent notes — and choosing sets the
 * capture's target before Send. The backend has always accepted `note_id` on
 * `POST /v1/captures`; nothing in the UI offered it, so the only ways to file
 * into a particular note were to say its name and hope the router agreed, or
 * to fix it afterwards from the "needs a target" row.
 *
 * The list is the library the app already holds. Online it is the notes
 * query, which the library screen has almost certainly just fetched; offline
 * it is the corpus cached on the device — recording works with no connection,
 * so the chooser has to as well.
 */

/** Enough to find a note made this week without a search field. */
const RECENT_LIMIT = 20;

export interface TargetChooserProps {
  noteId: string | null;
  onChoose: (noteId: string | null) => void;
  /** After Send the target has left the device and cannot be changed here. */
  disabled?: boolean;
}

export function TargetChooser({ noteId, onChoose, disabled = false }: TargetChooserProps) {
  const [open, setOpen] = useState(false);
  const listId = useId();

  const served = useNotes({ state: 'active' });
  const cached = useCachedNotes('active');
  // The note this screen was opened from may not be among the recent twenty,
  // and its own screen has just cached the full record — so its title is on
  // the device even when the list cannot be reached.
  const opened = useCachedNote(noteId ?? undefined);

  const notes: NoteWire[] =
    served.data?.pages.flatMap((page) => page.items) ?? cached.data ?? [];
  const recent = notes.slice(0, RECENT_LIMIT);

  const chosen = noteId ? (notes.find((note) => note.id === noteId) ?? opened.data) : undefined;
  const title = noteId === null ? 'New note' : (chosen?.title ?? 'This note');

  const choose = (id: string | null): void => {
    onChoose(id);
    setOpen(false);
  };

  return (
    <div className="target-chooser">
      <button
        type="button"
        className="target-chooser__pill"
        aria-expanded={open}
        aria-controls={listId}
        disabled={disabled}
        onClick={() => {
          setOpen((wasOpen) => !wasOpen);
        }}
      >
        {/* Real spaces between the spans: an accessible name is the text
            nodes run together, and "IntoNew note" is not a name. */}
        <span className="target-chooser__into">Into</span>{' '}
        <span className="target-chooser__title">{title}</span>{' '}
        <span className="target-chooser__caret" aria-hidden="true">
          ▾
        </span>
      </button>

      {open && (
        <div
          id={listId}
          className="target-chooser__sheet"
          role="group"
          aria-label="Where this recording goes"
        >
          <ul className="target-chooser__list" role="list">
            <li>
              <button
                type="button"
                className="target-chooser__option"
                aria-pressed={noteId === null}
                onClick={() => {
                  choose(null);
                }}
              >
                New note
              </button>
            </li>
            {recent.map((note) => (
              <li key={note.id}>
                <button
                  type="button"
                  className="target-chooser__option"
                  aria-pressed={note.id === noteId}
                  onClick={() => {
                    choose(note.id);
                  }}
                >
                  {note.title}
                </button>
              </li>
            ))}
          </ul>
          {recent.length === 0 && (
            <p className="target-chooser__empty">
              {served.fetchStatus === 'paused'
                ? 'No notes are on this device yet. The router will file it once you reconnect.'
                : 'No notes yet — this will start the first one.'}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
