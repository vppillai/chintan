import { useState } from 'react';
import { useNavigate } from 'react-router';

import {
  useNotes,
  useRetryCapture,
  useSetCaptureTarget,
  usePendingCaptures,
} from '@/api/queries.ts';
import {
  isTerminalStatus,
  type CaptureStatus,
  type CaptureTargetWire,
  type CaptureWire,
} from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';

/**
 * The capture progress card.
 *
 * Backed by `GET /v1/captures?status=pending` rather than a JavaScript
 * variable, so it survives navigation, reload, and app restart. v1 held the
 * in-flight capture id in a module-level field; a refresh stranded the audio
 * with no UI anywhere able to find it again.
 *
 * The stage labels are honest about where the pipeline actually is. There is
 * no determinate bar, because the client cannot know how long transcription
 * will take and a bar that sits at 100% reads as broken.
 */

const STAGES: { status: CaptureStatus; label: string }[] = [
  { status: 'uploaded', label: 'Queued' },
  { status: 'transcribing', label: 'Transcribing' },
  { status: 'routing', label: 'Filing' },
  { status: 'cleaning', label: 'Cleaning up' },
  { status: 'appending', label: 'Saving' },
];

function stageIndex(status: CaptureStatus): number {
  const index = STAGES.findIndex((stage) => stage.status === status);
  return index === -1 ? STAGES.length : index;
}

/**
 * How long a capture can sit in a non-terminal status before the card stops
 * trusting the pipeline and offers a way out.
 *
 * A capture only reaches this state if the upload event that should have
 * driven the worker never arrived, or the worker died mid-stage without
 * writing a `failed` status — both silent by design elsewhere in the stack
 * (`chintanctl reconcile`'s `stuck_capture` finding exists because of exactly
 * this). Without a client-side timeout the card polls forever showing a
 * stage strip that will never move, with no error and no Retry, because
 * `failed` was the only status that ever unlocked those actions.
 */
const STUCK_AFTER_MS = 10 * 60 * 1000;

function isStuck(capture: CaptureWire): boolean {
  if (isTerminalStatus(capture.status)) return false;
  const createdAt = Date.parse(capture.created_at);
  if (Number.isNaN(createdAt)) return false;
  return Date.now() - createdAt > STUCK_AFTER_MS;
}

function describe(capture: CaptureWire, stuck: boolean): string {
  switch (capture.status) {
    case 'appended':
      return 'Filed';
    case 'needs_target':
      return 'Which note should this go in?';
    case 'no_content':
      return 'Nothing to save from that recording';
    case 'spend_capped':
      return 'Daily spending cap reached';
    case 'failed':
      return capture.error ?? 'That capture did not finish';
    default:
      if (stuck) return 'Still not done — something may have gone wrong';
      return STAGES[stageIndex(capture.status)]?.label ?? 'Working';
  }
}

/**
 * Cards the user has closed.
 *
 * Module-level on purpose: the card is rendered by the shell and remounts every
 * time the sheet locks and unlocks, so component state would resurrect a card
 * the user had just dismissed. There is no server-side "seen" flag to sync
 * with — `no_content` and `failed` stay in the pending list by contract — so
 * this is a per-session client decision and nothing more.
 */
const dismissed = new Set<string>();

export function ProgressCard() {
  const navigate = useNavigate();
  const { data } = usePendingCaptures();
  const retry = useRetryCapture();
  const [, forceRender] = useState(0);

  const captures = (data?.items ?? []).filter((capture) => !dismissed.has(capture.id));
  if (captures.length === 0) return null;

  return (
    <section className="progress-stack" aria-label="Captures in progress">
      {captures.map((capture) => (
        <CaptureCard
          key={capture.id}
          capture={capture}
          onOpen={() => {
            if (capture.note_id) void navigate(ROUTES.note(capture.note_id));
          }}
          onRetry={() => retry.mutate(capture.id)}
          retrying={retry.isPending && retry.variables === capture.id}
          onDismiss={() => {
            dismissed.add(capture.id);
            forceRender((tick) => tick + 1);
          }}
        />
      ))}
    </section>
  );
}

interface CaptureCardProps {
  capture: CaptureWire;
  onOpen: () => void;
  onRetry: () => void;
  retrying: boolean;
  onDismiss: () => void;
}

function CaptureCard({ capture, onOpen, onRetry, retrying, onDismiss }: CaptureCardProps) {
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';
  const stuck = isStuck(capture);
  // A stuck capture gets the same way out a failed one does: retrying is safe
  // (the backend resumes from whichever artifact already exists) and dismissing
  // stops the card floating over the record surface forever.
  const actionable = failed || stuck;
  const done = capture.status === 'appended';
  const needsTarget = capture.status === 'needs_target';
  const current = stageIndex(capture.status);

  /*
   * The stage strip is for a capture that is still moving. It used to render
   * for every status the two explicit branches above did not name, which meant
   * `needs_target` and `no_content` showed all five stages *complete* while the
   * capture had in fact stopped and was waiting for the user.
   */
  const running = !isTerminalStatus(capture.status);

  return (
    <article className="progress-card" data-status={capture.status} data-stuck={stuck || undefined}>
      <div className="progress-card__body">
        <p className="progress-card__label" role="status" aria-live="polite">
          {describe(capture, stuck)}
        </p>

        {running && (
          <ol className="progress-card__stages" aria-label="Pipeline stage">
            {STAGES.map((stage, index) => (
              <li
                key={stage.status}
                className="progress-card__stage"
                data-state={index < current ? 'done' : index === current ? 'active' : 'todo'}
              >
                <span className="visually-hidden">
                  {stage.label}
                  {index < current ? ' complete' : index === current ? ' in progress' : ' pending'}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>

      <div className="progress-card__actions">
        {done && capture.note_id && (
          <button type="button" className="progress-card__action" onClick={onOpen}>
            <span>Open</span>
            <Icon name="back" size={16} className="progress-card__open-icon" />
          </button>
        )}

        {/*
          A real Retry, wired to POST /v1/captures/{id}/retry. In v1 the client
          method existed and was called from nowhere, so a failed capture was a
          dead end with a toast. Also offered once a non-terminal capture has
          sat past STUCK_AFTER_MS with no status change — RetryCapture resumes
          from whichever artifact already exists, so it is safe to call on a
          capture that never actually failed, only stalled.
        */}
        {actionable && (
          <button
            type="button"
            className="progress-card__action"
            onClick={onRetry}
            disabled={retrying}
          >
            <span>{retrying ? 'Retrying…' : 'Retry'}</span>
          </button>
        )}

        {/*
          Terminal and unactionable statuses need a way off the screen. Without
          one the card sat over the record surface for the rest of the session.
        */}
        {(actionable || capture.status === 'no_content') && (
          <button type="button" className="progress-card__action" onClick={onDismiss}>
            <span>Dismiss</span>
          </button>
        )}
      </div>

      {/*
        The card asked "Which note should this go in?" and rendered no way to
        answer it. `useSetCaptureTarget` wrapped the contract's target endpoint
        and was called from nowhere, so the capture — and the thought in it —
        was stuck permanently.

        Mounted only for `needs_target`, which is what keeps the notes list off
        the wire for a capture that is merely still transcribing.
      */}
      {needsTarget && <TargetPrompt capture={capture} />}
    </article>
  );
}

/**
 * Answers "which note should this go in?", leading with the router's answer.
 *
 * The pipeline pays for an LLM call to decide this and stores the result on the
 * capture; `handler/wire.go` used to strip both fields before the response left
 * the API, so the only thing this prompt could offer was an unranked list of
 * every note the user has — with no indication that anything had been computed
 * at all. v1 led with `Add to "<note>"`.
 *
 * Exactly one of the two fields is ever set. `suggested_note_id` names an
 * existing note the router was confident enough to propose but not confident
 * enough to append to unasked; `suggested_title` is what it would call a new
 * note when it found no plausible destination.
 */
function TargetPrompt({ capture }: { capture: CaptureWire }) {
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
      <div className="progress-card__actions">
        <button
          type="button"
          className="progress-card__action progress-card__action--primary"
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
          className="progress-card__action"
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
        every note the user owns is not something to unfold over the record
        surface unprompted.
      */}
      {!browsing && (
        <div className="progress-card__actions">
          <button
            type="button"
            className="progress-card__action"
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
