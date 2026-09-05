/**
 * The one HTTP path in the app.
 *
 * Everything the contract makes non-optional lives here so no call site can
 * forget it: bearer injection, an `Idempotency-Key` on every mutating request,
 * an abort timeout on every call, bounded retry with jittered backoff, and
 * problem+json parsed into a typed `ApiError` carrying the correlation id.
 */

import { config } from '@/config/env.ts';

import {
  ApiError,
  cancelledError,
  networkError,
  problemFromResponse,
  retryAfterMs,
  timeoutError,
} from './problem.ts';
import type { Session } from './session.ts';
import { bearerHeader, needsRefresh } from './tokens.ts';

export interface RetryPolicy {
  /** Attempts *after* the first. 0 disables retry. */
  maxRetries: number;
  baseDelayMs: number;
  maxDelayMs: number;
}

export const DEFAULT_RETRY: RetryPolicy = {
  maxRetries: 3,
  baseDelayMs: 400,
  maxDelayMs: 8_000,
};

/** No retries: for anything the user is waiting on interactively. */
export const NO_RETRY: RetryPolicy = { maxRetries: 0, baseDelayMs: 0, maxDelayMs: 0 };

export const DEFAULT_TIMEOUT_MS = 15_000;

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  /** Serialised as JSON. Use `rawBody` for anything else. */
  body?: unknown;
  rawBody?: BodyInit | undefined;
  query?: Record<string, string | number | boolean | undefined | null>;
  headers?: Record<string, string>;
  timeoutMs?: number;
  retry?: RetryPolicy;
  /** Caller cancellation — a navigation away, an abandoned recording. */
  signal?: AbortSignal | undefined;
  /**
   * Reuse a caller-supplied idempotency key so a retry of a *user-visible*
   * action replays the original response instead of creating a second note.
   * Omitted means one is generated per logical request (and held across this
   * request's internal retries, which is the entire point).
   */
  idempotencyKey?: string | undefined;
  /** Skip bearer injection. Only the health routes. */
  anonymous?: boolean;
}

const MUTATING = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  // Contract requires 8–128 chars. Only reached on ancient WebViews.
  return `idem-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

function buildUrl(
  baseUrl: string,
  path: string,
  query: RequestOptions['query'],
): string {
  const url = new URL(`${baseUrl}${path}`);
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value === undefined || value === null || value === '') continue;
    url.searchParams.set(key, String(value));
  }
  return url.toString();
}

function backoffDelay(attempt: number, policy: RetryPolicy): number {
  const exponential = policy.baseDelayMs * 2 ** attempt;
  const capped = Math.min(exponential, policy.maxDelayMs);
  // Full jitter. Without it, every client that failed together retries
  // together, which is how a recovering service is knocked back over.
  return Math.random() * capped;
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(cancelledError());
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(cancelledError());
    };
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

export class ApiClient {
  constructor(
    private readonly session: Session,
    private readonly baseUrl: string = config.apiUrl,
    private readonly fetchImpl: typeof fetch = (...args) => globalThis.fetch(...args),
  ) {}

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const response = await this.send(path, options);
    if (response.status === 204 || response.status === 205) {
      return undefined as T;
    }
    const contentType = response.headers.get('content-type') ?? '';
    if (!contentType.includes('json')) {
      return undefined as T;
    }
    return (await response.json()) as T;
  }

  /** Same pipeline, but hands back the `Response` for callers that need headers. */
  async send(path: string, options: RequestOptions = {}): Promise<Response> {
    const method = options.method ?? 'GET';
    // Mutating requests are retried too. That is only safe because every one
    // of them carries an idempotency key held constant across the attempts
    // below, so a replay returns the original response instead of acting twice.
    const policy = options.retry ?? DEFAULT_RETRY;

    // Generated ONCE, outside the retry loop. A key regenerated per attempt
    // would make every retry a fresh logical request, which is exactly the
    // duplicate-note defect the contract's idempotency rule exists to prevent.
    const idempotencyKey = MUTATING.has(method)
      ? (options.idempotencyKey ?? newIdempotencyKey())
      : undefined;

    let refreshed = false;
    let lastError: ApiError | null = null;

    for (let attempt = 0; attempt <= policy.maxRetries; attempt += 1) {
      if (options.signal?.aborted) throw cancelledError();

      let response: Response;
      try {
        response = await this.attempt(path, options, method, idempotencyKey);
      } catch (error) {
        const apiError = error as ApiError;
        if (apiError.kind === 'cancelled') throw apiError;
        lastError = apiError;
        if (attempt === policy.maxRetries || !apiError.isRetryable) throw apiError;
        await sleep(backoffDelay(attempt, policy), options.signal);
        continue;
      }

      if (response.ok) return response;

      const error = await problemFromResponse(response);

      /*
       * A 401 attempts a refresh BEFORE anything reaches the user, exactly
       * once per request. If the refresh succeeds we replay the same attempt
       * with the same idempotency key; if it fails, the original 401 surfaces
       * and the UI decides what to do about it. Nothing here reloads the page.
       */
      if (error.isUnauthorized && !refreshed && !options.anonymous) {
        refreshed = true;
        try {
          await this.session.refresh();
          attempt -= 1; // The replay is not one of the retry budget's attempts.
          continue;
        } catch (refreshError) {
          throw refreshError instanceof ApiError ? refreshError : error;
        }
      }

      lastError = error;
      if (attempt === policy.maxRetries || !error.isRetryable) throw error;

      const serverDelay = retryAfterMs(response);
      await sleep(serverDelay ?? backoffDelay(attempt, policy), options.signal);
    }

    throw lastError ?? new ApiError({ kind: 'network', status: 0, title: 'Request failed' });
  }

  /** One network attempt, with its own timeout. */
  private async attempt(
    path: string,
    options: RequestOptions,
    method: string,
    idempotencyKey: string | undefined,
  ): Promise<Response> {
    const headers = new Headers(options.headers);

    if (!options.anonymous) {
      // Refresh proactively when the token is within the skew window, so the
      // common case never produces a user-visible 401 at all.
      const current = this.session.current();
      if (current && needsRefresh(current)) {
        try {
          await this.session.refresh();
        } catch {
          /* Fall through and let the 401 path handle it. */
        }
      }
      const bearer = bearerHeader(this.session.current());
      if (bearer) headers.set('Authorization', bearer);
    }

    if (idempotencyKey) headers.set('Idempotency-Key', idempotencyKey);

    let body = options.rawBody;
    if (options.body !== undefined) {
      headers.set('Content-Type', 'application/json');
      body = JSON.stringify(options.body);
    }

    const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      controller.abort(timeoutError(timeoutMs));
    }, timeoutMs);

    const onCallerAbort = () => {
      controller.abort(cancelledError());
    };
    options.signal?.addEventListener('abort', onCallerAbort, { once: true });

    try {
      return await this.fetchImpl(buildUrl(this.baseUrl, path, options.query), {
        method,
        headers,
        ...(body === undefined ? {} : { body }),
        signal: controller.signal,
        credentials: 'omit',
      });
    } catch (cause) {
      if (options.signal?.aborted) throw cancelledError();
      if (controller.signal.aborted) throw timeoutError(timeoutMs);
      throw networkError(cause);
    } finally {
      clearTimeout(timer);
      options.signal?.removeEventListener('abort', onCallerAbort);
    }
  }
}
