/**
 * Microphone acquisition and encoder selection.
 *
 * The constraints matter more than they look. 44.1 kHz stereo is a
 * music-recording profile: two to three times the bytes over cellular for
 * content that a speech-to-text model downsamples to 16 kHz mono before it
 * looks at it. Nothing would be gained and an upload on a weak connection
 * would be three times more likely to fail.
 */

import type { CaptureContentType } from '@/api/schema.ts';

/** What Whisper-class models actually consume. */
export const TARGET_SAMPLE_RATE = 16_000;
export const TARGET_CHANNELS = 1;
/** Opus at 24 kbps is transparent for speech and is ~3.6 MB for 20 minutes. */
export const TARGET_BITS_PER_SECOND = 24_000;

export const AUDIO_CONSTRAINTS: MediaTrackConstraints = {
  channelCount: TARGET_CHANNELS,
  sampleRate: TARGET_SAMPLE_RATE,
  echoCancellation: true,
  noiseSuppression: true,
  autoGainControl: true,
};

/**
 * Candidate containers in preference order.
 *
 * Every entry is one the API contract's `content_type` enum accepts. Safari
 * produces `audio/mp4`; Chromium and Firefox produce WebM/Opus.
 */
const CANDIDATE_TYPES: { mimeType: string; contentType: CaptureContentType }[] = [
  { mimeType: 'audio/webm;codecs=opus', contentType: 'audio/webm' },
  { mimeType: 'audio/webm', contentType: 'audio/webm' },
  { mimeType: 'audio/mp4;codecs=opus', contentType: 'audio/mp4' },
  { mimeType: 'audio/mp4', contentType: 'audio/mp4' },
  { mimeType: 'audio/ogg;codecs=opus', contentType: 'audio/ogg' },
];

export interface EncoderChoice {
  /** Passed to `MediaRecorder`. May carry a codec parameter. */
  mimeType: string;
  /** Sent to the API, which accepts only the bare container types. */
  contentType: CaptureContentType;
}

export function chooseEncoder(): EncoderChoice | null {
  if (typeof MediaRecorder === 'undefined') return null;
  for (const candidate of CANDIDATE_TYPES) {
    if (MediaRecorder.isTypeSupported(candidate.mimeType)) return candidate;
  }
  // Some WebViews support recording but report nothing. Let the browser pick
  // its default rather than refusing to record at all.
  return { mimeType: '', contentType: 'audio/webm' };
}

export function isRecordingSupported(): boolean {
  return (
    typeof navigator !== 'undefined' &&
    typeof navigator.mediaDevices?.getUserMedia === 'function' &&
    typeof MediaRecorder !== 'undefined'
  );
}

export type MicrophoneFailure = 'permission-denied' | 'no-microphone' | 'unsupported';

export function classifyMicrophoneError(error: unknown): MicrophoneFailure {
  const name = (error as { name?: string } | null)?.name ?? '';
  if (name === 'NotAllowedError' || name === 'SecurityError') return 'permission-denied';
  if (name === 'NotFoundError' || name === 'OverconstrainedError') return 'no-microphone';
  return 'unsupported';
}

export async function requestMicrophone(): Promise<MediaStream> {
  return navigator.mediaDevices.getUserMedia({ audio: AUDIO_CONSTRAINTS, video: false });
}
