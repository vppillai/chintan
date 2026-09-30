import { useEffect, useRef, useState } from 'react';

import { isFilingRelevant, useRetryCapture } from '@/api/queries.ts';
import type { CaptureWire, NoteDetailWire } from '@/api/schema.ts';

import { LocalUploadItem, retryMessage } from './FilingRow.tsx';
import { FilingItem } from './filing/FilingItem.tsx';
import { dismissCapture, loadDismissed } from './dismissed.ts';
import type { CaptureModel } from './machine.ts';

/**
 * The recording on its way into the note the person is reading, under the
 * meta line and above the tab strip.
 *
 * Send used to return to the note's Recordings tab so the upload could be
 * watched there; it now returns to whichever tab the person left, and this
 * banner is what says the recording is still coming: the upload's own bar
 * while this device is sending, then the same four stage segments the
 * library's filing row draws, until the capture appends. Then, for a few
 * seconds, "Added at the end · Show": the body has refreshed, but in a long
 * note the paragraph is far from where the person is reading. A capture
 * that stopped short keeps the row's Retry and Dismiss, so the failure is
 * met where the recording was made rather than found later on Home.
 *
 * The Recordings tab lists the same row; the banner is for the other two
 * tabs, which is where someone who has just dictated a paragraph is looking.
 */
export function FilingBanner({
  note,
  localUpload,
  checklist = false,
  onShow,
}: {
  note: NoteDetailWire;
  /** This device's own upload into the note, from `useLocalUpload`. */
  localUpload: CaptureModel | null;
  /** A checklist's recording merges into the list, not onto its end. */
  checklist?: boolean;
  /** Show on the landed line: `before` is the note's body from before the recording. */
  onShow?: (before: string) => void;
}) {
  const retry = useRetryCapture();
  // Per device, like the library's receipts (`dismissed.ts`): a failure put
  // away here must not come back the next time the note is opened.
  const [dismissed, setDismissed] = useState(loadDismissed);
  const capture = bannerCapture(note.captures ?? [], dismissed);
  const [landed, clearLanded] = useLanded(note, capture?.id ?? localUpload?.serverCaptureId ?? null, Boolean(localUpload || capture));

  if (!localUpload && !capture) {
    if (!landed || !onShow) return null;
    /*
     * The body has refreshed, but in a long note the new paragraph is far
     * below where the person is reading and nothing pointed at it (R7-6b).
     * For a few seconds the banner says where it went, and Show takes them
     * there.
     */
    return (
      <section className="filing filing--banner" aria-label="Filing a recording">
        <p className="filing-landed" role="status">
          <span>{checklist ? 'Added to the list' : 'Added at the end'}</span>
          <button
            type="button"
            className="filing-landed__show"
            onClick={() => {
              // Its job is done: the text now points at itself, and focus
              // goes with it (the Text panel moves it there).
              clearLanded();
              onShow(landed.before);
            }}
          >
            Show
          </button>
        </p>
      </section>
    );
  }

  return (
    <section className="filing filing--banner" aria-label="Filing a recording">
      {localUpload ? (
        <LocalUploadItem model={localUpload} />
      ) : (
        capture && (
          <FilingItem
            capture={capture}
            onRetry={() => retry.mutate(capture.id)}
            retrying={retry.isPending && retry.variables === capture.id}
            retryError={
              retry.isError && retry.variables === capture.id ? retryMessage(retry.error) : null
            }
            onDismiss={() => {
              setDismissed(dismissCapture(capture.id, dismissed));
            }}
          />
        )
      )}
    </section>
  );
}

/**
 * The capture the banner is about: the newest of the note's captures that is
 * still moving, or has stopped short and not been put away. Never an
 * appended one — the body is its receipt — and `isFilingRelevant` is the
 * library's own rule for how long a stopped capture is worth a row. Pure,
 * so the rule is pinned by a test.
 */
export function bannerCapture(
  captures: readonly CaptureWire[],
  dismissed: ReadonlySet<string>,
): CaptureWire | null {
  return (
    captures
      .filter(
        (capture) =>
          capture.status !== 'appended' && isFilingRelevant(capture) && !dismissed.has(capture.id),
      )
      .sort((a, b) => b.created_at.localeCompare(a.created_at))[0] ?? null
  );
}

/** How long "Added at the end · Show" stands once the recording lands. */
export const LANDED_MS = 6000;

/**
 * The recording the banner was just showing, once it has appended: its id and
 * the note's body from when the banner first showed it, so Show can tell
 * what it added. Null while it is still moving, after `LANDED_MS`, and for a
 * capture that stopped short or was put away.
 */
function useLanded(
  note: NoteDetailWire,
  watching: string | null,
  busy: boolean,
): [{ id: string; before: string } | null, () => void] {
  const watched = useRef<{ id: string | null; before: string } | null>(null);
  const [landed, setLanded] = useState<{ id: string; before: string } | null>(null);

  useEffect(() => {
    if (busy) {
      // The body is taken once, when the banner first appears: the append
      // lands in a later poll, and an edit meanwhile only moves where Show
      // looks for the addition, not whether it finds the end of the note.
      watched.current = { id: watching ?? watched.current?.id ?? null, before: watched.current?.before ?? note.body };
      return;
    }
    const was = watched.current;
    watched.current = null;
    if (!was?.id) return;
    const done = note.captures?.find((capture) => capture.id === was.id);
    if (done?.status === 'appended') setLanded({ id: was.id, before: was.before });
  }, [busy, watching, note]);

  useEffect(() => {
    if (!landed) return;
    const timer = setTimeout(() => {
      setLanded(null);
    }, LANDED_MS);
    return () => {
      clearTimeout(timer);
    };
  }, [landed]);

  return [
    landed,
    () => {
      setLanded(null);
    },
  ];
}
