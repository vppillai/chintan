import { test as base, type Page, type Route } from '@playwright/test';

/**
 * A stub of the v2 API, driven by route interception.
 *
 * Every response here matches `docs/api/openapi.yaml` — envelopes, problem+json,
 * the capture lifecycle. That is the point of the specs: they assert the
 * frontend honours the contract, and they stay runnable with no AWS.
 */

export interface ApiState {
  notes: Record<string, NoteRecord>;
  captures: CaptureRecord[];
  /** Every request the app made, for asserting on headers and method. */
  requests: { method: string; url: string; headers: Record<string, string> }[];
  /** When true, every API call fails as if the device were offline. */
  offline: boolean;
  /** Forces the next PATCH to return 409 once. */
  conflictOnce: boolean;
}

interface NoteRecord {
  id: string;
  title: string;
  body: string;
  snippet?: string;
  tags?: string[];
  aliases?: string[];
  updated_at: string;
  version: number;
  archived: boolean;
  captures?: CaptureRecord[];
}

interface CaptureRecord {
  id: string;
  status: string;
  created_at: string;
  version: number;
  note_id?: string | null;
  error?: string | null;
  duration_ms?: number;
  has_peaks?: boolean;
  has_segments?: boolean;
}

export function freshState(): ApiState {
  return {
    notes: {
      'roof-repair': {
        id: 'roof-repair',
        title: 'Roof repair',
        body: 'Ridge tiles on the south slope have slipped.',
        snippet: 'Ridge tiles on the south slope have slipped.',
        tags: ['house'],
        aliases: [],
        updated_at: '2026-08-06T09:14:00.000Z',
        version: 3,
        archived: false,
        captures: [
          {
            id: 'cap-old',
            status: 'appended',
            created_at: '2026-08-06T09:10:00.000Z',
            version: 1,
            note_id: 'roof-repair',
            duration_ms: 12_000,
            has_peaks: true,
            has_segments: true,
          },
        ],
      },
      'reading-list': {
        id: 'reading-list',
        title: 'Reading list',
        body: 'Seeing Like a State.',
        snippet: 'Seeing Like a State.',
        tags: ['books'],
        aliases: [],
        updated_at: '2026-08-04T18:02:00.000Z',
        version: 1,
        archived: false,
        captures: [],
      },
    },
    captures: [],
    requests: [],
    offline: false,
    conflictOnce: false,
  };
}

const PEAKS = {
  version: 1,
  buckets: 40,
  duration_ms: 12_000,
  peaks: Array.from({ length: 40 }, (_, index) => Math.abs(Math.sin(index / 4))),
};

const SEGMENTS = {
  segments: [
    { id: 0, start: 0, end: 4, text: 'Ridge tiles on the south slope have slipped.' },
    { id: 1, start: 4, end: 8, text: 'Get two quotes before the autumn rain.' },
    { id: 2, start: 8, end: 12, text: 'Ellis quoted nine hundred.' },
  ],
};

/**
 * A thirteen-second silent WAV.
 *
 * Long enough that seeking to the third transcript segment (8s) is a real seek
 * rather than a clamp against the end of the file — a one-second fixture makes
 * every seek test pass or fail for the wrong reason.
 */
function silentWav(): Buffer {
  const sampleRate = 8_000;
  const samples = sampleRate * 13;
  const buffer = Buffer.alloc(44 + samples * 2);
  buffer.write('RIFF', 0);
  buffer.writeUInt32LE(36 + samples * 2, 4);
  buffer.write('WAVE', 8);
  buffer.write('fmt ', 12);
  buffer.writeUInt32LE(16, 16);
  buffer.writeUInt16LE(1, 20);
  buffer.writeUInt16LE(1, 22);
  buffer.writeUInt32LE(sampleRate, 24);
  buffer.writeUInt32LE(sampleRate * 2, 28);
  buffer.writeUInt16LE(2, 32);
  buffer.writeUInt16LE(16, 34);
  buffer.write('data', 36);
  buffer.writeUInt32LE(samples * 2, 40);
  return buffer;
}

function json(route: Route, body: unknown, status = 200): Promise<void> {
  return route.fulfill({
    status,
    contentType: 'application/json',
    headers: { 'X-Correlation-Id': 'e2e-correlation' },
    body: JSON.stringify(body),
  });
}

function problem(route: Route, status: number, extra: Record<string, unknown> = {}) {
  return route.fulfill({
    status,
    contentType: 'application/problem+json',
    headers: { 'X-Correlation-Id': 'e2e-correlation' },
    body: JSON.stringify({
      type: 'about:blank',
      title: 'Request failed',
      status,
      correlation_id: 'e2e-correlation',
      ...extra,
    }),
  });
}

export async function installApi(page: Page, state: ApiState): Promise<void> {
  // A token set the client accepts, so no sign-in flow is needed.
  await page.addInitScript(() => {
    localStorage.setItem(
      'chintan.tokens.v2',
      JSON.stringify({
        idToken: 'e2e-id-token',
        accessToken: 'e2e-access-token',
        refreshToken: 'e2e-refresh-token',
        expiresAt: Date.now() + 3_600_000,
        tokenType: 'Bearer',
      }),
    );
  });

  await page.route('**/api/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api/, '');
    const method = request.method();

    state.requests.push({
      method,
      url: `${path}${url.search}`,
      headers: request.headers(),
    });

    if (state.offline) {
      await route.abort('internetdisconnected');
      return;
    }

    // ---- Captures -------------------------------------------------------
    if (path === '/v1/captures' && method === 'POST') {
      const id = `cap-${state.captures.length + 1}`;
      state.captures.push({
        id,
        status: 'uploaded',
        created_at: new Date().toISOString(),
        version: 1,
        note_id: null,
      });
      await json(
        route,
        {
          capture: state.captures.at(-1),
          upload: {
            url: `${url.origin}/upload/${id}`,
            expires_at: new Date(Date.now() + 900_000).toISOString(),
            max_bytes: 33_554_432,
          },
          peaks_upload: {
            url: `${url.origin}/upload/${id}-peaks`,
            expires_at: new Date(Date.now() + 900_000).toISOString(),
          },
        },
        201,
      );
      return;
    }

    if (path === '/v1/captures' && method === 'GET') {
      const status = url.searchParams.get('status');
      const items =
        status === 'pending'
          ? state.captures.filter((capture) => capture.status !== 'appended')
          : state.captures;
      await json(route, { items });
      return;
    }

    const retryMatch = /^\/v1\/captures\/([^/]+)\/retry$/.exec(path);
    if (retryMatch && method === 'POST') {
      const capture = state.captures.find((item) => item.id === retryMatch[1]);
      if (capture) {
        capture.status = 'transcribing';
        capture.error = null;
      }
      await json(route, capture, 202);
      return;
    }

    const downloadMatch = /^\/v1\/captures\/([^/]+)\/download$/.exec(path);
    if (downloadMatch) {
      const kind = url.searchParams.get('kind');
      await json(route, {
        url: `${url.origin}/artifact/${downloadMatch[1]}/${kind}`,
        expires_at: new Date(Date.now() + 900_000).toISOString(),
      });
      return;
    }

    // ---- Notes ----------------------------------------------------------
    if (path === '/v1/notes' && method === 'GET') {
      await json(route, {
        items: Object.values(state.notes).filter((note) => !note.archived),
      });
      return;
    }

    const noteMatch = /^\/v1\/notes\/([^/]+)$/.exec(path);
    if (noteMatch) {
      const note = state.notes[noteMatch[1] ?? ''];
      if (!note) {
        await problem(route, 404, { title: 'Not found' });
        return;
      }
      if (method === 'GET') {
        await json(route, note);
        return;
      }
      if (method === 'PATCH') {
        if (state.conflictOnce) {
          state.conflictOnce = false;
          note.version += 1;
          note.body = 'Ridge tiles slipped. Ellis quoted nine hundred.';
          await problem(route, 409, {
            title: 'Someone else changed this first',
            detail: 'This note was updated elsewhere.',
            current_version: note.version,
          });
          return;
        }
        const body = request.postDataJSON() as Record<string, unknown>;
        if (typeof body['title'] === 'string') note.title = body['title'];
        if (typeof body['body'] === 'string') note.body = body['body'];
        if (Array.isArray(body['tags'])) note.tags = body['tags'] as string[];
        note.version += 1;
        await json(route, note);
        return;
      }
    }

    // ---- Search ---------------------------------------------------------
    if (path === '/v1/search') {
      const q = (url.searchParams.get('q') ?? '').toLowerCase();
      const items = Object.values(state.notes)
        .filter((note) => `${note.title} ${note.body}`.toLowerCase().includes(q))
        .map((note) => ({
          note_id: note.id,
          title: note.title,
          excerpt: note.snippet ?? '',
          matched_in: ['title'],
        }));
      await json(route, { items });
      return;
    }

    // ---- Settings, tags, auth ------------------------------------------
    if (path === '/v1/settings') {
      await json(route, {
        cleanup_mode: 'faithful',
        retention_days: 0,
        theme: 'ink',
        daily_spend_cap_micros: 0,
      });
      return;
    }
    if (path === '/v1/auth/webauthn/status') {
      await json(route, { enrolled: false });
      return;
    }
    if (path === '/v1/tags') {
      await json(route, { items: [] });
      return;
    }

    await json(route, { items: [] });
  });

  // Presigned uploads and artifact downloads.
  await page.route('**/upload/**', async (route) => {
    if (state.offline) {
      await route.abort('internetdisconnected');
      return;
    }
    await route.fulfill({ status: 200, body: '' });
  });

  await page.route('**/artifact/**', async (route) => {
    const url = route.request().url();
    if (url.endsWith('/peaks')) {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(PEAKS),
      });
      return;
    }
    if (url.endsWith('/segments')) {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(SEGMENTS),
      });
      return;
    }
    /*
     * Byte ranges are not optional here. A media element cannot seek in a
     * resource the server will not serve ranges for, so a plain 200 makes
     * `currentTime = 8` silently do nothing — which is exactly what S3 would
     * never do, and would make the seek specs test the stub rather than the app.
     */
    const audio = silentWav();
    const range = route.request().headers()['range'];
    const match = /bytes=(\d+)-(\d*)/.exec(range ?? '');

    if (match) {
      const start = Number(match[1]);
      const end = match[2] ? Number(match[2]) : audio.length - 1;
      const slice = audio.subarray(start, end + 1);
      await route.fulfill({
        status: 206,
        contentType: 'audio/wav',
        headers: {
          'Accept-Ranges': 'bytes',
          'Content-Range': `bytes ${start}-${end}/${audio.length}`,
          'Content-Length': String(slice.length),
        },
        body: slice,
      });
      return;
    }

    await route.fulfill({
      status: 200,
      contentType: 'audio/wav',
      headers: {
        'Accept-Ranges': 'bytes',
        'Content-Length': String(audio.length),
      },
      body: audio,
    });
  });
}

/**
 * `auto: true` matters: Playwright instantiates fixtures lazily, so a test that
 * destructures only `{ page }` would otherwise run with no API stub and no
 * token — which looks exactly like a broken app.
 */
export const test = base.extend<{ api: ApiState }>({
  api: [
    async ({ page }, use) => {
      const state = freshState();
      await installApi(page, state);
      await use(state);
    },
    { auto: true },
  ],
});

export { expect } from '@playwright/test';
