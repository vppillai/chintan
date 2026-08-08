import { describe, expect, it, vi } from 'vitest';

import { ApiError } from '@/api/problem.ts';
import type { AppConfig } from '@/config/env.ts';

import {
  authorizeUrl,
  exchangeCode,
  logoutUrl,
  readCallbackParams,
  redirectUri,
} from './oauth.ts';

const SETTINGS: AppConfig = {
  apiUrl: 'https://api.test',
  userPoolId: 'us-west-2_test',
  clientId: 'client-abc',
  cognitoDomain: 'https://chintan-dev-prod-338186951935.auth.us-west-2.amazoncognito.com',
  instance: 'dev',
  version: 'test-sha',
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

describe('the authorize request', () => {
  it('asks for a code with an S256 challenge and the openid scope', () => {
    const url = new URL(
      authorizeUrl({
        state: 'st-1',
        challenge: 'ch-1',
        redirectUri: 'https://app.test/repo/dev/',
        settings: SETTINGS,
      }),
    );

    expect(url.origin + url.pathname).toBe(`${SETTINGS.cognitoDomain}/oauth2/authorize`);
    expect(url.searchParams.get('response_type')).toBe('code');
    expect(url.searchParams.get('client_id')).toBe('client-abc');
    expect(url.searchParams.get('code_challenge')).toBe('ch-1');
    expect(url.searchParams.get('code_challenge_method')).toBe('S256');
    expect(url.searchParams.get('state')).toBe('st-1');
    expect(url.searchParams.get('scope')?.split(' ')).toContain('openid');
    expect(url.searchParams.get('redirect_uri')).toBe('https://app.test/repo/dev/');
  });

  it('never puts the verifier in the URL', () => {
    const url = authorizeUrl({
      state: 'st-1',
      challenge: 'ch-1',
      redirectUri: 'https://app.test/',
      settings: SETTINGS,
    });
    expect(url).not.toContain('code_verifier');
  });
});

describe('the redirect URI is the app base, because that is what is registered', () => {
  it('is the origin plus the build base, with its trailing slash', () => {
    // `infrastructure/template.yaml` registers exactly
    // `https://<host>/<repo>/<instance>/` as the client's only CallbackURL, and
    // Cognito matches `redirect_uri` byte for byte. A `/auth/callback` path
    // would fail with `redirect_mismatch` before a login form ever rendered.
    expect(redirectUri('https://app.test')).toBe('https://app.test/');
  });
});

describe('the logout request', () => {
  it('names the client and where to come back to', () => {
    const url = new URL(logoutUrl('https://app.test/repo/dev/', SETTINGS));
    expect(url.origin + url.pathname).toBe(`${SETTINGS.cognitoDomain}/logout`);
    expect(url.searchParams.get('client_id')).toBe('client-abc');
    expect(url.searchParams.get('logout_uri')).toBe('https://app.test/repo/dev/');
  });
});

describe('reading what came back on the query string', () => {
  it('reads a code and its state', () => {
    expect(readCallbackParams('?code=abc&state=xyz')).toEqual({
      kind: 'code',
      code: 'abc',
      state: 'xyz',
    });
  });

  it('reads a refusal, which is a real outcome and not an edge case', () => {
    expect(readCallbackParams('?error=access_denied&error_description=User+said+no')).toEqual({
      kind: 'error',
      error: 'access_denied',
      description: 'User said no',
    });
  });

  it('is null for an ordinary visit', () => {
    expect(readCallbackParams('')).toBeNull();
    expect(readCallbackParams('?q=roof')).toBeNull();
  });

  it('refuses a code with no state, which cannot be checked against anything', () => {
    expect(readCallbackParams('?code=abc')).toBeNull();
  });
});

describe('redeeming the code', () => {
  it('posts the authorization_code grant with the verifier', async () => {
    const fetchImpl = vi.fn<typeof fetch>(async () =>
      json({
        id_token: 'id-1',
        access_token: 'access-1',
        refresh_token: 'refresh-1',
        expires_in: 3600,
        token_type: 'Bearer',
      }),
    );

    const tokens = await exchangeCode({
      code: 'the-code',
      verifier: 'the-verifier',
      redirectUri: 'https://app.test/',
      settings: SETTINGS,
      fetchImpl,
      now: 1_000,
    });

    const [url, init] = fetchImpl.mock.calls[0] ?? [];
    expect(String(url)).toBe(`${SETTINGS.cognitoDomain}/oauth2/token`);
    expect((init?.headers as Record<string, string>)['Content-Type']).toBe(
      'application/x-www-form-urlencoded',
    );

    const body = new URLSearchParams(String(init?.body));
    expect(body.get('grant_type')).toBe('authorization_code');
    expect(body.get('code')).toBe('the-code');
    expect(body.get('code_verifier')).toBe('the-verifier');
    expect(body.get('client_id')).toBe('client-abc');
    // Must match the authorize request exactly, or Cognito rejects the grant.
    expect(body.get('redirect_uri')).toBe('https://app.test/');

    expect(tokens).toEqual({
      idToken: 'id-1',
      accessToken: 'access-1',
      refreshToken: 'refresh-1',
      expiresAt: 1_000 + 3_600_000,
      tokenType: 'Bearer',
    });
  });

  it('turns a rejected grant into a 401, not a 400', async () => {
    // A spent or replayed code is "you are not signed in", which is what the
    // UI has to render, rather than a bad-request the user cannot act on.
    await expect(
      exchangeCode({
        code: 'spent',
        verifier: 'v',
        redirectUri: 'https://app.test/',
        settings: SETTINGS,
        fetchImpl: async () => json({ error: 'invalid_grant' }, 400),
      }),
    ).rejects.toMatchObject({ status: 401 });
  });

  it('reports a dead network as offline rather than as a refusal', async () => {
    const failure = exchangeCode({
      code: 'c',
      verifier: 'v',
      redirectUri: 'https://app.test/',
      settings: SETTINGS,
      fetchImpl: async () => {
        throw new TypeError('Failed to fetch');
      },
    });

    await expect(failure).rejects.toBeInstanceOf(ApiError);
    await expect(failure).rejects.toMatchObject({ kind: 'network' });
  });
});
