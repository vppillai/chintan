import { afterEach, describe, expect, it, vi } from 'vitest';

import { audioSpecificConfig, mux, sampleEntries, toMp4 } from './toMp4.ts';

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
    // Three frames less the 1024-sample encoder delay the edit list skips.
    expect(view.getUint32(mvhd![1] + 24)).toBe(2 * 1024);
    const text = String.fromCharCode(...file);
    const stco = text.indexOf('stco');
    expect(view.getUint32(stco + 12)).toBe(boxes(file)[2]![1] + 8);
    // AAC-LC at 48 kHz, mono: object type 2, frequency index 3, one channel.
    expect(Array.from(audioSpecificConfig(1))).toEqual([0x11, 0x88]);
    expect(text).toContain('esds');
    const elst = text.indexOf('elst');
    expect(view.getUint32(elst + 8)).toBe(1);
    expect(view.getUint32(elst + 16)).toBe(1024);
  });

  it('writes an Opus sample entry with its dOps box for the fallback', () => {
    const opus = mux(sampleEntries.opus(1, 312), samples, [960, 960, 960], 'opus');
    const text = String.fromCharCode(...opus);
    expect(text.slice(8, 12)).toBe('isom');
    expect(text).toContain('Opus');
    expect(text).toContain('dOps');
  });
});

/**
 * `toMp4` over a stand-in `AudioEncoder`: one that, like Safari 26/27
 * (WebKit bug 302253), hands back a wrong AAC config — 22050 Hz, no
 * channels — and that takes a while to drain, so the loop must wait on it.
 */
describe('toMp4', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('writes its own 48 kHz mono AAC config, never the encoder’s, and never lets the encoder queue run away', async () => {
    let peak = 0;
    class FakeEncoder extends EventTarget {
      static isConfigSupported = async (config: { codec: string }) => ({ supported: config.codec === 'mp4a.40.2' });
      encodeQueueSize = 0;
      constructor(private readonly init: AudioEncoderInit) {
        super();
      }
      configure(): void {}
      encode(): void {
        this.encodeQueueSize += 1;
        peak = Math.max(peak, this.encodeQueueSize);
        setTimeout(() => {
          this.encodeQueueSize -= 1;
          this.init.output(
            {
              byteLength: 2,
              duration: 21_333,
              copyTo: (target: Uint8Array) => target.set([0xaa, 0xbb]),
            } as unknown as EncodedAudioChunk,
            // 22050 Hz, zero channels: what WebKit reports.
            { decoderConfig: { codec: 'mp4a.40.2', sampleRate: 22_050, numberOfChannels: 0, description: Uint8Array.of(0x13, 0x80) } },
          );
          this.dispatchEvent(new Event('dequeue'));
        }, 0);
      }
      async flush(): Promise<void> {
        while (this.encodeQueueSize > 0) await new Promise((resolve) => setTimeout(resolve, 0));
      }
      close(): void {}
    }
    vi.stubGlobal('AudioEncoder', FakeEncoder);
    vi.stubGlobal('AudioData', class { close(): void {} });
    // Two seconds of stereo: twenty blocks, downmixed to one channel.
    const length = 96_000;
    vi.stubGlobal('OfflineAudioContext', class {
      decodeAudioData = async () => ({ numberOfChannels: 2, length, getChannelData: () => new Float32Array(length) });
    });

    const file = await toMp4(new ArrayBuffer(8));
    expect(file.extension).toBe('m4a');
    const bytes = new Uint8Array(await file.blob.arrayBuffer());
    const text = String.fromCharCode(...bytes);
    // The DecoderSpecificInfo (tag 5, length 2) carries 48 kHz mono AAC-LC.
    const specific = text.indexOf('\x05\x02', text.indexOf('esds'));
    expect(Array.from(bytes.subarray(specific + 2, specific + 4))).toEqual([0x11, 0x88]);
    // The sample entry says one channel.
    const mp4aAt = text.indexOf('mp4a');
    expect(new DataView(bytes.buffer).getUint16(mp4aAt + 4 + 16)).toBe(1);
    expect(peak).toBeLessThanOrEqual(5);
  });
});
