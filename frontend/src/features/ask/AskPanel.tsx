import { useCallback, useId, useState } from 'react';
import { useNavigate } from 'react-router';

import { useCreateNote } from '@/api/queries.ts';
import { ROUTES } from '@/app/routes.ts';
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
 * notes, then the notes it drew on as chips that open them — with a
 * follow-up field beneath so "and when was that?" reads in context, and two
 * ways out: Save as note, which turns the thread into a note titled by the
 * first question, and Clear. The whole thread is one polite live region, so
 * an answer landing is read out without anyone having to go and find it.
 *
 * An answer the notes could not give is still an answer: the worker says so
 * plainly with `grounded` false, and the panel labels it "Not in your notes"
 * rather than dressing it up as a finding. A failed one shows the server's
 * fixed sentence and a Try again; so does one the client stopped waiting for.
 */
export function AskPanel({ thread, id }: { thread: AskThread; id?: string }) {
  const headingId = useId();
  const followUpId = useId();
  const navigate = useNavigate();
  const create = useCreateNote();
  const [followUp, setFollowUp] = useState('');
  const [costNoteShown, setCostNoteShown] = useState(() => !costNoteDismissed());
  const [saveError, setSaveError] = useState<string | null>(null);

  const { turns, busy } = thread;
  const note = noteFromThread(turns);

  /*
   * The date beside a source whose title another source shares. The wire
   * carries only id and title, so the date is the library's own copy on the
   * device — the corpus every list the user has seen is written to — which
   * is what the row for the same note shows.
   */
  const cached = useCachedNotes('active');
  const notes = cached.data;
  const when = useCallback(
    (noteId: string): string | null => {
      const found = notes?.find((candidate) => candidate.id === noteId);
      return found ? formatRowTime(found.updated_at) || null : null;
    },
    [notes],
  );

  const submitFollowUp = (): void => {
    if (busy) return;
    const question = followUp.trim();
    if (question.length === 0) return;
    thread.ask(question);
    setFollowUp('');
  };

  const saveAsNote = (): void => {
    if (!note) return;
    setSaveError(null);
    create.mutate(note, {
      onSuccess: (created) => {
        thread.clear();
        void navigate(ROUTES.note(created.id));
      },
      onError: () => {
        setSaveError('The note could not be saved. Try again.');
      },
    });
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
                  when={when}
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

          <form
            className="ask__follow-up"
            onSubmit={(event) => {
              event.preventDefault();
              submitFollowUp();
            }}
          >
            <label className="visually-hidden" htmlFor={followUpId}>
              Ask a follow-up
            </label>
            <input
              id={followUpId}
              className="search-input"
              type="text"
              value={followUp}
              placeholder="Ask a follow-up…"
              autoComplete="off"
              disabled={busy}
              maxLength={1000}
              onChange={(event) => {
                setFollowUp(event.target.value);
              }}
            />
          </form>

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

function Answer({
  turn,
  when,
  onRetry,
  onOpen,
}: {
  turn: AskTurn;
  /** The date a source chip adds when its title is not enough (see `sourceLabels`). */
  when: (noteId: string) => string | null;
  onRetry: () => void;
  onOpen: (noteId: string) => void;
}) {
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
      const labels = sourceLabels(turn.sources, when);
      return (
        <div className="ask__answer">
          {!turn.grounded && <p className="ask__ungrounded">Not in your notes</p>}
          <div className="ask__body">{renderMarkdown(turn.answer ?? '')}</div>
          {turn.sources.length > 0 && (
            <ul className="ask__sources" aria-label="Sources">
              {turn.sources.map((source, index) => (
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
            </ul>
          )}
        </div>
      );
    }
  }
}
