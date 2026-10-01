import { ApiError } from '@/api/problem.ts';
import { STUCK_AFTER_MS, isTerminalStatus, type CaptureStatus, type CaptureWire } from '@/api/schema.ts';

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
  // `transcribed` is in it too: the transcript is in and routing is next, and
  // it is where an inbox text capture starts (nothing to upload or
  // transcribe), so its strip picks up here rather than lighting every
  // segment done with no label, which is what an unlisted status draws.
  { label: 'Filing', statuses: ['transcribed', 'routing', 'cleaning'] },
  // `cleaned` is the hand-off into the append, and where a retry resumes when
  // the clean artifact already exists.
  { label: 'Saving', statuses: ['cleaned', 'appending'] },
];

export function stageIndex(status: CaptureStatus): number {
  const index = STAGES.findIndex((stage) => stage.statuses.includes(status));
  return index === -1 ? STAGES.length : index;
}

/**
 * Whether a non-terminal capture has sat past `STUCK_AFTER_MS` (`schema.ts`;
 * the poll backs off to once a minute at the same threshold) and the row
 * should stop trusting the pipeline and offer a way out. Measured from
 * `last_progress_at` — every stage hand-off re-stamps it, so a capture that
 * moved into transcribing nine minutes in is not called stuck a minute
 * later — else from `created_at`, on rows from before it was recorded. The
 * poll reads the same clock (`capturePollInterval`), so the two agree.
 */
export function isStuck(capture: CaptureWire): boolean {
  if (isTerminalStatus(capture.status)) return false;
  const since = Date.parse(capture.last_progress_at ?? capture.created_at);
  if (Number.isNaN(since)) return false;
  return Date.now() - since > STUCK_AFTER_MS;
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
const RETRY_ACCEPTED_AFTER_MS = 15 * 60 * 1000;
const RETRY_ACCEPTED_APPENDING_MS = 20 * 60 * 1000;

export function retryAccepted(capture: CaptureWire): boolean {
  if (isTerminalStatus(capture.status)) return false;
  const since = Date.parse(capture.last_progress_at ?? capture.created_at);
  if (Number.isNaN(since)) return false;
  const after =
    capture.status === 'appending' ? RETRY_ACCEPTED_APPENDING_MS : RETRY_ACCEPTED_AFTER_MS;
  return Date.now() - since > after;
}

/**
 * The row's title for a capture that is moving or stopped short. An appended
 * one is a receipt (`ReceiptGroup`). `recordedHere` is false on a device that
 * did not make the recording (`useRecordedHere`): there, a capture still at
 * `uploaded` is waiting on the device that holds the bytes, stuck or not, and
 * nothing this one can do will move it.
 */
export function describe(capture: CaptureWire, stuck: boolean, recordedHere = true): string {
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
      if (capture.status === 'uploaded' && !recordedHere) {
        return 'Waiting for the device that recorded it';
      }
      if (stuck) return 'Still not done — something may have gone wrong';
      return 'Filing your recording';
  }
}

/** The appended captures that landed in one note, as one receipt. */
export interface ReceiptGroup {
  noteId: string;
  /** Server order, newest first. */
  captureIds: string[];
  /** The first of `captureIds`: the row's React key, so the node of the capture that just landed is kept. */
  newestId: string;
  /** The latest landing in the group: the greatest `appended_at ?? created_at`, which orders the groups. */
  latestAt: string;
  /**
   * One of the captures made the note (`created_note`), so the receipt says
   * "Started" rather than "Filed into" and a misroute into a fresh note is
   * visible (R7-7c).
   */
  createdNote: boolean;
  /** The newest capture's `excerpt`, the receipt's muted second line (R7-7b). */
  excerpt: string | null;
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
        createdNote: capture.created_note === true,
        excerpt: capture.excerpt ?? null,
      });
      continue;
    }
    group.captureIds.push(capture.id);
    if (capture.created_note) group.createdNote = true;
    if (Date.parse(at) > Date.parse(group.latestAt)) group.latestAt = at;
  }
  return Array.from(byNote.values()).sort(
    (a, b) => Date.parse(b.latestAt) - Date.parse(a.latestAt),
  );
}

/**
 * "now", "2 min", "3 h", "4 d": how long ago a receipt's last recording
 * landed, in the few characters a one-line receipt on a 390 px phone has
 * beside its title (R7-7a). `describeAgo`'s "12 minutes ago" took a third of
 * the row and wrapped the title to three lines. The row adds " ago" for a
 * screen reader.
 */
export function describeAgoShort(iso: string, now: number): string {
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return '';
  const minutes = Math.round(Math.max(0, now - at) / 60_000);
  if (minutes < 1) return 'now';
  if (minutes < 60) return `${String(minutes)} min`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${String(hours)} h`;
  return `${String(Math.round(hours / 24))} d`;
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

/** Which glyph a filing notice wears, and the `data-kind` its row carries (F9). */
export type NoticeKind = 'moving' | 'needs' | 'failed' | 'filed' | 'started';

/**
 * The kind of a filing row: a receipt is filed or started; a capture is
 * moving, asking which note, or stopped short. A stuck capture is non-terminal
 * but needs the person, so it is `failed` like the rows it sits with.
 * `no_content` is `failed` too — it has stopped and only Dismiss is left — and
 * the stylesheet draws its glyph muted rather than in the alert colour, since
 * nothing went wrong. The title says which it is either way, so the glyph is
 * never the only cue.
 */
export function noticeKind(row: CaptureWire | ReceiptGroup): NoticeKind {
  if ('captureIds' in row) return row.createdNote ? 'started' : 'filed';
  switch (row.status) {
    case 'needs_target':
      return 'needs';
    case 'failed':
    case 'spend_capped':
    case 'no_content':
      return 'failed';
    case 'appended':
      return row.created_note ? 'started' : 'filed';
    default:
      return isStuck(row) ? 'failed' : 'moving';
  }
}

/** The library's filing tiers, in the order the tray draws them (`FilingRow`). */
export interface FilingTiers {
  /** Rows still moving through the pipeline. */
  moving: CaptureWire[];
  /** Rows that need the person: failed, capped, asking for a note, stuck. Never grouped, never folded. */
  needsYou: CaptureWire[];
  /** One receipt per note the rest landed in, newest landing first. */
  groups: ReceiptGroup[];
  /** One receipt is shown as itself. */
  shown: ReceiptGroup[];
  /** Two or more fold into one summary row. */
  folded: ReceiptGroup[];
}

/**
 * Sorts the captures the tray shows into its tiers. A stuck capture is
 * non-terminal but needs the person, so it sits with the failed ones rather
 * than among the rows still moving. Pure, beside `groupReceipts`, so the
 * tiering is testable without a poll.
 */
export function tierCaptures(captures: readonly CaptureWire[]): FilingTiers {
  const moving = captures.filter((capture) => !isTerminalStatus(capture.status) && !isStuck(capture));
  const needsYou = captures.filter(
    (capture) =>
      (isTerminalStatus(capture.status) && capture.status !== 'appended') || isStuck(capture),
  );
  const groups = groupReceipts(captures);
  return {
    moving,
    needsYou,
    groups,
    shown: groups.length === 1 ? groups : [],
    folded: groups.length > 1 ? groups : [],
  };
}
