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
  NoteCleanQueuedWire,
  NoteDetailWire,
  NotePurgeResponseWire,
  NoteWire,
  Page,
  PresignedDownloadWire,
  ProblemWire,
  ReadinessWire,
  RecordingUrlsWire,
  SearchHitWire,
  SettingsWire,
  TagWire,
  UsageWire,
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
  "default_language": "en",
  "retention_days": 0,
  "theme": "ink"
};

/** PUT /v1/settings → 200. The body is what was STORED, not what was sent, so a coerced value is visible to the client. */
export const settingsStored: SettingsWire = {
  "cleanup_mode": "polished",
  "daily_spend_cap_micros": 0,
  "default_language": "en",
  "retention_days": 30,
  "theme": "nocturne"
};

/** GET /v1/notes → 200. The envelope is {items, cursor}; the cursor is in the body, never in a header. */
export const notesPage: Page<NoteWire> = {
  "items": [
    {
      "aliases": [],
      "archived": false,
      "auto_clean": false,
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
    },
    {
      "aliases": [
        "kitchen",
        "reno"
      ],
      "archived": false,
      "auto_clean": false,
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
    }
  ]
};

/** GET /v1/notes?include=search_text → 200. Each item carries search_text, the lowercased body the server searches, so an offline corpus can match what GET /v1/search matches. Absent without the include. */
export const notesPageWithSearchText: Page<NoteWire> = {
  "items": [
    {
      "aliases": [],
      "archived": false,
      "auto_clean": false,
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
    },
    {
      "aliases": [
        "kitchen",
        "reno"
      ],
      "archived": false,
      "auto_clean": false,
      "created_at": "2026-01-01T00:00:00.000000000Z",
      "id": "fixture-id",
      "purge_after": null,
      "search_text": "quotes are in. the tiler can start on the fourteenth.",
      "snippet": "Quotes are in. The tiler can start on the fourteenth.",
      "tags": [
        "house",
        "money"
      ],
      "title": "Kitchen rebuild",
      "updated_at": "2026-01-01T00:00:00.000000000Z",
      "version": 2
    }
  ]
};

/** GET /v1/notes/{noteId} → 200, with body and captures. cleaned is null until the note has been cleaned once. */
export const noteDetail: NoteDetailWire = {
  "aliases": [
    "kitchen",
    "reno"
  ],
  "archived": false,
  "auto_clean": false,
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
      "suggested_note_id": null,
      "suggested_title": null,
      "version": 1
    }
  ],
  "cleaned": null,
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

/** GET /v1/notes/{noteId} → 200 for a note with a whole-note cleaned view (auto_clean on, structured). stale is true because the body changed after generated_at; the view is read-only and regenerated by POST /clean. */
export const noteDetailCleaned: NoteDetailWire = {
  "aliases": [],
  "archived": false,
  "auto_clean": true,
  "body": "the gutter leaks. call the roofer on the fourteenth.",
  "captures": [],
  "cleaned": {
    "body": "# Roof repair\n\n- The gutter leaks.\n- Call the roofer on the fourteenth.",
    "generated_at": "2026-01-01T00:00:00.000000000Z",
    "mode": "structured",
    "stale": true
  },
  "cleaned_mode": "structured",
  "created_at": "2026-01-01T00:00:00.000000000Z",
  "id": "fixture-id",
  "purge_after": null,
  "snippet": "the gutter leaks. call the roofer on the fourteenth.",
  "tags": [],
  "title": "Roof repair",
  "updated_at": "2026-01-01T00:00:00.000000000Z",
  "version": 4
};

/** POST /v1/notes/{noteId}/clean → 202. The worker regenerates the view asynchronously; poll GET /v1/notes/{noteId} for a newer generated_at or an error. */
export const noteCleanQueued: NoteCleanQueuedWire = {
  "mode": "polished",
  "status": "queued"
};

/** POST /v1/notes → 201 */
export const noteCreated: NoteWire = {
  "aliases": [],
  "archived": false,
  "auto_clean": false,
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
      "suggested_note_id": null,
      "suggested_title": null,
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
      "suggested_note_id": "contract-suggested-note",
      "suggested_title": null,
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
      "suggested_note_id": null,
      "suggested_title": "Kitchen rebuild",
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
      "suggested_note_id": null,
      "suggested_title": null,
      "version": 1
    }
  ]
};

/** GET /v1/captures/{captureId} → 200 for a needs_target capture the router matched to an existing note. `suggested_note_id` is what the "Add to <note>" prompt is built from. */
export const captureSuggestedNote: CaptureWire = {
  "appended_at": null,
  "created_at": "2026-01-01T00:00:00.000000000Z",
  "duration_ms": null,
  "error": null,
  "has_peaks": false,
  "has_segments": false,
  "id": "fixture-id",
  "note_id": null,
  "status": "needs_target",
  "suggested_note_id": "contract-suggested-note",
  "suggested_title": null,
  "version": 1
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
  "suggested_note_id": null,
  "suggested_title": null,
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
    "suggested_note_id": null,
    "suggested_title": null,
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

/** POST /v1/captures/{captureId}/move → 200. The re-pointed capture: note_id is the target, and its paragraph went with it. */
export const captureMoved: CaptureWire = {
  "appended_at": "2026-01-01T00:00:00.000000000Z",
  "created_at": "2026-01-01T00:00:00.000000000Z",
  "duration_ms": null,
  "error": null,
  "has_peaks": false,
  "has_segments": false,
  "id": "fixture-id",
  "note_id": "fixture-note-id",
  "status": "appended",
  "suggested_note_id": null,
  "suggested_title": null,
  "version": 2
};

/** GET /v1/notes/{noteId}/recordings/urls → 200. One presigned GET per recording that still has its audio, oldest first, with the filename to save it under: <note-title-slug>-<yyyymmdd-hhmm>.<ext>. */
export const recordingUrls: RecordingUrlsWire = {
  "items": [
    {
      "capture_id": "c_take_1",
      "expires_at": "2026-01-01T00:00:00.000000000Z",
      "filename": "kitchen-rebuild-20260101-0930.webm",
      "url": "https://example.invalid/presigned"
    },
    {
      "capture_id": "c_misfiled",
      "expires_at": "2026-01-01T00:00:00.000000000Z",
      "filename": "kitchen-rebuild-20260101-1200.webm",
      "url": "https://example.invalid/presigned"
    },
    {
      "capture_id": "c_take_2",
      "expires_at": "2026-01-01T00:00:00.000000000Z",
      "filename": "kitchen-rebuild-20260101-1506.webm",
      "url": "https://example.invalid/presigned"
    }
  ]
};

/** POST /v1/captures/{captureId}/move → 503 after a rollback. `type` is the retryable URI: nothing changed, send the same request again. */
export const problemRetryable: ProblemWire = {
  "correlation_id": "00000000-0000-4000-8000-000000000000",
  "detail": "the recording could not be moved and nothing was changed; try again",
  "instance": "/v1/fixture",
  "status": 503,
  "title": "Service Unavailable",
  "type": "https://github.com/vppillai/chintan/blob/main/docs/api/openapi.yaml#retryable"
};

/** GET /v1/usage?month=2026-01 → 200. The caller's own provider spend for the month in microdollars: totals, the split by pipeline stage, one line per day — and `aws`, the instance's AWS spend for the month as last read from the stack's budget. */
export const usage: UsageWire = {
  "audio_seconds": 28.5,
  "aws": {
    "as_of": "2026-01-05T06:00:00Z",
    "budget_micros": 10000000,
    "month_micros": 2345678
  },
  "calls": 3,
  "cost_micros": 1371,
  "days": [
    {
      "audio_seconds": 28.5,
      "calls": 2,
      "cost_micros": 951,
      "date": "2026-01-03",
      "input_tokens": 900,
      "output_tokens": 300
    },
    {
      "calls": 1,
      "cost_micros": 420,
      "date": "2026-01-04",
      "input_tokens": 1200,
      "output_tokens": 100
    }
  ],
  "input_tokens": 2100,
  "month": "2026-01",
  "ops": {
    "cleanup": {
      "calls": 1,
      "cost_micros": 640,
      "input_tokens": 900,
      "output_tokens": 300
    },
    "route": {
      "calls": 1,
      "cost_micros": 420,
      "input_tokens": 1200,
      "output_tokens": 100
    },
    "transcribe": {
      "audio_seconds": 28.5,
      "calls": 1,
      "cost_micros": 311
    }
  },
  "output_tokens": 400
};

/** GET /v1/usage?month=2025-12 → 200 for a month with no usage: zeros and empty collections, never 404; `aws` is null when no reading has been recorded for the month. */
export const usageEmpty: UsageWire = {
  "aws": null,
  "calls": 0,
  "cost_micros": 0,
  "days": [],
  "month": "2025-12",
  "ops": {}
};

/** POST /v1/export → 202 */
export const exportJob: ExportJobWire = {
  "bytes": 2048,
  "expires_at": "2026-01-01T00:00:00.000000000Z",
  "id": "fixture-id",
  "status": "ready",
  "url": "https://example.invalid/presigned"
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

/** POST /v1/notes/purge → 200. One result per note: purged, not_found, and an active note refused. 200 even when some failed, because no transaction spans DynamoDB and S3. */
export const notePurgeResults: NotePurgeResponseWire = {
  "results": [
    {
      "note_id": "fixture-note-id",
      "status": "purged"
    },
    {
      "detail": "no such note",
      "note_id": "fixture-note-id",
      "status": "not_found"
    },
    {
      "detail": "this note is not archived, so it was not deleted; archive it first",
      "note_id": "fixture-note-id",
      "status": "failed"
    }
  ]
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
