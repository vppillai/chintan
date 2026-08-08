/**
 * GENERATED — do not edit by hand.
 *
 * Every constant below is a real response body, captured from the Go router in
 * internal/handler over httptest and written out by
 * TestContractResponsesAreWhatTheFrontendTypesDeclare. Regenerate with:
 *
 *     cd backend && CHINTAN_UPDATE_FIXTURES=1 go test ./internal/handler/ -run Contract
 *
 * The type annotations are the point. An annotated object literal is "fresh" to
 * TypeScript, so excess property checking applies: if the backend adds, renames
 * or retypes a field that schema.ts does not declare the same way, "bun run
 * typecheck" fails here. That is the only thing standing between two
 * independently written implementations and a field name that only one of them
 * changed.
 *
 * Values that differ every run — ids, timestamps, presigned URLs, measured
 * latencies — are replaced with stable stand-ins so the file is reviewable. The
 * SHAPE is never altered: presence, absence, null and type all survive, and
 * upload.headers is copied through verbatim because every entry in it is inside
 * the presigned signature.
 */

import type {
  CaptureCreatedWire,
  CaptureWire,
  ExportJobWire,
  MatchResponseWire,
  NoteDetailWire,
  NoteWire,
  Page,
  PresignedDownloadWire,
  ProblemWire,
  ReadinessWire,
  SearchHitWire,
  SettingsWire,
  TagWire,
  TokenSetWire,
  WebAuthnOptionsWire,
} from '../schema.ts';

/** GET /v1/health → 200 */
export const health: { status: 'ok' } = {
  "status": "ok"
};

/** GET /v1/health/ready → 200 */
export const ready: ReadinessWire = {
  "checks": {
    "dynamodb": {
      "latency_ms": 1,
      "ok": true
    },
    "s3": {
      "latency_ms": 1,
      "ok": true
    }
  },
  "status": "ok"
};

/** GET /v1/health/ready → 503, a dependency is not answering */
export const readyDegraded: ProblemWire = {
  "correlation_id": "00000000-0000-4000-8000-000000000000",
  "detail": "a dependency is not answering",
  "instance": "/v1/fixture",
  "status": 503,
  "title": "Service Unavailable",
  "type": "about:blank"
};

/** GET /v1/settings → 200, the defaults a new tenant gets */
export const settings: SettingsWire = {
  "cleanup_mode": "faithful",
  "daily_spend_cap_micros": 0,
  "retention_days": 0,
  "theme": "ink"
};

/** PUT /v1/settings → 200. This is what was STORED, not what was sent: v1 echoed the request back and hid every coercion. */
export const settingsStored: SettingsWire = {
  "cleanup_mode": "polished",
  "daily_spend_cap_micros": 0,
  "retention_days": 30,
  "theme": "nocturne"
};

/** GET /v1/notes → 200. The envelope is {items, cursor}; a bare array plus X-Next-Cursor was the v1 shape and is gone. */
export const notesPage: Page<NoteWire> = {
  "items": [
    {
      "aliases": [
        "kitchen",
        "reno"
      ],
      "archived": false,
      "created_at": "2026-01-01T00:00:00.000000000Z",
      "id": "fixture-id",
      "purge_after": null,
      "snippet": "Quotes are in. The tiler can start on the fourteenth.",
      "tags": [
        "house",
        "money"
      ],
      "title": "Kitchen rebuild",
      "updated_at": "2026-01-01T00:00:00.000000000Z",
      "version": 2
    },
    {
      "aliases": [],
      "archived": false,
      "created_at": "2026-01-01T00:00:00.000000000Z",
      "id": "fixture-id",
      "purge_after": null,
      "tags": [
        "house"
      ],
      "title": "Reading list",
      "updated_at": "2026-01-01T00:00:00.000000000Z",
      "verbatim": true,
      "version": 2
    }
  ]
};

/** GET /v1/notes/{noteId} → 200, with body and captures */
export const noteDetail: NoteDetailWire = {
  "aliases": [
    "kitchen",
    "reno"
  ],
  "archived": false,
  "body": "Quotes are in. The tiler can start on the fourteenth.",
  "captures": [
    {
      "appended_at": "2026-01-01T00:00:00.000000000Z",
      "created_at": "2026-01-01T00:00:00.000000000Z",
      "duration_ms": 18400,
      "error": null,
      "has_peaks": true,
      "has_segments": true,
      "id": "fixture-id",
      "note_id": "fixture-note-id",
      "status": "appended",
      "version": 1
    }
  ],
  "created_at": "2026-01-01T00:00:00.000000000Z",
  "id": "fixture-id",
  "purge_after": null,
  "snippet": "Quotes are in. The tiler can start on the fourteenth.",
  "tags": [
    "house",
    "money"
  ],
  "title": "Kitchen rebuild",
  "updated_at": "2026-01-01T00:00:00.000000000Z",
  "version": 2
};

/** POST /v1/notes → 201 */
export const noteCreated: NoteWire = {
  "aliases": [],
  "archived": false,
  "created_at": "2026-01-01T00:00:00.000000000Z",
  "id": "fixture-id",
  "purge_after": null,
  "tags": [],
  "title": "A new thought",
  "updated_at": "2026-01-01T00:00:00.000000000Z",
  "version": 1
};

/** GET /v1/tags → 200, one entry per tag in use with its count */
export const tagsPage: Page<TagWire> = {
  "items": [
    {
      "count": 2,
      "name": "house"
    },
    {
      "count": 1,
      "name": "money"
    }
  ]
};

/** GET /v1/search → 200. matched_in names the fields that matched; excerpt is the surrounding context. */
export const searchPage: Page<SearchHitWire> = {
  "items": [
    {
      "excerpt": "Quotes are in. The tiler can start on the fourteenth.",
      "matched_in": [
        "body"
      ],
      "note_id": "fixture-note-id",
      "title": "Kitchen rebuild"
    }
  ]
};

/** POST /v1/notes/match → 200 */
export const matchResponse: MatchResponseWire = {
  "auto_select_id": "fixture-note-id",
  "auto_selected": true,
  "candidates": [
    {
      "note_id": "fixture-note-id",
      "score": 0.87,
      "title": "Kitchen rebuild"
    }
  ],
  "confidence": 0.87
};

/** GET /v1/captures → 200. Includes the unrouted needs_target capture the progress card has to show. */
export const capturesPage: Page<CaptureWire> = {
  "items": [
    {
      "appended_at": null,
      "created_at": "2026-01-01T00:00:00.000000000Z",
      "duration_ms": null,
      "error": "the speech provider returned 503",
      "has_peaks": false,
      "has_segments": false,
      "id": "fixture-id",
      "note_id": "fixture-note-id",
      "status": "failed",
      "version": 1
    },
    {
      "appended_at": null,
      "created_at": "2026-01-01T00:00:00.000000000Z",
      "duration_ms": null,
      "error": null,
      "has_peaks": false,
      "has_segments": false,
      "id": "fixture-id",
      "note_id": null,
      "status": "needs_target",
      "version": 1
    },
    {
      "appended_at": "2026-01-01T00:00:00.000000000Z",
      "created_at": "2026-01-01T00:00:00.000000000Z",
      "duration_ms": 9100,
      "error": null,
      "has_peaks": true,
      "has_segments": true,
      "id": "fixture-id",
      "note_id": "fixture-note-id",
      "status": "appended",
      "version": 1
    }
  ]
};

/** GET /v1/captures/{captureId} → 200 for a failed capture. `error` is the text the progress card renders. */
export const captureFailed: CaptureWire = {
  "appended_at": null,
  "created_at": "2026-01-01T00:00:00.000000000Z",
  "duration_ms": null,
  "error": "the speech provider returned 503",
  "has_peaks": false,
  "has_segments": false,
  "id": "fixture-id",
  "note_id": "fixture-note-id",
  "status": "failed",
  "version": 1
};

/** GET /v1/captures/{captureId}/download?kind=audio → 200 */
export const captureDownload: PresignedDownloadWire = {
  "expires_at": "2026-01-01T00:00:00.000000000Z",
  "url": "https://example.invalid/presigned"
};

/** POST /v1/captures → 201. upload.headers reaches the client verbatim; x-amz-tagging is inside the signature, so dropping it makes the PUT 403. */
export const captureCreated: CaptureCreatedWire = {
  "capture": {
    "appended_at": null,
    "created_at": "2026-01-01T00:00:00.000000000Z",
    "duration_ms": 12000,
    "error": null,
    "has_peaks": true,
    "has_segments": false,
    "id": "fixture-id",
    "note_id": null,
    "status": "uploaded",
    "version": 1
  },
  "peaks_upload": {
    "expires_at": "2026-01-01T00:00:00.000000000Z",
    "headers": {
      "Content-Type": "application/json"
    },
    "max_bytes": 2097152,
    "url": "https://example.invalid/presigned"
  },
  "upload": {
    "expires_at": "2026-01-01T00:00:00.000000000Z",
    "headers": {
      "Content-Type": "audio/webm",
      "x-amz-tagging": "chintan-artifact=capture-audio"
    },
    "max_bytes": 4194304,
    "url": "https://example.invalid/presigned"
  }
};

/** POST /v1/export → 202 */
export const exportJob: ExportJobWire = {
  "bytes": 2048,
  "expires_at": "2026-01-01T00:00:00.000000000Z",
  "id": "fixture-id",
  "status": "ready",
  "url": "https://example.invalid/presigned"
};

/** GET /v1/auth/webauthn/status → 200 */
export const webauthnStatus: { enrolled: boolean } = {
  "enrolled": false
};

/** POST /v1/auth/webauthn/register/options → 200 */
export const webauthnOptions: WebAuthnOptionsWire = {
  "challenge_id": "fixture-challenge-id",
  "options": {
    "challenge": "x"
  }
};

/** POST /v1/auth/webauthn/login → 200, the Cognito token set as it arrives */
export const tokenSet: TokenSetWire = {
  "access_token": "access",
  "expires_in": 3600,
  "id_token": "id",
  "refresh_token": "",
  "token_type": "Bearer"
};

/** GET /v1/notes/{noteId} → 404 */
export const problemNotFound: ProblemWire = {
  "correlation_id": "00000000-0000-4000-8000-000000000000",
  "detail": "no such resource",
  "instance": "/v1/fixture",
  "status": 404,
  "title": "Not Found",
  "type": "about:blank"
};

/** PUT /v1/settings → 400 */
export const problemValidation: ProblemWire = {
  "correlation_id": "00000000-0000-4000-8000-000000000000",
  "detail": "theme must be ink, nocturne or system",
  "instance": "/v1/fixture",
  "status": 400,
  "title": "Bad Request",
  "type": "about:blank"
};

/** PATCH /v1/notes/{noteId} → 409. current_version is what lets an optimistic-concurrency loser reconcile instead of guessing. */
export const problemConflict: ProblemWire = {
  "correlation_id": "00000000-0000-4000-8000-000000000000",
  "current_version": 2,
  "detail": "the resource changed since you read it; re-read and retry",
  "instance": "/v1/fixture",
  "status": 409,
  "title": "Conflict",
  "type": "about:blank"
};

/** POST /v1/captures → 429 for the daily spend cap, which the client must not treat as a retryable 429. */
export const problemSpendCapped: ProblemWire = {
  "correlation_id": "00000000-0000-4000-8000-000000000000",
  "detail": "the daily provider spend cap has been reached",
  "instance": "/v1/fixture",
  "status": 429,
  "title": "Too Many Requests",
  "type": "https://github.com/vppillai/chintan/blob/main/docs/api/openapi.yaml#spend-capped"
};

/** Every CaptureStatus the Go backend can write. Compared against CAPTURE_STATUSES. */
export const BACKEND_CAPTURE_STATUSES = [
  "uploaded",
  "transcribing",
  "routing",
  "cleaning",
  "appending",
  "appended",
  "needs_target",
  "no_content",
  "failed",
  "spend_capped"
] as const;

/**
 * The statuses the pipeline is still moving through — service.CaptureIsPending.
 * This is the polling question, and its complement must be exactly the
 * frontend's TERMINAL_CAPTURE_STATUSES: a status in neither set is one the
 * progress card polls forever.
 */
export const BACKEND_PENDING_CAPTURE_STATUSES = [
  "uploaded",
  "transcribing",
  "routing",
  "cleaning",
  "appending"
] as const;

/** The field names search can report in matched_in. Must be a subset of SearchField. */
export const BACKEND_SEARCH_MATCH_FIELDS = [
  "title",
  "alias",
  "tag",
  "body"
] as const;

/** The audio container types POST /v1/captures accepts. Must contain every CaptureContentType. */
export const BACKEND_CAPTURE_CONTENT_TYPES = [
  "audio/webm",
  "audio/mp4",
  "audio/ogg",
  "audio/wav",
  "audio/mpeg",
  "audio/mp3",
  "audio/m4a",
  "audio/wave",
  "audio/x-wav",
  "audio/x-m4a"
] as const;
