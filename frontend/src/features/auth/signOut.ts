/**
 * Signing out, and what that has to mean.
 *
 * Sign-out on this product is not "forget my token". It is the control someone
 * reaches for when the phone is about to be handed over or has been lost, so it
 * has to leave the device with nothing on it and leave Cognito with no session
 * to sign the same person straight back in.
 *
 * Three things, in this order:
 *
 *   1. Drop the token set.
 *   2. Empty the query cache and IndexedDB.
 *   3. Hand the browser to Cognito's `/logout`.
 *
 * The redirect is last because it ends this document. Nothing here talks to
 * the API: every credential lives with Cognito, so ending the hosted-UI session
 * is the whole of the server-side half.
 */

import type { QueryClient } from '@tanstack/react-query';

import type { Session } from '@/api/session.ts';
import { unconfirmedCaptures } from '@/features/capture/buffer.ts';
import { clearAllLocalData } from '@/offline/db.ts';
import { count as queuedCount } from '@/offline/queue.ts';
import { config } from '@/config/env.ts';

import { logoutUrl, redirectUri } from './oauth.ts';
import { clearPending } from './pending.ts';

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
}

export async function performSignOut({
  session,
  queryClient,
  navigate = (url) => {
    window.location.assign(url);
  },
}: SignOutInput): Promise<void> {
  session.clear();
  clearPending();
  queryClient.clear();

  // The cached note corpus, the buffered audio, and the mutation queue all go.
  await clearAllLocalData().catch(() => {
    /* Storage denied; the token is already gone, which is the security-relevant half. */
  });

  // Unconfigured build (or a test): there is no hosted UI to end a session at,
  // and navigating to `/logout` on an empty origin would strand the user.
  if (config.cognitoDomain.length > 0 && config.clientId.length > 0) {
    navigate(logoutUrl(redirectUri()));
  }
}
