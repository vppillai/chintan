import { describe, expect, it } from 'vitest';

import { audioSpecificConfig, mux, sampleEntries } from './toMp4.ts';

/** The top-level boxes of an MP4, as [type, offset, size]. */
function boxes(bytes: Uint8Array, from = 0, to = bytes.length): [string, number, number][] {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const out: [string, number, number][] = [];
  for (let at = from; at < to; ) {
    const size = view.getUint32(at);
    out.push([String.fromCharCode(...bytes.subarray(at + 4, at + 8)), at, size]);
    at += size;
  }
  return out;
}

describe('the MP4 muxer', () => {
  // Three AAC frames of 1024 samples, made-up payloads: the container is what is under test.
  const samples = [Uint8Array.of(1, 2, 3), Uint8Array.of(4, 5), Uint8Array.of(6, 7, 8, 9)];
  const file = mux(sampleEntries.mp4a(1, audioSpecificConfig(1)), samples, [1024, 1024, 1024], 'aac');
  const view = new DataView(file.buffer);

  it('writes ftyp (M4A), moov, then mdat holding the samples back to back', () => {
    expect(boxes(file).map(([type]) => type)).toEqual(['ftyp', 'moov', 'mdat']);
    expect(String.fromCharCode(...file.subarray(8, 12))).toBe('M4A ');
    const [, mdat, size] = boxes(file)[2]!;
    expect(Array.from(file.subarray(mdat + 8, mdat + size))).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9]);
    expect(mdat + size).toBe(file.length);
  });

  it('says the duration in the movie header and points the chunk offset at the samples', () => {
    const [, moov, moovSize] = boxes(file)[1]!;
    const [mvhd] = boxes(file, moov + 8, moov + moovSize);
    expect(mvhd![0]).toBe('mvhd');
    // version/flags, two times, then the timescale and the duration.
    expect(view.getUint32(mvhd![1] + 20)).toBe(48_000);
    expect(view.getUint32(mvhd![1] + 24)).toBe(3 * 1024);
    const text = String.fromCharCode(...file);
    const stco = text.indexOf('stco');
    expect(view.getUint32(stco + 12)).toBe(boxes(file)[2]![1] + 8);
    // AAC-LC at 48 kHz, mono: object type 2, frequency index 3, one channel.
    expect(Array.from(audioSpecificConfig(1))).toEqual([0x11, 0x88]);
    expect(text).toContain('esds');
  });

  it('writes an Opus sample entry with its dOps box for the fallback', () => {
    const opus = mux(sampleEntries.opus(1, 312), samples, [960, 960, 960], 'opus');
    const text = String.fromCharCode(...opus);
    expect(text.slice(8, 12)).toBe('isom');
    expect(text).toContain('Opus');
    expect(text).toContain('dOps');
  });
});
