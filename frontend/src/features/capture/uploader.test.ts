import { describe, expect, it, vi } from 'vitest';

import type { ChintanApi } from '@/api/endpoints.ts';
import { ApiError } from '@/api/problem.ts';
import type { CaptureCreatedWire } from '@/api/schema.ts';

import type { CaptureEvent } from './machine.ts';
import { uploadCapture, type UploadDeps, type UploadRequest } from './uploader.ts';

const REQUEST: UploadRequest = {
  localId: 'cap-1',
  contentType: 'audio/webm',
  durationMs: 12_000,
  noteId: null,
  peaks: [0.1, 0.5, 0.9],
};

function created(overrides: Partial<CaptureCreatedWire> = {}): CaptureCreatedWire {
  return {
    capture: {
      id: 'srv-1',
      status: 'uploaded',
      created_at: '2026-08-07T10:00:00.000Z',
      version: 1,
    },
    upload: {
      url: 'https://s3.test/audio',
      expires_at: '2026-08-07T10:15:00.000Z',
      max_bytes: 1_000_000,
    },
    peaks_upload: {
      url: 'https://s3.test/peaks',
      expires_at: '2026-08-07T10:15:00.000Z',
    },
    ...overrides,
  };
}

interface Harness {
  api: ChintanApi;
  deps: UploadDeps;
  events: CaptureEvent[];
  puts: string[];
  confirmed: string[];
}

function harness(options: {
  createCapture?: () => Promise<CaptureCreatedWire>;
  put?: UploadDeps['put'];
  assemble?: UploadDeps['assemble'];
} = {}): Harness {
  const events: CaptureEvent[] = [];
  const puts: string[] = [];
  const confirmed: string[] = [];

  const api = {
    createCapture: vi.fn(options.createCapture ?? (async () => created())),
  } as unknown as ChintanApi;

  const deps: UploadDeps = {
    assemble: options.assemble ?? (async () => new Blob(['audio-bytes'])),
    put:
      options.put ??
      (async (upload) => {
        puts.push(upload.url);
      }),
    confirm: async (localId: string) => {
      confirmed.push(localId);
    },
    saveRecord: async () => {},
  };

  return { api, deps, events, puts, confirmed };
}

const emitInto = (events: CaptureEvent[]) => (event: CaptureEvent) => {
  events.push(event);
};

const kinds = (events: CaptureEvent[]) => events.map((event) => event.type);

describe('a successful upload', () => {
  it('creates, uploads audio, uploads peaks, then confirms — in that order', async () => {
    const h = harness();

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.puts).toEqual(['https://s3.test/audio', 'https://s3.test/peaks']);
    expect(h.confirmed).toEqual(['cap-1']);
    expect(kinds(h.events)).toContain('uploadDone');
    expect(kinds(h.events).indexOf('captureCreated')).toBeLessThan(
      kinds(h.events).indexOf('uploadDone'),
    );
  });

  it('keys the create with the recording local id, so a resume replays', async () => {
    const h = harness();

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.api.createCapture).toHaveBeenCalledWith(
      expect.objectContaining({ content_type: 'audio/webm', duration_ms: 12_000 }),
      'cap-1',
    );
  });

  it('reports the server capture id so the progress card can take over', async () => {
    const h = harness();

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.events).toContainEqual({ type: 'captureCreated', serverCaptureId: 'srv-1' });
  });

  it('skips peaks when the instance did not offer an upload for them', async () => {
    const h = harness({
      createCapture: async () => {
        const { peaks_upload: _omitted, ...withoutPeaks } = created();
        return withoutPeaks;
      },
    });

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.puts).toEqual(['https://s3.test/audio']);
    expect(kinds(h.events)).toContain('uploadDone');
  });
});

describe('the local audio is pruned only after the server has it', () => {
  it('does not confirm when the create fails', async () => {
    const h = harness({
      createCapture: async () => {
        throw new ApiError({ kind: 'network', status: 0, title: 'No connection' });
      },
    });

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.confirmed).toEqual([]);
    expect(h.events).toContainEqual(
      expect.objectContaining({ type: 'uploadFailed', recoverable: true }),
    );
  });

  it('does not confirm when the audio PUT fails', async () => {
    // The single failure the product cannot absorb: pruning before the server
    // has the bytes loses the recording outright.
    const h = harness({
      put: async () => {
        throw new Error('network reset');
      },
    });

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.confirmed).toEqual([]);
    const failure = h.events.find((event) => event.type === 'uploadFailed');
    expect(failure).toMatchObject({ recoverable: true });
    expect((failure as { message: string }).message).toMatch(/safe on this device/i);
  });

  it('still confirms when only the peaks upload fails', async () => {
    // A missing waveform renders a plain player. It is not worth failing an
    // upload that otherwise succeeded.
    const h = harness({
      put: async (upload) => {
        if (upload.url.endsWith('/peaks')) throw new Error('peaks rejected');
      },
    });

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.confirmed).toEqual(['cap-1']);
    expect(kinds(h.events)).toContain('uploadDone');
  });
});

describe('failure classification', () => {
  it('reports the spend cap as its own event, not a generic failure', async () => {
    const h = harness({
      createCapture: async () => {
        throw new ApiError({
          kind: 'http',
          status: 429,
          title: 'Daily spend cap reached',
          type: 'https://chintan.dev/problems/spend-cap',
        } as never);
      },
    });

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(kinds(h.events)).toContain('spendCapped');
    expect(kinds(h.events)).not.toContain('uploadFailed');
  });

  it('marks an unreadable local buffer as unrecoverable', async () => {
    const h = harness({
      assemble: async () => {
        throw new Error('IndexedDB gone');
      },
    });

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.events).toContainEqual(
      expect.objectContaining({ type: 'uploadFailed', recoverable: false }),
    );
  });

  it('refuses to upload an empty recording', async () => {
    const h = harness({ assemble: async () => new Blob([]) });

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(h.api.createCapture).not.toHaveBeenCalled();
    expect(h.events).toContainEqual(
      expect.objectContaining({ type: 'uploadFailed', recoverable: false }),
    );
  });
});
