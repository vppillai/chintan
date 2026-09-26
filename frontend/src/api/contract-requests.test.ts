/**
 * The frontend↔backend contract check, request half.
 *
 * This file does not assert against a document. It drives the real `ChintanApi`
 * — the same object every screen calls — against a stub `fetch`, records the
 * exact method, path, query string and JSON body that came out, and writes them
 * to `__fixtures__/requests.json`.
 *
 * The Go side then replays that file through the real router
 * (`backend/internal/handler/contract_test.go`). `decodeJSON` there calls
 * `DisallowUnknownFields`, so a field renamed on this side and nowhere else
 * arrives as a 400 and fails the backend test. Nothing else in either codebase
 * checks that a request this app sends is one the API accepts.
 *
 * The identifiers below are fixed on purpose: the Go harness seeds exactly
 * these, so a replayed request reaches a real handler rather than stopping at a
 * 404 that would hide a shape problem behind it.
 */

import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import process from 'node:process';

import { describe, expect, it, vi } from 'vitest';

import { ApiClient } from './client.ts';
import { ChintanApi } from './endpoints.ts';
import { Session } from './session.ts';
import { createMemoryTokenStore, type TokenSet } from './tokens.ts';

const BASE_URL = 'https://contract.invalid';

/** Seeded by `seededContractHarness` in Go. Changing one means changing both. */
const NOTE_ID = 'contract-note';
const NOTE_ID_2 = 'contract-note-2';
const ARCHIVED_NOTE_ID = 'contract-archived-note';
const CAPTURE_ID = 'contract-capture';
const DEVICE_ID = 'contract-device';
const ASK_ID = 'contract-ask';
/** Not seeded: the Go replay accepts a 404 for an id it does not hold, and the route is what is checked. */
const EXPORT_ID = 'contract-export';

/**
 * Stands in for a continuation token.
 *
 * A cursor is opaque and the backend validates it — it carries the partition it
 * was issued for — so this side cannot invent one that would be accepted. The Go
 * replay substitutes a cursor issued by the same collection. Paging is still
 * exercised for real; only the token's bytes come from the other side.
 */
const CURSOR_PLACEHOLDER = '__CONTRACT_CURSOR__';

const MUTATING = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

/**
 * Methods of `ChintanApi` this recorder deliberately leaves out, each with its
 * reason. The guard at the end fails on a method that is neither here nor
 * called above: the devices and pins routes shipped with no recording, so the
 * Go replay never exercised them, and nothing said so until a review did.
 */
const ALLOWED_UNRECORDED = new Map<string, string>();

/** One recorded call, exactly as it is written to disk. */
interface RecordedRequest {
  name: string;
  method: string;
  /** Path and query string, as the backend's router sees it. */
  path: string;
  body?: unknown;
}

function fixedTokens(): TokenSet {
  return {
    idToken: 'id-token',
    accessToken: 'access-token',
    refreshToken: 'refresh-token',
    expiresAt: Date.now() + 3_600_000,
    tokenType: 'Bearer',
  };
}

describe('the requests the frontend actually sends', () => {
  it('are recorded from the real ApiClient for the backend to replay', async () => {
    const recorded: RecordedRequest[] = [];
    const idempotencyKeys: (string | null)[] = [];
    let label = 'unnamed';

    const fetchImpl: typeof fetch = async (input, init) => {
      const url = new URL(String(input));
      const raw = init?.body;
      const method = init?.method ?? 'GET';
      idempotencyKeys.push(new Headers(init?.headers).get('Idempotency-Key'));
      recorded.push({
        name: label,
        method,
        path: `${url.pathname}${url.search}`,
        ...(typeof raw === 'string' ? { body: JSON.parse(raw) as unknown } : {}),
      });
      // 204 everywhere: this half of the contract is about what goes out. What
      // comes back is pinned by responses.ts and contract.test.ts.
      return new Response(null, { status: 204 });
    };

    const session = new Session(createMemoryTokenStore(fixedTokens()), {
      refresh: async (current) => current,
    });
    // Every method is spied so the guard at the end can say which were never
    // called, by the method itself rather than by the label it was recorded under.
    const spies = new Map(
      Object.getOwnPropertyNames(ChintanApi.prototype)
        .filter((method) => method !== 'constructor')
        .map((method) => [method, vi.spyOn(ChintanApi.prototype, method as keyof ChintanApi)]),
    );
    const api = new ChintanApi(new ApiClient(session, BASE_URL, fetchImpl));

    const call = async (name: string, run: () => Promise<unknown>): Promise<void> => {
      label = name;
      await run();
    };

    /* ---- health ------------------------------------------------------- */
    await call('health', () => api.health());
    await call('ready', () => api.ready());

    /* ---- settings ----------------------------------------------------- */
    await call('getSettings', () => api.getSettings());
    await call('putSettings', () =>
      api.putSettings({
        cleanup_mode: 'polished',
        retention_days: 30,
        theme: 'nocturne',
        default_language: 'ml',
        daily_spend_cap_micros: 500_000,
      }),
    );

    /* ---- usage -------------------------------------------------------- */
    await call('getUsage', () => api.getUsage());
    await call('getUsageMonth', () => api.getUsage('2026-01'));

    /* ---- ask ---------------------------------------------------------- */
    await call('ask', () =>
      api.ask(
        {
          question: 'what did I decide about the roof?',
          history: [{ question: 'what leaks?', answer: 'The gutter on the south side.' }],
        },
        'ask-local-1',
      ),
    );
    await call('getAsk', () => api.getAsk(ASK_ID));

    /* ---- notes -------------------------------------------------------- */
    await call('listNotes', () => api.listNotes());
    await call('listNotesFiltered', () =>
      api.listNotes({ state: 'archived', tag: 'house', limit: 25 }),
    );
    await call('listNotesPaged', () => api.listNotes({ cursor: CURSOR_PLACEHOLDER, limit: 200 }));
    await call('listNotesCorpus', () => api.listNotes({ include: 'search_text', limit: 200 }));
    await call('getNote', () => api.getNote(NOTE_ID));
    await call('createNote', () =>
      api.createNote({
        title: 'Kitchen rebuild',
        body: 'Quotes are in.',
        aliases: ['kitchen'],
        tags: ['house', 'money'],
      }),
    );
    await call('updateNote', () =>
      api.updateNote(NOTE_ID, {
        version: 1,
        title: 'Kitchen rebuild',
        body: 'The tiler can start on the fourteenth.',
        aliases: ['kitchen', 'reno'],
        tags: ['house'],
        verbatim: true,
        language: 'ml',
        auto_clean: true,
        cleaned_mode: 'structured',
      }),
    );
    // The pin-only PATCH is its own shape — `pinned` beside `version` and
    // nothing else — and the drag's reorder names every pinned note.
    await call('pinNote', () => api.updateNote(NOTE_ID, { version: 1, pinned: true }));
    await call('reorderPins', () => api.reorderPins({ ids: [NOTE_ID_2, NOTE_ID] }));
    await call('cleanNote', () => api.cleanNote(NOTE_ID));
    await call('cleanNoteMode', () => api.cleanNote(NOTE_ID, { mode: 'polished' }));
    await call('archiveNote', () => api.archiveNote(NOTE_ID));
    await call('restoreNote', () => api.restoreNote(ARCHIVED_NOTE_ID));
    await call('deleteNoteForever', () => api.deleteNoteForever(ARCHIVED_NOTE_ID));
    await call('purgeNotesBatch', () => api.purgeNotesBatch([ARCHIVED_NOTE_ID]));
    await call('recordingUrls', () => api.recordingUrls(NOTE_ID));
    await call('listTags', () => api.listTags());

    /* ---- search ------------------------------------------------------- */
    await call('search', () => api.search('tiler'));
    await call('searchPaged', () => api.search('tiler', { cursor: CURSOR_PLACEHOLDER, limit: 20 }));

    /* ---- captures ----------------------------------------------------- */
    // Every value of the status filter, because an enum member the backend
    // does not parse is a 400 and the progress card is the caller.
    for (const status of ['pending', 'failed', 'needs_target', 'all'] as const) {
      await call(`listCaptures_${status}`, () => api.listCaptures({ status }));
    }
    await call('listCapturesPaged', () =>
      api.listCaptures({ cursor: CURSOR_PLACEHOLDER, limit: 10 }),
    );
    await call('createCapture', () =>
      api.createCapture(
        {
          content_type: 'audio/webm',
          note_id: NOTE_ID,
          duration_ms: 12_000,
          size_bytes: 1_048_576,
        },
        'capture-local-1',
      ),
    );
    await call('createCaptureUnrouted', () =>
      api.createCapture({ content_type: 'audio/mp4' }, 'capture-local-2'),
    );
    await call('getCapture', () => api.getCapture(CAPTURE_ID));
    await call('setCaptureTargetExisting', () =>
      api.setCaptureTarget(CAPTURE_ID, { note_id: NOTE_ID }),
    );
    await call('setCaptureTargetNew', () =>
      api.setCaptureTarget(CAPTURE_ID, { new_note_title: 'A brand new note' }),
    );
    await call('retryCapture', () => api.retryCapture(CAPTURE_ID));
    await call('moveCapture', () => api.moveCapture(CAPTURE_ID, { note_id: NOTE_ID }));
    await call('moveCaptureToNewNote', () =>
      api.moveCapture(CAPTURE_ID, { new_note_title: 'A brand new note' }),
    );
    await call('deleteCapture', () => api.deleteCapture(CAPTURE_ID));
    // Every artifact kind, because `kind` is a query enum the backend parses.
    for (const kind of ['audio', 'raw', 'clean', 'segments', 'peaks'] as const) {
      await call(`downloadUrl_${kind}`, () => api.downloadUrl(CAPTURE_ID, kind));
    }
    // Both bodies the app sends: a language, and `{}` for the note's own.
    await call('retranscribeCapture', () =>
      api.retranscribeCapture(CAPTURE_ID, { language: 'ml' }),
    );
    await call('retranscribeCaptureDefault', () => api.retranscribeCapture(CAPTURE_ID));

    /* ---- export ------------------------------------------------------- */
    await call('startExport', () => api.startExport('export-local-1'));
    await call('getExport', () => api.getExport(EXPORT_ID));

    /* ---- devices ------------------------------------------------------ */
    await call('createDevice', () => api.createDevice({ name: 'Contract device' }));
    await call('listDevices', () => api.listDevices());
    await call('deleteDevice', () => api.deleteDevice(DEVICE_ID));

    /* ---- what the recording itself has to be true of ------------------- */

    // Every method the app can call is recorded above, or named in
    // ALLOWED_UNRECORDED with a reason. Otherwise the Go replay is silent about
    // a route the app sends to, as it was for devices and pins.
    const unrecorded = [...spies]
      .filter(([method, spy]) => spy.mock.calls.length === 0 && !ALLOWED_UNRECORDED.has(method))
      .map(([method]) => method);
    expect(unrecorded, 'ChintanApi methods this recorder never calls').toEqual([]);

    // A duplicate name would silently overwrite a Go subtest and hide whichever
    // call lost.
    expect(new Set(recorded.map((r) => r.name)).size).toBe(recorded.length);

    // `undefined` reaching a URL is the classic template-literal bug, and it
    // produces a path the backend answers 404 for.
    for (const request of recorded) {
      expect(request.path, `${request.name} built a path containing "undefined"`).not.toContain(
        'undefined',
      );
      expect(request.path.startsWith('/v1/')).toBe(true);
    }

    // Every mutating request must carry an idempotency key: the client retries
    // them, and a retry without one is a second note.
    recorded.forEach((request, index) => {
      if (!MUTATING.has(request.method)) return;
      const key = idempotencyKeys[index];
      expect(key, `${request.name} sent no Idempotency-Key`).toBeTruthy();
    });

    // Resolved from the Vitest root rather than from import.meta.url, which the
    // transform rewrites to something that is not a file: URL.
    const apiDir = join(process.cwd(), 'src', 'api');
    expect(
      existsSync(apiDir),
      `cannot find ${apiDir}; run vitest from the frontend/ directory`,
    ).toBe(true);

    const outputDir = join(apiDir, '__fixtures__');
    mkdirSync(outputDir, { recursive: true });
    writeFileSync(
      join(outputDir, 'requests.json'),
      `${JSON.stringify(recorded, null, 2)}\n`,
      'utf8',
    );
  });
});
