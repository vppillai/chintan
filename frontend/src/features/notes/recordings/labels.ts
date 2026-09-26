/**
 * The words on a recording row and on the notice line under the rows, as
 * pure functions of the wire: no React, no DOM, so each sentence is tested
 * as a sentence. `Recordings` and `RecordingRow` read them; nothing writes
 * a label anywhere else.
 */

import { ApiError } from '@/api/problem.ts';
import type { CaptureStatus, CaptureWire, NoteWire } from '@/api/schema.ts';
import { AUTO_LANGUAGE, languageName } from '@/features/settings/languages.ts';

/** The capture that was moving on the last render and is `appended` on this one. */
export function justLanded(
  before: readonly CaptureWire[],
  after: readonly CaptureWire[],
): CaptureWire | undefined {
  const was = new Map<string, CaptureStatus>(before.map((capture) => [capture.id, capture.status]));
  return after.find(
    (capture) =>
      capture.status === 'appended' && was.has(capture.id) && was.get(capture.id) !== 'appended',
  );
}

/** A capture that arrived through the inbox under a device key rather than from this app. */
export function isFromDevice(capture: Pick<CaptureWire, 'source'>): boolean {
  return (capture.source ?? 'app').startsWith('device:');
}

/**
 * "From Watch" for a row a device sent, named from the devices list; "From a
 * device" when the list no longer has it (removed since) or has not answered.
 * Nothing for a recording made here — every other row on the tab is one, and
 * saying so would be the "Filed" repeated down the list all over again.
 */
export function sourceLabel(
  capture: Pick<CaptureWire, 'source'>,
  names: ReadonlyMap<string, string>,
): string | null {
  if (!isFromDevice(capture)) return null;
  const id = (capture.source ?? '').slice('device:'.length);
  return `From ${names.get(id) ?? 'a device'}`;
}

export interface Notice {
  text: string;
  tone: 'ok' | 'error';
  /** The note the recordings went to, offered as "Open <title>". */
  target?: Pick<NoteWire, 'id' | 'title'>;
}

/**
 * "Tamil" — the language Whisper heard, when it is worth a chip: under
 * Auto-detect always, because what it picked is the one thing the reader
 * cannot otherwise know; under a chosen language only when the two differ,
 * which is the transcript that came back in the wrong script. The worker
 * stores Whisper's English name, so the comparison is by name; a code the
 * curated list lacks still compares by `Intl` name through `languageName`.
 */
export function heardAs(detected: string | null, effective: string): string | null {
  if (!detected) return null;
  const name = detected.charAt(0).toUpperCase() + detected.slice(1);
  if (effective === AUTO_LANGUAGE) return name;
  const same = [languageName(effective), effective].some(
    (candidate) => candidate.toLowerCase() === detected.toLowerCase(),
  );
  return same ? null : name;
}

/** "Transcribe again in Malayalam", or the honest form for Auto-detect. */
export function retranscribeLabel(language: string): string {
  return language === AUTO_LANGUAGE
    ? 'Transcribe again (auto-detect)'
    : `Transcribe again in ${languageName(language)}`;
}

/** "Wait until it has finished filing" for a 409; the server's own words otherwise. */
export function failureText(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.isConflict) return 'Wait until it has finished filing.';
    return error.userMessage;
  }
  return 'That did not go through. Try again.';
}

export function describeOutcome(
  done: number,
  failed: readonly { error: unknown }[],
  verb: 'deleted' | 'moved',
): Notice {
  if (failed.length === 0) {
    return {
      text: done === 1 ? `Recording ${verb}` : `${String(done)} recordings ${verb}`,
      tone: 'ok',
    };
  }
  // One refusal is quoted; several are counted, and the first reason stands
  // for them — a batch that stalled is almost always stalled for one reason.
  const reason = failureText(failed[0]?.error);
  if (done === 0) {
    return {
      text: failed.length === 1 ? reason : `${String(failed.length)} could not be ${verb}. ${reason}`,
      tone: 'error',
    };
  }
  return {
    text: `${String(done)} ${verb}; ${String(failed.length)} could not be. ${reason}`,
    tone: 'error',
  };
}

/**
 * How a recording was filed, in the words of the row — when there is
 * something to say.
 *
 * A recording that reached the note says nothing: every row on this tab is
 * in the note by definition, and "Filed" repeated down the list said so
 * six times (review 2026-09-21, T19). The wire says *where* a capture went
 * and *whether* it got there, not who decided — `CaptureWire` carries
 * `note_id`, the router's `suggested_*` fields (cleared the moment a target
 * is set) and `appended_at`, nothing that separates "the router chose this
 * note" from "the user chose it" — so there is no truer word to put there.
 * The states that are worth a word are the ones still moving or gone wrong.
 */
export function filedLabel(capture: CaptureWire): string {
  switch (capture.status) {
    case 'appended':
      return '';
    case 'needs_target':
      return 'Needs a target';
    case 'failed':
      return 'Failed';
    case 'spend_capped':
      return 'Spending cap reached';
    case 'no_content':
      return 'Nothing to save';
    default:
      return 'Filing…';
  }
}
