/**
 * PKCE, the way RFC 7636 specifies it.
 *
 * Every value here is generated with `crypto.getRandomValues`, and
 * `Math.random` must not appear in this file: it is a predictable PRNG seeded
 * per page load, and drawing `state` from it makes the one parameter whose
 * entire job is to be unguessable, guessable.
 *
 * The verifier is the secret half of the exchange. Without it, an authorization
 * code intercepted in transit (a shared device's history, a malicious app
 * claiming the redirect, a proxy log) is redeemable by whoever holds it; with
 * it, the code is useless to anyone who did not start the flow.
 */

/** 32 bytes: the top of RFC 7636's 43–128 character range once encoded. */
const VERIFIER_BYTES = 32;
/** 16 bytes is 128 bits of state entropy, which is plenty for CSRF. */
const STATE_BYTES = 16;

/**
 * base64url without padding, as RFC 7636 section 4.2 requires.
 *
 * Plain base64 would round-trip through a query string as `+` → space and `/`
 * → a path separator, and the server would compare a different string than the
 * one we sent.
 */
export function base64UrlEncode(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Cryptographically random, URL-safe. The only randomness source in the flow. */
export function randomUrlSafe(byteLength: number): string {
  const bytes = new Uint8Array(byteLength);
  crypto.getRandomValues(bytes);
  return base64UrlEncode(bytes);
}

export function createVerifier(): string {
  return randomUrlSafe(VERIFIER_BYTES);
}

export function createState(): string {
  return randomUrlSafe(STATE_BYTES);
}

/**
 * S256, never `plain`.
 *
 * `plain` puts the verifier itself in the authorize URL, which defeats the
 * point: the value is then in browser history and in every log between here and
 * Cognito.
 */
export async function challengeFor(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  return base64UrlEncode(new Uint8Array(digest));
}

export const CODE_CHALLENGE_METHOD = 'S256';
