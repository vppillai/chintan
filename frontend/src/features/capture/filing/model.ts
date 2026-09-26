import { ApiError } from '@/api/problem.ts';
import { isTerminalStatus, type CaptureStatus, type CaptureWire } from '@/api/schema.ts';

/**
 * What a filing row says and when, as pure functions over the wire row, so
 * the rules are testable without a poll and shared by every surface that
 * draws one (the library's `FilingRow`, the note's `FilingBanner`, a
 * recording row on the Recordings tab).
 *
 * Four segments — uploaded, transcribing, filing, saving — and no percentage,
 * because the client cannot know how long transcription will take and a bar
 * that sits at 100% reads as broken.
 */

export interface Stage {
  label: string;
  /** The statuses this segment is lit for. */
  statuses: readonly CaptureStatus[];
}

export const STAGES: readonly Stage[] = [
  // "Upload", not "Uploaded": the strip suffixes " in progress" and
  // " complete" for a screen reader, and "Uploaded in progress" was nonsense.
  { label: 'Upload', statuses: ['uploaded'] },
  { label: 'Transcribing', statuses: ['transcribing'] },
  // Routing and cleaning are one segment to the user: "working out where this
  // goes and what it says" is one step, however many the pipeline takes.
  { label: 'Filing', statuses: ['routing', 'cleaning'] },
  { label: 'Saving', statuses: ['appending'] },
];

export function stageIndex(status: CaptureStatus): number {
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
export const STUCK_AFTER_MS = 10 * 60 * 1000;

export function isStuck(capture: CaptureWire): boolean {
  if (isTerminalStatus(capture.status)) return false;
  const createdAt = Date.parse(capture.created_at);
  if (Number.isNaN(createdAt)) return false;
  return Date.now() - createdAt > STUCK_AFTER_MS;
}

/**
 * When the server will accept a Retry of a capture that is still moving.
 *
 * `RetryCapture` refuses an in-flight capture until no worker can still be
 * on it: fifteen minutes since the row was last written (the worker's
 * timeout), or the twenty-minute append lease while `appending`. The row
 * offered Retry at ten minutes, so for five to ten minutes every tap came
 * back "still in flight". The copy keeps `STUCK_AFTER_MS`; the button waits
 * for this.
 *
 * The server measures from `last_progress_at`, which every stage hand-off
 * re-stamps, and the API does not put that field on the wire yet — so until
 * it does, the row measures from `created_at`: exact for a capture that never
 * moved, early by however long it did move for one that reached transcribing
 * before it stalled, and the tap inside that gap is answered by the 409's
 * own sentence. Read when carried, so the gap closes the day it is sent.
 */
export const RETRY_ACCEPTED_AFTER_MS = 15 * 60 * 1000;
export const RETRY_ACCEPTED_APPENDING_MS = 20 * 60 * 1000;

export function retryAccepted(capture: CaptureWire): boolean {
  if (isTerminalStatus(capture.status)) return false;
  const since = Date.parse(capture.last_progress_at ?? capture.created_at);
  if (Number.isNaN(since)) return false;
  const after =
    capture.status === 'appending' ? RETRY_ACCEPTED_APPENDING_MS : RETRY_ACCEPTED_AFTER_MS;
  return Date.now() - since > after;
}

export function describe(capture: CaptureWire, stuck: boolean, noteTitle?: string): string {
  switch (capture.status) {
    case 'appended':
      // Two receipts stacked were indistinguishable: the note is on the
      // capture, and its title is on the device whenever the library has
      // listed it. "Filed" alone only when it is not.
      return noteTitle ? `Filed into “${noteTitle}”` : 'Filed';
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
 * How many "Filed" receipts are shown at once.
 *
 * A receipt stays for a day or until the user acts on it (`isFilingRelevant`),
 * and on a device that has dismissed none — a second phone, cleared storage,
 * the QA account after a day of recordings — that is every capture appended
 * in the last day among the newest twenty: the QA pass saw nineteen
 * full-height cards above the first note. Three is enough to say "your last
 * recordings landed, here they are"; the rest are counted. Rows that still
 * need something — moving, failed, asking for a target — are never hidden
 * behind the cap.
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

/**
 * What a Retry that the server refused says under the row. The problem's
 * `detail` is one of the backend's fixed sentences — "that recording has
 * already been filed", "an identical request is still in flight" — and is
 * written for a person; anything else is the client's own sentence.
 */
export function retryMessage(error: unknown): string {
  return error instanceof ApiError ? error.userMessage : 'The retry did not go through. Try again.';
}
