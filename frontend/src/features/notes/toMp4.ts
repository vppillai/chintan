/**
 * A recording as an MP4 audio file, made on the device.
 *
 * The bucket holds what the recorder made — WebM/Opus from a browser, M4A or
 * MP3 from a shortcut — and the You screen's "Download format" can ask for
 * MP4 instead. Converting here rather than on the server means no transcoder
 * in the worker, no Lambda layer and no second object per recording; the
 * cost is that this file is loaded only when an MP4 download is asked for
 * (a dynamic import from `Recordings`), never at launch.
 *
 * The browser does the codec work: `decodeAudioData` reads any format it can
 * play, resampled to 48 kHz, and WebCodecs' `AudioEncoder` writes AAC-LC,
 * which is what "MP4 audio" means to every player — iOS, Android, Windows,
 * macOS. Where the browser has no AAC encoder (Firefox, Chromium builds
 * without the proprietary codecs) it writes Opus in the same MP4 container,
 * named `.mp4` rather than `.m4a`; Chrome, Edge, Firefox and Safari 17+ play
 * that. Where there is no `AudioEncoder` at all (Safari before 26),
 * `Mp4Unsupported` says so.
 *
 * Mono, whatever the source: these are voice notes, and one channel halves
 * the encoder's work. The AAC config is always the one written here
 * (`audioSpecificConfig`), never the encoder's own `decoderConfig.description`:
 * Safari 26/27 hands back a wrong one (22050 Hz, 0 channels; WebKit bug
 * 302253), which made a silent file on an iPhone.
 *
 * The container is written here, about a hundred lines of ISO BMFF boxes:
 * one audio track, every sample in one chunk, `moov` before `mdat` so a
 * player can start before the whole file has arrived, and for AAC an edit
 * list that skips `AAC_PRIMING` samples of encoder delay.
 */

const SAMPLE_RATE = 48_000;
/** Samples handed to the encoder per call: 100 ms, any size works. */
const BLOCK = 4_800;
/**
 * Blocks the encoder may hold before the loop waits for it. Unbounded, a
 * twenty-minute recording queued every block at once (~740 MB in the
 * renderer); bounded, the decoded PCM is what remains (note-screen.md,
 * "The Recordings tab").
 */
const MAX_QUEUE = 4;
/**
 * AAC encoder delay skipped by the edit list. 1024 is the common floor
 * (Apple's encoder primes 2112), so the trim never cuts speech; a larger
 * delay leaves the remainder, about 23 ms, as leading silence.
 */
const AAC_PRIMING = 1024;
const CHANNELS = 1;

class Mp4Unsupported extends Error {
  constructor() {
    super('this browser cannot encode audio');
    this.name = 'Mp4Unsupported';
  }
}

export interface Mp4File {
  blob: Blob;
  /** `m4a` for AAC, `mp4` for the Opus fallback. */
  extension: 'm4a' | 'mp4';
}

type Codec = 'aac' | 'opus';

export async function toMp4(input: ArrayBuffer): Promise<Mp4File> {
  if (typeof AudioEncoder === 'undefined') throw new Mp4Unsupported();
  const audio = await new OfflineAudioContext(1, 1, SAMPLE_RATE).decodeAudioData(input);

  const aac = { codec: 'mp4a.40.2', sampleRate: SAMPLE_RATE, numberOfChannels: CHANNELS, bitrate: 96_000 };
  const opus = { codec: 'opus', sampleRate: SAMPLE_RATE, numberOfChannels: CHANNELS, bitrate: 64_000 };
  let codec: Codec;
  if ((await AudioEncoder.isConfigSupported(aac)).supported) codec = 'aac';
  else if ((await AudioEncoder.isConfigSupported(opus)).supported) codec = 'opus';
  else throw new Mp4Unsupported();

  const samples: Uint8Array[] = [];
  const durations: number[] = [];
  let failure: unknown = null;
  const encoder = new AudioEncoder({
    output: (chunk) => {
      const bytes = new Uint8Array(chunk.byteLength);
      chunk.copyTo(bytes);
      samples.push(bytes);
      durations.push(Math.round(((chunk.duration ?? 0) * SAMPLE_RATE) / 1e6) || (codec === 'aac' ? 1024 : 960));
    },
    error: (error) => {
      failure = error;
    },
  });
  encoder.configure(codec === 'aac' ? aac : opus);
  const sources = Array.from({ length: audio.numberOfChannels }, (_, c) => audio.getChannelData(c));
  for (let start = 0; start < audio.length && !failure; start += BLOCK) {
    while (encoder.encodeQueueSize > MAX_QUEUE) {
      await new Promise((resolve) => {
        encoder.addEventListener('dequeue', resolve, { once: true });
      });
    }
    const frames = Math.min(BLOCK, audio.length - start);
    const mono = new Float32Array(frames);
    for (const source of sources) {
      for (let i = 0; i < frames; i += 1) mono[i]! += source[start + i]! / sources.length;
    }
    const data = new AudioData({
      format: 'f32-planar',
      sampleRate: SAMPLE_RATE,
      numberOfFrames: frames,
      numberOfChannels: CHANNELS,
      timestamp: Math.round((start * 1e6) / SAMPLE_RATE),
      data: mono,
    });
    encoder.encode(data);
    data.close();
  }
  await encoder.flush();
  encoder.close();
  if (failure) throw failure instanceof Error ? failure : new Error('audio encoding failed');

  const entry = codec === 'aac' ? mp4a(CHANNELS, audioSpecificConfig(CHANNELS)) : opusEntry(CHANNELS, OPUS_PRE_SKIP);
  const bytes = mux(entry, samples, durations, codec);
  return {
    blob: new Blob([bytes as BlobPart], { type: codec === 'aac' ? 'audio/mp4' : 'video/mp4' }),
    extension: codec === 'aac' ? 'm4a' : 'mp4',
  };
}

/* ---------------------------------------------------------------------------
   ISO BMFF (ISO/IEC 14496-12, -14; Opus in ISOBMFF). Exported for the test.
   --------------------------------------------------------------------------- */

function concat(parts: readonly Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((sum, part) => sum + part.length, 0));
  let at = 0;
  for (const part of parts) {
    out.set(part, at);
    at += part.length;
  }
  return out;
}

const u8 = (n: number): Uint8Array => Uint8Array.of(n & 0xff);
const u16 = (n: number): Uint8Array => Uint8Array.of((n >>> 8) & 0xff, n & 0xff);
const u32 = (n: number): Uint8Array =>
  Uint8Array.of((n >>> 24) & 0xff, (n >>> 16) & 0xff, (n >>> 8) & 0xff, n & 0xff);
const ascii = (text: string): Uint8Array => Uint8Array.from(text, (c) => c.charCodeAt(0));
const zeros = (n: number): Uint8Array => new Uint8Array(n);

function box(type: string, ...body: Uint8Array[]): Uint8Array {
  const content = concat(body);
  return concat([u32(content.length + 8), ascii(type), content]);
}

/** A full box: a box whose body starts with version and flags. */
function fullBox(type: string, version: number, flags: number, ...body: Uint8Array[]): Uint8Array {
  return box(type, u8(version), u8(flags >> 16), u16(flags & 0xffff), ...body);
}

const MATRIX = concat([u32(0x10000), u32(0), u32(0), u32(0), u32(0x10000), u32(0), u32(0), u32(0), u32(0x40000000)]);

/** AAC-LC's two-byte AudioSpecificConfig, for an encoder that hands none over. */
export function audioSpecificConfig(channels: number): Uint8Array {
  const frequencyIndex = 3; // 48 kHz
  return Uint8Array.of((2 << 3) | (frequencyIndex >> 1), ((frequencyIndex & 1) << 7) | (channels << 3));
}

/** libopus's pre-skip at 48 kHz, the value every encoder of it writes in its own OpusHead. */
const OPUS_PRE_SKIP = 312;

/** The fields every audio sample entry starts with. */
function audioEntry(type: string, channels: number, ...tail: Uint8Array[]): Uint8Array {
  return box(
    type,
    zeros(6),
    u16(1), // data_reference_index
    zeros(8),
    u16(channels),
    u16(16),
    zeros(4),
    u32(SAMPLE_RATE * 0x10000),
    ...tail,
  );
}

function mp4a(channels: number, config: Uint8Array): Uint8Array {
  // ES_Descriptor ⊃ DecoderConfigDescriptor ⊃ DecoderSpecificInfo, then SLConfig.
  const specific = concat([u8(0x05), u8(config.length), config]);
  const decoderConfig = concat([
    u8(0x04),
    u8(13 + specific.length),
    u8(0x40), // MPEG-4 audio
    u8(0x15), // audio stream
    zeros(3),
    u32(0),
    u32(0),
    specific,
  ]);
  const es = concat([u8(0x03), u8(3 + decoderConfig.length + 3), u16(0), u8(0), decoderConfig, u8(0x06), u8(1), u8(0x02)]);
  return audioEntry('mp4a', channels, fullBox('esds', 0, 0, es));
}

function opusEntry(channels: number, preSkip: number): Uint8Array {
  const dOps = box('dOps', u8(0), u8(channels), u16(preSkip), u32(SAMPLE_RATE), u16(0), u8(0));
  return audioEntry('Opus', channels, dOps);
}

/** Run-length `stts` entries from per-sample durations. */
function timeToSample(durations: readonly number[]): Uint8Array {
  const runs: [number, number][] = [];
  for (const duration of durations) {
    const last = runs.at(-1);
    if (last && last[1] === duration) last[0] += 1;
    else runs.push([1, duration]);
  }
  return fullBox('stts', 0, 0, u32(runs.length), ...runs.flatMap(([count, delta]) => [u32(count), u32(delta)]));
}

export function mux(
  sampleEntry: Uint8Array,
  samples: readonly Uint8Array[],
  durations: readonly number[],
  codec: Codec,
): Uint8Array {
  const duration = durations.reduce((sum, d) => sum + d, 0);
  // What plays: the media less the encoder delay the edit list skips.
  const shown = codec === 'aac' ? Math.max(duration - AAC_PRIMING, 0) : duration;
  const brands = codec === 'aac' ? ['M4A ', 'M4A ', 'isom', 'mp42'] : ['isom', 'isom', 'iso2', 'mp41'];
  const ftyp = box('ftyp', ascii(brands[0]!), u32(0), ...brands.slice(1).map(ascii));

  const moovFor = (offset: number): Uint8Array =>
    box(
      'moov',
      fullBox('mvhd', 0, 0, u32(0), u32(0), u32(SAMPLE_RATE), u32(shown), u32(0x10000), u16(0x100), zeros(10), MATRIX, zeros(24), u32(2)),
      box(
        'trak',
        fullBox('tkhd', 0, 3, u32(0), u32(0), u32(1), u32(0), u32(shown), zeros(8), u16(0), u16(0), u16(0x100), u16(0), MATRIX, u32(0), u32(0)),
        // An edit list for AAC: the movie starts after the encoder delay.
        ...(codec === 'aac'
          ? [box('edts', fullBox('elst', 0, 0, u32(1), u32(shown), u32(AAC_PRIMING), u16(1), u16(0)))]
          : []),
        box(
          'mdia',
          fullBox('mdhd', 0, 0, u32(0), u32(0), u32(SAMPLE_RATE), u32(duration), u16(0x55c4), u16(0)),
          fullBox('hdlr', 0, 0, u32(0), ascii('soun'), zeros(12), ascii('SoundHandler\0')),
          box(
            'minf',
            fullBox('smhd', 0, 0, u16(0), u16(0)),
            box('dinf', fullBox('dref', 0, 0, u32(1), fullBox('url ', 0, 1))),
            box(
              'stbl',
              fullBox('stsd', 0, 0, u32(1), sampleEntry),
              timeToSample(durations),
              fullBox('stsc', 0, 0, u32(1), u32(1), u32(samples.length), u32(1)),
              fullBox('stsz', 0, 0, u32(0), u32(samples.length), ...samples.map((s) => u32(s.length))),
              fullBox('stco', 0, 0, u32(1), u32(offset)),
            ),
          ),
        ),
      ),
    );

  // The chunk offset is a fixed-width field, so the moov's size does not
  // depend on its value: measure once, then write it with the real offset.
  const moovSize = moovFor(0).length;
  const mdatHeader = 8;
  const moov = moovFor(ftyp.length + moovSize + mdatHeader);
  const media = concat(samples);
  return concat([ftyp, moov, u32(media.length + mdatHeader), ascii('mdat'), media]);
}

/** The entry `toMp4` writes for AAC, exported so the test can mux without an encoder. */
export const sampleEntries = { mp4a, opus: opusEntry };
