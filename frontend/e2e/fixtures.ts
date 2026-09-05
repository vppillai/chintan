import { test as base, type Page, type Route } from '@playwright/test';

/**
 * A stub of the Chintan API, driven by route interception.
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
  /**
   * Makes every PATCH fail on its merits, as a validation error does. Set to
   * exercise a queued edit the server will never accept, however often it is
   * replayed.
   */
  rejectPatch: { status: number; detail: string } | null;
  /** Ids passed to DELETE /v1/notes/{id}/permanent, in order. */
  purged: string[];
  /** Ids passed to DELETE /v1/captures/{id}, in order. */
  deletedCaptures: string[];
  /** The hosted UI, as exercised by the sign-in and sign-out specs. */
  auth: AuthState;
  /** What `PUT /v1/settings` last stored, as `GET` returns it. */
  settings: Record<string, unknown>;
}

/**
 * What the stubbed Cognito hosted UI saw.
 *
 * Recorded rather than merely answered, because the interesting assertions are
 * about the *request*: that a PKCE challenge was sent, that the verifier came
 * back on the exchange, that the logout named the client.
 */
export interface AuthState {
  authorize: Record<string, string>[];
  token: Record<string, string>[];
  logout: Record<string, string>[];
  /** Set to refuse the exchange, standing in for an expired or replayed code. */
  rejectExchange: boolean;
  /** Set to send the user back with `error=access_denied` instead of a code. */
  denyLogin: boolean;
  /** What `/passkeys/add` was asked with, per visit. */
  passkeyAdd: Record<string, string>[];
  /**
   * Whether the managed login still holds its own session. It does not for a
   * user who has been refreshing tokens for days, and then `/passkeys/add`
   * bounces straight back with `result=invalid_session`.
   */
  passkeySession: boolean;
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
  /** RFC3339, set by the server when a note is archived. */
  purge_after?: string | null;
  /** `auto`, an ISO-639-1 code, or absent to inherit `default_language`. */
  language?: string;
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
  /** What the router proposed. Exactly one of the two is ever set. */
  suggested_note_id?: string | null;
  suggested_title?: string | null;
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
      /*
       * Already archived, so the archive list has something in it before this
       * session archives anything. Invisible to every other spec: the notes
       * endpoint filters on `state`, which defaults to active.
       */
      'old-fence': {
        id: 'old-fence',
        title: 'Old fence',
        body: 'Replaced in spring.',
        snippet: 'Replaced in spring.',
        tags: [],
        aliases: [],
        updated_at: '2026-07-02T11:00:00.000Z',
        version: 2,
        archived: true,
        // Relative, not a literal date: the archive spec asserts this row says
        // "deletes in …", which is only true while the date is in the future.
        // The literal this used to be (2026-08-18) expired and took CI with it.
        purge_after: new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString(),
        captures: [],
      },
      /*
       * Archived with no purge date. `purge_after` is optional in the contract
       * and a note archived before retention was configured has none; this is
       * the row that renders "Deletes in NaN days" if the absence is divided by.
       */
      'stray-thought': {
        id: 'stray-thought',
        title: 'Stray thought',
        body: 'Nothing came of it.',
        snippet: 'Nothing came of it.',
        tags: [],
        aliases: [],
        updated_at: '2026-06-11T08:30:00.000Z',
        version: 1,
        archived: true,
        captures: [],
      },
    },
    captures: [],
    requests: [],
    settings: {
      cleanup_mode: 'faithful',
      retention_days: 0,
      theme: 'ink',
      default_language: 'en',
      daily_spend_cap_micros: 0,
    },
    offline: false,
    conflictOnce: false,
    rejectPatch: null,
    purged: [],
    deletedCaptures: [],
    auth: {
      authorize: [],
      token: [],
      logout: [],
      rejectExchange: false,
      denyLogin: false,
      passkeyAdd: [],
      passkeySession: true,
    },
  };
}

const PEAKS = {
  version: 1,
  buckets: 40,
  duration_ms: 12_000,
  peaks: Array.from({ length: 40 }, (_, index) => Math.abs(Math.sin(index / 4))),
};

/** The shape the worker writes: milliseconds, with a word list the app ignores. */
const SEGMENTS = {
  version: 1,
  language: 'English',
  duration_ms: 12_000,
  segments: [
    { start_ms: 0, end_ms: 4_000, text: ' Ridge tiles on the south slope have slipped.' },
    { start_ms: 4_000, end_ms: 8_000, text: ' Get two quotes before the autumn rain.' },
    { start_ms: 8_000, end_ms: 12_000, text: ' Ellis quoted nine hundred.' },
  ],
  words: [{ start_ms: 0, end_ms: 400, text: 'Ridge' }],
};

/** Must match `VITE_COGNITO_DOMAIN` in `playwright.config.ts`. */
export const COGNITO_ORIGIN = 'https://cognito.e2e.test';

/**
 * Where the presigned artifact URLs point: another origin, as the S3 bucket is.
 *
 * They used to be served from the app's own origin, which hid the one
 * behaviour that matters about them — the `<audio>` element and the download
 * `fetch` are cross-origin requests, and whether the second succeeds depends
 * on how the first was made. The stub answers like a bucket with a CORS rule:
 * `Access-Control-Allow-Origin` only when the request carried an `Origin`.
 */
export const ARTIFACT_ORIGIN = 'https://artifacts.e2e.test';

/** What cleanup produced, and what became the note body. */
const CLEANED =
  'Ridge tiles on the south slope have slipped.\n\nGet two quotes before the autumn rain; Ellis quoted nine hundred.';

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

/**
 * A redirect the stub can make in every engine.
 *
 * Cognito answers `/oauth2/authorize` and `/logout` with a 302, and that is
 * what this fulfilled — until the WebKit project: Playwright can only fulfil a
 * redirect status in Chromium ("Cannot fulfill with redirect status: 302").
 * A 200 carrying a zero-second meta refresh is the same navigation as far as
 * the app can tell — the document is destroyed and rebuilt on the callback URL
 * — and it works in all three engines.
 */
function redirect(route: Route, location: string): Promise<void> {
  // `location.replace`, so the stub page leaves no history entry — the same
  // shape a 302 has. The meta refresh is the fallback for a document whose
  // script never runs.
  const target = JSON.stringify(location);
  const attribute = location.replace(/"/g, '&quot;');
  return route.fulfill({
    status: 200,
    contentType: 'text/html',
    body:
      `<!doctype html><title>Redirecting</title>` +
      `<meta http-equiv="refresh" content="0;url=${attribute}">` +
      `<script>location.replace(${target});</script>`,
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

/** Statuses the pipeline will not move on from; a capture in one may be deleted or moved. */
const TERMINAL = new Set(['appended', 'needs_target', 'no_content', 'failed', 'spend_capped']);

function findCapture(
  state: ApiState,
  captureId: string,
): { note: NoteRecord; capture: CaptureRecord } | null {
  for (const note of Object.values(state.notes)) {
    const capture = (note.captures ?? []).find((item) => item.id === captureId);
    if (capture) return { note, capture };
  }
  return null;
}

/**
 * Cuts a capture out of its note: the row, and the paragraph at the capture's
 * chronological position. Returns the paragraph so a move can carry it.
 */
function removeCapture(note: NoteRecord, capture: CaptureRecord): string {
  const ordered = [...(note.captures ?? [])].sort((a, b) => a.created_at.localeCompare(b.created_at));
  const index = ordered.findIndex((item) => item.id === capture.id);
  const paragraphs = note.body.split('\n\n');
  const [paragraph = ''] = index >= 0 && index < paragraphs.length ? paragraphs.splice(index, 1) : [];
  note.body = paragraphs.join('\n\n');
  note.snippet = note.body.split('\n')[0] ?? '';
  note.captures = (note.captures ?? []).filter((item) => item.id !== capture.id);
  note.version += 1;
  return paragraph;
}

export async function installApi(page: Page, state: ApiState): Promise<void> {
  /*
   * A token set the client accepts, so no sign-in flow is needed.
   *
   * Seeded ONCE per tab, not on every document. An init script runs on every
   * navigation, so an unconditional seed re-authenticated the app the moment
   * anything navigated — which made sign-out untestable and, worse, made it
   * look like it worked: the session was cleared, the redirect went to the
   * hosted UI, and the app came back signed in because the fixture had just put
   * the token back.
   */
  await page.addInitScript(() => {
    if (sessionStorage.getItem('e2e.auth.seeded')) return;
    sessionStorage.setItem('e2e.auth.seeded', '1');
    /*
     * The library's passkey nudge is answered up front, so it appears in the
     * one spec about it (`passkey.spec.ts`, which clears this) and in none of
     * the others, whose layouts, focus order and screenshots predate it.
     */
    localStorage.setItem('chintan.passkey.nudge.v1', 'not-now');
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
      // `note_id` is the target the client chose, as the real endpoint stores it.
      const body = (request.postDataJSON() ?? {}) as { note_id?: string | null };
      const created: CaptureRecord = {
        id,
        status: 'uploaded',
        created_at: new Date().toISOString(),
        version: 1,
        note_id: body.note_id ?? null,
      };
      state.captures.push(created);
      // A capture aimed at a note is one of that note's recordings from the
      // moment it exists, as the real server lists it. The same object, so a
      // spec that moves `api.captures[0].status` along moves the note's row.
      const target = body.note_id ? state.notes[body.note_id] : undefined;
      if (target) target.captures = [...(target.captures ?? []), created];
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
        url: `${ARTIFACT_ORIGIN}/artifact/${downloadMatch[1]}/${kind}`,
        expires_at: new Date(Date.now() + 900_000).toISOString(),
      });
      return;
    }

    /*
     * One recording out of its note, or into another. Both cut the capture's
     * paragraph out of the body along the append marker the real server keeps;
     * the stub has no markers, so it drops the paragraph whose position matches
     * the capture's position among the note's recordings — the shape the app
     * sees is the same: a shorter body, a bumped version, one row fewer.
     */
    const captureMatch = /^\/v1\/captures\/([^/]+)$/.exec(path);
    if (captureMatch && method === 'DELETE') {
      const found = findCapture(state, captureMatch[1] ?? '');
      if (!found) {
        await problem(route, 404, { title: 'Not found' });
        return;
      }
      if (!TERMINAL.has(found.capture.status)) {
        await problem(route, 409, {
          title: 'Still filing',
          detail: 'This recording is still moving through the pipeline.',
        });
        return;
      }
      removeCapture(found.note, found.capture);
      state.deletedCaptures.push(found.capture.id);
      await route.fulfill({ status: 204, body: '' });
      return;
    }

    const moveMatch = /^\/v1\/captures\/([^/]+)\/move$/.exec(path);
    if (moveMatch && method === 'POST') {
      const found = findCapture(state, moveMatch[1] ?? '');
      const body = (request.postDataJSON() ?? {}) as { note_id?: string };
      const target = state.notes[body.note_id ?? ''];
      if (!found || !target) {
        await problem(route, 404, { title: 'Not found' });
        return;
      }
      if (target.archived) {
        await problem(route, 409, { title: 'Conflict', detail: 'That note is archived.' });
        return;
      }
      if (!TERMINAL.has(found.capture.status)) {
        await problem(route, 409, {
          title: 'Still filing',
          detail: 'This recording is still moving through the pipeline.',
        });
        return;
      }
      if (found.note.id === target.id) {
        await route.fulfill({ status: 204, body: '' });
        return;
      }
      const paragraph = removeCapture(found.note, found.capture);
      found.capture.note_id = target.id;
      target.captures = [...(target.captures ?? []), found.capture].sort((a, b) =>
        a.created_at.localeCompare(b.created_at),
      );
      target.body = [target.body, paragraph].filter(Boolean).join('\n\n');
      target.snippet = target.body.split('\n')[0] ?? '';
      target.version += 1;
      await json(route, found.capture);
      return;
    }

    /*
     * The bulk-download manifest: every recording of the note that still has
     * audio, oldest first, named `<slug>-<yyyymmdd-hhmm>.<ext>`.
     */
    const urlsMatch = /^\/v1\/notes\/([^/]+)\/recordings\/urls$/.exec(path);
    if (urlsMatch && method === 'GET') {
      const note = state.notes[urlsMatch[1] ?? ''];
      if (!note) {
        await problem(route, 404, { title: 'Not found' });
        return;
      }
      const slug = note.title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
      const items = [...(note.captures ?? [])]
        .filter((capture) => capture.status === 'appended')
        .sort((a, b) => a.created_at.localeCompare(b.created_at))
        .map((capture) => ({
          capture_id: capture.id,
          filename: `${slug}-${capture.created_at.replace(/[-:]/g, '').slice(0, 8)}-${capture.created_at.replace(/[-:]/g, '').slice(9, 13)}.wav`,
          url: `${ARTIFACT_ORIGIN}/artifact/${capture.id}/audio`,
          expires_at: new Date(Date.now() + 900_000).toISOString(),
        }));
      await json(route, { items });
      return;
    }

    // ---- Notes ----------------------------------------------------------
    if (path === '/v1/notes' && method === 'GET') {
      // `state` defaults to active, exactly as `openapi.yaml` declares it.
      const wanted = url.searchParams.get('state') ?? 'active';
      const tag = url.searchParams.get('tag');
      // `include=search_text` adds the lowercased body the server searches.
      const corpus = url.searchParams.get('include') === 'search_text';
      await json(route, {
        items: Object.values(state.notes)
          .filter((note) => (wanted === 'archived' ? note.archived : !note.archived))
          .filter((note) => !tag || (note.tags ?? []).includes(tag))
          .map((note) => (corpus ? { ...note, search_text: note.body.toLowerCase() } : note)),
      });
      return;
    }

    const restoreMatch = /^\/v1\/notes\/([^/]+)\/restore$/.exec(path);
    if (restoreMatch && method === 'POST') {
      const note = state.notes[restoreMatch[1] ?? ''];
      if (!note) {
        await problem(route, 404, { title: 'Not found' });
        return;
      }
      note.archived = false;
      note.purge_after = null;
      note.version += 1;
      await json(route, note);
      return;
    }

    /*
     * The batch purge — "empty the archive". One verdict per note, as the
     * contract says: an active note is refused rather than deleted, so a stale
     * listing cannot turn "clear my archive" into "delete my notes".
     */
    if (path === '/v1/notes/purge' && method === 'POST') {
      const body = request.postDataJSON() as { note_ids: string[] };
      const results = body.note_ids.map((id) => {
        const note = state.notes[id];
        if (!note) return { note_id: id, status: 'not_found' };
        if (!note.archived) {
          return { note_id: id, status: 'failed', detail: 'This note is not archived.' };
        }
        state.purged.push(id);
        delete state.notes[id];
        return { note_id: id, status: 'purged' };
      });
      await json(route, { results });
      return;
    }

    const purgeMatch = /^\/v1\/notes\/([^/]+)\/permanent$/.exec(path);
    if (purgeMatch && method === 'DELETE') {
      const id = purgeMatch[1] ?? '';
      if (!state.notes[id]) {
        await problem(route, 404, { title: 'Not found' });
        return;
      }
      state.purged.push(id);
      delete state.notes[id];
      await route.fulfill({ status: 204, body: '' });
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
      if (method === 'DELETE') {
        note.archived = true;
        // Thirty days, which is what `archiveNote`'s doc comment promises.
        note.purge_after = new Date(Date.now() + 30 * 86_400_000).toISOString();
        note.version += 1;
        await route.fulfill({ status: 204, body: '' });
        return;
      }
      if (method === 'PATCH') {
        if (state.rejectPatch) {
          await problem(route, state.rejectPatch.status, {
            title: 'That request was not valid',
            detail: state.rejectPatch.detail,
          });
          return;
        }
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
        if (typeof body['language'] === 'string') {
          // The empty string means "inherit again", which the wire spells as absence.
          if (body['language'] === '') delete note.language;
          else note.language = body['language'];
        }
        note.version += 1;
        await json(route, note);
        return;
      }
    }

    // ---- Search ---------------------------------------------------------
    if (path === '/v1/search') {
      const q = (url.searchParams.get('q') ?? '').toLowerCase();
      const items = Object.values(state.notes)
        .filter((note) => !note.archived)
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

    // ---- Usage ----------------------------------------------------------
    if (path === '/v1/usage') {
      // The current UTC month, so the You screen's chart marks a "today".
      const now = new Date();
      const month = `${String(now.getUTCFullYear())}-${String(now.getUTCMonth() + 1).padStart(2, '0')}`;
      const today = `${month}-${String(now.getUTCDate()).padStart(2, '0')}`;
      await json(route, {
        month,
        cost_micros: 40_791,
        calls: 118,
        audio_seconds: 1391.2,
        input_tokens: 84_210,
        output_tokens: 15_332,
        ops: {
          transcribe: { cost_micros: 20_230, calls: 50, audio_seconds: 1391.2 },
          route: { cost_micros: 9_497, calls: 18, input_tokens: 23_188, output_tokens: 2_201 },
          cleanup: { cost_micros: 11_842, calls: 50, input_tokens: 61_022, output_tokens: 13_131 },
        },
        days: [{ date: today, cost_micros: 40_791, calls: 118, audio_seconds: 1391.2 }],
      });
      return;
    }

    // ---- Settings and tags ---------------------------------------------
    if (path === '/v1/settings') {
      if (method === 'PUT') {
        // Returns what was stored, as the contract says; the cap is read-only.
        const body = (request.postDataJSON() ?? {}) as Record<string, unknown>;
        state.settings = {
          ...state.settings,
          ...body,
          daily_spend_cap_micros: state.settings['daily_spend_cap_micros'],
        };
      }
      await json(route, state.settings);
      return;
    }
    if (path === '/v1/tags') {
      // Derived from the active notes, as the real endpoint does.
      const counts = new Map<string, number>();
      for (const note of Object.values(state.notes)) {
        if (note.archived) continue;
        for (const tag of note.tags ?? []) counts.set(tag, (counts.get(tag) ?? 0) + 1);
      }
      await json(route, {
        items: Array.from(counts, ([name, count]) => ({ name, count })),
      });
      return;
    }

    await json(route, { items: [] });
  });

  /*
   * The Cognito hosted UI, on its own origin as the real one is.
   *
   * `VITE_COGNITO_DOMAIN` is `https://cognito.e2e.test` under test, so the whole
   * authorization-code round trip — a real cross-origin navigation out of the
   * app and a real redirect back onto the base URL carrying the code — happens
   * in the browser rather than being mocked at the module level. That is the
   * only way to exercise the part that has to survive the document being
   * destroyed and rebuilt.
   */
  await page.route(`${COGNITO_ORIGIN}/**`, async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;

    if (path === '/oauth2/authorize') {
      const params = Object.fromEntries(url.searchParams);
      state.auth.authorize.push(params);

      // Cognito only ever returns to a registered callback URL, so the stub
      // returns to whatever the app asked for and the spec asserts on it.
      const back = new URL(params['redirect_uri'] ?? '/');
      if (state.auth.denyLogin) {
        back.searchParams.set('error', 'access_denied');
        back.searchParams.set('error_description', 'User cancelled the sign-in.');
      } else {
        back.searchParams.set('code', `e2e-code-${String(state.auth.authorize.length)}`);
        back.searchParams.set('state', params['state'] ?? '');
      }
      await redirect(route, back.href);
      return;
    }

    if (path === '/oauth2/token') {
      const body = Object.fromEntries(new URLSearchParams(route.request().postData() ?? ''));
      state.auth.token.push(body);

      if (state.auth.rejectExchange) {
        await route.fulfill({
          status: 400,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'invalid_grant' }),
        });
        return;
      }

      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id_token: 'e2e-id-token',
          access_token: 'e2e-access-token',
          refresh_token: 'e2e-refresh-token',
          expires_in: 3600,
          token_type: 'Bearer',
        }),
      });
      return;
    }

    if (path === '/logout') {
      const params = Object.fromEntries(url.searchParams);
      state.auth.logout.push(params);
      await redirect(route, params['logout_uri'] ?? '/');
      return;
    }

    /*
     * The managed login's passkey page, as observed against prod: with a live
     * session it runs the ceremony on its own origin and returns to the
     * redirect with `result=success`; without one it returns at once with
     * `result=invalid_session`. The ceremony itself is Cognito's to prove.
     */
    if (path === '/passkeys/add') {
      const params = Object.fromEntries(url.searchParams);
      state.auth.passkeyAdd.push(params);
      if (!params['redirect_uri']) {
        await route.fulfill({ status: 400, body: 'Missing required parameter redirect_uri' });
        return;
      }
      const back = new URL(params['redirect_uri']);
      back.searchParams.set('result', state.auth.passkeySession ? 'success' : 'invalid_session');
      await redirect(route, back.href);
      return;
    }

    await route.fulfill({ status: 404, body: '' });
  });

  // Presigned uploads and artifact downloads.
  await page.route('**/upload/**', async (route) => {
    if (state.offline) {
      await route.abort('internetdisconnected');
      return;
    }
    await route.fulfill({ status: 200, body: '' });
  });

  await page.route(`${ARTIFACT_ORIGIN}/artifact/**`, async (route) => {
    const url = route.request().url();
    /*
     * S3's CORS rule, as the bucket applies it: the allow-origin header is
     * written only in answer to a request that named its origin. A no-cors
     * media request gets a response with no such header — which is exactly the
     * response a later CORS `fetch` must not be served from the cache.
     */
    const origin = route.request().headers()['origin'];
    const cors: Record<string, string> = origin
      ? { 'Access-Control-Allow-Origin': origin, Vary: 'Origin' }
      : {};

    if (url.endsWith('/peaks')) {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: cors,
        body: JSON.stringify(PEAKS),
      });
      return;
    }
    if (url.endsWith('/segments')) {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: cors,
        body: JSON.stringify(SEGMENTS),
      });
      return;
    }
    if (url.endsWith('/clean')) {
      // text/plain, matching what the pipeline writes to `clean.txt`.
      await route.fulfill({ status: 200, contentType: 'text/plain', headers: cors, body: CLEANED });
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
          ...cors,
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
        ...cors,
        'Accept-Ranges': 'bytes',
        'Content-Length': String(audio.length),
      },
      body: audio,
    });
  });
}

/**
 * Starts the page with no token, as a first-time visitor or someone who signed
 * out has.
 *
 * Registered *after* `installApi`'s seed, and init scripts run in registration
 * order, so this removes what that put there. Must be called before the first
 * `page.goto`.
 */
export async function startSignedOut(page: Page): Promise<void> {
  await page.addInitScript(() => {
    sessionStorage.setItem('e2e.auth.seeded', '1');
    localStorage.removeItem('chintan.tokens.v2');
  });
}

/** The token set the app holds once a stubbed sign-in completes. */
export async function storedTokens(page: Page): Promise<Record<string, unknown> | null> {
  return page.evaluate(() => {
    const raw = localStorage.getItem('chintan.tokens.v2');
    return raw ? (JSON.parse(raw) as Record<string, unknown>) : null;
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
