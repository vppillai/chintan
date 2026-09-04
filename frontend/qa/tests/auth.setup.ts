import { mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { expect, test } from '@playwright/test';

import { CREDS, libraryReady, note, record, requireCreds, shot, text } from './helpers.ts';

/**
 * Signs in through the real Cognito managed-login page, screenshots every
 * screen it shows on the way (so the report can say whether a passkey
 * enrolment is ever offered), and saves the tokens for the other specs.
 */
test('sign in through Cognito managed login', async ({ page }, info) => {
  requireCreds();
  const rec = record(page);
  const stateDir = fileURLToPath(new URL('../state/', import.meta.url));
  mkdirSync(stateDir, { recursive: true });

  await page.goto('./');
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
  await shot(page, info, 'signin-00-app-signed-out');

  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/amazoncognito\.com/, { timeout: 30_000 });
  await page.waitForLoadState('networkidle');
  await shot(page, info, 'signin-01-cognito-username');
  note(info, `Cognito screen 1 text: ${JSON.stringify((await text(page)).slice(0, 600))}`);

  // Username, then Next.
  const username = page.locator('input[name="username"], input[type="email"], input[type="text"]').first();
  await username.fill(CREDS.user);
  const next = page.getByRole('button', { name: /^(Next|Continue)$/ });
  await next.first().click();

  const password = page.locator('input[type="password"]').first();
  await password.waitFor({ timeout: 20_000 });
  await page.waitForLoadState('networkidle');
  await shot(page, info, 'signin-02-cognito-password');
  note(info, `Cognito screen 2 text: ${JSON.stringify((await text(page)).slice(0, 600))}`);
  await password.fill(CREDS.pass);
  await shot(page, info, 'signin-02b-cognito-password-filled');
  await page.getByRole('button', { name: /^(Continue|Sign in|Next)$/ }).first().click();

  // Whatever Cognito shows after the password — a passkey offer, an MFA step,
  // or the redirect straight back to the app — is what we want on record.
  const deadline = Date.now() + 45_000;
  let step = 3;
  while (Date.now() < deadline) {
    const url = page.url();
    if (url.startsWith('https://vppillai.github.io/')) break;
    await page.waitForTimeout(1_000);
    if (/amazoncognito\.com/.test(url)) {
      const body = await text(page).catch(() => '');
      if (body) {
        await shot(page, info, `signin-0${step}-cognito-after-password`);
        note(info, `Cognito screen ${step} (${url}): ${JSON.stringify(body.slice(0, 800))}`);
        step += 1;
        // A passkey/MFA interstitial usually has a skip; take it so the run continues.
        const skip = page.getByRole('button', { name: /skip|not now|later|continue/i }).first();
        if (await skip.isVisible().catch(() => false)) {
          note(info, `Clicked interstitial button: ${await skip.innerText()}`);
          await skip.click();
        }
      }
      await page.waitForTimeout(1_500);
    }
  }
  await page.waitForURL(/vppillai\.github\.io/, { timeout: 30_000 });
  await libraryReady(page);
  await shot(page, info, 'signin-09-app-signed-in');
  const tokens = await page.evaluate(() => window.localStorage.getItem('chintan.tokens.v2'));
  expect(tokens, 'tokens are in localStorage after the exchange').toBeTruthy();
  note(info, `Signed in; token set keys: ${Object.keys(JSON.parse(tokens ?? '{}')).join(',')}`);
  rec.dump(info, 'signin');
  await page.context().storageState({ path: `${stateDir}${info.project.name.replace('setup-', '')}.json` });
});
