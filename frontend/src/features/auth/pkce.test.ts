import { describe, expect, it, vi } from 'vitest';

import {
  base64UrlEncode,
  challengeFor,
  createState,
  createVerifier,
  randomUrlSafe,
} from './pkce.ts';

describe('the flow is seeded from a CSPRNG, not Math.random', () => {
  it('draws the verifier from crypto.getRandomValues', () => {
    /*
     * v1 built its OAuth `state` from `Math.random().toString(36)` — a
     * predictable PRNG, seeded per page load — which makes the one parameter
     * whose whole job is to be unguessable, guessable.
     */
    const random = vi.spyOn(Math, 'random');
    const strong = vi.spyOn(crypto, 'getRandomValues');

    createVerifier();

    expect(strong).toHaveBeenCalled();
    expect(random, 'Math.random must not appear anywhere in the flow').not.toHaveBeenCalled();
  });

  it('draws the state from crypto.getRandomValues', () => {
    const random = vi.spyOn(Math, 'random');
    const strong = vi.spyOn(crypto, 'getRandomValues');

    createState();

    expect(strong).toHaveBeenCalled();
    expect(random).not.toHaveBeenCalled();
  });

  it('produces a different value every time', () => {
    const seen = new Set(Array.from({ length: 50 }, () => createVerifier()));
    expect(seen.size).toBe(50);
  });
});

describe('the verifier and challenge satisfy RFC 7636', () => {
  it('keeps the verifier inside the 43–128 character range', () => {
    const verifier = createVerifier();
    expect(verifier.length).toBeGreaterThanOrEqual(43);
    expect(verifier.length).toBeLessThanOrEqual(128);
  });

  it('uses only unreserved URL-safe characters, unpadded', () => {
    // Plain base64 would round-trip through a query string as `+` → space and
    // `/` → a path separator, and the server would compare a different string.
    for (const value of [createVerifier(), createState()]) {
      expect(value).toMatch(/^[A-Za-z0-9\-_]+$/);
      expect(value).not.toContain('=');
    }
  });

  it('derives the challenge as base64url(SHA-256(verifier))', async () => {
    const verifier = 'dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk';
    // The worked example from RFC 7636 Appendix B.
    expect(await challengeFor(verifier)).toBe('E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM');
  });

  it('never sends the verifier itself as the challenge', async () => {
    // Which is what `plain` does, putting the secret in the address bar and in
    // every log between the browser and Cognito.
    const verifier = createVerifier();
    expect(await challengeFor(verifier)).not.toBe(verifier);
  });
});

describe('base64UrlEncode', () => {
  it('encodes without padding and without + or /', () => {
    expect(base64UrlEncode(new Uint8Array([251, 255, 190]))).toBe('-_--');
    expect(base64UrlEncode(new Uint8Array([0]))).toBe('AA');
  });

  it('is empty for no bytes', () => {
    expect(base64UrlEncode(new Uint8Array())).toBe('');
    expect(randomUrlSafe(0)).toBe('');
  });
});
