import { describe, expect, it, vi } from 'vitest';

import type { ChintanApi } from '@/api/endpoints.ts';
import { ApiError } from '@/api/problem.ts';
import type { CaptureCreatedWire, CaptureWire } from '@/api/schema.ts';

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
      // Relative, not a fixed date. A presign is a thirty-minute credential, so
      // a hardcoded timestamp stops describing "a fresh upload URL" the day
      // after it is written — and now that the uploader refuses to spend a
      // request on a dead credential, that difference decides the test.
      expires_at: new Date(Date.now() + 15 * 60_000).toISOString(),
      max_bytes: 1_000_000,
    },
    peaks_upload: {
      url: 'https://s3.test/peaks',
      expires_at: new Date(Date.now() + 15 * 60_000).toISOString(),
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
  createCapture?: (body?: unknown, key?: string) => Promise<CaptureCreatedWire>;
  getCapture?: (captureId: string) => Promise<CaptureWire>;
  put?: UploadDeps['put'];
  assemble?: UploadDeps['assemble'];
} = {}): Harness {
  const events: CaptureEvent[] = [];
  const puts: string[] = [];
  const confirmed: string[] = [];

  const api = {
    createCapture: vi.fn(options.createCapture ?? (async () => created())),
    getCapture: vi.fn(
      options.getCapture ??
        (async () => {
          throw new Error('getCapture was not stubbed for this case');
        }),
    ),
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

/**
 * Resuming a stranded recording, when the upload credential has died.
 *
 * Found in production. Three resume attempts for capture
 * `c_18c9ef2e5eebb202_a86c00b7a9cdcc42` went out with an identical
 * `X-Amz-Date=20260808T204149Z` and an identical `X-Amz-Signature`, against a
 * URL carrying `X-Amz-Expires=1800` — so it died at 21:11:49Z and the attempts
 * were made at 22:06Z. The object never landed and never could.
 *
 * The stale credential is not stored on this device: nothing in `StoredCapture`
 * holds a URL, and `uploadCapture` mints one by calling `POST /v1/captures`
 * every time. It comes back stale because that call is keyed with the
 * recording's local id and the server's idempotency layer **replays the stored
 * response body verbatim**, presigned URL included, for the life of the record
 * (`handler/idempotency.go:109-122`). The key that stops a resume creating a
 * second capture is the same key that pins it to a thirty-minute credential
 * minted hours earlier.
 *
 * The durable fix is the server re-minting on replay. Until then the client
 * must not send a request it can already tell will fail.
 */

const HOUR = 3_600_000;

function presign(url: string, expiresInMs: number): CaptureCreatedWire {
  return created({
    upload: {
      url,
      expires_at: new Date(Date.now() + expiresInMs).toISOString(),
      max_bytes: 1_000_000,
    },
    peaks_upload: {
      url: `${url}-peaks`,
      expires_at: new Date(Date.now() + expiresInMs).toISOString(),
    },
  });
}

describe('a resumed upload never replays a dead credential', () => {
  it('asks for a fresh presign instead of PUTting to the expired one', async () => {
    const keys: (string | undefined)[] = [];
    const h = harness({
      createCapture: async (_body?: unknown, key?: string) => {
        keys.push(key);
        // The server replays the original response for the recording's own id,
        // credential and all. Any other key does the work again.
        return key === 'cap-1'
          ? presign('https://s3.test/stale', -HOUR)
          : presign('https://s3.test/fresh', HOUR);
      },
      getCapture: async () => ({
        id: 'srv-1',
        status: 'uploaded' as const,
        created_at: '2026-08-07T10:00:00.000Z',
        version: 1,
      }),
    });

    await uploadCapture(
      h.api,
      { ...REQUEST, serverCaptureId: 'srv-1' },
      emitInto(h.events),
      h.deps,
    );

    // The assertion that matters: the bytes went to the URL that is alive.
    // "a PUT happened" would pass against the bug.
    expect(h.puts).toEqual(['https://s3.test/fresh', 'https://s3.test/fresh-peaks']);
    expect(h.puts).not.toContain('https://s3.test/stale');
    expect(keys[0]).toBe('cap-1');
    expect(keys[1], 'the second create reused the pinned key').not.toBe('cap-1');
    expect(h.confirmed).toEqual(['cap-1']);
    expect(kinds(h.events)).toContain('uploadDone');
  });

  it('reports the new capture id, so the progress card follows the right one', async () => {
    const h = harness({
      createCapture: async (_body?: unknown, key?: string) =>
        key === 'cap-1'
          ? presign('https://s3.test/stale', -HOUR)
          : { ...presign('https://s3.test/fresh', HOUR), capture: { ...created().capture, id: 'srv-2' } },
      getCapture: async () => ({
        id: 'srv-1',
        status: 'uploaded' as const,
        created_at: '2026-08-07T10:00:00.000Z',
        version: 1,
      }),
    });

    await uploadCapture(
      h.api,
      { ...REQUEST, serverCaptureId: 'srv-1' },
      emitInto(h.events),
      h.deps,
    );

    expect(h.events).toContainEqual({ type: 'captureCreated', serverCaptureId: 'srv-2' });
  });

  it('does not re-key a first attempt, so one recording is still one capture', async () => {
    const keys: (string | undefined)[] = [];
    const h = harness({
      createCapture: async (_body?: unknown, key?: string) => {
        keys.push(key);
        return presign('https://s3.test/audio', HOUR);
      },
    });

    await uploadCapture(h.api, REQUEST, emitInto(h.events), h.deps);

    expect(keys).toEqual(['cap-1']);
  });

  it('sends nothing at all when the audio already landed', async () => {
    // The bytes reached S3 and the pipeline moved on; only the local confirm
    // was lost. Uploading again appends the same dictation to the note twice.
    const h = harness({
      getCapture: async () => ({
        id: 'srv-1',
        status: 'appended' as const,
        created_at: '2026-08-07T10:00:00.000Z',
        version: 2,
      }),
    });

    await uploadCapture(
      h.api,
      { ...REQUEST, serverCaptureId: 'srv-1' },
      emitInto(h.events),
      h.deps,
    );

    expect(h.puts).toEqual([]);
    expect(h.api.createCapture).not.toHaveBeenCalled();
    expect(h.confirmed).toEqual(['cap-1']);
    expect(kinds(h.events)).toContain('uploadDone');
  });

  it('says why, rather than failing silently, when no live credential can be had', async () => {
    const h = harness({
      createCapture: async () => presign('https://s3.test/stale', -HOUR),
      getCapture: async () => ({
        id: 'srv-1',
        status: 'uploaded' as const,
        created_at: '2026-08-07T10:00:00.000Z',
        version: 1,
      }),
    });

    await uploadCapture(
      h.api,
      { ...REQUEST, serverCaptureId: 'srv-1' },
      emitInto(h.events),
      h.deps,
    );

    // Never attempted: expiry is knowable before the request.
    expect(h.puts).toEqual([]);
    const failure = h.events.find((event) => event.type === 'uploadFailed');
    expect(failure).toBeDefined();
    expect((failure as { message: string }).message).toMatch(/expired/i);
    expect((failure as { message: string }).message).toMatch(/still on this device/i);
    expect(h.confirmed).toEqual([]);
  });
});
