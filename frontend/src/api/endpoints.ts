/**
 * Typed wrappers over the operations in `docs/api/openapi.yaml` that this app
 * calls.
 *
 * One method per operation, so a contract change is a compile error rather
 * than a runtime surprise, and so no component ever builds a URL by hand.
 *
 * Not every operation: `POST /v1/notes/match` and the two export endpoints are
 * served and have no screen. Their wrappers sat here for two releases with no
 * caller, and the request-contract recorder replayed them against the backend
 * on every run — proving the router accepted requests nobody sends. They come
 * back with the screen that needs them; the wire types stay in `schema.ts`
 * because the response-contract fixtures still pin them.
 */

import { NO_RETRY } from './client.ts';
import type { ApiClient, RequestOptions } from './client.ts';
import type {
  CaptureArtifactKind,
  CaptureCreateWire,
  CaptureCreatedWire,
  CaptureListQuery,
  CaptureTargetWire,
  CaptureWire,
  NoteCreateWire,
  NoteDetailWire,
  NoteListQuery,
  NotePurgeResponseWire,
  NoteUpdateWire,
  NoteWire,
  Page,
  PageQuery,
  PresignedDownloadWire,
  PresignedUploadWire,
  ReadinessWire,
  SearchHitWire,
  SettingsWire,
  TagWire,
  UsageWire,
} from './schema.ts';

type Query = NonNullable<RequestOptions['query']>;

function pageQuery(query: PageQuery = {}): Query {
  return { cursor: query.cursor, limit: query.limit };
}

export class ChintanApi {
  constructor(private readonly client: ApiClient) {}

  /* ---- Health --------------------------------------------------------- */

  health(): Promise<{ status: 'ok' }> {
    return this.client.request('/v1/health', { anonymous: true, retry: NO_RETRY });
  }

  ready(): Promise<ReadinessWire> {
    return this.client.request('/v1/health/ready', { anonymous: true, retry: NO_RETRY });
  }

  /* ---- Settings ------------------------------------------------------- */

  getSettings(): Promise<SettingsWire> {
    return this.client.request('/v1/settings');
  }

  putSettings(body: SettingsWire, idempotencyKey?: string): Promise<SettingsWire> {
    return this.client.request('/v1/settings', {
      method: 'PUT',
      body,
      idempotencyKey,
    });
  }

  /* ---- Usage ---------------------------------------------------------- */

  /**
   * The caller's own provider usage for one UTC month, `yyyy-mm`; the current
   * month when omitted. A month with nothing in it is zeros, not a 404.
   */
  getUsage(month?: string): Promise<UsageWire> {
    return this.client.request('/v1/usage', { query: { month } });
  }

  /* ---- Notes ---------------------------------------------------------- */

  listNotes(query: NoteListQuery = {}): Promise<Page<NoteWire>> {
    return this.client.request('/v1/notes', {
      query: { ...pageQuery(query), state: query.state, tag: query.tag },
    });
  }

  getNote(noteId: string): Promise<NoteDetailWire> {
    return this.client.request(`/v1/notes/${encodeURIComponent(noteId)}`);
  }

  createNote(body: NoteCreateWire, idempotencyKey?: string): Promise<NoteWire> {
    return this.client.request('/v1/notes', { method: 'POST', body, idempotencyKey });
  }

  updateNote(
    noteId: string,
    body: NoteUpdateWire,
    idempotencyKey?: string,
  ): Promise<NoteWire> {
    return this.client.request(`/v1/notes/${encodeURIComponent(noteId)}`, {
      method: 'PATCH',
      body,
      idempotencyKey,
    });
  }

  /** Soft delete. Recoverable via `restoreNote` for 30 days. */
  archiveNote(noteId: string): Promise<void> {
    return this.client.request(`/v1/notes/${encodeURIComponent(noteId)}`, {
      method: 'DELETE',
    });
  }

  restoreNote(noteId: string): Promise<NoteWire> {
    return this.client.request(`/v1/notes/${encodeURIComponent(noteId)}/restore`, {
      method: 'POST',
    });
  }

  /** Irreversible, and one of the two actions gated by the confirm dialog. */
  deleteNoteForever(noteId: string): Promise<void> {
    return this.client.request(`/v1/notes/${encodeURIComponent(noteId)}/permanent`, {
      method: 'DELETE',
      retry: NO_RETRY,
    });
  }

  /**
   * Batch purge. No "all" flag exists server-side by design — the caller
   * lists the notes it means and names them explicitly (see the backend's own
   * `NotePurgeRequest` doc comment) — so "empty the archive" is this client
   * gathering every archived note's id and sending them here, chunked at
   * `service.MaxPurgeBatch` if there are more than that at once.
   */
  purgeNotesBatch(noteIds: string[]): Promise<NotePurgeResponseWire> {
    return this.client.request('/v1/notes/purge', {
      method: 'POST',
      body: { note_ids: noteIds },
      retry: NO_RETRY,
    });
  }

  listTags(): Promise<{ items: TagWire[] }> {
    return this.client.request('/v1/tags');
  }

  /* ---- Search --------------------------------------------------------- */

  search(q: string, query: PageQuery = {}): Promise<Page<SearchHitWire>> {
    return this.client.request('/v1/search', { query: { q, ...pageQuery(query) } });
  }

  /* ---- Captures ------------------------------------------------------- */

  listCaptures(query: CaptureListQuery = {}): Promise<Page<CaptureWire>> {
    return this.client.request('/v1/captures', {
      query: { ...pageQuery(query), status: query.status },
    });
  }

  /**
   * Begins a capture. Fast by contract — it writes the row and returns a
   * presigned PUT; the upload event drives the pipeline out of band.
   *
   * The idempotency key must be the capture's own stable key, so that a retry
   * of this call after a flaky response reuses the original upload URL instead
   * of stranding a half-created capture.
   */
  createCapture(
    body: CaptureCreateWire,
    idempotencyKey: string,
  ): Promise<CaptureCreatedWire> {
    return this.client.request('/v1/captures', {
      method: 'POST',
      body,
      idempotencyKey,
    });
  }

  getCapture(captureId: string): Promise<CaptureWire> {
    return this.client.request(`/v1/captures/${encodeURIComponent(captureId)}`);
  }

  setCaptureTarget(
    captureId: string,
    body: CaptureTargetWire,
    idempotencyKey?: string,
  ): Promise<CaptureWire> {
    return this.client.request(`/v1/captures/${encodeURIComponent(captureId)}/target`, {
      method: 'POST',
      body,
      idempotencyKey,
    });
  }

  /**
   * Retries a failed capture from its last good stage.
   *
   * In v1 an equivalent method existed on the client and was called from
   * nowhere: a failed capture was a dead end with a toast.
   */
  retryCapture(captureId: string, idempotencyKey?: string): Promise<CaptureWire> {
    return this.client.request(`/v1/captures/${encodeURIComponent(captureId)}/retry`, {
      method: 'POST',
      idempotencyKey,
    });
  }

  downloadUrl(
    captureId: string,
    kind: CaptureArtifactKind,
  ): Promise<PresignedDownloadWire> {
    return this.client.request(
      `/v1/captures/${encodeURIComponent(captureId)}/download`,
      { query: { kind } },
    );
  }
}

/**
 * S3 refused the signature itself: expired, malformed, or tampered with.
 *
 * Its own type because it is the one upload failure that a retry can never
 * fix. Everything else `putPresigned` sees — a 5xx, a dropped connection — is
 * worth another attempt, and treating them alike is what turned an expired
 * credential into five identical requests.
 */
export class PresignRejected extends Error {
  constructor(readonly status: number) {
    super(`Upload rejected with ${status}`);
    this.name = 'PresignRejected';
  }
}

/**
 * True when a presigned credential is past its expiry, or close enough that
 * the request would lose the race.
 *
 * A presign is a thirty-minute credential. Anything that has been sitting
 * around — a resumed recording, a replayed idempotent response — has very
 * likely outlived it, and that is knowable before spending a request and a
 * backoff on finding out.
 */
export function presignExpired(
  upload: { expires_at?: string },
  now: number = Date.now(),
  skewMs = 30_000,
): boolean {
  if (!upload.expires_at) return false;
  const at = Date.parse(upload.expires_at);
  if (!Number.isFinite(at)) return false;
  return at - skewMs <= now;
}

/**
 * Uploads bytes to a presigned URL.
 *
 * Deliberately not routed through `ApiClient`: a presigned PUT is
 * pre-authorised, and attaching our bearer would break the S3 signature. It
 * still gets a timeout and bounded retry, because a capture upload dying on a
 * cellular blip is the one failure the product cannot absorb.
 */
export async function putPresigned(
  upload: PresignedUploadWire,
  body: Blob | ArrayBuffer | string,
  options: {
    contentType?: string;
    signal?: AbortSignal;
    maxRetries?: number;
    fetchImpl?: typeof fetch;
    onAttempt?: (attempt: number) => void;
  } = {},
): Promise<void> {
  const fetchImpl = options.fetchImpl ?? ((...args: Parameters<typeof fetch>) =>
    globalThis.fetch(...args));
  const maxRetries = options.maxRetries ?? 4;
  const headers = new Headers(upload.headers);
  if (options.contentType) headers.set('Content-Type', options.contentType);

  let lastError: unknown;
  for (let attempt = 0; attempt <= maxRetries; attempt += 1) {
    if (options.signal?.aborted) throw new DOMException('Aborted', 'AbortError');
    options.onAttempt?.(attempt);
    try {
      const response = await fetchImpl(upload.url, {
        method: 'PUT',
        headers,
        body,
        ...(options.signal ? { signal: options.signal } : {}),
      });
      if (response.ok) return;
      /*
       * A presigned URL that has expired returns 403. Retrying will not fix
       * it — the caller has to mint a new one.
       *
       * This used to `throw` here, which read as "stop", and the `catch` two
       * lines below caught its own throw, recorded it as `lastError` and let
       * the loop run its full budget. One dead credential therefore became
       * five identical 403s with backoff between them: the user waited seconds
       * for a failure that was knowable at the first response, and the console
       * filled with repeated errors that looked like a flaky network rather
       * than an expired signature. `PresignRejected` is thrown past the catch.
       */
      if (response.status === 403 || response.status === 400) {
        throw new PresignRejected(response.status);
      }
      lastError = new Error(`Upload failed with ${response.status}`);
    } catch (error) {
      if ((error as Error)?.name === 'AbortError') throw error;
      if (error instanceof PresignRejected) throw error;
      lastError = error;
    }
    if (attempt < maxRetries) {
      await new Promise((resolve) =>
        setTimeout(resolve, Math.random() * Math.min(500 * 2 ** attempt, 8_000)),
      );
    }
  }
  throw lastError instanceof Error ? lastError : new Error('Upload failed');
}
