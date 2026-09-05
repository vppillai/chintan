/**
 * Who is signed in, read from the id token the session already holds.
 *
 * The You screen opens with the account, and the only thing the app knows
 * about the account is the id token Cognito minted: its `email` claim (the
 * `email` scope is requested in `oauth.ts`) and `auth_time`, the moment the
 * sign-in happened. Nothing is fetched for this — a round trip to the user
 * pool for a name the token already carries would be a network call on every
 * visit to You for no new information.
 *
 * Decoding is deliberately forgiving. A token is three base64url segments and
 * the middle one is JSON, but the app never verifies it here — the API does
 * that on every request — and the e2e stub's token is not a JWT at all. Any
 * shape that is not what Cognito sends yields an identity with nothing in it,
 * and the header says "Signed in" without a name rather than failing.
 */

export interface SignedInIdentity {
  /** The account's email, or null when the token does not carry one. */
  email: string | null;
  /** When this session was signed in, or null when the token does not say. */
  signedInAt: Date | null;
}

const EMPTY: SignedInIdentity = { email: null, signedInAt: null };

export function identityFromIdToken(token: string | null | undefined): SignedInIdentity {
  const claims = decodePayload(token);
  if (!claims) return EMPTY;
  const email = typeof claims['email'] === 'string' && claims['email'].trim() ? claims['email'].trim() : null;
  const authTime = claims['auth_time'];
  const signedInAt =
    typeof authTime === 'number' && Number.isFinite(authTime) ? new Date(authTime * 1000) : null;
  return { email, signedInAt };
}

function decodePayload(token: string | null | undefined): Record<string, unknown> | null {
  if (!token) return null;
  const segments = token.split('.');
  if (segments.length !== 3 || !segments[1]) return null;
  try {
    const base64 = segments[1].replace(/-/g, '+').replace(/_/g, '/');
    const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4);
    // The claims are UTF-8; `atob` yields Latin-1 bytes, so decode them as such.
    const bytes = Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
    const parsed: unknown = JSON.parse(new TextDecoder().decode(bytes));
    return parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

/**
 * The one letter in the roundel: the first character of the email, in the
 * locale's upper case so a non-Latin address gets its own script's capital.
 */
export function initialFor(email: string | null): string | null {
  const first = email?.trim().charAt(0);
  return first ? first.toLocaleUpperCase() : null;
}

/**
 * "Signed in today", "… yesterday", "… 3 days ago", "… 2 weeks ago": how long
 * this session has been running, coarsely, because the point is a sense of
 * how stale the sign-in is rather than a timestamp. `null` when the token
 * does not say, and the header falls back to a sentence without a time.
 */
export function signedInLabel(at: Date | null, now: Date = new Date()): string | null {
  if (!at || Number.isNaN(at.getTime())) return null;
  const days = Math.max(0, Math.floor((now.getTime() - at.getTime()) / 86_400_000));
  if (days === 0) return 'Signed in today';
  if (days === 1) return 'Signed in yesterday';
  if (days < 14) return `Signed in ${String(days)} days ago`;
  const weeks = Math.round(days / 7);
  if (days < 60) return `Signed in ${String(weeks)} weeks ago`;
  const months = Math.round(days / 30);
  return months === 1 ? 'Signed in a month ago' : `Signed in ${String(months)} months ago`;
}
