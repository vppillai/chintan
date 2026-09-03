/**
 * Wire types, hand-written from `docs/api/openapi.yaml`.
 *
 * Hand-written rather than generated on purpose. The document is 980 lines and
 * uses `oneOf` discriminated bodies and `allOf` composition that every
 * TypeScript generator renders into something less readable than this, and a
 * generated file still has to be committed and reviewed. This is the same
 * amount of reviewable text with none of the toolchain.
 *
 * Field names are the wire's snake_case. Nothing above `endpoints.ts` should
 * need to touch these directly; that module maps them into the shapes the UI
 * uses. Keep this file in step with the YAML — it is the contract.
 */

/* ---------------------------------------------------------------------------
   Errors — RFC 9457
   --------------------------------------------------------------------------- */

export interface ProblemWire {
  type: string;
  title: string;
  status: number;
  detail?: string;
  instance?: string;
  correlation_id?: string;
  /** Present on 409 so the client can reconcile an optimistic-concurrency loss. */
  current_version?: number;
}

/* ---------------------------------------------------------------------------
   Pagination — every collection is an envelope, never a bare array
   --------------------------------------------------------------------------- */

export interface Page<T> {
  items: T[];
  /** Absent or empty when the collection is exhausted. */
  cursor?: string;
}

export interface PageQuery {
  cursor?: string;
  /** 1–200, default 50. */
  limit?: number;
}

/* ---------------------------------------------------------------------------
   Health and settings
   --------------------------------------------------------------------------- */

export interface ReadinessWire {
  status: 'ok' | 'degraded';
  checks: Record<string, { ok?: boolean; latency_ms?: number }>;
}

export type CleanupMode = 'faithful' | 'polished';
export type ThemeSetting = 'ink' | 'nocturne' | 'system';

export interface SettingsWire {
  cleanup_mode: CleanupMode;
  retention_days: number;
  theme: ThemeSetting;
  daily_spend_cap_micros?: number;
}

/* ---------------------------------------------------------------------------
   Notes
   --------------------------------------------------------------------------- */

export interface NoteWire {
  id: string;
  title: string;
  aliases?: string[];
  tags?: string[];
  snippet?: string;
  updated_at: string;
  created_at?: string;
  version: number;
  archived: boolean;
  purge_after?: string | null;
  /**
   * Cleanup is bypassed for this note. Writable through `NoteUpdateWire` and
   * returned on every read — the toggle has to render the state it is in.
   *
   * Present here because the contract fixtures proved the backend sends it. The
   * OpenAPI document listed it on `NoteUpdate` only.
   */
  verbatim?: boolean;
}

export interface NoteDetailWire extends NoteWire {
  body: string;
  captures?: CaptureWire[];
}

export interface NoteCreateWire {
  title: string;
  body?: string;
  aliases?: string[];
  tags?: string[];
}

export interface NoteUpdateWire {
  version: number;
  title?: string;
  body?: string;
  aliases?: string[];
  tags?: string[];
  verbatim?: boolean;
}

export type NoteState = 'active' | 'archived';

/**
 * Batch purge. The only batch endpoint on this API, and it exists because a
 * purge cascades to every capture's audio, transcripts, segments and peaks —
 * a hundred of those driven one at a time from a phone is a hundred round trips
 * that all have to survive the connection.
 *
 * Explicit ids only. "Clear all" is this client listing its archive and sending
 * the ids in batches; there is no server-side "delete everything" flag, on
 * purpose.
 */
export interface NotePurgeRequestWire {
  /** At most 100 per request. */
  note_ids: string[];
}

/**
 * One note's outcome.
 *
 * `purged` — gone, along with everything it owned.
 * `not_found` — no such note. Also what a replayed batch reports, which is what
 * makes a retry safe.
 * `failed` — still here. Either it was not archived (an active note is refused,
 * so a stale listing cannot turn "clear my archive" into "delete my notes"), or
 * part of its cascade failed and the row was kept so it can be retried.
 */
export type NotePurgeStatus = 'purged' | 'not_found' | 'failed';

export interface NotePurgeResultWire {
  note_id: string;
  status: NotePurgeStatus;
  /** Safe to display. Never contains provider or AWS internals. */
  detail?: string;
}

/**
 * The response is 200 even when some notes failed: no transaction spans
 * DynamoDB and S3, so one verdict for the batch would be a claim the server
 * cannot make. Render what survived.
 */
export interface NotePurgeResponseWire {
  results: NotePurgeResultWire[];
}

export interface NoteListQuery extends PageQuery {
  state?: NoteState;
  tag?: string;
}

export interface TagWire {
  name: string;
  count: number;
}

/* ---------------------------------------------------------------------------
   Captures
   --------------------------------------------------------------------------- */

export const CAPTURE_STATUSES = [
  'uploaded',
  'transcribing',
  'routing',
  'cleaning',
  'appending',
  'appended',
  'needs_target',
  'no_content',
  'failed',
  'spend_capped',
] as const;

export type CaptureStatus = (typeof CAPTURE_STATUSES)[number];

/** Statuses the pipeline will not move on from without user action. */
export const TERMINAL_CAPTURE_STATUSES: readonly CaptureStatus[] = [
  'appended',
  'needs_target',
  'no_content',
  'failed',
  'spend_capped',
];

export function isTerminalStatus(status: CaptureStatus): boolean {
  return TERMINAL_CAPTURE_STATUSES.includes(status);
}

export interface CaptureWire {
  id: string;
  note_id?: string | null;
  status: CaptureStatus;
  error?: string | null;
  created_at: string;
  /**
   * Where the router thinks this recording belongs. Exactly one of the two is
   * ever set, and only on a `needs_target` capture: `suggested_note_id` names an
   * existing note it was confident enough to propose but not to append to
   * unasked, `suggested_title` is the title it would give a new note when there
   * was no plausible destination.
   *
   * The backend has always computed these — it pays an LLM call for them — and
   * used to drop them before the response left the API, which is why the
   * "where should this go?" prompt could only show an unranked list of every
   * note. They are declared here so the fixtures typecheck; rendering them is
   * the capture screen's to do.
   */
  suggested_note_id?: string | null;
  suggested_title?: string | null;
  appended_at?: string | null;
  duration_ms?: number | null;
  has_segments?: boolean;
  has_peaks?: boolean;
  version: number;
}

export type CaptureContentType = 'audio/webm' | 'audio/mp4' | 'audio/ogg' | 'audio/wav';

export interface CaptureCreateWire {
  content_type: CaptureContentType;
  note_id?: string | null;
  duration_ms?: number;
  size_bytes?: number;
}

export interface PresignedUploadWire {
  url: string;
  expires_at: string;
  max_bytes?: number;
  headers?: Record<string, string>;
}

export interface CaptureCreatedWire {
  capture: CaptureWire;
  upload: PresignedUploadWire & { max_bytes: number };
  /** Absent when the instance does not accept client-computed peaks. */
  peaks_upload?: PresignedUploadWire;
}

export type CaptureTargetWire = { note_id: string } | { new_note_title: string };

export type CaptureListStatus = 'pending' | 'failed' | 'needs_target' | 'all';

export interface CaptureListQuery extends PageQuery {
  status?: CaptureListStatus;
}

export type CaptureArtifactKind = 'audio' | 'raw' | 'clean' | 'segments' | 'peaks';

export interface PresignedDownloadWire {
  url: string;
  expires_at: string;
}

/* ---------------------------------------------------------------------------
   Match and search
   --------------------------------------------------------------------------- */

export interface MatchCandidateWire {
  note_id: string;
  title: string;
  score: number;
  reason?: string;
}

export interface MatchResponseWire {
  confidence: number;
  auto_selected?: boolean;
  candidates: MatchCandidateWire[];
  /**
   * The note the router picked when it was confident enough to pick one, which
   * is the same fact `auto_selected` reports as a boolean.
   *
   * The backend has always sent it and neither the OpenAPI document nor this
   * file declared it; the contract fixtures are what surfaced that.
   */
  auto_select_id?: string | null;
}

export type SearchField = 'title' | 'alias' | 'tag' | 'body' | 'transcript';

export interface SearchHitWire {
  note_id: string;
  title: string;
  excerpt?: string;
  matched_in?: SearchField[];
}

/* ---------------------------------------------------------------------------
   Export and auth
   --------------------------------------------------------------------------- */

export interface ExportJobWire {
  id: string;
  status: 'pending' | 'running' | 'ready' | 'failed';
  url?: string | null;
  expires_at?: string | null;
  bytes?: number | null;
}

/**
 * The Cognito token set as it arrives on the wire. The ONLY place these
 * snake_case names are allowed to appear outside this file is
 * `tokens.tokenSetFromWire`, which parses them into the internal shape.
 */
export interface TokenSetWire {
  id_token: string;
  access_token: string;
  refresh_token?: string;
  expires_in: number;
  token_type: string;
}
