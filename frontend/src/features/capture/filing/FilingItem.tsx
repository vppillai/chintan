import { isTerminalStatus, type CaptureWire } from '@/api/schema.ts';
import { Icon } from '@/components/Icon.tsx';
import { describeAgo, formatDurationShort } from '@/features/notes/groups.ts';

import { TargetPrompt } from './TargetPrompt.tsx';
import {
  STAGES,
  describe,
  isStuck,
  retryAccepted,
  stageIndex,
  type ReceiptGroup,
} from './model.ts';

export type FilingItemProps =
  | {
      /** A capture that is moving or stopped short. */
      capture: CaptureWire;
      onRetry: () => void;
      retrying: boolean;
      /** Why the last Retry on this row failed, or `null`. Silent failure is not an option here. */
      retryError: string | null;
      onDismiss: () => void;
    }
  | {
      /** The captures that landed in one note, as one receipt. */
      receipt: ReceiptGroup;
      /** The title of the note, when the device has it. */
      noteTitle?: string | undefined;
      /** The clock the receipt's "just now" is read against; the row re-renders on its owner's minute tick. */
      now: number;
      /** Open the note. */
      onOpen: () => void;
      onDismiss: () => void;
    };

/**
 * One row of the filing section: a capture that is moving or stopped short,
 * or the receipt for everything that landed in one note. The library's
 * filing row lists these; the note screen's `FilingBanner` draws one for the
 * recording on its way into the open note, so a failure is met with the same
 * Retry and Dismiss wherever it is read.
 *
 * The receipt is a branch of this component rather than a component of its
 * own on purpose. React keeps a DOM node across renders only for an element
 * of the same type under the same key: the moving row for capture `x` and
 * the receipt keyed by `x` (the group's newest id) must both be a
 * `<FilingItem>`, or the `<p role="status">` is swapped at the flip and the
 * landing — what a person waiting on Home is listening for — goes unspoken.
 */
export function FilingItem(props: FilingItemProps) {
  if ('receipt' in props) {
    /*
     * A receipt is one control. The row itself opens the note — with a
     * chevron, as every row that leads somewhere has — and the × dismisses
     * it. The title stays where every row keeps it — first in the head,
     * outside the button — so the live region a moving row rendered is the
     * node the receipt updates. The button is the chevron, named by the title
     * and its own hidden words, and its ::after stretches over the row
     * (capture.css). The slot a moving row gives to the recording's length
     * says how long ago the last one landed: the note's Recordings tab has
     * the lengths.
     */
    const { receipt, noteTitle, now, onOpen, onDismiss } = props;
    const count = receipt.captureIds.length;
    const titleId = `filing-title-${receipt.newestId}`;
    return (
      <article className="filing-row filing-row--receipt" data-status="appended">
        <div className="filing-row__head">
          <p id={titleId} className="filing-row__title" role="status" aria-live="polite">
            {count > 1 && <span className="numeric">{count}</span>}
            {count > 1 ? ' filed' : 'Filed'}
            {noteTitle ? ` into “${noteTitle}”` : ''}
          </p>
          <span className="filing-row__duration numeric">{describeAgo(receipt.latestAt, now)}</span>
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

  const { capture, onRetry, retrying, retryError, onDismiss } = props;
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';
  const stuck = isStuck(capture);
  // A stuck capture gets the same way out a failed one does: retrying is safe
  // (the backend resumes from whichever artifact already exists) and dismissing
  // stops the row sitting at the top of the library forever. Retry itself
  // waits until the server will take it — see `retryAccepted`.
  const actionable = failed || stuck;
  const retryable = failed || retryAccepted(capture);
  const needsTarget = capture.status === 'needs_target';
  const stage = STAGES[stageIndex(capture.status)];
  const duration =
    typeof capture.duration_ms === 'number' && capture.duration_ms > 0
      ? formatDurationShort(capture.duration_ms)
      : null;

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
          {describe(capture, stuck)}
          {running && stage && !stuck && (
            <span className="visually-hidden">{` — ${stage.label}`}</span>
          )}
        </p>
        {duration && <span className="filing-row__duration numeric">{duration}</span>}
      </div>

      {running && <FilingStages capture={capture} />}

      {(actionable || capture.status === 'no_content') && (
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
            A stopped capture needs a way off the screen: nothing will ever
            refetch it away on its own, and a row that only says what went
            wrong would otherwise sit at the top of the library for ever.
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
