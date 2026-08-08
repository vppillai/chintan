/**
 * Non-visual feedback.
 *
 * The stated use case is eyes-off — driving, walking. A toast in the corner of
 * a screen the user is not looking at is not feedback. A rising tone on start,
 * a falling tone on stop, and a haptic tick are.
 *
 * Everything here is best-effort and silent on failure: audio output can be
 * blocked by autoplay policy and `vibrate` is unimplemented on iOS. Neither is
 * a reason to fail a recording.
 */

const START_TONE_HZ = 660;
const STOP_TONE_HZ = 440;
const TONE_MS = 120;

/** Kept low: this is a confirmation, not an alert. */
const TONE_GAIN = 0.08;

export interface FeedbackOptions {
  haptics?: boolean;
  tones?: boolean;
}

async function tone(context: AudioContext, frequency: number): Promise<void> {
  try {
    if (context.state === 'suspended') await context.resume();
    const oscillator = context.createOscillator();
    const gain = context.createGain();
    oscillator.type = 'sine';
    oscillator.frequency.value = frequency;

    const now = context.currentTime;
    const end = now + TONE_MS / 1000;
    // Ramped rather than switched: an abrupt gain change produces an audible
    // click, which sounds like a fault.
    gain.gain.setValueAtTime(0, now);
    gain.gain.linearRampToValueAtTime(TONE_GAIN, now + 0.01);
    gain.gain.linearRampToValueAtTime(0, end);

    oscillator.connect(gain).connect(context.destination);
    oscillator.start(now);
    oscillator.stop(end);
  } catch {
    /* Autoplay policy or a closed context. Not worth surfacing. */
  }
}

function vibrate(pattern: number | number[]): void {
  try {
    navigator.vibrate?.(pattern);
  } catch {
    /* Unsupported. */
  }
}

export function startFeedback(
  context: AudioContext | null,
  options: FeedbackOptions = {},
): void {
  if (options.tones !== false && context) void tone(context, START_TONE_HZ);
  if (options.haptics !== false) vibrate(40);
}

export function stopFeedback(
  context: AudioContext | null,
  options: FeedbackOptions = {},
): void {
  if (options.tones !== false && context) void tone(context, STOP_TONE_HZ);
  if (options.haptics !== false) vibrate([30, 60, 30]);
}

/** A distinct double-buzz for anything that went wrong. */
export function errorFeedback(options: FeedbackOptions = {}): void {
  if (options.haptics !== false) vibrate([80, 80, 80]);
}
