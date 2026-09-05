import { describe, expect, it } from 'vitest';

import { identityFromIdToken, initialFor, signedInLabel } from './identity.ts';

/** A token shaped as Cognito mints one: header, claims, signature, unverified here. */
function tokenWith(claims: Record<string, unknown>): string {
  const encode = (value: unknown): string =>
    Buffer.from(JSON.stringify(value), 'utf8')
      .toString('base64')
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '');
  return `${encode({ alg: 'RS256', kid: 'k' })}.${encode(claims)}.signature`;
}

describe('the signed-in identity, read from the id token', () => {
  it('takes the email and the sign-in time from the claims', () => {
    const identity = identityFromIdToken(
      tokenWith({ email: 'owner@example.com', auth_time: 1_767_225_600, sub: 'abc' }),
    );
    expect(identity.email).toBe('owner@example.com');
    expect(identity.signedInAt?.toISOString()).toBe('2026-01-01T00:00:00.000Z');
  });

  it('survives an address in another script, which base64 alone would mangle', () => {
    expect(identityFromIdToken(tokenWith({ email: 'വൈശാഖ്@example.com' })).email).toBe(
      'വൈശാഖ്@example.com',
    );
    expect(initialFor('വൈശാഖ്@example.com')).toBe('വ');
  });

  it('yields nothing, rather than throwing, for anything that is not a Cognito token', () => {
    // The e2e stub's token, a bare string, an absent session, a token whose
    // middle segment is not JSON, and one whose claims are not an object.
    for (const raw of ['e2e-id-token', '', null, undefined, 'a.b.c', `x.${btoa('[1]')}.y`]) {
      expect(identityFromIdToken(raw)).toEqual({ email: null, signedInAt: null });
    }
    expect(identityFromIdToken(tokenWith({ email: '  ', auth_time: 'noon' }))).toEqual({
      email: null,
      signedInAt: null,
    });
  });
});

describe('the roundel and the sign-in line', () => {
  it('shows the first letter of the address as a capital, or nothing', () => {
    expect(initialFor('vpillai@example.com')).toBe('V');
    expect(initialFor(null)).toBeNull();
    expect(initialFor('  ')).toBeNull();
  });

  it('says how old the session is in coarse steps', () => {
    const now = new Date('2026-09-05T12:00:00Z');
    const daysAgo = (days: number): Date => new Date(now.getTime() - days * 86_400_000);
    expect(signedInLabel(daysAgo(0), now)).toBe('Signed in today');
    expect(signedInLabel(daysAgo(1), now)).toBe('Signed in yesterday');
    expect(signedInLabel(daysAgo(3), now)).toBe('Signed in 3 days ago');
    expect(signedInLabel(daysAgo(20), now)).toBe('Signed in 3 weeks ago');
    expect(signedInLabel(daysAgo(70), now)).toBe('Signed in 2 months ago');
    expect(signedInLabel(null, now)).toBeNull();
  });
});
