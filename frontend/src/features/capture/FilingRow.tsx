import { useQueryClient } from '@tanstack/react-query';
import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { refreshAppendedNote, useRetryCapture, usePendingCaptures } from '@/api/queries.ts';
import { ROUTES } from '@/app/routes.ts';
import { formatDurationShort } from '@/features/notes/groups.ts';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNotes } from '@/offline/useNotesCache.ts';

import { UNSENT_CAPTURES_KEY } from './ResumePrompt.tsx';
import { dismissCapture, loadDismissed } from './dismissed.ts';
import { FilingItem } from './filing/FilingItem.tsx';
import { capFiledRows, retryMessage } from './filing/model.ts';
import { useLocalUpload } from './filing/useLocalUpload.ts';
import { canRetryUpload, type CaptureModel } from './machine.ts';
import { useCaptureStore } from './store.ts';
import { isTargeted, loadTargeted } from './targeted.ts';
import { awaitsConnection } from './useResendOnReconnect.ts';

// The row's parts live under `filing/`; the screens that draw a part of the
// row on their own — a note's Recordings tab, `/talk`, the note screen — keep
// importing them from here.
export { FilingStages } from './filing/FilingItem.tsx';
export { FILED_ROWS_MAX, retryMessage } from './filing/model.ts';
export { TargetPrompt } from './filing/TargetPrompt.tsx';
export { useLocalUpload } from './filing/useLocalUpload.ts';

/**
 * A recording being filed, as a row at the top of the library.
 *
 * Backed by `GET /v1/captures` rather than a JavaScript variable, so it
 * survives navigation, reload, and app restart. An in-flight capture id held
 * in a module-level field is lost on refresh, stranding the audio with no UI
 * anywhere able to find it again. It is a list row rather than a card floating
 * in the shell over every screen, because a recording on its way into the
 * library belongs at the top of the library.
 *
 * What a row says, and the four stage segments, are `filing/model.ts`; one
 * capture's row is `filing/FilingItem.tsx`; this device's own upload is
 * `filing/useLocalUpload.ts` and `LocalUploadItem` below.
 */
export function FilingRow() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = usePendingCaptures();
  const local = useLocalUpload(data?.items ?? [], undefined, { homeOnly: true });
  const retry = useRetryCapture();
  /*
   * Rows the user has closed, read from the device on mount. The library
   * remounts every time a note is opened and closed, so component state alone
   * would resurrect a row the user had just dismissed; a module-level set did
   * that job for one session and forgot it on reload. See `dismissed.ts`.
   */
  const [dismissed, setDismissed] = useState(loadDismissed);

  /*
   * The device is written here, in the handler, and the state follows. This
   * used to be one call — `setDismissed((current) => dismissCapture(id,
   * current))` — with the `localStorage` write inside the updater. React runs
   * an updater when it renders, not when it is queued: "Open the note" queues
   * it and navigates in the same tick, and a fiber that unmounts before its
   * next render never runs its updaters, so the dismissal could be lost
   * exactly when the user acted on the row. (Strict mode also runs updaters
   * twice.) A side effect belongs in the handler.
   */
  const dismiss = (captureId: string): void => {
    setDismissed(dismissCapture(captureId, dismissed));
  };

  /*
   * Captures a person aimed at a note are that note's to show (contract §3):
   * they are on its Recordings tab from the first "Uploading" onward, and a
   * receipt for each one here too made Home a wall of them after a day of
   * recording into one note. The server's flag decides; the device's own
   * memory of what it sent with a note stands in for backends that do not
   * send it yet. Re-read whenever the poll answers, since the uploader may
   * have written to it while this row was mounted.
   */
  const untargeted = useMemo(() => {
    const remembered = loadTargeted();
    return (data?.items ?? []).filter((capture) => !isTargeted(capture, remembered));
  }, [data]);
  const captures = untargeted.filter((capture) => !dismissed.has(capture.id));

  // The receipts name their note by title. The device's copy of the library
  // is the source — every list page the person has seen is in it — rather
  // than a request of its own for a row that is a receipt.
  const cached = useCachedNotes('active');
  const titles = useMemo(
    () => new Map((cached.data ?? []).map((note) => [note.id, note.title])),
    [cached.data],
  );

  if (captures.length === 0 && !local) return null;

  // Everything that still needs something is shown; the receipts are capped.
  const { visible, filedHidden } = capFiledRows(captures);

  return (
    <section className="filing" aria-label="Recordings being filed">
      {local && <LocalUploadItem model={local} />}
      {visible.map((capture) => (
        <FilingItem
          key={capture.id}
          capture={capture}
          noteTitle={capture.note_id ? titles.get(capture.note_id) : undefined}
          onOpen={() => {
            if (!capture.note_id) return;
            // The row says the note has just been written to, so the copy the
            // app holds is by definition older than what the user is about to
            // read. The poll usually caught the transition already; this is
            // for when it did not (a poll that first saw the capture appended).
            refreshAppendedNote(queryClient, capture.note_id);
            // Opening the note is acting on the row: it has been read, and the
            // library the user comes back to should not offer it again.
            dismiss(capture.id);
            void navigate(ROUTES.note(capture.note_id));
          }}
          onRetry={() => retry.mutate(capture.id)}
          retrying={retry.isPending && retry.variables === capture.id}
          retryError={
            retry.isError && retry.variables === capture.id ? retryMessage(retry.error) : null
          }
          onDismiss={() => {
            dismiss(capture.id);
          }}
        />
      ))}
      {filedHidden > 0 && (
        <p className="filing__more" role="status">
          <span className="numeric">{filedHidden}</span> more filed
        </p>
      )}
    </section>
  );
}

/** The row for an upload this device is still making. See `useLocalUpload`. */
export function LocalUploadItem({ model }: { model: CaptureModel }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const send = useCaptureStore((state) => state.send);
  const discard = useCaptureStore((state) => state.discard);
  const online = useOnline();

  const failed = model.state === 'failed';
  const landed = model.state === 'uploaded';
  const percent = Math.round((landed ? 1 : model.uploadProgress) * 100);
  // The device is offline and this is the recording that will go out on its
  // own when it is not — say so, rather than leaving "did not finish" as the
  // last word beside a banner that already says why.
  const willResend = failed && !online && awaitsConnection(model);

  return (
    <article
      className="filing-row"
      data-status={failed ? 'upload-failed' : model.state}
      data-local="true"
    >
      <div className="filing-row__head">
        <p className="filing-row__title" role="status" aria-live="polite">
          {failed ? (
            model.failure?.message
          ) : landed ? (
            'Uploaded'
          ) : (
            <>
              Uploading… <span className="numeric">{percent}</span>%
            </>
          )}
        </p>
        {model.elapsedMs > 0 && (
          <span className="filing-row__duration numeric">
            {formatDurationShort(model.elapsedMs)}
          </span>
        )}
      </div>

      {/*
        A determinate bar, because for once the client does know the shape of
        the work: the uploader's coarse steps. It hands over to the stage strip
        of the server row as soon as that exists.
      */}
      {!failed && (
        <div className="filing-row__upload" aria-hidden="true">
          <span className="filing-row__upload-fill" style={{ inlineSize: `${percent}%` }} />
        </div>
      )}

      {willResend && (
        <p className="filing-row__status">It will be sent when you&rsquo;re back online.</p>
      )}

      {failed && (
        <div className="filing-row__actions">
          {canRetryUpload(model) && (
            <button
              type="button"
              className="filing-row__action filing-row__action--primary"
              onClick={() => void send(api)}
            >
              <span>Retry</span>
            </button>
          )}
          <button
            type="button"
            className="filing-row__action"
            onClick={() => {
              void discard().then(() => {
                void queryClient.invalidateQueries({ queryKey: UNSENT_CAPTURES_KEY });
              });
            }}
          >
            <span>Discard</span>
          </button>
        </div>
      )}
    </article>
  );
}
