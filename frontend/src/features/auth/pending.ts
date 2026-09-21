/**
 * The half of the flow that has to survive a full-page redirect.
 *
 * Between the authorize redirect and the callback the tab navigates away to
 * Cognito and back, so the verifier and the state cannot live in a React ref or
 * a module variable — both are gone by the time the code arrives.
 *
 * `localStorage`, not `sessionStorage`. It was `sessionStorage` on the
 * argument that the flow is per-tab and lasts seconds — and on an iOS
 * home-screen app that argument fails in the one place it matters: switching
 * to Messages to read the MFA code routinely gets the PWA's process killed,
 * and `sessionStorage` dies with it. The redirect back then found no pending
 * flow and said "That sign-in could not be completed. Please try again." to
 * someone who had done everything right. `localStorage` survives the kill.
 *
 * The secret is still bounded: `PENDING_TTL_MS` refuses anything older than
 * ten minutes, `takePending` removes the entry as it reads it, and a verifier
 * is worthless once its code has been exchanged or has expired at Cognito —
 * so nothing sits in storage longer than it means anything.
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
    return typeof localStorage === 'undefined' ? null : localStorage;
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

function parsePending(raw: string | null, now: number): PendingAuth | null {
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

/**
 * Whether the flow this device started, and is still waiting on, is the one
 * `state` names — without consuming it.
 *
 * This is what tells Cognito's answer apart from a link someone was sent:
 * the hosted UI redirects here only in reply to a request from here, and
 * echoes the `state` that request carried. An `error` on the query string
 * with no flow behind it, or with a different state — the pending entry is in
 * `localStorage`, so another tab's genuine sign-in is one this tab can see —
 * is not an outcome to report, nor a flow to end.
 */
export function hasPendingFlow(state: string | null, now: number = Date.now()): boolean {
  const store = storage();
  if (!store) return false;
  try {
    return parsePending(store.getItem(PENDING_AUTH_KEY), now)?.state === state;
  } catch {
    return false;
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

  let raw: string | null;
  try {
    raw = store.getItem(PENDING_AUTH_KEY);
    store.removeItem(PENDING_AUTH_KEY);
  } catch {
    return null;
  }
  return parsePending(raw, now);
}

export function clearPending(): void {
  try {
    storage()?.removeItem(PENDING_AUTH_KEY);
  } catch {
    /* Nothing to do. */
  }
}
