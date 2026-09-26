import { useState } from 'react';

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
 * library's filing row draws, until the capture appends — at which point the
 * body has already refreshed and there is nothing left to say. A capture
 * that stopped short keeps the row's Retry and Dismiss, so the failure is
 * met where the recording was made rather than found later on Home.
 *
 * The Recordings tab lists the same row; the banner is for the other two
 * tabs, which is where someone who has just dictated a paragraph is looking.
 */
export function FilingBanner({
  note,
  localUpload,
}: {
  note: NoteDetailWire;
  /** This device's own upload into the note, from `useLocalUpload`. */
  localUpload: CaptureModel | null;
}) {
  const retry = useRetryCapture();
  // Per device, like the library's receipts (`dismissed.ts`): a failure put
  // away here must not come back the next time the note is opened.
  const [dismissed, setDismissed] = useState(loadDismissed);
  const capture = bannerCapture(note.captures ?? [], dismissed);

  if (!localUpload && !capture) return null;

  return (
    <section className="filing filing--banner" aria-label="Filing a recording">
      {localUpload ? (
        <LocalUploadItem model={localUpload} />
      ) : (
        capture && (
          <FilingItem
            capture={capture}
            // Never taken: an appended capture is not a candidate, and the
            // receipt that opens the note is the appended row's alone.
            onOpen={() => {}}
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
