import { useState } from 'react';

import { useNotes, useSetCaptureTarget } from '@/api/queries.ts';
import type { CaptureTargetWire, CaptureWire } from '@/api/schema.ts';

/**
 * Answers "which note should this go in?", leading with the router's answer.
 *
 * The pipeline pays for an LLM call to decide this and stores the result on the
 * capture, so the prompt leads with that answer. Offering an unranked list of
 * every note the user has, or a bare `Add to "<note>"`, would hide that
 * anything had been computed at all.
 *
 * Exactly one of the two fields is ever set. `suggested_note_id` names an
 * existing note the router was confident enough to propose but not confident
 * enough to append to unasked; `suggested_title` is what it would call a new
 * note when it found no plausible destination.
 *
 * Exported for the note screen's recording rows, which offer the same answer
 * for a capture read from inside a note.
 */
export function TargetPrompt({ capture }: { capture: CaptureWire }) {
  const setTarget = useSetCaptureTarget();
  const { data } = useNotes({ state: 'active' });
  /** The user asked to see the library instead of the router's answer. */
  const [browsing, setBrowsing] = useState(false);
  /** The library is open on the path where there was no answer to lead with. */
  const [picking, setPicking] = useState(false);

  const notes = data?.pages.flatMap((page) => page.items) ?? [];

  /*
   * Resolved against the loaded library rather than fetched on its own. The
   * router can name a note beyond the first page, and there is no honest
   * `Add to ""` — so an unresolvable suggestion falls back to the plain picker
   * rather than to a button with a hole in it.
   */
  const suggestedNote = capture.suggested_note_id
    ? notes.find((note) => note.id === capture.suggested_note_id)
    : undefined;
  const suggestedTitle = capture.suggested_title?.trim() ?? '';

  const suggestion: { label: string; target: CaptureTargetWire } | null = suggestedNote
    ? { label: `Add to “${suggestedNote.title}”`, target: { note_id: suggestedNote.id } }
    : suggestedTitle
      ? { label: `Start “${suggestedTitle}”`, target: { new_note_title: suggestedTitle } }
      : null;

  const choose = (target: CaptureTargetWire): void => {
    setTarget.mutate({ captureId: capture.id, target });
  };

  if (suggestion && !browsing) {
    return (
      <div className="filing-row__actions">
        <button
          type="button"
          className="filing-row__action filing-row__action--primary"
          disabled={setTarget.isPending}
          onClick={() => {
            choose(suggestion.target);
          }}
        >
          <span>{setTarget.isPending ? 'Filing…' : suggestion.label}</span>
        </button>

        {/* Disagreeing has to be one tap, or the suggestion becomes a trap. */}
        <button
          type="button"
          className="filing-row__action"
          disabled={setTarget.isPending}
          onClick={() => {
            setBrowsing(true);
          }}
        >
          <span>Choose another note</span>
        </button>

        {setTarget.isError && (
          <p className="target-picker__error" role="alert">
            That did not go through. Try again.
          </p>
        )}
      </div>
    );
  }

  const open = browsing || picking;

  return (
    <>
      {/*
        With no suggestion the library stays behind a tap, as it has: a list of
        every note the user owns is not something to unfold in the middle of
        the library unprompted.
      */}
      {!browsing && (
        <div className="filing-row__actions">
          <button
            type="button"
            className="filing-row__action"
            aria-expanded={picking}
            onClick={() => {
              setPicking((wasOpen) => !wasOpen);
            }}
          >
            <span>{picking ? 'Cancel' : 'Choose a note'}</span>
          </button>
        </div>
      )}

      {open && (
        <BrowsePicker
          captureId={capture.id}
          notes={notes}
          onChoose={choose}
          pending={setTarget.isPending}
          failed={setTarget.isError}
          /* Only offered when there is something to go back to. */
          onBack={
            suggestion
              ? () => {
                  setBrowsing(false);
                }
              : null
          }
        />
      )}
    </>
  );
}

/**
 * The whole library, plus a field for a note that does not exist yet.
 *
 * Both spellings the contract accepts are offered — an existing note, or a new
 * one by title — because the router asks this question precisely when it could
 * not tell whether the thought belonged to something the user already has.
 */
function BrowsePicker({
  captureId,
  notes,
  onChoose,
  pending,
  failed,
  onBack,
}: {
  captureId: string;
  notes: readonly { id: string; title: string }[];
  onChoose: (target: CaptureTargetWire) => void;
  pending: boolean;
  failed: boolean;
  onBack: (() => void) | null;
}) {
  const [title, setTitle] = useState('');

  return (
    <div className="target-picker">
      {onBack && (
        <button
          type="button"
          className="target-picker__back"
          disabled={pending}
          onClick={onBack}
        >
          Back to the suggestion
        </button>
      )}

      <ul className="target-picker__list" role="list">
        {notes.map((note) => (
          <li key={note.id}>
            <button
              type="button"
              className="target-picker__option"
              disabled={pending}
              onClick={() => {
                onChoose({ note_id: note.id });
              }}
            >
              {note.title}
            </button>
          </li>
        ))}
      </ul>

      <form
        className="target-picker__new"
        onSubmit={(event) => {
          event.preventDefault();
          const trimmed = title.trim();
          if (!trimmed) return;
          onChoose({ new_note_title: trimmed });
        }}
      >
        <label className="visually-hidden" htmlFor={`new-note-${captureId}`}>
          New note title
        </label>
        <input
          id={`new-note-${captureId}`}
          className="target-picker__input"
          value={title}
          placeholder="Or start a new note"
          onChange={(event) => {
            setTitle(event.target.value);
          }}
        />
        <button
          type="submit"
          className="target-picker__option"
          disabled={pending || title.trim().length === 0}
        >
          Create
        </button>
      </form>

      {failed && (
        <p className="target-picker__error" role="alert">
          That did not go through. Try again.
        </p>
      )}
    </div>
  );
}
