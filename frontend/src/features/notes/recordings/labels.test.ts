import { describe, expect, it } from 'vitest';

import type { CaptureWire } from '@/api/schema.ts';

import { filedLabel, heardAs, justLanded, retranscribeLabel, sourceLabel } from './labels.ts';

/** A filed recording, as `Recordings.test.tsx` has it; the labels read only its status and source. */
const CAPTURE: CaptureWire = {
  id: 'cap-1',
  status: 'appended',
  created_at: '2026-08-06T09:10:00.000Z',
  version: 1,
  note_id: 'roof-repair',
  duration_ms: 12_000,
  has_peaks: false,
  has_segments: false,
};

describe('the language Whisper heard', () => {
  it('is decided by name against the effective language, and always said under auto-detect', () => {
    expect(heardAs('Tamil', 'ml')).toBe('Tamil');
    expect(heardAs('malayalam', 'ml')).toBeNull();
    expect(heardAs('English', 'en')).toBeNull();
    expect(heardAs('English', 'auto')).toBe('English');
    expect(heardAs(null, 'ml')).toBeNull();
    // A code the curated list lacks still compares by its Intl name.
    expect(heardAs('Icelandic', 'is')).toBeNull();
    expect(heardAs('Icelandic', 'en')).toBe('Icelandic');
  });
});

describe('transcribing a recording again', () => {
  it('is honest about auto-detect', () => {
    expect(retranscribeLabel('ta')).toBe('Transcribe again in Tamil');
    expect(retranscribeLabel('auto')).toBe('Transcribe again (auto-detect)');
  });
});

describe('a recording still being made into this note', () => {
  it('says nothing about a row that is filed, and names every other state', () => {
    expect(filedLabel(CAPTURE)).toBe('');
    expect(filedLabel({ ...CAPTURE, status: 'transcribing' })).toBe('Filing…');
    expect(filedLabel({ ...CAPTURE, status: 'needs_target' })).toBe('Needs a target');
    expect(filedLabel({ ...CAPTURE, status: 'failed' })).toBe('Failed');
  });

  it('knows a landing from a row that arrived already filed', () => {
    const moving: CaptureWire = { ...CAPTURE, status: 'cleaning' };
    expect(justLanded([moving], [{ ...moving, status: 'appended' }])?.id).toBe(CAPTURE.id);
    expect(justLanded([], [CAPTURE])).toBeUndefined();
    expect(justLanded([CAPTURE], [CAPTURE])).toBeUndefined();
  });
});

describe('a capture that a device sent', () => {
  it('is decided by the source prefix alone', () => {
    const names = new Map([['dev_1', 'Watch']]);
    expect(sourceLabel({ source: 'device:dev_1' }, names)).toBe('From Watch');
    expect(sourceLabel({ source: 'device:dev_2' }, names)).toBe('From a device');
    expect(sourceLabel({ source: 'app' }, names)).toBeNull();
    expect(sourceLabel({}, names)).toBeNull();
  });
});
