import { isTerminalStatus, type CaptureWire } from '@/api/schema.ts';
import { Icon } from '@/components/Icon.tsx';
import { formatDurationShort } from '@/features/notes/groups.ts';

import { TargetPrompt } from './TargetPrompt.tsx';
import { STAGES, describe, isStuck, retryAccepted, stageIndex } from './model.ts';

export interface FilingItemProps {
  capture: CaptureWire;
  /** The title of `capture.note_id`, when the device has it. */
  noteTitle?: string | undefined;
  onOpen: () => void;
  onRetry: () => void;
  retrying: boolean;
  /** Why the last Retry on this row failed, or `null`. Silent failure is not an option here. */
  retryError: string | null;
  onDismiss: () => void;
}

/**
 * One capture's row. The library's filing row lists these; the note screen's
 * `FilingBanner` draws one for the recording on its way into the open note,
 * so a failure is met with the same Retry and Dismiss wherever it is read.
 */
export function FilingItem({
  capture,
  noteTitle,
  onOpen,
  onRetry,
  retrying,
  retryError,
  onDismiss,
}: FilingItemProps) {
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';
  const stuck = isStuck(capture);
  // A stuck capture gets the same way out a failed one does: retrying is safe
  // (the backend resumes from whichever artifact already exists) and dismissing
  // stops the row sitting at the top of the library forever. Retry itself
  // waits until the server will take it — see `retryAccepted`.
  const actionable = failed || stuck;
  const retryable = failed || retryAccepted(capture);
  const done = capture.status === 'appended';
  const needsTarget = capture.status === 'needs_target';
  const stage = STAGES[stageIndex(capture.status)];
  const duration =
    typeof capture.duration_ms === 'number' && capture.duration_ms > 0
      ? formatDurationShort(capture.duration_ms)
      : null;

  if (done && capture.note_id) {
    /*
     * A receipt is one control. The row itself opens the note — with a
     * chevron, as every row that leads somewhere has — and the × dismisses
     * it; the "Open the note" pill under a bare "Filed" was a second thing to
     * find on a row whose whole point is the note.
     *
     * The title stays where every row keeps it — first in the head, outside
     * the button — so the `<p role="status">` a moving row rendered is the
     * same node once the poll flips it to appended. A live region announces a
     * change to its text, not the text it mounted with: when the receipt was
     * its own subtree React swapped the node and the landing went unspoken.
     * The button is the chevron, named by the title and its own hidden words,
     * and its ::after stretches over the row (capture.css).
     */
    const titleId = `filing-title-${capture.id}`;
    return (
      <article className="filing-row filing-row--receipt" data-status={capture.status}>
        <div className="filing-row__head">
          <p id={titleId} className="filing-row__title" role="status" aria-live="polite">
            {describe(capture, stuck, noteTitle)}
          </p>
          {duration && <span className="filing-row__duration numeric">{duration}</span>}
        </div>
        <button
          type="button"
          className="filing-row__receipt"
          aria-labelledby={`${titleId} ${titleId}-open`}
          onClick={onOpen}
        >
          <Icon name="chevron-right" size={18} />
          <span id={`${titleId}-open`} className="visually-hidden">
            Open the note
          </span>
        </button>
        <button
          type="button"
          className="filing-row__dismiss"
          aria-label="Dismiss"
          onClick={onDismiss}
        >
          <Icon name="close" size={18} />
        </button>
      </article>
    );
  }

  /*
   * The stage strip is for a capture that is still moving. It used to render
   * for every status the explicit branches did not name, which meant
   * `needs_target` and `no_content` showed every stage *complete* while the
   * capture had in fact stopped and was waiting for the user.
   */
  const running = !isTerminalStatus(capture.status);

  return (
    <article className="filing-row" data-status={capture.status} data-stuck={stuck || undefined}>
      <div className="filing-row__head">
        <p className="filing-row__title" role="status" aria-live="polite">
          {describe(capture, stuck, noteTitle)}
          {running && stage && !stuck && (
            <span className="visually-hidden">{` — ${stage.label}`}</span>
          )}
        </p>
        {duration && <span className="filing-row__duration numeric">{duration}</span>}
      </div>

      {running && <FilingStages capture={capture} />}

      {(done || actionable || capture.status === 'no_content') && (
        <div className="filing-row__actions">
          {/*
            A real Retry, wired to POST /v1/captures/{id}/retry, so a failed
            capture is never a dead end with a toast. Also offered once a
            non-terminal capture has sat long enough that the server will
            start a fresh run — RetryCapture resumes from whichever artifact
            already exists, so it is safe to call on a capture that never
            actually failed, only stalled.
          */}
          {retryable && (
            <button
              type="button"
              className="filing-row__action"
              onClick={onRetry}
              disabled={retrying}
            >
              <span>{retrying ? 'Retrying…' : 'Retry'}</span>
            </button>
          )}

          {/*
            Terminal statuses need a way off the screen. `done` is included
            too: a "Filed" row stays until the user acts on it, and polling
            stops the moment nothing left is non-terminal — so once the last
            capture appends, nothing else will ever refetch this away. Dismiss
            or Open (which also dismisses) is how it leaves.
          */}
          <button type="button" className="filing-row__action" onClick={onDismiss}>
            <span>Dismiss</span>
          </button>
        </div>
      )}

      {retryError && (
        <p className="filing-row__error" role="alert">
          {retryError}
        </p>
      )}

      {/*
        The row asks "Which note should this go in?" and must render a way to
        answer it. `useSetCaptureTarget` wrapped the contract's target endpoint
        and was once called from nowhere, so the capture — and the thought in
        it — was stuck permanently.

        Mounted only for `needs_target`, which is what keeps the notes list off
        the wire for a capture that is merely still transcribing.
      */}
      {needsTarget && <TargetPrompt capture={capture} />}
    </article>
  );
}

/**
 * The four stage segments and the name of the current one, for a capture that
 * is still moving. The library's filing row draws it under the title; a note's
 * recording row draws the same strip while its recording is being filed, so
 * a recording made into a note shows the same progress wherever it is read
 * from and turns into an ordinary row when it lands.
 */
export function FilingStages({ capture }: { capture: CaptureWire }) {
  const current = stageIndex(capture.status);
  const stage = STAGES[current];
  return (
    <>
      <ol className="filing-row__stages" aria-label="Filing progress">
        {STAGES.map((step, index) => (
          <li
            key={step.label}
            className="filing-row__stage"
            data-state={index < current ? 'done' : index === current ? 'active' : 'todo'}
          >
            <span className="visually-hidden">
              {step.label}
              {index < current ? ' complete' : index === current ? ' in progress' : ' pending'}
            </span>
          </li>
        ))}
      </ol>
      {stage && (
        <p className="filing-row__status" aria-hidden="true">
          {stage.label}
        </p>
      )}
    </>
  );
}
