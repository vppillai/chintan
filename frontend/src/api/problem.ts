/**
 * RFC 9457 `application/problem+json` handling.
 *
 * Every non-2xx the API produces is a problem document. The one thing that
 * matters operationally is `correlation_id`: it is on the response header, in
 * the body, and in the server's log line for the same request, so a user who
 * reports "it failed" can be traced without guessing.
 */

import type { ProblemWire } from './schema.ts';

export const CORRELATION_HEADER = 'X-Correlation-Id';

/**
 * The `type` the API puts on a spend-cap 429.
 *
 * It has to be exact and it has to come from the backend, because the two 429s
 * this API produces — a rate limit and a budget — share the title "Too Many
 * Requests" and need opposite handling. `httperr.TypeSpendCapped` is the other
 * end of this string; the contract fixtures fail if the two stop matching.
 */
export const SPEND_CAP_PROBLEM_TYPE =
  'https://github.com/vppillai/chintan/blob/main/docs/api/openapi.yaml#spend-capped';

/**
 * The two biometric outcomes the client has to tell apart, machine-readably.
 *
 * Both were matched on prose until the backend gave them types — `httperr.go`
 * is the other end of these strings, exactly as it is for the spend cap, and
 * for the same reason: the alternative is behaviour that changes when somebody
 * rewords a sentence.
 *
 *   not-enrolled           this account has no credential, so there is nothing
 *                          to offer. An account fact, not a server fault.
 *   re-enrolment-required  the assertion verified and the vault behind it could
 *                          not be opened. Enrolling again is the entire fix.
 */
export const BIOMETRIC_NOT_ENROLLED_TYPE =
  'https://github.com/vppillai/chintan/blob/main/docs/api/openapi.yaml#biometric-not-enrolled';

export const BIOMETRIC_RE_ENROLMENT_TYPE =
  'https://github.com/vppillai/chintan/blob/main/docs/api/openapi.yaml#biometric-re-enrolment-required';

export type ApiErrorKind =
  /** Non-2xx carrying (or presumed to carry) a problem document. */
  | 'http'
  /** The request never completed: DNS, TCP, CORS, offline. */
  | 'network'
  /** Our own AbortController fired. */
  | 'timeout'
  /** The caller's signal aborted — a navigation, a cancelled recording. */
  | 'cancelled';

export interface ApiErrorInit {
  kind: ApiErrorKind;
  status: number;
  title: string;
  detail?: string | undefined;
  problemType?: string | undefined;
  instance?: string | undefined;
  correlationId?: string | undefined;
  currentVersion?: number | undefined;
  cause?: unknown;
}

/**
 * The single error type every call in this client throws.
 *
 * Callers branch on `status` or the predicates below, never on message text.
 * v1 classified errors by `strings.Contains` on both sides of the wire; a
 * rewording of an AWS message changed behaviour.
 */
export class ApiError extends Error {
  readonly kind: ApiErrorKind;
  readonly status: number;
  readonly title: string;
  readonly detail: string | undefined;
  readonly problemType: string;
  readonly instance: string | undefined;
  readonly correlationId: string | undefined;
  /** Present on 409 so an optimistic-concurrency loser can reconcile. */
  readonly currentVersion: number | undefined;

  constructor(init: ApiErrorInit) {
    super(init.detail ?? init.title, init.cause ? { cause: init.cause } : undefined);
    this.name = 'ApiError';
    this.kind = init.kind;
    this.status = init.status;
    this.title = init.title;
    this.detail = init.detail;
    this.problemType = init.problemType ?? 'about:blank';
    this.instance = init.instance;
    this.correlationId = init.correlationId;
    this.currentVersion = init.currentVersion;
  }

  /**
   * Safe to put in front of a user. Problem `detail` is specified as such.
   *
   * The offline sentence used to be "No connection. Your work is saved on this
   * device and will sync." — asserted by *every* failed request, including
   * reads, and true of none of them, because at the time nothing wrote to the
   * mutation queue at all. What is saved and what will sync is a fact only the
   * caller knows, so the caller says it: the note editor renders
   * `SAVE_LABELS.queued` once the edit is genuinely in IndexedDB, and the
   * offline banner counts what is actually waiting. This sentence now says only
   * what is true of any request that did not leave the device.
   */
  get userMessage(): string {
    switch (this.kind) {
      case 'network':
        return 'No connection, so that did not reach the server.';
      case 'timeout':
        return 'That took too long. It will be retried.';
      case 'cancelled':
        return 'Cancelled.';
      case 'http':
        return this.detail ?? this.title;
      default:
        return this.title;
    }
  }

  /** Worth retrying without user involvement. */
  get isRetryable(): boolean {
    if (this.kind === 'network' || this.kind === 'timeout') return true;
    if (this.kind === 'cancelled') return false;
    // 429 is retryable in general, but not when it means the spend cap: that
    // needs a human decision, not a backoff.
    if (this.status === 429) return !this.isSpendCapped;
    return this.status >= 500;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }

  get isConflict(): boolean {
    return this.status === 409;
  }

  get isNotFound(): boolean {
    return this.status === 404;
  }

  /**
   * The daily provider spend cap, which the contract makes a distinct case
   * precisely so the UI can explain it rather than saying "something failed".
   */
  get isSpendCapped(): boolean {
    if (this.status !== 429) return false;
    // The machine-readable discriminator first and exactly. This is the branch
    // the real API takes: its spend-cap body is titled "Too Many Requests" like
    // every other 429, and only `type` tells them apart.
    if (this.problemType === SPEND_CAP_PROBLEM_TYPE) return true;
    // A body with no such type is still read for the words, because a gateway
    // or an older deploy can produce one. It is a fallback, not the rule:
    // classifying by prose is what made a reworded message a behaviour change
    // in v1.
    return /spend|cap|budget/i.test(`${this.problemType} ${this.title}`);
  }

  /** True when the client is offline or the request never left the device. */
  get isOffline(): boolean {
    return this.kind === 'network';
  }
}

function asProblem(value: unknown): ProblemWire | null {
  if (typeof value !== 'object' || value === null) return null;
  const candidate = value as Record<string, unknown>;
  if (typeof candidate['title'] !== 'string' || typeof candidate['status'] !== 'number') {
    return null;
  }
  return candidate as unknown as ProblemWire;
}

const STATUS_TITLES: Record<number, string> = {
  400: 'That request was not valid',
  401: 'Your session has expired',
  403: 'Not permitted',
  404: 'Not found',
  409: 'Someone else changed this first',
  413: 'That is too large',
  429: 'Too many requests',
  500: 'Something went wrong',
  502: 'The service is unreachable',
  503: 'The service is unavailable',
  504: 'The service timed out',
};

/**
 * Builds an `ApiError` from a non-2xx response.
 *
 * A body that is not a problem document is not an error in itself — a gateway
 * can return HTML for a 502 — so this degrades to a status-derived title
 * rather than surfacing whatever text happened to arrive. v1 rendered raw
 * upstream bodies to the user, which is how DynamoDB table names reached the
 * screen.
 */
export async function problemFromResponse(response: Response): Promise<ApiError> {
  const correlationId =
    response.headers.get(CORRELATION_HEADER) ??
    response.headers.get(CORRELATION_HEADER.toLowerCase()) ??
    undefined;

  let problem: ProblemWire | null = null;
  try {
    const contentType = response.headers.get('content-type') ?? '';
    if (contentType.includes('json')) {
      problem = asProblem(await response.json());
    }
  } catch {
    /* Unparseable body. Fall through to the status-derived title. */
  }

  return new ApiError({
    kind: 'http',
    status: problem?.status ?? response.status,
    title: problem?.title ?? STATUS_TITLES[response.status] ?? 'Request failed',
    detail: problem?.detail,
    problemType: problem?.type,
    instance: problem?.instance,
    correlationId: problem?.correlation_id ?? correlationId,
    currentVersion: problem?.current_version,
  });
}

export function networkError(cause: unknown): ApiError {
  return new ApiError({
    kind: 'network',
    status: 0,
    title: 'No connection',
    cause,
  });
}

export function timeoutError(ms: number): ApiError {
  return new ApiError({
    kind: 'timeout',
    status: 0,
    title: 'Request timed out',
    detail: `No response within ${Math.round(ms / 1000)}s.`,
  });
}

export function cancelledError(): ApiError {
  return new ApiError({ kind: 'cancelled', status: 0, title: 'Cancelled' });
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError;
}
