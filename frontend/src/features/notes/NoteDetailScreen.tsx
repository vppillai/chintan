import { useNavigate, useParams } from 'react-router';

import { ApiError } from '@/api/problem.ts';
import { useNote } from '@/api/queries.ts';
import type { NoteDetailWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNote } from '@/offline/useNotesCache.ts';

import { NoteActions } from './NoteActions.tsx';
import { Recordings } from './Recordings.tsx';
import { SAVE_LABELS } from './autosave.ts';
import { describeMoment, describeRecordings } from './groups.ts';
import { useNoteEditor } from './useNoteEditor.ts';

/**
 * A note.
 *
 * Top to bottom: the way back, the title, one line of metadata, the body, the
 * recordings it was written from, and the action bar. The cleaned text is the
 * document; the recordings below are its sources. The player used to sit
 * *above* the body with its transcript, so the first thing on screen was the
 * raw material and the note itself was below the fold.
 */
export function NoteDetailScreen() {
  const { id } = useParams<{ id: string }>();
  const online = useOnline();
  const { data: served, isLoading, fetchStatus, error } = useNote(id);
  const cached = useCachedNote(id);

  /*
   * The device's copy stands in when the server has not answered. Only a full
   * note qualifies — `useCachedNote` refuses a list row — because rendering a
   * real title over an empty body invites the user to type into a note whose
   * text is merely missing, and the next PATCH would erase it.
   */
  const note = served ?? cached.data ?? undefined;
  const offlineCopy = !served && Boolean(cached.data);
  const editor = useNoteEditor(note);

  // Paused means offline, not slow: TanStack never runs the query at all, so
  // waiting for it would be waiting forever.
  const paused = fetchStatus === 'paused';

  if ((isLoading || cached.isLoading) && !paused && !note) {
    return (
      <div className="screen">
        <p className="screen__empty" role="status">
          Loading…
        </p>
      </div>
    );
  }

  if (!note) {
    /*
     * Two different sentences, because they are two different situations and
     * the screen used to say the first one for both. A note that is simply not
     * on this device was reported as one that "may have been archived or
     * purged" — describing a deletion that never happened, to a user who could
     * see the note one screen earlier.
     */
    const unreachable = paused || !online || (error instanceof ApiError && error.isOffline);

    return (
      <div className="screen">
        <header className="screen__header screen__header--detail">
          <BackLink />
          <h1>{unreachable ? 'Not on this device' : 'Note not found'}</h1>
        </header>
        <p className="screen__empty">
          {unreachable
            ? 'This note has not been opened on this device, so there is no copy here to read. It will be here once you have a connection.'
            : 'No note with that identifier. It may have been archived or purged.'}
        </p>
      </div>
    );
  }

  return (
    <div className="screen note-screen">
      <header className="screen__header screen__header--detail">
        <BackLink />
      </header>

      {offlineCopy && (
        <p className="screen__count" role="status">
          Saved on this device. Edits are kept here and sent when you reconnect.
        </p>
      )}

      <label className="visually-hidden" htmlFor="note-title">
        Note title
      </label>
      <input
        id="note-title"
        className="note-title-input"
        value={editor.model.draft.title}
        onChange={(event) => {
          editor.edit({ title: event.target.value });
        }}
        onBlur={() => void editor.saveNow()}
      />

      <NoteMeta note={note} tags={editor.model.draft.tags} />

      <SaveIndicator editor={editor} />

      <label className="visually-hidden" htmlFor="note-body">
        Note body
      </label>
      <textarea
        id="note-body"
        className="note-body-input prose"
        value={editor.model.draft.body}
        rows={12}
        onChange={(event) => {
          editor.edit({ body: event.target.value });
        }}
        onBlur={() => void editor.saveNow()}
      />

      <Recordings captures={note.captures ?? []} />

      <NoteActions note={note} editor={editor} />
    </div>
  );
}

/**
 * "Updated today 14:02 · house · 3 recordings · 4:12".
 *
 * The tags shown are the draft's, not the server's, so adding one in the bar
 * shows up here at once rather than after the save lands.
 */
function NoteMeta({ note, tags }: { note: NoteDetailWire; tags: readonly string[] }) {
  const updated = describeMoment(note.updated_at);
  const parts = [
    // "Updated today 14:02", but "Updated 6 Aug 09:14" — a month keeps its case.
    updated ? `Updated ${updated.replace(/^(Today|Yesterday)/, (day) => day.toLowerCase())}` : null,
    ...tags,
    describeRecordings(note),
  ].filter((part): part is string => Boolean(part));

  // A real " · " between the facts, not a CSS pseudo-element: a screen reader
  // reads text, and "housereading list3 recordings" is not a sentence.
  return <p className="note-meta">{parts.join(' · ')}</p>;
}

/**
 * ‹ Notes.
 *
 * Goes back through history when there is history to go back through, so a
 * note opened from a filtered library returns to that filter; only a cold
 * start with nothing beneath it goes to the library directly. React Router
 * numbers its entries in `history.state.idx`, and `useBackGuard` seeds the
 * library under any deep link, so the fallback is rarely taken — it is here
 * for the case where it is.
 */
function BackLink() {
  const navigate = useNavigate();
  return (
    <button
      type="button"
      className="back-link"
      onClick={() => {
        const index = (window.history.state as { idx?: number } | null)?.idx ?? 0;
        if (index > 0) void navigate(-1);
        else void navigate(ROUTES.notes);
      }}
    >
      <Icon name="back" size={18} />
      <span className="visually-hidden">Back to </span>Notes
    </button>
  );
}

/**
 * Autosave state, rendered.
 *
 * v1 swallowed autosave failures entirely, and its "unsaved" indicator was a
 * `.btn-warning` class with no CSS behind it — invisible on every screen.
 */
function SaveIndicator({ editor }: { editor: ReturnType<typeof useNoteEditor> }) {
  const { model } = editor;
  if (model.state === 'clean') return null;

  if (model.state === 'conflict') {
    return (
      <div className="save-conflict" role="alert">
        <p className="save-conflict__title">{SAVE_LABELS.conflict}</p>
        <p className="save-conflict__body">
          A voice capture or another device saved this note while you were editing. Nothing
          has been overwritten — choose which version to keep.
        </p>
        <div className="save-conflict__actions">
          <button type="button" className="save-conflict__action" onClick={editor.takeTheirs}>
            Use the newer version
          </button>
          <button
            type="button"
            className="save-conflict__action"
            onClick={() => {
              editor.keepMine();
              void editor.saveNow();
            }}
          >
            Keep my edits
          </button>
        </div>
      </div>
    );
  }

  return (
    <p
      className="save-indicator"
      data-state={model.state}
      role="status"
      aria-live="polite"
    >
      {model.error ?? SAVE_LABELS[model.state]}
      {model.state === 'error' && (
        <button
          type="button"
          className="save-indicator__retry"
          onClick={() => void editor.saveNow()}
        >
          Try again
        </button>
      )}
    </p>
  );
}
