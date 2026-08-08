/**
 * Capture artifacts: the audio, its peak envelope, and its timestamped
 * transcript.
 *
 * All three are fetched through short-lived presigned URLs from
 * `GET /v1/captures/{id}/download?kind=…`. The URLs are never navigated to —
 * they feed an `<audio>` element and two `fetch` calls. v1 opened the audio URL
 * with `window.open`, which either downloaded the file or navigated the user
 * out of the app, losing whatever they were doing.
 */

import type { ChintanApi } from '@/api/endpoints.ts';
import { ApiError } from '@/api/problem.ts';
import type { PeaksDocument } from '@/features/capture/peaks.ts';

/**
 * One timestamped span of the RAW transcript.
 *
 * Raw, not cleaned, and that distinction is the whole design. See
 * `TranscriptPanel` for why aligning cleaned prose onto these times is refused
 * rather than approximated.
 */
export interface TranscriptSegment {
  id: number;
  /** Seconds from the start of the recording. */
  start: number;
  end: number;
  text: string;
}

export interface SegmentsDocument {
  segments: TranscriptSegment[];
}

function isSegment(value: unknown): value is TranscriptSegment {
  if (typeof value !== 'object' || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate['start'] === 'number' &&
    typeof candidate['end'] === 'number' &&
    typeof candidate['text'] === 'string'
  );
}

/** Tolerates a bare array as well as the enveloped form. */
export function parseSegments(raw: unknown): TranscriptSegment[] {
  const list = Array.isArray(raw)
    ? raw
    : ((raw as SegmentsDocument | null)?.segments ?? []);
  return list
    .filter(isSegment)
    .map((segment, index) => ({
      id: typeof segment.id === 'number' ? segment.id : index,
      start: segment.start,
      end: segment.end,
      text: segment.text.trim(),
    }))
    .filter((segment) => segment.text.length > 0)
    .sort((a, b) => a.start - b.start);
}

export function parsePeaks(raw: unknown): number[] {
  if (Array.isArray(raw)) return raw.filter((value): value is number => typeof value === 'number');
  const document = raw as PeaksDocument | null;
  return Array.isArray(document?.peaks)
    ? document.peaks.filter((value): value is number => typeof value === 'number')
    : [];
}

async function fetchJson(url: string, signal?: AbortSignal): Promise<unknown> {
  const response = await fetch(url, signal ? { signal } : {});
  if (!response.ok) throw new Error(`Artifact fetch failed: ${response.status}`);
  return response.json();
}

/** The cleaned transcript is `text/plain`, not JSON. */
async function fetchText(url: string, signal?: AbortSignal): Promise<string> {
  const response = await fetch(url, signal ? { signal } : {});
  if (!response.ok) throw new Error(`Artifact fetch failed: ${response.status}`);
  return response.text();
}

export interface CaptureArtifacts {
  audioUrl: string | null;
  peaks: number[];
  segments: TranscriptSegment[];
  /**
   * The text cleanup produced, which is what became the note.
   *
   * Empty when the capture has no `clean` artifact — a capture that stopped at
   * `no_content`, or one recorded before cleanup existed. The panel hides the
   * Cleaned view in that case rather than offering a control whose only
   * possible outcome is an empty tab, which is what it did while this was
   * hard-coded to `''` for every capture in the app.
   */
  cleanedText: string;
}

/**
 * Loads everything the player needs for one capture.
 *
 * Peaks and segments are optional by contract: captures recorded before v2 have
 * neither, and `has_peaks` / `has_segments` say so. A missing artifact
 * downgrades to a plain player rather than failing the screen — there is no
 * backfill and there never will be.
 */
export async function loadCaptureArtifacts(
  api: ChintanApi,
  captureId: string,
  options: { hasPeaks?: boolean; hasSegments?: boolean; signal?: AbortSignal } = {},
): Promise<CaptureArtifacts> {
  const audio = await api
    .downloadUrl(captureId, 'audio')
    .catch((error: unknown) => {
      // A purged recording (retention) is an expected 404, not a broken screen.
      if (error instanceof ApiError && error.isNotFound) return null;
      throw error;
    });

  const [peaks, segments, cleanedText] = await Promise.all([
    options.hasPeaks === false
      ? Promise.resolve([])
      : api
          .downloadUrl(captureId, 'peaks')
          .then((link) => fetchJson(link.url, options.signal))
          .then(parsePeaks)
          .catch(() => []),
    options.hasSegments === false
      ? Promise.resolve([])
      : api
          .downloadUrl(captureId, 'segments')
          .then((link) => fetchJson(link.url, options.signal))
          .then(parseSegments)
          .catch(() => []),
    // No `has_clean` flag exists on the contract, so this is asked for and
    // allowed to 404 like the other optional artifacts.
    api
      .downloadUrl(captureId, 'clean')
      .then((link) => fetchText(link.url, options.signal))
      .then((text) => text.trim())
      .catch(() => ''),
  ]);

  return { audioUrl: audio?.url ?? null, peaks, segments, cleanedText };
}

/** The segment covering `time`, or the last one before it. Binary search. */
export function activeSegmentIndex(
  segments: readonly TranscriptSegment[],
  time: number,
): number {
  let low = 0;
  let high = segments.length - 1;
  let found = -1;

  while (low <= high) {
    const mid = (low + high) >> 1;
    const segment = segments[mid];
    if (!segment) break;
    if (time < segment.start) {
      high = mid - 1;
    } else {
      found = mid;
      low = mid + 1;
    }
  }

  // Past the end of the last segment there is no active line: highlighting one
  // through a trailing silence is a lie about where playback is.
  const candidate = segments[found];
  if (candidate && time > candidate.end + 0.75) return -1;
  return found;
}

/** `m:ss` with tabular numerals. */
export function formatTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '0:00';
  const total = Math.floor(seconds);
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}
