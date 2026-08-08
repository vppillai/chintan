import { useState } from 'react';
import { useNavigate } from 'react-router';

import {
  useNotes,
  useRetryCapture,
  useSetCaptureTarget,
  usePendingCaptures,
} from '@/api/queries.ts';
import { isTerminalStatus, type CaptureStatus, type CaptureWire } from '@/api/schema.ts';
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

function describe(capture: CaptureWire): string {
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
  const [picking, setPicking] = useState(false);
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';
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
    <article className="progress-card" data-status={capture.status}>
      <div className="progress-card__body">
        <p className="progress-card__label" role="status" aria-live="polite">
          {describe(capture)}
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
          The card asked "Which note should this go in?" and rendered no way to
          answer it. `useSetCaptureTarget` wrapped the contract's target
          endpoint and was called from nowhere, so the capture — and the thought
          in it — was stuck permanently.
        */}
        {needsTarget && (
          <button
            type="button"
            className="progress-card__action"
            aria-expanded={picking}
            onClick={() => {
              setPicking((open) => !open);
            }}
          >
            <span>{picking ? 'Cancel' : 'Choose a note'}</span>
          </button>
        )}

        {/*
          A real Retry, wired to POST /v1/captures/{id}/retry. In v1 the client
          method existed and was called from nowhere, so a failed capture was a
          dead end with a toast.
        */}
        {failed && (
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
        {(failed || capture.status === 'no_content') && (
          <button type="button" className="progress-card__action" onClick={onDismiss}>
            <span>Dismiss</span>
          </button>
        )}
      </div>

      {needsTarget && picking && (
        <TargetPicker
          captureId={capture.id}
          onDone={() => {
            setPicking(false);
          }}
        />
      )}
    </article>
  );
}

/**
 * Answers "which note should this go in?".
 *
 * Both spellings the contract accepts are offered — an existing note, or a new
 * one by title — because the router asks this question precisely when it could
 * not tell whether the thought belonged to something the user already has.
 */
function TargetPicker({ captureId, onDone }: { captureId: string; onDone: () => void }) {
  const setTarget = useSetCaptureTarget();
  const { data } = useNotes({ state: 'active' });
  const [title, setTitle] = useState('');

  const notes = data?.pages.flatMap((page) => page.items) ?? [];

  const choose = (target: { note_id: string } | { new_note_title: string }): void => {
    setTarget.mutate({ captureId, target }, { onSuccess: onDone });
  };

  return (
    <div className="target-picker">
      <ul className="target-picker__list" role="list">
        {notes.map((note) => (
          <li key={note.id}>
            <button
              type="button"
              className="target-picker__option"
              disabled={setTarget.isPending}
              onClick={() => {
                choose({ note_id: note.id });
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
          choose({ new_note_title: trimmed });
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
          disabled={setTarget.isPending || title.trim().length === 0}
        >
          Create
        </button>
      </form>

      {setTarget.isError && (
        <p className="target-picker__error" role="alert">
          That did not go through. Try again.
        </p>
      )}
    </div>
  );
}
