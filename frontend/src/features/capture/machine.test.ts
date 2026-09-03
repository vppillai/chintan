import { describe, expect, it } from 'vitest';

import {
  DURATION_WARNING_MS,
  INITIAL_CAPTURE,
  MAX_BYTES,
  MAX_DURATION_MS,
  SIZE_WARNING_BYTES,
  canRetryUpload,
  captureReducer,
  formatElapsed,
  hasBufferedAudio,
  isCaptureBusy,
  isLive,
  type CaptureEvent,
  type CaptureModel,
} from './machine.ts';

const T0 = 1_700_000_000_000;

/** Applies a sequence, so tests read as a story rather than as plumbing. */
function run(events: CaptureEvent[], from: CaptureModel = INITIAL_CAPTURE): CaptureModel {
  return events.reduce(captureReducer, from);
}

const requestAndStart: CaptureEvent[] = [
  { type: 'request', localId: 'cap-1' },
  { type: 'streamReady', now: T0 },
];

describe('nothing claims recording before the microphone is live', () => {
  it('is requesting, not recording, while getUserMedia is in flight', () => {
    const model = run([{ type: 'request', localId: 'cap-1' }]);

    expect(model.state).toBe('requesting');
    expect(isLive(model)).toBe(false);
    expect(model.elapsedMs).toBe(0);
  });

  it('only becomes live once the stream arrives', () => {
    const model = run(requestAndStart);
    expect(model.state).toBe('recording');
    expect(isLive(model)).toBe(true);
  });

  it('ignores a stream that arrives after the request was abandoned', () => {
    const model = run([
      { type: 'request', localId: 'cap-1' },
      { type: 'permissionDenied' },
      { type: 'streamReady', now: T0 },
    ]);
    expect(model.state).toBe('failed');
  });
});

describe('permission and device failures', () => {
  it('reports a denied permission without claiming a recording', () => {
    const model = run([{ type: 'request', localId: 'cap-1' }, { type: 'permissionDenied' }]);

    expect(model.state).toBe('failed');
    expect(model.failure?.kind).toBe('permission-denied');
    expect(model.failure?.recoverable).toBe(false);
    expect(isLive(model)).toBe(false);
  });

  it('distinguishes a missing microphone from a refused one', () => {
    const model = run([{ type: 'request', localId: 'cap-1' }, { type: 'noMicrophone' }]);
    expect(model.failure?.kind).toBe('no-microphone');
  });

  it('reports an unsupported browser as its own case', () => {
    const model = run([{ type: 'request', localId: 'cap-1' }, { type: 'unsupported' }]);
    expect(model.failure?.kind).toBe('unsupported');
  });
});

describe('elapsed time', () => {
  it('advances with the clock while recording', () => {
    const model = run([...requestAndStart, { type: 'tick', now: T0 + 5_000 }]);
    expect(model.elapsedMs).toBe(5_000);
  });

  it('does not advance while paused', () => {
    const paused = run([
      ...requestAndStart,
      { type: 'tick', now: T0 + 5_000 },
      { type: 'pause', now: T0 + 5_000 },
      { type: 'tick', now: T0 + 60_000 },
    ]);

    expect(paused.state).toBe('paused');
    expect(paused.elapsedMs).toBe(5_000);
  });

  it('resumes counting from where it paused, not from zero', () => {
    const model = run([
      ...requestAndStart,
      { type: 'pause', now: T0 + 5_000 },
      { type: 'resume', now: T0 + 60_000 },
      { type: 'tick', now: T0 + 63_000 },
    ]);

    expect(model.elapsedMs).toBe(8_000);
  });

  it('formats as mm:ss and grows to h:mm:ss', () => {
    expect(formatElapsed(0)).toBe('00:00');
    expect(formatElapsed(65_000)).toBe('01:05');
    expect(formatElapsed(3_725_000)).toBe('1:02:05');
  });
});

describe('interruption produces a saved partial, never a discard', () => {
  it('stops cleanly and keeps the audio when the track ends mid-recording', () => {
    // An incoming call. v1 handled no track event, so the recording truncated
    // silently and the timer kept counting over a dead stream.
    const model = run([
      ...requestAndStart,
      { type: 'data', bytes: 40_000 },
      { type: 'tick', now: T0 + 9_000 },
      { type: 'trackEnded', now: T0 + 9_000 },
    ]);

    expect(model.state).toBe('stopping');
    expect(model.interrupted).toBe(true);
    expect(model.bytes).toBe(40_000);
    expect(model.elapsedMs).toBe(9_000);
  });

  it('reaches review with the partial audio intact', () => {
    const model = run([
      ...requestAndStart,
      { type: 'data', bytes: 40_000 },
      { type: 'trackEnded', now: T0 + 9_000 },
      { type: 'finalised' },
    ]);

    expect(model.state).toBe('review');
    expect(model.interrupted).toBe(true);
    expect(hasBufferedAudio(model)).toBe(true);
  });

  it('fails honestly when the interruption arrived before any audio', () => {
    const model = run([...requestAndStart, { type: 'trackEnded', now: T0 + 500 }]);
    expect(model.state).toBe('failed');
    expect(model.failure?.kind).toBe('recorder-failed');
  });

  it('treats a muted track as recoverable, flagging rather than stopping', () => {
    const model = run([
      ...requestAndStart,
      { type: 'data', bytes: 1_000 },
      { type: 'trackMuted' },
    ]);

    expect(model.state).toBe('recording');
    expect(model.interrupted).toBe(true);
  });

  it('keeps buffered audio when the recorder itself errors', () => {
    const model = run([
      ...requestAndStart,
      { type: 'data', bytes: 12_000 },
      { type: 'recorderError' },
    ]);

    expect(model.state).toBe('review');
    expect(model.bytes).toBe(12_000);
    expect(model.interrupted).toBe(true);
  });

  it('fails when the recorder errors with nothing buffered', () => {
    const model = run([...requestAndStart, { type: 'recorderError' }]);
    expect(model.state).toBe('failed');
  });
});

describe('caps warn and then stop, rather than truncating', () => {
  it('warns before the duration limit', () => {
    const model = run([...requestAndStart, { type: 'tick', now: T0 + DURATION_WARNING_MS }]);

    expect(model.nearDurationLimit).toBe(true);
    expect(model.state).toBe('recording');
  });

  it('stops at the duration limit and keeps the recording', () => {
    const model = run([
      ...requestAndStart,
      { type: 'data', bytes: 3_000_000 },
      { type: 'tick', now: T0 + MAX_DURATION_MS },
    ]);

    expect(model.state).toBe('stopping');
    expect(model.capReached).toBe('duration');
    expect(model.bytes).toBe(3_000_000);
    expect(model.elapsedMs).toBe(MAX_DURATION_MS);
  });

  it('warns before the size limit', () => {
    const model = run([...requestAndStart, { type: 'data', bytes: SIZE_WARNING_BYTES }]);
    expect(model.nearSizeLimit).toBe(true);
    expect(model.state).toBe('recording');
  });

  it('stops at the size limit', () => {
    const model = run([...requestAndStart, { type: 'data', bytes: MAX_BYTES }]);
    expect(model.state).toBe('stopping');
    expect(model.capReached).toBe('size');
  });

  it('accepts the final chunk that arrives after a cap-triggered stop', () => {
    const model = run([
      ...requestAndStart,
      { type: 'data', bytes: MAX_BYTES },
      { type: 'data', bytes: 5_000 },
    ]);

    expect(model.bytes).toBe(MAX_BYTES + 5_000);
    expect(model.state).toBe('stopping');
  });
});

describe('stop and review', () => {
  it('goes stopping then review, holding the elapsed time', () => {
    const stopping = run([
      ...requestAndStart,
      { type: 'data', bytes: 8_000 },
      { type: 'stop', now: T0 + 12_000 },
    ]);
    expect(stopping.state).toBe('stopping');
    expect(stopping.elapsedMs).toBe(12_000);

    const review = captureReducer(stopping, { type: 'finalised' });
    expect(review.state).toBe('review');
    expect(review.elapsedMs).toBe(12_000);
  });

  it('fails rather than uploading zero bytes', () => {
    // Uploading silence burns a transcription call to produce nothing.
    const model = run([...requestAndStart, { type: 'stop', now: T0 + 400 }, { type: 'finalised' }]);

    expect(model.state).toBe('failed');
    expect(model.failure?.kind).toBe('recorder-failed');
  });

  it('can be stopped from paused', () => {
    const model = run([
      ...requestAndStart,
      { type: 'data', bytes: 100 },
      { type: 'pause', now: T0 + 3_000 },
      { type: 'stop', now: T0 + 30_000 },
      { type: 'finalised' },
    ]);

    expect(model.state).toBe('review');
    expect(model.elapsedMs).toBe(3_000);
  });
});

describe('upload', () => {
  const reviewed = run([
    ...requestAndStart,
    { type: 'data', bytes: 90_000 },
    { type: 'stop', now: T0 + 20_000 },
    { type: 'finalised' },
  ]);

  it('runs review -> uploading -> uploaded', () => {
    const model = run(
      [
        { type: 'uploadStart' },
        { type: 'captureCreated', serverCaptureId: 'srv-1' },
        { type: 'uploadProgress', progress: 0.5 },
        { type: 'uploadDone' },
      ],
      reviewed,
    );

    expect(model.state).toBe('uploaded');
    expect(model.serverCaptureId).toBe('srv-1');
    expect(model.uploadProgress).toBe(1);
  });

  it('clamps progress into 0..1', () => {
    const model = run(
      [{ type: 'uploadStart' }, { type: 'uploadProgress', progress: 4 }],
      reviewed,
    );
    expect(model.uploadProgress).toBe(1);
  });

  it('keeps the recording recoverable when the upload fails', () => {
    // Offline mid-upload. The bytes are still in IndexedDB, so this is a
    // Resend button rather than a lost recording.
    const model = run(
      [{ type: 'uploadStart' }, { type: 'uploadFailed', message: 'No connection' }],
      reviewed,
    );

    expect(model.state).toBe('failed');
    expect(model.failure?.recoverable).toBe(true);
    expect(canRetryUpload(model)).toBe(true);
    expect(hasBufferedAudio(model)).toBe(true);
  });

  it('allows a failed upload to be retried from the failed state', () => {
    const failed = run(
      [{ type: 'uploadStart' }, { type: 'uploadFailed', message: 'No connection' }],
      reviewed,
    );
    const retrying = captureReducer(failed, { type: 'uploadStart' });

    expect(retrying.state).toBe('uploading');
    expect(retrying.failure).toBeNull();
  });

  it('reports the spend cap as its own recoverable case', () => {
    const model = run(
      [{ type: 'uploadStart' }, { type: 'spendCapped', message: 'Daily cap reached' }],
      reviewed,
    );

    expect(model.failure?.kind).toBe('spend-capped');
    // The cap resets tomorrow, so the audio is kept.
    expect(canRetryUpload(model)).toBe(true);
  });

  it('marks an unreadable local buffer as unrecoverable', () => {
    const model = run(
      [
        { type: 'uploadStart' },
        { type: 'uploadFailed', message: 'Could not read', recoverable: false },
      ],
      reviewed,
    );
    expect(canRetryUpload(model)).toBe(false);
  });
});

describe('busy-ness, which the shell indicator reports', () => {
  it.each([
    ['requesting', run([{ type: 'request', localId: 'c' }])],
    ['recording', run(requestAndStart)],
    ['paused', run([...requestAndStart, { type: 'pause', now: T0 + 1 }])],
    [
      'stopping',
      run([...requestAndStart, { type: 'data', bytes: 1 }, { type: 'stop', now: T0 + 1 }]),
    ],
  ])('is busy while %s', (_label, model) => {
    expect(isCaptureBusy(model)).toBe(true);
  });

  it.each([
    ['idle', INITIAL_CAPTURE],
    [
      'review',
      run([...requestAndStart, { type: 'data', bytes: 1 }, { type: 'stop', now: T0 }, { type: 'finalised' }]),
    ],
  ])('is not busy while %s', (_label, model) => {
    expect(isCaptureBusy(model)).toBe(false);
  });
});

describe('changing the target mid-recording', () => {
  it('moves a live recording to the chosen note, and back to a new note', () => {
    const live = run([...requestAndStart, { type: 'target', noteId: 'roof-repair' }]);
    expect(live.noteId).toBe('roof-repair');
    expect(live.state).toBe('recording');
    expect(captureReducer(live, { type: 'target', noteId: null }).noteId).toBeNull();
  });

  it('still applies in review, where Send is about to read it', () => {
    const reviewed = run([
      ...requestAndStart,
      { type: 'data', bytes: 1 },
      { type: 'stop', now: T0 + 1 },
      { type: 'finalised' },
      { type: 'target', noteId: 'reading-list' },
    ]);
    expect(reviewed.state).toBe('review');
    expect(reviewed.noteId).toBe('reading-list');
  });

  it('is ignored once the upload has begun, because the target has been sent', () => {
    const uploading = run([
      ...requestAndStart,
      { type: 'data', bytes: 1 },
      { type: 'stop', now: T0 + 1 },
      { type: 'finalised' },
      { type: 'uploadStart' },
    ]);
    expect(captureReducer(uploading, { type: 'target', noteId: 'x' })).toBe(uploading);
    expect(captureReducer(INITIAL_CAPTURE, { type: 'target', noteId: 'x' })).toBe(INITIAL_CAPTURE);
  });
});

describe('discard and reset', () => {
  it('returns to idle and forgets the recording', () => {
    const model = run([
      ...requestAndStart,
      { type: 'data', bytes: 5_000 },
      { type: 'stop', now: T0 + 4_000 },
      { type: 'finalised' },
      { type: 'discard' },
    ]);

    expect(model).toEqual(INITIAL_CAPTURE);
  });

  it('keeps the chosen target note across a fresh request', () => {
    const model = run([{ type: 'request', localId: 'cap-2', noteId: 'roof-repair' }]);
    expect(model.noteId).toBe('roof-repair');
  });
});
