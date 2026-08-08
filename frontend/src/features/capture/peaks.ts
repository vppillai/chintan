/**
 * The waveform amplitude envelope, computed in the browser.
 *
 * The `AnalyserNode` already holds the decoded signal while recording, so
 * reducing it to an envelope costs nothing here. Doing it server-side would
 * mean shipping an Opus decoder into a Lambda to reconstruct a signal the
 * client had in its hands — which is why `peaks_upload` exists on the create
 * response.
 *
 * Two consumers, one array: the live canvas during recording, and
 * `peaks.json` for the scrubbable player on the note screen.
 */

/** Resolution of the stored envelope. ~2 KB of JSON, enough for any width. */
export const PEAK_BUCKETS = 800;

export class PeakCollector {
  /** One 0..1 amplitude per analyser frame, before downsampling. */
  private readonly samples: number[] = [];
  private peak = 0;

  /**
   * Folds one analyser frame into the running envelope.
   *
   * Takes the frame's RMS rather than its maximum: a single click should not
   * make a whole bucket look loud, and RMS is what reads as "how loud was this
   * moment" to a person scrubbing.
   */
  push(timeDomain: Uint8Array): number {
    let sumSquares = 0;
    for (let index = 0; index < timeDomain.length; index += 1) {
      // Uint8 time-domain data is centred on 128.
      const centred = ((timeDomain[index] ?? 128) - 128) / 128;
      sumSquares += centred * centred;
    }
    const rms = timeDomain.length > 0 ? Math.sqrt(sumSquares / timeDomain.length) : 0;
    this.samples.push(rms);
    if (rms > this.peak) this.peak = rms;
    return rms;
  }

  get length(): number {
    return this.samples.length;
  }

  /** The most recent `count` amplitudes, for the live canvas. */
  recent(count: number): number[] {
    return this.samples.slice(-count);
  }

  /**
   * The envelope for `peaks.json`: `buckets` values in 0..1, normalised so a
   * quietly recorded note still renders as a waveform rather than a flat line.
   */
  envelope(buckets: number = PEAK_BUCKETS): number[] {
    return downsample(this.samples, buckets, this.peak);
  }

  reset(): void {
    this.samples.length = 0;
    this.peak = 0;
  }
}

export function downsample(
  samples: readonly number[],
  buckets: number,
  peak?: number,
): number[] {
  if (samples.length === 0) return [];
  const max = peak && peak > 0 ? peak : Math.max(...samples, Number.EPSILON);
  const size = samples.length / buckets;
  const output: number[] = [];

  for (let bucket = 0; bucket < buckets; bucket += 1) {
    const start = Math.floor(bucket * size);
    const end = Math.min(samples.length, Math.max(start + 1, Math.floor((bucket + 1) * size)));
    let highest = 0;
    for (let index = start; index < end; index += 1) {
      const value = samples[index] ?? 0;
      if (value > highest) highest = value;
    }
    // Rounded to three places: the difference is invisible at any plausible
    // pixel density and it roughly halves the JSON.
    output.push(Math.round(Math.min(1, highest / max) * 1000) / 1000);
  }

  return output;
}

export interface PeaksDocument {
  version: 1;
  buckets: number;
  duration_ms: number;
  peaks: number[];
}

export function peaksDocument(peaks: number[], durationMs: number): PeaksDocument {
  return { version: 1, buckets: peaks.length, duration_ms: Math.round(durationMs), peaks };
}
