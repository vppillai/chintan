/**
 * The half of the flow that has to survive a full-page redirect.
 *
 * Between the authorize redirect and the callback the tab navigates away to
 * Cognito and back, so the verifier and the state cannot live in a React ref or
 * a module variable — both are gone by the time the code arrives.
 *
 * `sessionStorage` rather than `localStorage`: this is per-tab, single-use, and
 * lasts seconds. Leaving a spent verifier in `localStorage` for the life of the
 * device would be storing a secret long after it means anything.
 */

export const PENDING_AUTH_KEY = 'chintan.auth.pending.v1';

export interface PendingAuth {
  state: string;
  verifier: string;
  /** Where the user was, so signing in does not also lose their place. */
  returnTo: string;
  startedAt: number;
}

/**
 * A flow older than this is abandoned rather than resumed. It bounds how long a
 * verifier sits in storage, and stops a stale entry from a week-old tab
 * accepting a `state` it should not.
 */
export const PENDING_TTL_MS = 10 * 60_000;

function storage(): Storage | null {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage;
  } catch {
    // Storage disabled by policy. Sign-in cannot complete, and saying so is
    // better than a redirect loop that never lands.
    return null;
  }
}

function isPendingAuth(value: unknown): value is PendingAuth {
  if (typeof value !== 'object' || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate['state'] === 'string' &&
    typeof candidate['verifier'] === 'string' &&
    typeof candidate['returnTo'] === 'string' &&
    typeof candidate['startedAt'] === 'number'
  );
}

export function rememberPending(pending: PendingAuth): void {
  try {
    storage()?.setItem(PENDING_AUTH_KEY, JSON.stringify(pending));
  } catch {
    /* Quota or private mode. Surfaced when the callback finds nothing. */
  }
}

/**
 * Reads and removes in one step.
 *
 * Single-use on purpose: an authorization code is single-use at Cognito too, so
 * leaving the verifier behind only creates a second chance to replay something
 * that will be rejected anyway.
 */
export function takePending(now: number = Date.now()): PendingAuth | null {
  const store = storage();
  if (!store) return null;

  let raw: string | null = null;
  try {
    raw = store.getItem(PENDING_AUTH_KEY);
    store.removeItem(PENDING_AUTH_KEY);
  } catch {
    return null;
  }
  if (!raw) return null;

  try {
    const parsed: unknown = JSON.parse(raw);
    if (!isPendingAuth(parsed)) return null;
    if (now - parsed.startedAt > PENDING_TTL_MS) return null;
    return parsed;
  } catch {
    return null;
  }
}

export function clearPending(): void {
  try {
    storage()?.removeItem(PENDING_AUTH_KEY);
  } catch {
    /* Nothing to do. */
  }
}
