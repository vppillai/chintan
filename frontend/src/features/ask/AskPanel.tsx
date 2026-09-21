import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useNavigate } from 'react-router';

import { useCreateNote } from '@/api/queries.ts';
import { ROUTES } from '@/app/routes.ts';
import type { NoteWire } from '@/api/schema.ts';
import { formatRowTime } from '@/features/notes/groups.ts';
import { renderMarkdown } from '@/features/notes/markdown.ts';
import { useCachedNotes } from '@/offline/useNotesCache.ts';

import { COST_NOTE, costNoteDismissed, dismissCostNote } from './costNote.ts';
import { TIMEOUT_MESSAGE, noteFromThread, sourceLabels, type AskTurn } from './thread.ts';
import type { AskThread } from './useAskThread.ts';

/**
 * The Ask panel: what stands in for the list while the field is in Ask mode.
 *
 * A short thread — each question echoed, then its answer drawn from the
 * notes, then the notes it drew on as chips that open them, three at a time
 * with the rest behind "+n more" — and two ways out: Save as note, which
 * turns the thread into a note titled by the first question, and Clear. The
 * follow-up is typed into the library's own field above the panel, which
 * changes its placeholder once a thread is open: two fields for one
 * conversation was one too many (round-3 T21). The whole thread is one
 * polite live region, so an answer landing is read out without anyone
 * having to go and find it.
 *
 * An answer the notes could not give is still an answer: the worker says so
 * plainly with `grounded` false, and the panel labels it "Not in your notes"
 * rather than dressing it up as a finding. A failed one shows the server's
 * fixed sentence and a Try again; so does one the client stopped waiting for.
 */
export function AskPanel({ thread, id }: { thread: AskThread; id?: string }) {
  const headingId = useId();
  const navigate = useNavigate();
  const create = useCreateNote();
  const [costNoteShown, setCostNoteShown] = useState(() => !costNoteDismissed());
  const [saveError, setSaveError] = useState<string | null>(null);

  const { turns, busy } = thread;
  const note = noteFromThread(turns);

  /*
   * Save as note can settle after the panel has gone — a source chip tapped
   * while the POST was in flight. The note exists then whatever happens
   * here, so the thread must still be ended (or the returning panel offers
   * Save again under a fresh key, and a second note is made); what must not
   * happen is a navigation the user did not just ask for, from a screen they
   * have already left. `thread.clear` writes storage directly when no panel
   * holds the state.
   */
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  /*
   * The date and snippet beside a source whose title another source shares.
   * The wire carries only id and title, so both come from the library's own
   * copy on the device — the corpus every list the user has seen is written
   * to — which is what the row for the same note shows.
   */
  const cached = useCachedNotes('active');
  const notes = cached.data;
  const lookup = useCallback(
    (noteId: string): NoteWire | undefined => notes?.find((candidate) => candidate.id === noteId),
    [notes],
  );

  const saveAsNote = (): void => {
    if (!note) return;
    setSaveError(null);
    // Chained on the promise rather than given as per-call callbacks, which
    // TanStack drops once the component has unmounted (see `mounted`).
    void create.mutateAsync(note).then(
      (created) => {
        thread.clear();
        if (mounted.current) void navigate(ROUTES.note(created.id));
      },
      () => {
        if (mounted.current) setSaveError('The note could not be saved. Try again.');
      },
    );
  };

  return (
    <section id={id} className="ask" aria-labelledby={headingId}>
      <h2 id={headingId} className="visually-hidden">
        Ask your notes
      </h2>

      {costNoteShown && (
        <p className="ask__cost">
          <span>{COST_NOTE}</span>
          <button
            type="button"
            className="ask__cost-dismiss"
            onClick={() => {
              dismissCostNote();
              setCostNoteShown(false);
            }}
          >
            Got it
          </button>
        </p>
      )}

      {turns.length === 0 ? (
        <p className="ask__intro">
          Ask a question and the answer is drawn from your notes, with the notes it came from
          beneath it. Press Enter to ask.
        </p>
      ) : (
        <>
          <ol className="ask__thread" aria-live="polite" aria-label="Questions and answers">
            {turns.map((turn) => (
              <li key={turn.key} className="ask__turn" data-status={turn.status}>
                <p className="ask__question">{turn.question}</p>
                <Answer
                  turn={turn}
                  lookup={lookup}
                  onRetry={() => {
                    thread.retry(turn.key);
                  }}
                  onOpen={(noteId) => {
                    void navigate(ROUTES.note(noteId));
                  }}
                />
              </li>
            ))}
          </ol>

          <div className="ask__actions">
            <button
              type="button"
              className="ask__action ask__action--primary"
              disabled={note === null || busy || create.isPending}
              onClick={saveAsNote}
            >
              {create.isPending ? 'Saving…' : 'Save as note'}
            </button>
            <button
              type="button"
              className="ask__action"
              disabled={busy || create.isPending}
              onClick={() => {
                setSaveError(null);
                thread.clear();
              }}
            >
              Clear
            </button>
          </div>
          {saveError && (
            <p className="ask__error" role="alert">
              {saveError}
            </p>
          )}
        </>
      )}
    </section>
  );
}

/** How many source chips an answer shows before "+n more". */
export const SOURCE_CHIPS_SHOWN = 3;

function Answer({
  turn,
  lookup,
  onRetry,
  onOpen,
}: {
  turn: AskTurn;
  /** The device's copy of a note, for the date or words a chip adds when its title is not enough. */
  lookup: (noteId: string) => NoteWire | undefined;
  onRetry: () => void;
  onOpen: (noteId: string) => void;
}) {
  // Every source, once asked for; an answer over eight notes was a wall of
  // chips taller than the answer (round-3 T21). "+1 more" hid one chip
  // behind a chip of the same size, so a fourth is shown outright (QA
  // 2026-09-21, finding 11).
  const [allSources, setAllSources] = useState(false);
  switch (turn.status) {
    case 'asking':
    case 'pending':
      return <p className="ask__waiting">Reading your notes…</p>;
    case 'failed':
    case 'timeout':
      return (
        <div className="ask__failed">
          <p className="ask__error">{turn.status === 'timeout' ? TIMEOUT_MESSAGE : turn.error}</p>
          <button type="button" className="ask__action" onClick={onRetry}>
            Try again
          </button>
        </div>
      );
    default: {
      const labels = sourceLabels(
        turn.sources,
        (noteId) => {
          const note = lookup(noteId);
          return note ? formatRowTime(note.updated_at) || null : null;
        },
        (noteId) => lookup(noteId)?.snippet ?? null,
      );
      const shown =
        allSources || turn.sources.length <= SOURCE_CHIPS_SHOWN + 1
          ? turn.sources
          : turn.sources.slice(0, SOURCE_CHIPS_SHOWN);
      const hidden = turn.sources.length - shown.length;
      return (
        <div className="ask__answer">
          {!turn.grounded && <p className="ask__ungrounded">Not in your notes</p>}
          <div className="ask__body">{renderMarkdown(turn.answer ?? '')}</div>
          {turn.sources.length > 0 && (
            <ul className="ask__sources" aria-label="Sources">
              {shown.map((source, index) => (
                <li key={source.note_id}>
                  <button
                    type="button"
                    className="ask__source"
                    onClick={() => {
                      onOpen(source.note_id);
                    }}
                  >
                    {labels[index] ?? source.title}
                  </button>
                </li>
              ))}
              {hidden > 0 && (
                <li>
                  <button
                    type="button"
                    className="ask__source"
                    onClick={() => {
                      setAllSources(true);
                    }}
                  >
                    {`+${String(hidden)} more`}
                  </button>
                </li>
              )}
            </ul>
          )}
        </div>
      );
    }
  }
}
