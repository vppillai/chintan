import { ApiError } from '@/api/problem.ts';
import { STUCK_AFTER_MS } from '@/api/queries.ts';
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
 * Whether a non-terminal capture has sat past `STUCK_AFTER_MS` (defined with
 * the poll, which backs off to once a minute at the same point) and the row
 * should stop trusting the pipeline and offer a way out.
 */
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

/** The row's title for a capture that is moving or stopped short. An appended one is a receipt (`ReceiptGroup`). */
export function describe(capture: CaptureWire, stuck: boolean): string {
  switch (capture.status) {
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
 * How many receipts are drawn as rows before the rest fold behind a summary.
 *
 * A receipt stays for a day or until the user acts on it (`isFilingRelevant`),
 * and on a device that has dismissed none — a second phone, cleared storage,
 * a ring's day of recordings — that is every capture appended in the last day
 * among the newest twenty. One row per note keeps thirteen ring recordings
 * into one note to one line; three notes is enough to say "your last
 * recordings landed, here they are", and the rest are folded, not gone. Rows
 * that still need something — moving, failed, asking for a target — are never
 * grouped and never folded.
 */
export const FILED_ROWS_MAX = 3;

/** The appended captures that landed in one note, as one receipt. */
export interface ReceiptGroup {
  noteId: string;
  /** Server order, newest first. */
  captureIds: string[];
  /** The first of `captureIds`: the row's React key, so the node of the capture that just landed is kept. */
  newestId: string;
  /** The latest landing in the group: the greatest `appended_at ?? created_at`, which orders the groups. */
  latestAt: string;
}

/**
 * One group per destination note among the appended captures, newest
 * landing first. An appended capture without a note is skipped — the wire
 * always names one, and a receipt with nothing to open is not a receipt.
 * Pure, so the grouping is testable without a poll.
 */
export function groupReceipts(captures: readonly CaptureWire[]): ReceiptGroup[] {
  const byNote = new Map<string, ReceiptGroup>();
  for (const capture of captures) {
    if (capture.status !== 'appended' || !capture.note_id) continue;
    const at = capture.appended_at ?? capture.created_at;
    const group = byNote.get(capture.note_id);
    if (!group) {
      byNote.set(capture.note_id, {
        noteId: capture.note_id,
        captureIds: [capture.id],
        newestId: capture.id,
        latestAt: at,
      });
      continue;
    }
    group.captureIds.push(capture.id);
    if (Date.parse(at) > Date.parse(group.latestAt)) group.latestAt = at;
  }
  return Array.from(byNote.values()).sort(
    (a, b) => Date.parse(b.latestAt) - Date.parse(a.latestAt),
  );
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
