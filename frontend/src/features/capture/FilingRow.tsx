import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import {
  queryKeys,
  refreshAppendedNote,
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
import { formatDurationShort } from '@/features/notes/groups.ts';
import { useOnline } from '@/hooks/useOnline.ts';

import { UNSENT_CAPTURES_KEY } from './ResumePrompt.tsx';
import { dismissCapture, loadDismissed } from './dismissed.ts';
import { canRetryUpload, type CaptureModel } from './machine.ts';
import { useCaptureStore } from './store.ts';
import { awaitsConnection } from './useResendOnReconnect.ts';

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
 * Four segments — uploaded, transcribing, filing, saving — and no percentage,
 * because the client cannot know how long transcription will take and a bar
 * that sits at 100% reads as broken.
 */

interface Stage {
  label: string;
  /** The statuses this segment is lit for. */
  statuses: readonly CaptureStatus[];
}

const STAGES: readonly Stage[] = [
  { label: 'Uploaded', statuses: ['uploaded'] },
  { label: 'Transcribing', statuses: ['transcribing'] },
  // Routing and cleaning are one segment to the user: "working out where this
  // goes and what it says" is one step, however many the pipeline takes.
  { label: 'Filing', statuses: ['routing', 'cleaning'] },
  { label: 'Saving', statuses: ['appending'] },
];

function stageIndex(status: CaptureStatus): number {
  const index = STAGES.findIndex((stage) => stage.statuses.includes(status));
  return index === -1 ? STAGES.length : index;
}

/**
 * How long a capture can sit in a non-terminal status before the row stops
 * trusting the pipeline and offers a way out.
 *
 * A capture only reaches this state if the upload event that should have
 * driven the worker never arrived, or the worker died mid-stage without
 * writing a `failed` status — both silent by design elsewhere in the stack
 * (`chintanctl reconcile`'s `stuck_capture` finding exists because of exactly
 * this). Without a client-side timeout the row polls forever showing a stage
 * strip that will never move, with no error and no Retry.
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
      return 'Filing your recording';
  }
}

/**
 * How long a landed upload's local row waits for the server's row to replace
 * it before giving up its place. The poll is asked for at once, so normally
 * this is a few hundred milliseconds; the bound is for a connection that died
 * between the PUT landing and the poll, where the row would otherwise sit at
 * "Uploaded" for ever.
 */
const HANDOFF_GRACE_MS = 10_000;

/**
 * The upload in progress, read from the capture store rather than the server.
 *
 * Send hands off to the library at once, so for the seconds between the tap
 * and `POST /v1/captures` returning there is no server row to show — and the
 * server never knows about the PUT at all until the object lands. This row
 * covers that gap: "Uploading… 40%" from the store's own progress, then
 * "Uploaded" until the poll returns the real row, which replaces it and
 * releases the machine. A failed upload stays here with Retry and Discard,
 * because the bytes are still on this device and only this device can act.
 */
function useLocalUpload(serverItems: readonly CaptureWire[]): CaptureModel | null {
  const model = useCaptureStore((state) => state.model);
  const reset = useCaptureStore((state) => state.reset);
  const queryClient = useQueryClient();

  const uploading = model.state === 'uploading';
  const landed = model.state === 'uploaded';
  const failed =
    model.state === 'failed' &&
    (model.failure?.kind === 'upload-failed' || model.failure?.kind === 'spend-capped');
  const serverHasIt =
    landed &&
    model.serverCaptureId !== null &&
    serverItems.some((capture) => capture.id === model.serverCaptureId);

  useEffect(() => {
    if (!landed) return;
    // The server has the audio: ask for its row now rather than at the poll's
    // next tick, and the device's list of unsent recordings is one shorter.
    void queryClient.invalidateQueries({ queryKey: queryKeys.pendingCaptures() });
    void queryClient.invalidateQueries({ queryKey: UNSENT_CAPTURES_KEY });
  }, [landed, queryClient]);

  useEffect(() => {
    if (!landed) return;
    if (serverHasIt) {
      // The server's row is on screen; the machine has nothing left to say.
      reset();
      return;
    }
    const timer = setTimeout(reset, HANDOFF_GRACE_MS);
    return () => {
      clearTimeout(timer);
    };
  }, [landed, serverHasIt, reset]);

  if (uploading || failed || (landed && !serverHasIt)) return model;
  return null;
}

/**
 * How many "Filed" receipts are shown at once.
 *
 * A receipt stays until the user acts on it, and on a device that has never
 * dismissed any — a second phone, cleared storage, the QA account after a day
 * of recordings — that is every appended capture among the newest twenty: the
 * QA pass saw nineteen full-height cards above the first note. Three is enough
 * to say "your last recordings landed, here they are"; the rest are counted.
 * Rows that still need something — moving, failed, asking for a target — are
 * never hidden behind the cap.
 */
export const FILED_ROWS_MAX = 3;

/**
 * The rows to draw, and how many receipts were left out. Order is kept — the
 * server's, newest first — so the receipts shown are the most recent and the
 * hidden ones are older. Pure, so the cap is testable without a poll.
 */
export function capFiledRows(
  captures: readonly CaptureWire[],
  max: number = FILED_ROWS_MAX,
): { visible: CaptureWire[]; filedHidden: number } {
  let filed = 0;
  const visible = captures.filter((capture) => {
    if (capture.status !== 'appended') return true;
    filed += 1;
    return filed <= max;
  });
  return { visible, filedHidden: Math.max(0, filed - max) };
}

export function FilingRow() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = usePendingCaptures();
  const local = useLocalUpload(data?.items ?? []);
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

  const captures = (data?.items ?? []).filter((capture) => !dismissed.has(capture.id));
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
function LocalUploadItem({ model }: { model: CaptureModel }) {
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

interface FilingItemProps {
  capture: CaptureWire;
  onOpen: () => void;
  onRetry: () => void;
  retrying: boolean;
  onDismiss: () => void;
}

function FilingItem({ capture, onOpen, onRetry, retrying, onDismiss }: FilingItemProps) {
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';
  const stuck = isStuck(capture);
  // A stuck capture gets the same way out a failed one does: retrying is safe
  // (the backend resumes from whichever artifact already exists) and dismissing
  // stops the row sitting at the top of the library forever.
  const actionable = failed || stuck;
  const done = capture.status === 'appended';
  const needsTarget = capture.status === 'needs_target';
  const current = stageIndex(capture.status);
  const stage = STAGES[current];

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
        {typeof capture.duration_ms === 'number' && capture.duration_ms > 0 && (
          <span className="filing-row__duration numeric">
            {formatDurationShort(capture.duration_ms)}
          </span>
        )}
      </div>

      {running && (
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
      )}

      {running && stage && (
        <p className="filing-row__status" aria-hidden="true">
          {stage.label}
        </p>
      )}

      {(done || actionable || capture.status === 'no_content') && (
        <div className="filing-row__actions">
          {done && capture.note_id && (
            <button type="button" className="filing-row__action" onClick={onOpen}>
              <span>Open the note</span>
              <Icon name="back" size={16} className="filing-row__open-icon" />
            </button>
          )}

          {/*
            A real Retry, wired to POST /v1/captures/{id}/retry, so a failed
            capture is never a dead end with a toast. Also offered once a
            non-terminal capture has sat past STUCK_AFTER_MS with no status
            change — RetryCapture resumes from whichever artifact already
            exists, so it is safe to call on a capture that never actually
            failed, only stalled.
          */}
          {actionable && (
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
