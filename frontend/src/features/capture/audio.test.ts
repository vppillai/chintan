import { describe, expect, it } from 'vitest';

import { classifyMicrophoneError } from './audio.ts';

/**
 * Each `getUserMedia` refusal has its own sentence on the capture screen, and
 * a denied permission must never read as "this browser cannot record" — that
 * sentence is for the browser that has no MediaRecorder or no encoder, which
 * `RecorderController.start` reports before the microphone is ever asked for.
 */
describe('classifyMicrophoneError', () => {
  it.each([
    ['NotAllowedError', 'permission-denied'],
    ['SecurityError', 'permission-denied'],
    ['NotFoundError', 'no-microphone'],
    ['OverconstrainedError', 'no-microphone'],
    ['NotReadableError', 'unsupported'],
    ['AbortError', 'unsupported'],
  ] as const)('maps %s to %s', (name, kind) => {
    const error = Object.assign(new Error(name), { name });
    expect(classifyMicrophoneError(error)).toBe(kind);
  });

  it('treats anything that is not a DOMException as unsupported rather than throwing', () => {
    expect(classifyMicrophoneError(null)).toBe('unsupported');
    expect(classifyMicrophoneError('denied')).toBe('unsupported');
    expect(classifyMicrophoneError({})).toBe('unsupported');
  });
});
