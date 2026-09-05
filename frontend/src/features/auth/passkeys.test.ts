import { describe, expect, it } from 'vitest';

import type { AppConfig } from '@/config/env.ts';

import {
  PASSKEY_NUDGE_KEY,
  dismissNudge,
  nudgeDismissed,
  passkeyAddUrl,
  passkeysSupported,
  readPasskeyResult,
} from './passkeys.ts';

const SETTINGS: AppConfig = {
  apiUrl: 'https://api.test',
  userPoolId: 'us-west-2_test',
  clientId: 'client-abc',
  cognitoDomain: 'https://cognito.test',
  instance: 'dev',
  version: 'test',
  appName: 'Chintan',
  appDescription: 'Speak a thought. It files itself.',
};

/**
 * The hand-off to the managed login's own passkey page.
 *
 * Verified against prod on 2026-09-04: `/passkeys/add` wants exactly
 * `client_id` and `redirect_uri`, refuses the request with "Missing required
 * parameter redirect_uri" without the second, and returns to the redirect with
 * `?result=success` or `?result=invalid_session`.
 */
describe('the passkey page URL', () => {
  it('names the client and the registered callback, and nothing else', () => {
    const url = new URL(passkeyAddUrl('https://app.test/chintan/dev/', SETTINGS));
    expect(url.origin).toBe('https://cognito.test');
    expect(url.pathname).toBe('/passkeys/add');
    expect(Object.fromEntries(url.searchParams)).toEqual({
      client_id: 'client-abc',
      redirect_uri: 'https://app.test/chintan/dev/',
    });
  });
});

describe('reading what the managed login answered', () => {
  it('recognises the two outcomes it documents', () => {
    expect(readPasskeyResult('?result=success')).toBe('success');
    expect(readPasskeyResult('?result=invalid_session')).toBe('invalid_session');
  });

  it('reports an unknown value as an outcome rather than dropping it', () => {
    // A return from that page with a value this code does not know is still a
    // return from that page; "nothing happened" would be the wrong sentence.
    expect(readPasskeyResult('?result=cancelled')).toBe('other');
  });

  it('is silent when the URL carries no answer', () => {
    expect(readPasskeyResult('')).toBeNull();
    expect(readPasskeyResult('?q=roof&view=archived')).toBeNull();
  });
});

describe('whether this browser can take part at all', () => {
  it('needs WebAuthn, which jsdom does not have', () => {
    expect(passkeysSupported(window)).toBe(false);
    expect(passkeysSupported(undefined)).toBe(false);
  });

  it('is satisfied by a PublicKeyCredential constructor', () => {
    const fake = { PublicKeyCredential: class {} } as unknown as Window;
    expect(passkeysSupported(fake)).toBe(true);
  });
});

describe('the nudge is remembered per device', () => {
  it('starts undismissed and stays dismissed once answered', () => {
    expect(nudgeDismissed(window.localStorage)).toBe(false);
    dismissNudge('not-now', window.localStorage);
    expect(nudgeDismissed(window.localStorage)).toBe(true);
    expect(window.localStorage.getItem(PASSKEY_NUDGE_KEY)).toBe('not-now');
  });

  it('records a passkey having been added as a dismissal too', () => {
    dismissNudge('added', window.localStorage);
    expect(nudgeDismissed(window.localStorage)).toBe(true);
  });

  it('reads a denied storage as dismissed rather than nagging on every visit', () => {
    const denied = {
      getItem: () => {
        throw new DOMException('denied', 'SecurityError');
      },
      setItem: () => {
        throw new DOMException('denied', 'SecurityError');
      },
    } as unknown as Storage;
    expect(nudgeDismissed(denied)).toBe(true);
    expect(() => {
      dismissNudge('not-now', denied);
    }).not.toThrow();
  });
});
