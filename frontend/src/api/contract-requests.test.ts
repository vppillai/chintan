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

import { describe, expect, it } from 'vitest';

import { ApiClient } from './client.ts';
import { ChintanApi } from './endpoints.ts';
import { Session } from './session.ts';
import { createMemoryTokenStore, type TokenSet } from './tokens.ts';

const BASE_URL = 'https://contract.invalid';

/** Seeded by `seededContractHarness` in Go. Changing one means changing both. */
const NOTE_ID = 'contract-note';
const ARCHIVED_NOTE_ID = 'contract-archived-note';
const CAPTURE_ID = 'contract-capture';

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
      }),
    );
    await call('archiveNote', () => api.archiveNote(NOTE_ID));
    await call('restoreNote', () => api.restoreNote(ARCHIVED_NOTE_ID));
    await call('deleteNoteForever', () => api.deleteNoteForever(ARCHIVED_NOTE_ID));
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
    // Every artifact kind, because `kind` is a query enum the backend parses.
    for (const kind of ['audio', 'raw', 'clean', 'segments', 'peaks'] as const) {
      await call(`downloadUrl_${kind}`, () => api.downloadUrl(CAPTURE_ID, kind));
    }

    /* ---- what the recording itself has to be true of ------------------- */

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
