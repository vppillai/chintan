import { describe, expect, it } from 'vitest';

import { PEAK_FLOOR, PeakCollector } from './peaks.ts';

/** An analyser frame: a square wave of the given swing around the 128 centre. */
function frame(swing: number, length = 64): Uint8Array {
  const data = new Uint8Array(length);
  for (let index = 0; index < length; index += 1) {
    data[index] = 128 + (index % 2 === 0 ? swing : -swing);
  }
  return data;
}

describe('the live view and the envelope share one scale', () => {
  it('draws a quiet frame after a loud one in proportion to the loud one', () => {
    /*
     * The live canvas used to be fed raw RMS while the envelope was normalised
     * to the loudest moment, so the bars a person watched while speaking were
     * a fraction of the bars they scrubbed through afterwards. Both now divide
     * by the running peak: a frame at a quarter of the loudest is a quarter
     * of the height, in both places.
     */
    const peaks = new PeakCollector();
    peaks.push(frame(120));
    peaks.push(frame(30));

    const recent = peaks.recent(2);
    expect(recent[0]).toBeCloseTo(1, 5);
    expect(recent[1]).toBeCloseTo(0.25, 5);
  });

  it('keeps silence near-flat instead of normalising noise up to full scale', () => {
    // A few LSB of quantisation noise is what a muted microphone produces.
    const peaks = new PeakCollector();
    for (let index = 0; index < 20; index += 1) peaks.push(frame(1));

    const rms = 1 / 128;
    for (const value of peaks.recent(20)) {
      expect(value).toBeCloseTo(rms / PEAK_FLOOR, 5);
      expect(value).toBeLessThan(0.25);
    }
    for (const value of peaks.envelope(20)) {
      expect(value).toBeLessThan(0.25);
    }
  });

  it('gives a frame of pure silence no height at all', () => {
    const peaks = new PeakCollector();
    peaks.push(frame(0));
    expect(peaks.recent(1)).toEqual([0]);
    expect(peaks.envelope(1)).toEqual([0]);
  });

  it('agrees with the envelope on the last N values once the recording has ended', () => {
    const peaks = new PeakCollector();
    const swings = [5, 40, 90, 12, 127, 60, 3, 33];
    for (const swing of swings) peaks.push(frame(swing));

    // One bucket per sample, so the envelope is the sample list itself.
    const envelope = peaks.envelope(swings.length);
    const recent = peaks.recent(swings.length);
    expect(envelope).toHaveLength(recent.length);
    for (let index = 0; index < recent.length; index += 1) {
      // The envelope is rounded to three places for the JSON; the live view is not.
      expect(recent[index]).toBeCloseTo(envelope[index] ?? -1, 3);
    }
    // The loudest moment is full height in both.
    expect(Math.max(...recent)).toBeCloseTo(1, 5);
    expect(Math.max(...envelope)).toBe(1);
  });

  it('rescales what has already been drawn when something louder arrives', () => {
    // The scale is the running peak, so the same early frame shrinks as the
    // recording finds its level — which is what the envelope will show too.
    const peaks = new PeakCollector();
    peaks.push(frame(30));
    expect(peaks.recent(1)[0]).toBeCloseTo(1, 5);
    peaks.push(frame(120));
    expect(peaks.recent(2)[0]).toBeCloseTo(0.25, 5);
  });
});
