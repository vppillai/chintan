import type { Page } from '@playwright/test';

import { expect, test, type ApiState } from './fixtures.ts';

/**
 * Passkey set-up, as a hand-off to the managed login.
 *
 * The app cannot run the registration ceremony itself: Cognito's relying party
 * is its own managed-login domain and the browser refuses `credentials.create`
 * from `vppillai.github.io` with a SecurityError (verified against prod,
 * 2026-09-04 — see `src/features/auth/passkeys.ts`). What the app can do is
 * send the user to Cognito's `/passkeys/add` page and read the answer it
 * returns on the callback URL. These specs cover that round trip against the
 * stubbed hosted UI in `fixtures.ts`; the ceremony itself is Cognito's.
 */

/** The fixture answers the nudge for every other spec; this one wants to see it. */
async function withNudge(page: Page): Promise<void> {
  await page.addInitScript(() => {
    if (sessionStorage.getItem('e2e.nudge.cleared')) return;
    sessionStorage.setItem('e2e.nudge.cleared', '1');
    localStorage.removeItem('chintan.passkey.nudge.v1');
  });
}

test('the library suggests a passkey once, and Not now is remembered', async ({ page }) => {
  await withNudge(page);
  await page.goto('/');

  const nudge = page.getByRole('note', { name: /passkey suggestion/i });
  await expect(nudge).toContainText(/sign in faster next time/i);

  await nudge.getByRole('button', { name: /not now/i }).click();
  await expect(nudge).toHaveCount(0);

  await page.reload();
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await expect(page.getByRole('note', { name: /passkey suggestion/i })).toHaveCount(0);
});

test('Add a passkey hands off to the managed login and reports the result on You', async ({
  page,
  api,
}) => {
  await page.goto('/settings');

  await page.getByRole('button', { name: /add a passkey on this device/i }).click();

  // Round trip: the stub's /passkeys/add returns to the base URL with
  // `result=success`; the shell moves the person back to You with the news.
  await expect(page).toHaveURL(/\/settings\?passkey=success$/, { timeout: 15_000 });
  await expect(page.getByText(/passkey added/i)).toBeVisible();

  // The page was asked with exactly what it requires: the client and the one
  // callback the deployed client registers.
  expect(api.auth.passkeyAdd).toHaveLength(1);
  expect(api.auth.passkeyAdd[0]?.['client_id']).toBe('e2e-client');
  expect(api.auth.passkeyAdd[0]?.['redirect_uri']).toBe('http://127.0.0.1:4173/');

  // Back is the library, not out of the app.
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('heading', { name: 'Notes' })).toBeVisible();
});

test('an ended managed-login session is explained, and a fresh sign-in offered', async ({
  page,
  api,
}) => {
  (api as ApiState).auth.passkeySession = false;
  await page.goto('/settings');

  await page.getByRole('button', { name: /add a passkey on this device/i }).click();

  await expect(page).toHaveURL(/\/settings\?passkey=invalid_session$/, { timeout: 15_000 });
  await expect(page.getByRole('alert')).toContainText(/no longer had your session/i);

  // "Sign in again" is the real authorization-code flow, landing back signed in.
  await page.getByRole('button', { name: /sign in again/i }).click();
  await expect(page.getByRole('button', { name: /^record$/i })).toBeVisible({ timeout: 15_000 });
  expect(api.auth.authorize).toHaveLength(1);
  expect(api.auth.token).toHaveLength(1);
});
