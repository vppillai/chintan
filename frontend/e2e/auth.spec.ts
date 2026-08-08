import { expect, startSignedOut, storedTokens, test } from './fixtures.ts';

/**
 * Sign-in and sign-out, driven through a real browser redirect.
 *
 * The app had token *handling* — storage, single-flight refresh, 401 recovery —
 * and no token *acquisition or disposal*: no hosted-UI redirect, no
 * authorization-code exchange, no PKCE, no sign-out control anywhere.
 * `Session.set()` was called only by the refresh path and `Session.clear()`
 * only when a refresh failed. A visitor with no token got the whole shell and a
 * console full of 401s.
 *
 * The hosted UI is stubbed at `**\/cognito/**` (see fixtures), so the redirect
 * out of the app and back onto the base URL is genuine — which is the half that
 * has to survive the document being destroyed.
 */

test.describe('signing in', () => {
  test('a signed-out visitor is offered a way in, not a shell of 401s', async ({
    page,
    api,
  }) => {
    await startSignedOut(page);
    await page.goto('/');

    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();

    // None of the authenticated surfaces mount, so nothing asks the API who
    // this is. `GET /v1/captures?status=pending → 401` was the reported symptom.
    await expect(page.getByRole('button', { name: /record/i })).toHaveCount(0);
    await expect(page.getByRole('navigation', { name: 'Library' })).toHaveCount(0);
    expect(api.requests.filter((request) => request.url.startsWith('/v1/'))).toHaveLength(0);
  });

  test('completes the authorization-code flow and lands in the app', async ({ page, api }) => {
    await startSignedOut(page);
    await page.goto('/');

    await page.getByRole('button', { name: 'Sign in' }).click();

    // Back on the record surface, signed in.
    await expect(page.getByRole('button', { name: /record/i })).toBeVisible({ timeout: 15_000 });
    await expect(page).toHaveURL(/\/$/);

    const tokens = await storedTokens(page);
    expect(tokens?.['idToken']).toBe('e2e-id-token');
    expect(tokens?.['refreshToken']).toBe('e2e-refresh-token');

    // And the app is actually using it.
    await page.getByRole('link', { name: 'Notes' }).click();
    await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
    const notes = api.requests.find((request) => request.url.startsWith('/v1/notes'));
    expect(notes?.headers['authorization']).toBe('Bearer e2e-id-token');
  });

  test('asks for a code with a PKCE challenge, and redeems it with the verifier', async ({
    page,
    api,
  }) => {
    await startSignedOut(page);
    await page.goto('/');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('button', { name: /record/i })).toBeVisible({ timeout: 15_000 });

    const authorize = api.auth.authorize[0];
    expect(authorize?.['response_type']).toBe('code');
    expect(authorize?.['code_challenge_method']).toBe('S256');
    expect(authorize?.['scope']).toContain('openid');
    // The redirect URI is the app's base URL, which is the only value the
    // deployed user pool client registers.
    expect(authorize?.['redirect_uri']).toBe('http://127.0.0.1:4173/');

    const exchange = api.auth.token[0];
    expect(exchange?.['grant_type']).toBe('authorization_code');
    expect(exchange?.['redirect_uri']).toBe('http://127.0.0.1:4173/');
    expect(exchange?.['code_verifier']).toBeTruthy();

    // The challenge is the SHA-256 of the verifier, base64url, unpadded — not
    // the verifier itself, which `plain` would have put in the address bar.
    const verifier = exchange?.['code_verifier'] ?? '';
    const expected = await page.evaluate(async (value) => {
      const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value));
      let binary = '';
      for (const byte of new Uint8Array(digest)) binary += String.fromCharCode(byte);
      return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    }, verifier);
    expect(authorize?.['code_challenge']).toBe(expected);
    expect(authorize?.['code_challenge']).not.toBe(verifier);
  });

  test('leaves no authorization code in the address bar', async ({ page }) => {
    // A code left on the URL is replayed by a reload and travels in any shared
    // link. It is single-use at Cognito, so the replay only ever shows an error.
    await startSignedOut(page);
    await page.goto('/');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('button', { name: /record/i })).toBeVisible({ timeout: 15_000 });

    expect(new URL(page.url()).searchParams.get('code')).toBeNull();
    expect(new URL(page.url()).searchParams.get('state')).toBeNull();
  });

  test('says so when the sign-in was cancelled', async ({ page, api }) => {
    api.auth.denyLogin = true;
    await startSignedOut(page);
    await page.goto('/');

    await page.getByRole('button', { name: 'Sign in' }).click();

    await expect(page.getByRole('alert')).toContainText(/cancelled/i, { timeout: 15_000 });
    // And offers the way back in rather than a dead end.
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
  });

  test('says so when the code cannot be redeemed', async ({ page, api }) => {
    api.auth.rejectExchange = true;
    await startSignedOut(page);
    await page.goto('/');

    await page.getByRole('button', { name: 'Sign in' }).click();

    await expect(page.getByRole('alert')).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
    expect(await storedTokens(page)).toBeNull();
  });

  test('refuses a code whose state does not match the flow this device started', async ({
    page,
  }) => {
    // The CSRF boundary. A code delivered with someone else's state must not be
    // redeemed, however well-formed it looks.
    await startSignedOut(page);
    await page.goto('/?code=injected-code&state=not-the-state-we-stored');

    await expect(page.getByRole('alert')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
    expect(await storedTokens(page)).toBeNull();
  });
});

test.describe('signing out', () => {
  test('is reachable from Settings and ends the Cognito session too', async ({ page, api }) => {
    await page.goto('/settings');

    await page.getByRole('button', { name: /sign out/i }).first().click();
    await page.getByRole('dialog').getByRole('button', { name: /^Sign out/ }).click();

    // Back to the signed-out surface.
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible({ timeout: 15_000 });

    /*
     * Polled, and tolerant of the read itself being interrupted.
     *
     * Sign-out re-renders this screen and *then* hands the browser to Cognito's
     * `/logout`, which the stub bounces straight back — so there is a window in
     * which evaluating anything in the page throws "execution context was
     * destroyed". The assertion is unchanged: the token set must be gone. Only
     * the reading of it now survives the navigation it is racing.
     */
    await expect
      .poll(() => storedTokens(page).catch(() => 'unreadable'), { timeout: 15_000 })
      .toBeNull();

    // The hosted UI's own session is ended, so the next sign-in prompts rather
    // than silently signing the same person back in. Polled, because clearing
    // the session re-renders this screen before the redirect is issued.
    await expect.poll(() => api.auth.logout.length, { timeout: 15_000 }).toBe(1);
    expect(api.auth.logout[0]?.['logout_uri']).toBe('http://127.0.0.1:4173/');
    expect(api.auth.logout[0]?.['client_id']).toBe('e2e-client');
  });

  test('clears everything this device was holding', async ({ page, api }) => {
    await page.goto('/notes');
    await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

    await page.goto('/settings');
    await page.getByRole('button', { name: /sign out/i }).first().click();
    await page.getByRole('dialog').getByRole('button', { name: /^Sign out/ }).click();

    /*
     * Wait for the redirect, not for the screen. Clearing the session
     * re-renders the signed-out surface immediately, while emptying IndexedDB
     * and handing over to the hosted UI both happen after — so asserting on the
     * first thing to appear reads the database mid-clear.
     */
    await expect.poll(() => api.auth.logout.length, { timeout: 15_000 }).toBe(1);
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible({ timeout: 15_000 });

    const remaining = await page.evaluate(async () => {
      const open = indexedDB.open('chintan');
      const db = await new Promise<IDBDatabase>((resolve, reject) => {
        open.onsuccess = () => resolve(open.result);
        open.onerror = () => reject(open.error);
      });
      const countOf = (store: string) =>
        new Promise<number>((resolve) => {
          const request = db.transaction(store).objectStore(store).count();
          request.onsuccess = () => resolve(request.result);
          request.onerror = () => resolve(-1);
        });
      return {
        chunks: await countOf('captureChunks'),
        captures: await countOf('captures'),
        mutations: await countOf('mutations'),
      };
    });
    expect(remaining).toEqual({ chunks: 0, captures: 0, mutations: 0 });

    // And the cached corpus is gone with it: the library does not come back
    // without a fetch.
    expect(await storedTokens(page)).toBeNull();
  });

  test('warns before destroying a recording that never reached the server', async ({
    page,
    api,
  }) => {
    /*
     * The one artifact the product cannot lose. The audio is in IndexedDB on
     * this device and nowhere else, and signing out clears it — so this dialog
     * is the last moment anyone can be told.
     */
    await page.goto('/');
    api.offline = true;

    await page.getByRole('button', { name: /record/i }).click();
    await expect(page.locator('.capture__state')).toHaveText('Recording');
    await page.waitForTimeout(1_200);
    await page.getByRole('button', { name: 'Stop' }).click();
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page.getByText(/safe on this device/i)).toBeVisible({ timeout: 15_000 });

    api.offline = false;
    await page.goto('/settings');
    await page.getByRole('button', { name: /sign out/i }).first().click();

    const dialog = page.getByRole('dialog');
    await expect(dialog).toContainText(/has not reached the server/i);
    await expect(dialog).toContainText(/not saved anywhere else/i);

    // And it is genuinely cancellable — the recording survives saying no.
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    expect(await storedTokens(page)).not.toBeNull();
  });

  test('turns biometric unlock off, so the next person cannot unlock back in', async ({
    page,
    api,
  }) => {
    api.auth.webauthnEnrolled = true;
    await page.goto('/settings');

    await page.getByRole('button', { name: /sign out/i }).first().click();
    await expect(page.getByRole('dialog')).toContainText(/biometric unlock will also be turned off/i);
    await page.getByRole('dialog').getByRole('button', { name: /^Sign out/ }).click();

    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible({ timeout: 15_000 });
    expect(api.auth.webauthnDisabled).toBe(1);
  });

  test('does not mention biometrics when nothing is enrolled', async ({ page, api }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: /sign out/i }).first().click();

    await expect(page.getByRole('dialog')).not.toContainText(/biometric/i);
    expect(api.auth.webauthnDisabled).toBe(0);
  });
});
