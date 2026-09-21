/**
 * Signing out, and what that has to mean.
 *
 * Sign-out on this product is not "forget my token". It is the control someone
 * reaches for when the phone is about to be handed over or has been lost, so it
 * has to leave the device with nothing on it and leave Cognito with no session
 * to sign the same person straight back in.
 *
 * Five things, in this order:
 *
 *   1. Drop the token set.
 *   2. Empty the query cache and IndexedDB.
 *   3. Empty the app's Web Storage of anything that is one person's.
 *   4. Revoke the refresh token at Cognito, best-effort.
 *   5. Hand the browser to Cognito's `/logout`.
 *
 * The redirect is last because it ends this document. The revoke is what
 * makes step 1 mean anything for the phone this exists for: `/logout` ends the
 * hosted UI's cookie and nothing else, so a refresh token copied out of
 * storage before the sign-out would otherwise go on minting access tokens for
 * its full thirty days. Nothing here talks to the API; every credential lives
 * with Cognito.
 */

import type { QueryClient } from '@tanstack/react-query';

import type { Session } from '@/api/session.ts';
import { COST_NOTE_KEY } from '@/features/ask/costNote.ts';
import { unconfirmedCaptures } from '@/features/capture/buffer.ts';
import { DISMISSED_KEY } from '@/features/capture/dismissed.ts';
import { TARGETED_KEY } from '@/features/capture/targeted.ts';
import { clearAllLocalData } from '@/offline/db.ts';
import { count as queuedCount } from '@/offline/queue.ts';
import { config } from '@/config/env.ts';

import { logoutUrl, redirectUri } from './oauth.ts';
import { clearPending } from './pending.ts';

/**
 * Every key the app writes starts with this. All of `sessionStorage` under it
 * is one person's — the Ask thread with its questions, answers and note
 * titles; which tab of which note was open — and Cognito's `/logout` and back
 * is the same tab, so the next person to sign in there would otherwise find
 * it. `localStorage` is sorted by hand below.
 */
const APP_STORAGE_PREFIX = 'chintan.';

/**
 * What in `localStorage` names a person rather than the device. The filing
 * sets are per-device decisions, but the ids in them are one person's
 * captures; the Ask cost note was shown to the person paying, and the next
 * one should hear it once too. The theme and the passkey nudge stay: a theme
 * is the device's, and the nudge is about a passkey on this device. Tokens
 * and the pending sign-in have their own clears above.
 */
const PERSONAL_LOCAL_KEYS: readonly string[] = [COST_NOTE_KEY, DISMISSED_KEY, TARGETED_KEY];

/** Storage denied or absent (a test without a window) is simply nothing to clear. */
export function clearPersonalStorage(): void {
  try {
    const keys: string[] = [];
    for (let i = 0; i < sessionStorage.length; i += 1) {
      const key = sessionStorage.key(i);
      if (key?.startsWith(APP_STORAGE_PREFIX)) keys.push(key);
    }
    for (const key of keys) sessionStorage.removeItem(key);
  } catch {
    /* Nothing to do. */
  }
  try {
    for (const key of PERSONAL_LOCAL_KEYS) localStorage.removeItem(key);
  } catch {
    /* Nothing to do. */
  }
}

/** Work that exists on this device and nowhere else. */
export interface UnsentWork {
  /** Recordings the server has never acknowledged. */
  captures: number;
  /** Edits made offline and not yet flushed. */
  queued: number;
}

export function hasUnsentWork(work: UnsentWork): boolean {
  return work.captures > 0 || work.queued > 0;
}

/**
 * What signing out would destroy.
 *
 * Read *before* the confirm dialog, because an unconfirmed capture is the one
 * artifact in the product that exists in exactly one place: the audio is in
 * IndexedDB on this device and has never reached the server. Signing out
 * without saying so would delete a recording the user believes they made.
 */
export async function readUnsentWork(): Promise<UnsentWork> {
  const [captures, queued] = await Promise.all([
    unconfirmedCaptures()
      .then((records) => records.length)
      .catch(() => 0),
    queuedCount().catch(() => 0),
  ]);
  return { captures, queued };
}

export interface SignOutInput {
  session: Session;
  queryClient: QueryClient;
  /** Injected by tests; production navigates for real. */
  navigate?: (url: string) => void;
  fetchImpl?: typeof fetch;
}

export async function performSignOut({
  session,
  queryClient,
  navigate = (url) => {
    window.location.assign(url);
  },
  fetchImpl = globalThis.fetch.bind(globalThis),
}: SignOutInput): Promise<void> {
  // Read before the clear takes it away; sent only once the device is clean.
  const refreshToken = session.current()?.refreshToken ?? null;

  session.clear();
  clearPending();
  queryClient.clear();

  // The cached note corpus, the buffered audio, and the mutation queue all go.
  await clearAllLocalData().catch(() => {
    /* Storage denied; the token is already gone, which is the security-relevant half. */
  });
  clearPersonalStorage();

  // Unconfigured build (or a test): there is no hosted UI to end a session at,
  // and navigating to `/logout` on an empty origin would strand the user.
  if (config.cognitoDomain.length > 0 && config.clientId.length > 0) {
    if (refreshToken) await revokeRefreshToken(refreshToken, fetchImpl);
    navigate(logoutUrl(redirectUri()));
  }
}

/** A captive portal or a hung connection must not hold the sign-out hostage. */
const REVOKE_TIMEOUT_MS = 5_000;

/**
 * `POST /oauth2/revoke`, RFC 7009 as Cognito serves it for a public client:
 * the token and the client id, form-encoded, no secret. Cognito then refuses
 * the refresh grant and every token that grant produced; the id token already
 * in a copier's hands still passes the API's stateless JWT check until it
 * expires, an hour at most, and nothing shorter is available without a
 * server-side denylist.
 *
 * Best-effort on purpose. The device is already clean by the time this runs,
 * and a sign-out with no connection still has to finish: an offline failure,
 * a refusal or the timeout all fall through to `/logout`.
 */
async function revokeRefreshToken(refreshToken: string, fetchImpl: typeof fetch): Promise<void> {
  try {
    await fetchImpl(`${config.cognitoDomain}/oauth2/revoke`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ token: refreshToken, client_id: config.clientId }),
      signal: AbortSignal.timeout(REVOKE_TIMEOUT_MS),
    });
  } catch {
    /* Offline, refused or timed out: the token ages out at Cognito on its own. */
  }
}
