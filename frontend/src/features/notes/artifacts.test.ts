import { describe, expect, it, vi } from 'vitest';

import type { ChintanApi } from '@/api/endpoints.ts';
import { ApiError } from '@/api/problem.ts';

import {
  activeSegmentIndex,
  formatTime,
  loadCaptureArtifacts,
  parsePeaks,
  parseSegments,
  type TranscriptSegment,
} from './artifacts.ts';

const SEGMENTS: TranscriptSegment[] = [
  { id: 0, start: 0, end: 3.2, text: 'Ridge tiles on the south slope have slipped.' },
  { id: 1, start: 3.2, end: 7.8, text: 'Get two quotes before the autumn rain.' },
  { id: 2, start: 8.5, end: 12, text: 'Ellis quoted nine hundred.' },
];

describe('parseSegments', () => {
  it('reads the enveloped form', () => {
    expect(parseSegments({ segments: SEGMENTS })).toHaveLength(3);
  });

  it('reads a bare array', () => {
    expect(parseSegments(SEGMENTS)).toHaveLength(3);
  });

  it('sorts by start time, so a reordered file still plays in order', () => {
    const shuffled = [SEGMENTS[2], SEGMENTS[0], SEGMENTS[1]];
    expect(parseSegments(shuffled).map((segment) => segment.start)).toEqual([0, 3.2, 8.5]);
  });

  it('drops malformed and empty entries rather than rendering blanks', () => {
    const parsed = parseSegments([
      { start: 0, end: 1, text: '  ' },
      { start: 1, end: 2 },
      { start: 2, end: 3, text: 'kept' },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.text).toBe('kept');
  });

  it('is empty for a missing document', () => {
    expect(parseSegments(null)).toEqual([]);
    expect(parseSegments(undefined)).toEqual([]);
  });
});

describe('parsePeaks', () => {
  it('reads the enveloped form', () => {
    expect(parsePeaks({ version: 1, buckets: 3, duration_ms: 1000, peaks: [0.1, 0.5, 1] })).toEqual([
      0.1, 0.5, 1,
    ]);
  });

  it('reads a bare array and drops non-numbers', () => {
    expect(parsePeaks([0.2, 'x', 0.4])).toEqual([0.2, 0.4]);
  });

  it('is empty for a capture with no peaks', () => {
    expect(parsePeaks(null)).toEqual([]);
  });
});

describe('activeSegmentIndex', () => {
  it('finds the segment covering the playhead', () => {
    expect(activeSegmentIndex(SEGMENTS, 0)).toBe(0);
    expect(activeSegmentIndex(SEGMENTS, 3)).toBe(0);
    expect(activeSegmentIndex(SEGMENTS, 3.2)).toBe(1);
    expect(activeSegmentIndex(SEGMENTS, 9)).toBe(2);
  });

  it('has no active segment before the first one starts', () => {
    expect(activeSegmentIndex([{ id: 0, start: 2, end: 4, text: 'later' }], 0.5)).toBe(-1);
  });

  it('has no active segment during a long gap', () => {
    // Highlighting a line through trailing silence is a lie about where
    // playback actually is.
    expect(activeSegmentIndex(SEGMENTS, 8.4)).toBe(1);
    expect(activeSegmentIndex(SEGMENTS, 20)).toBe(-1);
  });

  it('is empty-safe', () => {
    expect(activeSegmentIndex([], 5)).toBe(-1);
  });
});

describe('loadCaptureArtifacts fetches the cleaned transcript', () => {
  function api(available: Record<string, string>): ChintanApi {
    return {
      downloadUrl: vi.fn(async (_captureId: string, kind: string) => {
        if (!(kind in available)) {
          throw new ApiError({ kind: 'http', status: 404, title: 'Not found' });
        }
        return { url: `https://objects.test/${kind}`, expires_at: '2026-08-08T00:00:00Z' };
      }),
    } as unknown as ChintanApi;
  }

  it('returns the cleaned text so the panel has something to show', async () => {
    // The screen passed a hard-coded `''`, so the Cleaned tab was permanently
    // empty on every capture the pipeline had cleaned successfully.
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: string) =>
        input.endsWith('/clean')
          ? new Response('Ridge tiles have slipped. Ellis quoted nine hundred pounds.\n')
          : new Response('{}', { headers: { 'content-type': 'application/json' } }),
      ),
    );

    const artifacts = await loadCaptureArtifacts(
      api({ audio: '', clean: '' }),
      'cap-1',
      { hasPeaks: false, hasSegments: false },
    );

    expect(artifacts.cleanedText).toBe(
      'Ridge tiles have slipped. Ellis quoted nine hundred pounds.',
    );
    expect(artifacts.audioUrl).toBe('https://objects.test/audio');
  });

  it('is empty, not broken, for a capture with no clean artifact', async () => {
    const artifacts = await loadCaptureArtifacts(api({ audio: '' }), 'cap-2', {
      hasPeaks: false,
      hasSegments: false,
    });
    expect(artifacts.cleanedText).toBe('');
  });
});

describe('formatTime', () => {
  it('renders m:ss', () => {
    expect(formatTime(0)).toBe('0:00');
    expect(formatTime(9)).toBe('0:09');
    expect(formatTime(65)).toBe('1:05');
    expect(formatTime(3_600)).toBe('60:00');
  });

  it('survives a stream with no known duration', () => {
    expect(formatTime(Number.NaN)).toBe('0:00');
    expect(formatTime(Number.POSITIVE_INFINITY)).toBe('0:00');
    expect(formatTime(-4)).toBe('0:00');
  });
});
