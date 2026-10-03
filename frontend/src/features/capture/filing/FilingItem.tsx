import { useDeleteStuckCapture, useTranscribeAnyway } from '@/api/queries/captures.ts';
import { isTerminalStatus, type CaptureWire } from '@/api/schema.ts';
import { Icon, type IconName } from '@/components/Icon.tsx';
import { announce } from '@/components/StatusRegion.tsx';
import { SwipeRow } from '@/components/SwipeRow.tsx';
import { formatDurationShort } from '@/features/notes/groups.ts';
import { useMinuteNow } from '@/hooks/useMinuteNow.ts';

import { TargetPrompt } from './TargetPrompt.tsx';
import { useRecordedHere } from './useLocalUpload.ts';
import {
  STAGES,
  describe,
  describeAgoShort,
  isStuck,
  noticeKind,
  retryAccepted,
  retryMessage,
  stageIndex,
  type NoticeKind,
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
      /**
       * False for a receipt inside the fold: the section's own live region
       * speaks for the fold, and a status per folded row would speak each
       * one again whenever the fold opened.
       */
      live?: boolean;
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
  const recordedHere = useRecordedHere('receipt' in props ? null : props.capture.id);
  // A moving row's age ticks; a stopped or filed row's words do not change with time.
  const now = useMinuteNow(!('receipt' in props) && !isTerminalStatus(props.capture.status));
  /*
   * Both branches sit in a SwipeRow, so the root is the same element whether
   * the row is moving or a receipt and the live region below survives the
   * flip. The bodies are called as functions, not rendered as components of
   * their own, for the same reason: two component types at one place would
   * be a remount. Only a receipt offers the tray; a moving or stopped row's
   * controls are on its face.
   */
  if ('receipt' in props) {
    const { receipt, noteTitle, now, onOpen, onDismiss, live = true } = props;
    return (
      <SwipeRow
        className="filing-swipe"
        label="Receipt actions"
        actions={[{ id: 'dismiss', label: 'Dismiss', icon: 'close', onSelect: onDismiss }]}
      >
        {receiptBody({ receipt, noteTitle, now, onOpen, onDismiss, live })}
      </SwipeRow>
    );
  }
  return (
    <SwipeRow className="filing-swipe" label="Filing actions" actions={[]}>
      {captureBody({ ...props, recordedHere, now })}
    </SwipeRow>
  );
}

/** What a receipt says: "Filed into “Roof”", "3 filed into “Roof”", or "Started “Roof”" for a note made for it. */
export function receiptTitle(receipt: ReceiptGroup, noteTitle: string | undefined): string {
  const count = receipt.captureIds.length;
  const into = noteTitle ? ` “${noteTitle}”` : '';
  if (receipt.createdNote) {
    const started = noteTitle ? `Started${into}` : 'Started a note';
    return count > 1 ? `${started} · ${String(count)} recordings` : started;
  }
  const filed = noteTitle ? ` into${into}` : '';
  return count > 1 ? `${String(count)} filed${filed}` : `Filed${filed}`;
}

const GLYPHS: Record<NoticeKind, IconName> = {
  moving: 'bindu',
  needs: 'route',
  failed: 'alert',
  filed: 'check',
  started: 'plus',
};

/**
 * The status cue before a notice's text (F9): what kind of row this is, at a
 * glance, in the tray's first column. Decoration only — the Icon is
 * `aria-hidden` and the title says the same in words.
 */
export function NoticeGlyph({ kind }: { kind: NoticeKind }) {
  return (
    <span className="filing-row__glyph">
      <Icon name={GLYPHS[kind]} size={18} />
    </span>
  );
}

/** The ×. One name everywhere, "Dismiss", and always drawn (F9 supersedes R7-7a's hidden ×). */
/**
 * The override on a "Nothing heard" row. A component of its own so the
 * mutation hook is mounted only for the row that needs it, since the row's
 * body is a plain function shared with the receipt branch.
 */
function TranscribeAnywayButton({ captureId }: { captureId: string }) {
  const transcribeAnyway = useTranscribeAnyway();
  return (
    <div className="filing-row__actions">
      <button
        type="button"
        className="filing-row__action"
        onClick={() => transcribeAnyway.mutate(captureId)}
        disabled={transcribeAnyway.isPending}
      >
        <span>{transcribeAnyway.isPending ? 'Sending…' : 'Transcribe anyway'}</span>
      </button>
    </div>
  );
}

function DismissButton({ onDismiss }: { onDismiss: () => void }) {
  return (
    <button type="button" className="filing-row__dismiss" aria-label="Dismiss" onClick={onDismiss}>
      <Icon name="close" size={18} />
    </button>
  );
}

/**
 * The × on a stuck row. Once the server will let the capture go
 * (`retryAccepted`: the same bound as Retry, `service.CaptureStuck`) it is
 * deleted — dismissing only hid it on this device while the server kept it,
 * every other device kept drawing it and the poll kept asking after it once
 * a minute for ever. A refusal (an upload whose object may yet land) hides
 * the row here, as before, and says the server's sentence through the live
 * region, since the row it would sit under is going. Before the bound the ×
 * hides the row as it always did. `onDismiss` runs on success as well: it is
 * the parent's focus hand-off, and the id it remembers never comes back.
 */
function StuckDismissButton({
  capture,
  onDismiss,
}: {
  capture: CaptureWire;
  onDismiss: () => void;
}) {
  const remove = useDeleteStuckCapture();
  return (
    <DismissButton
      onDismiss={() => {
        if (!retryAccepted(capture)) {
          onDismiss();
          return;
        }
        remove.mutate(capture, {
          onSuccess: onDismiss,
          onError: (error) => {
            announce(retryMessage(error, 'Could not delete the recording. Try again.'));
            onDismiss();
          },
        });
      }}
    />
  );
}

/*
 * A receipt is one line (R7-7a): the title, cut with an ellipsis, then
 * "· 2 min", and the × at the end. The row itself opens the note: the
 * zero-size "Open the note" button's ::after stretches over it (capture.css),
 * and the title stays first in the head, outside the button, so the live
 * region a moving row rendered is the node the receipt updates. There is no
 * chevron (F9): three trailing controls were what squeezed the title.
 */
function receiptBody({
  receipt,
  noteTitle,
  now,
  onOpen,
  onDismiss,
  live,
}: {
  receipt: ReceiptGroup;
  noteTitle: string | undefined;
  now: number;
  onOpen: () => void;
  onDismiss: () => void;
  live: boolean;
}) {
  const titleId = `filing-title-${receipt.newestId}`;
  const ago = describeAgoShort(receipt.latestAt, now);
  const kind = noticeKind(receipt);
  return (
    <article className="filing-row filing-row--receipt" data-status="appended" data-kind={kind}>
      <NoticeGlyph kind={kind} />
      <div className="filing-row__body">
        <div className="filing-row__head">
          <p
            id={titleId}
            className="filing-row__title"
            role={live ? 'status' : undefined}
            aria-live={live ? 'polite' : undefined}
          >
            {receiptTitle(receipt, noteTitle)}
          </p>
          {ago && (
            <span className="filing-row__duration numeric">
              <span aria-hidden="true">· </span>
              {ago}
              {ago !== 'now' && <span className="visually-hidden"> ago</span>}
            </span>
          )}
        </div>
        {receipt.excerpt && <p className="filing-row__excerpt">{receipt.excerpt}</p>}
        <button
          type="button"
          className="filing-row__receipt"
          aria-labelledby={`${titleId} ${titleId}-open`}
          onClick={onOpen}
        >
          <span id={`${titleId}-open`} className="visually-hidden">
            Open the note
          </span>
        </button>
      </div>
      <DismissButton onDismiss={onDismiss} />
    </article>
  );
}

function captureBody({
  capture,
  onRetry,
  retrying,
  retryError,
  onDismiss,
  recordedHere,
  now,
}: Extract<FilingItemProps, { capture: CaptureWire }> & { recordedHere: boolean; now: number }) {
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';
  const stuck = isStuck(capture, now);
  // A stuck capture gets the same way out a failed one does: retrying is safe
  // (the backend resumes from whichever artifact already exists) and dismissing
  // stops the row sitting at the top of the library forever. Retry itself
  // waits until the server will take it — see `retryAccepted`.
  const actionable = failed || stuck;
  const retryable = failed || retryAccepted(capture, now);
  // "Nothing heard" is the recorder's or the provider's judgement; the
  // person's outranks it. The same /retranscribe as the Recordings tab's
  // Transcribe again, with the server lifting its gates for the run.
  const unheard = capture.status === 'no_content' && Boolean(capture.gate);
  const needsTarget = capture.status === 'needs_target';
  // Another device's upload that has not landed: its title says so, and the
  // strip's "Upload" is not appended to it.
  const waiting = capture.status === 'uploaded' && !recordedHere;
  const stage = STAGES[stageIndex(capture.status)];
  const duration =
    typeof capture.duration_ms === 'number' && capture.duration_ms > 0
      ? formatDurationShort(capture.duration_ms)
      : null;

  /*
   * The stage strip is for a capture that is still moving. It used to render
   * for every status the explicit branches did not name, which meant
   * `needs_target` and `no_content` showed every stage *complete* while the
   * capture had in fact stopped and was waiting for the user — and for a
   * stuck one, which drew a segment "in progress" under "still not done".
   */
  const running = !isTerminalStatus(capture.status) && !stuck;
  // What was said, on the rows that need the person: "which note?" and a
  // failure are answered from memory of the recording, and its length alone
  // did not bring back a ring capture from hours ago (R7-7b).
  const excerpt = (needsTarget || actionable) && capture.excerpt ? capture.excerpt : null;
  const { sentence, age } = describe(capture, recordedHere, now);

  return (
    <article className="filing-row" data-status={capture.status} data-kind={noticeKind(capture)}>
      <NoticeGlyph kind={noticeKind(capture)} />
      <div className="filing-row__body">
        <div className="filing-row__head">
          <p className="filing-row__title" role="status" aria-live="polite">
            {sentence}
            {running && stage && !waiting && (
              <span className="visually-hidden">{` — ${stage.label}`}</span>
            )}
          </p>
          {/*
            The age sits beside the live sentence, not in it, like a receipt's
            "· 2 min": it changes every minute, and inside the region a stuck
            row would be read out again each minute for ever. Hidden from the
            reader, who has the sentence; a sighted glance has the number.
          */}
          {age && (
            <span className="filing-row__duration numeric" aria-hidden="true">
              · {age}
            </span>
          )}
          {duration && <span className="filing-row__duration numeric">{duration}</span>}
        </div>

        {excerpt && <p className="filing-row__excerpt">{excerpt}</p>}

        {running && <FilingStages capture={capture} />}

        {retryable && (
          <div className="filing-row__actions">
            {/*
              A real Retry, wired to POST /v1/captures/{id}/retry, so a failed
              capture is never a dead end with a toast. Also offered once a
              non-terminal capture has sat long enough that the server will
              start a fresh run — RetryCapture resumes from whichever artifact
              already exists, so it is safe to call on a capture that never
              actually failed, only stalled.
            */}
            <button
              type="button"
              className="filing-row__action"
              onClick={onRetry}
              disabled={retrying}
            >
              <span>{retrying ? 'Retrying…' : 'Retry'}</span>
            </button>
          </div>
        )}

        {unheard && <TranscribeAnywayButton captureId={capture.id} />}

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
      </div>

      {/*
        A stopped capture needs a way off the screen: nothing will ever
        refetch it away on its own, and a row that only says what went wrong
        would otherwise sit at the top of the library for ever. Not on a row
        asking which note: putting that away would hide a recording that is
        in no note yet, with nowhere else to find it — answering is its way
        off the screen.
      */}
      {stuck ? (
        <StuckDismissButton capture={capture} onDismiss={onDismiss} />
      ) : (
        (actionable || capture.status === 'no_content') && <DismissButton onDismiss={onDismiss} />
      )}
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
