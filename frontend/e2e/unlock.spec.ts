import type { Page } from '@playwright/test';

import { COGNITO_ORIGIN, expect, startSignedOut, storedTokens, test } from './fixtures.ts';

/**
 * Biometric unlock — the half that was never built.
 *
 * v2 shipped the settings toggle, a real WebAuthn registration and a
 * server-side sealed refresh-token vault, and no assertion path at all: no
 * `webauthn/login` wrapper in `endpoints.ts`, no assertion helper beside
 * `performRegistration`, and `useAuth` redirecting to the hosted UI regardless.
 * Enrolling produced a credential that no code could ever use, and it looked
 * like it worked.
 *
 * The ceremony itself is stubbed rather than performed. A virtual authenticator
 * needs an RP ID, an RP ID must be a registrable domain, and the test server is
 * `127.0.0.1` — so the *browser's* half is replaced and everything on either
 * side of it is real: the two requests, the base64url encoding of the
 * assertion, the token set, and the session it produces.
 */

/** A stand-in for the platform authenticator, returning real ArrayBuffers. */
async function withStubbedAuthenticator(
  page: Page,
  behaviour: 'succeed' | 'cancel' = 'succeed',
): Promise<void> {
  await page.addInitScript((mode) => {
    const bytes = (text: string): ArrayBuffer => {
      const encoded = new TextEncoder().encode(text);
      return encoded.buffer as ArrayBuffer;
    };

    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: {
        create: () => Promise.reject(new Error('not used')),
        get: (request: { publicKey?: PublicKeyCredentialRequestOptions }) => {
          if (mode === 'cancel') {
            const error = new Error('The operation was cancelled');
            error.name = 'NotAllowedError';
            return Promise.reject(error);
          }
          // Record what the app decoded, so the spec can assert the challenge
          // survived the base64url round trip into a BufferSource.
          (window as unknown as { __assertRequest?: unknown }).__assertRequest = {
            challengeBytes: Array.from(
              new Uint8Array(request.publicKey?.challenge as ArrayBuffer),
            ),
            allowCredentials: (request.publicKey?.allowCredentials ?? []).length,
          };
          return Promise.resolve({
            id: 'e2e-cred',
            rawId: bytes('e2e-cred'),
            type: 'public-key',
            response: {
              clientDataJSON: bytes('{"type":"webauthn.get"}'),
              authenticatorData: bytes('authenticator-data'),
              signature: bytes('signature'),
              userHandle: bytes('user-1'),
            },
          });
        },
      },
    });
    // The app gates the control on this existing.
    Object.defineProperty(window, 'PublicKeyCredential', {
      configurable: true,
      value: function PublicKeyCredential() {},
    });
  }, behaviour);
}

test('an enrolled user unlocks without the hosted UI', async ({ page, api }) => {
  api.auth.webauthnEnrolled = true;
  await withStubbedAuthenticator(page);
  await startSignedOut(page);

  await page.goto('/');

  const unlock = page.getByRole('button', { name: 'Unlock with biometrics' });
  await expect(unlock).toBeVisible();
  // Beside Sign in, not instead of it.
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();

  await unlock.click();

  // Signed in, and the record surface is up.
  await expect(page.getByRole('button', { name: /record/i })).toBeVisible();

  // The whole point: no round trip to Cognito.
  expect(api.auth.authorize, 'the hosted UI was opened for an unlock').toEqual([]);

  expect(api.auth.webauthnLogins).toHaveLength(1);
  const posted = api.auth.webauthnLogins[0];
  expect(posted?.['challenge_id']).toBe('e2e-assert-challenge');

  const credential = posted?.['credential'] as Record<string, unknown>;
  const response = credential['response'] as Record<string, unknown>;
  // Every field the server needs to verify an assertion, base64url encoded.
  expect(Object.keys(response).sort()).toEqual([
    'authenticatorData',
    'clientDataJSON',
    'signature',
    'userHandle',
  ]);

  // The vault's token set is the session now, not a second-class one.
  const tokens = await storedTokens(page);
  expect(tokens?.['accessToken']).toBe('e2e-unlocked-access-token');
  expect(tokens?.['refreshToken']).toBe('e2e-unlocked-refresh-token');
});

test('the unlock requests carry no bearer, because there is no session yet', async ({
  page,
  api,
}) => {
  api.auth.webauthnEnrolled = true;
  await withStubbedAuthenticator(page);
  await startSignedOut(page);

  await page.goto('/');
  await page.getByRole('button', { name: 'Unlock with biometrics' }).click();
  await expect(page.getByRole('button', { name: /record/i })).toBeVisible();

  const ceremony = api.requests.filter((entry) =>
    entry.url.startsWith('/v1/auth/webauthn/login'),
  );
  expect(ceremony.map((entry) => entry.url)).toEqual([
    '/v1/auth/webauthn/login/options',
    '/v1/auth/webauthn/login',
  ]);
  for (const entry of ceremony) {
    expect(entry.headers['authorization'], `${entry.url} sent a bearer`).toBeUndefined();
  }
});

test('the challenge survives the base64url round trip into the ceremony', async ({
  page,
  api,
}) => {
  api.auth.webauthnEnrolled = true;
  await withStubbedAuthenticator(page);
  await startSignedOut(page);

  await page.goto('/');
  await page.getByRole('button', { name: 'Unlock with biometrics' }).click();
  await expect(page.getByRole('button', { name: /record/i })).toBeVisible();

  const seen = await page.evaluate(
    () => (window as unknown as { __assertRequest?: { challengeBytes: number[]; allowCredentials: number } }).__assertRequest,
  );
  // "e2e-assert-challenge", which is what the fixture base64url-encoded. Getting
  // this wrong produces a credential that enrols and then never verifies.
  expect(new TextDecoder().decode(new Uint8Array(seen?.challengeBytes ?? []))).toBe(
    'e2e-assert-challenge',
  );
  expect(seen?.allowCredentials).toBe(1);
});

test('a vault that cannot be opened asks for re-enrolment, not for another finger', async ({
  page,
  api,
}) => {
  api.auth.webauthnEnrolled = true;
  api.auth.vaultNeedsReEnrolment = true;
  await withStubbedAuthenticator(page);
  await startSignedOut(page);

  await page.goto('/');
  await page.getByRole('button', { name: 'Unlock with biometrics' }).click();

  await expect(page.getByText(/set up again on this device/i)).toBeVisible();
  // Not an error: the assertion verified, and trying the same finger again will
  // never work. The way out is the button that is still there.
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeEnabled();
});

test('a cancelled unlock says so and leaves sign-in available', async ({ page, api }) => {
  api.auth.webauthnEnrolled = true;
  await withStubbedAuthenticator(page, 'cancel');
  await startSignedOut(page);

  await page.goto('/');
  await page.getByRole('button', { name: 'Unlock with biometrics' }).click();

  await expect(page.getByRole('alert')).toContainText(/cancelled/i);
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeEnabled();
  expect(api.auth.webauthnLogins).toEqual([]);
});

test('an account with nothing enrolled is told so, and can still sign in', async ({
  page,
}) => {
  await withStubbedAuthenticator(page);
  await startSignedOut(page);

  await page.goto('/');
  await page.getByRole('button', { name: 'Unlock with biometrics' }).click();

  await expect(page.getByRole('alert')).toContainText(/not set up on this account/i);

  // And the hosted UI still works from here.
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => !url.href.startsWith(COGNITO_ORIGIN), { timeout: 15_000 });
  await expect(page.getByRole('button', { name: /record/i })).toBeVisible();
});
