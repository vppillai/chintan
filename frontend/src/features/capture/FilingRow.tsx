import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { refreshAppendedNote, useRetryCapture, usePendingCaptures } from '@/api/queries.ts';
import { isTerminalStatus } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { formatDurationShort } from '@/features/notes/groups.ts';
import { useOnline } from '@/hooks/useOnline.ts';
import { useCachedNotes } from '@/offline/useNotesCache.ts';

import { UNSENT_CAPTURES_KEY } from './ResumePrompt.tsx';
import { dismissCapture, dismissCaptures, loadDismissed } from './dismissed.ts';
import { FilingItem, NoticeGlyph, receiptTitle } from './filing/FilingItem.tsx';
import {
  groupReceipts,
  isStuck,
  retryMessage,
  type ReceiptGroup,
} from './filing/model.ts';
import { useLocalUpload } from './filing/useLocalUpload.ts';
import { canRetryUpload, type CaptureModel } from './machine.ts';
import { useCaptureStore } from './store.ts';
import { isTargeted, loadTargeted } from './targeted.ts';
import { awaitsConnection } from './useResendOnReconnect.ts';

// The row's parts live under `filing/`; the screens that draw a part of the
// row on their own — a note's Recordings tab, the note screen — keep
// importing them from here.
export { FilingStages } from './filing/FilingItem.tsx';
export { retryMessage } from './filing/model.ts';
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
 * Four tiers, in this order: this device's own upload, rows still moving,
 * rows that need the person (failed, capped, asking for a note, stuck — never
 * grouped, never folded), then one receipt per note the rest landed in,
 * newest landing first. One receipt is a row; two or more fold into one
 * summary row, "3 filed into 2 notes", that expands in place and carries
 * "Clear all" (R7-7a): at 390 px three receipt cards and a fold line left
 * room for about one note below them. A ring's day of thirteen recordings
 * into one note is still one line.
 *
 * What a row says, and the four stage segments, are `filing/model.ts`; one
 * row is `filing/FilingItem.tsx`; this device's own upload is
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
    focusPastDismissed();
    setDismissed(dismissCapture(captureId, dismissed));
  };
  /** Every capture of the group, in one state update, so one render removes the row. */
  const dismissGroup = (group: ReceiptGroup): void => {
    // One of two receipts in the open fold: the fold goes, the survivor is
    // drawn as a lone row in another parent, and the neighbour focus was
    // handed to unmounts with the fold. The effect below puts focus on the
    // survivor's × once it is drawn.
    if (groups.length === 2 && document.activeElement?.closest('.filing-fold__rows')) {
      refocusSurvivor.current = true;
    }
    focusPastDismissed();
    setDismissed(dismissCaptures(group.captureIds, dismissed));
  };
  const openGroup = (group: ReceiptGroup): void => {
    // The row says the note has just been written to, so the copy the app
    // holds is by definition older than what the user is about to read. The
    // poll usually caught the transition already; this is for when it did
    // not (a poll that first saw the capture appended).
    refreshAppendedNote(queryClient, group.noteId);
    // Opening the note is acting on the row: it has been read, and the
    // library the user comes back to should not offer it again. No focus
    // hand-off here: `useRouteFocus` places focus on the note screen.
    setDismissed(dismissCaptures(group.captureIds, dismissed));
    void navigate(ROUTES.note(group.noteId));
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

  // The tiers. A stuck capture is non-terminal but needs the person, so it
  // sits with the failed ones rather than among the rows still moving.
  const moving = captures.filter((capture) => !isTerminalStatus(capture.status) && !isStuck(capture));
  const needsYou = captures.filter(
    (capture) =>
      (isTerminalStatus(capture.status) && capture.status !== 'appended') || isStuck(capture),
  );
  const groups = groupReceipts(captures);
  // One receipt is shown as itself; two or more are the fold's.
  const shown = groups.length === 1 ? groups : [];
  const folded = groups.length > 1 ? groups : [];
  const foldedCaptures = folded.reduce((sum, group) => sum + group.captureIds.length, 0);
  const [expanded, setExpanded] = useState(false);
  const sectionRef = useRef<HTMLElement>(null);
  const refocusSurvivor = useRef(false);
  useEffect(() => {
    if (!refocusSurvivor.current) return;
    refocusSurvivor.current = false;
    sectionRef.current?.querySelector<HTMLElement>('.filing-row--receipt .filing-row__dismiss')?.focus();
  });

  /*
   * What a screen reader hears when a recording lands into the fold. A
   * receipt on its own announces through its row's status (FilingItem), but
   * a landing that makes or grows the fold goes into rows that are not drawn
   * until it is opened, so the section says it here, in a region that is
   * always mounted — a live region announces a change, never the text it
   * mounted with. Only a growing count is a landing: a dismissal, or the
   * receipts already there when Home opens, say nothing.
   */
  const foldSentence = folded.length
    ? `${String(foldedCaptures)} filed into ${String(folded.length)} notes`
    : '';
  const newestFolded = folded[0];
  const landing = newestFolded
    ? `${receiptTitle(newestFolded, titles.get(newestFolded.noteId))}. ${foldSentence}`
    : '';
  const [announcement, setAnnouncement] = useState('');
  const seenFolded = useRef<number | null>(null);
  const answered = data !== undefined;
  useEffect(() => {
    if (!answered) return;
    const previous = seenFolded.current;
    seenFolded.current = foldedCaptures;
    if (previous !== null && foldedCaptures > previous && landing) setAnnouncement(landing);
  }, [answered, foldedCaptures, landing]);

  const clearAll = (): void => {
    focusPastDismissed();
    setDismissed(dismissCaptures(folded.flatMap((group) => group.captureIds), dismissed));
    setExpanded(false);
  };

  // The receipts say how long ago the last recording landed. A minute is the
  // grain `describeAgo` speaks in, and the tick runs only while there is a
  // receipt to read it.
  const [now, setNow] = useState(Date.now);
  const hasGroups = groups.length > 0;
  useEffect(() => {
    if (!hasGroups) return;
    const timer = setInterval(() => setNow(Date.now()), 60_000);
    return () => {
      clearInterval(timer);
    };
  }, [hasGroups]);

  // The tiers decide, not the raw list: an appended capture the server did
  // not name a note for is in no tier, and alone it must not leave an empty
  // labelled section on the page.
  const live = (
    <p className="visually-hidden" aria-live="polite" aria-atomic="true">
      {announcement}
    </p>
  );
  if (moving.length + needsYou.length + groups.length === 0 && !local) return live;

  const receipt = (group: ReceiptGroup, inFold = false) => (
    <FilingItem
      key={group.newestId}
      receipt={group}
      live={!inFold}
      noteTitle={titles.get(group.noteId)}
      now={now}
      onOpen={() => {
        openGroup(group);
      }}
      onDismiss={() => {
        dismissGroup(group);
      }}
    />
  );

  return (
    <>
    {live}
    {/*
      Notices, not notes (F9): one tray pressed into the page under its own
      heading, so it reads as a different thing from the raised cards below,
      and lands in heading navigation beside "Pinned" and "Today".
    */}
    <section ref={sectionRef} className="filing" aria-labelledby="filing-heading">
      <h2 id="filing-heading" className="note-group__label filing__label">
        Filing
      </h2>
      <div className="filing__tray">
        {local && <LocalUploadItem model={local} />}
        {/*
          One array, so a capture keeps its key — and its DOM node, and the live
          region that announces the landing — as it passes from moving to
          receipt: React finds a key only among siblings of the same array.
        */}
        {[...moving, ...needsYou, ...shown].map((row) =>
          'captureIds' in row ? (
            receipt(row)
          ) : (
            <FilingItem
              key={row.id}
              capture={row}
              onRetry={() => retry.mutate(row.id)}
              retrying={retry.isPending && retry.variables === row.id}
              retryError={retry.isError && retry.variables === row.id ? retryMessage(retry.error) : null}
              onDismiss={() => {
                dismiss(row.id);
              }}
            />
          ),
        )}
        {folded.length > 0 && (
          <div className="filing-fold" data-expanded={expanded || undefined}>
            <div className="filing-row filing-row--fold" data-kind="filed">
              <NoticeGlyph kind="filed" />
              {/*
                A button with aria-expanded rather than a <details>, because
                "Clear all" sits on the same line and a control inside a
                <summary> is not one a screen reader can reach on its own.
                A landing into the fold is announced by the section's own
                region above, not by this sentence.
              */}
              <button
                type="button"
                className="filing-fold__toggle"
                aria-expanded={expanded}
                aria-controls={expanded ? 'filing-fold-rows' : undefined}
                onClick={() => {
                  setExpanded((open) => !open);
                }}
              >
                <span className="filing-row__title">
                  <span className="numeric">{foldedCaptures}</span> filed into{' '}
                  <span className="numeric">{folded.length}</span> notes
                </span>
                <Icon name="chevron-right" size={18} className="filing-fold__chevron" />
              </button>
              <button type="button" className="filing-row__action filing-fold__clear" onClick={clearAll}>
                <span>Clear all</span>
              </button>
            </div>
            {expanded && (
              <div id="filing-fold-rows" className="filing-fold__rows">
                {folded.map((group) => receipt(group, true))}
              </div>
            )}
          </div>
        )}
      </div>
    </section>
    </>
  );
}

/**
 * Moves focus off a row about to leave, when its control holds focus: a ×
 * pressed from the keyboard used to take focus with it to `<body>`, and the
 * next Tab started from the top of the page. Focus goes to the neighbouring
 * row's own control — its Retry or ×, or the folded receipts' summary —
 * else, when this was the last row, to the library's heading, which
 * `NotesScreen` makes focusable for the purpose. Read before the state
 * update, while the row is still in the document.
 *
 * When no row holds focus, nothing moves. Safari — macOS and iOS, where the
 * PWA lives — does not focus a tapped or clicked button, so after a tap the
 * active element is `<body>`, and focusing the heading from there scrolled a
 * reader who dismissed a receipt mid-list back to the top of Home.
 */
function focusPastDismissed(): void {
  const active = document.activeElement;
  // The row's unit among its siblings: the swipe wrapper around a row, or
  // the fold as a whole when its own "Clear all" was pressed.
  const row =
    active?.closest<HTMLElement>('.filing-fold__clear') ? active.closest<HTMLElement>('.filing-fold')
    : (active?.closest<HTMLElement>('.filing-swipe') ?? active?.closest<HTMLElement>('.filing-row'));
  if (!row) return;
  const neighbour = row.nextElementSibling ?? row.previousElementSibling;
  const target =
    neighbour?.querySelector<HTMLElement>(
      '.filing-row__dismiss, .filing-row__action, .filing-fold__toggle',
    ) ?? document.querySelector<HTMLElement>('.library-heading');
  target?.focus();
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
      data-kind={failed ? 'failed' : 'moving'}
      data-local="true"
    >
      <NoticeGlyph kind={failed ? 'failed' : 'moving'} />
      <div className="filing-row__body">
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
            {/*
              A word, never an ×: Discard deletes the only copy of the audio,
              and an × on these rows means "put away" (F9).
            */}
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
      </div>
    </article>
  );
}
