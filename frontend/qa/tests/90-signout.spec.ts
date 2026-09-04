import { expect, test } from '@playwright/test';

import { CREDS, libraryReady, note, record, requireCreds, shot, sleep, text } from './helpers.ts';

test('sign out and back in', async ({ page }, info) => {
  requireCreds();
  const rec = record(page);
  await page.goto('./settings');
  await page.getByRole('heading', { name: 'You' }).waitFor();
  await page.getByRole('button', { name: 'Sign out' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  note(info, `sign-out dialog: ${JSON.stringify(await dialog.innerText())}`);
  await shot(page, info, 'out-01-dialog');
  await dialog.getByRole('button', { name: /^Sign out/ }).click();
  await sleep(4_000);
  note(info, `after sign out: URL ${page.url()}; text ${JSON.stringify((await text(page)).slice(0, 300))}`);
  note(info, `tokens left in localStorage: ${await page.evaluate(() => window.localStorage.getItem('chintan.tokens.v2'))}; IndexedDB databases: ${JSON.stringify(await page.evaluate(async () => (await indexedDB.databases()).map((d) => d.name)))}`);
  await shot(page, info, 'out-02-signed-out');
  note(info, `requests during sign-out: ${rec.requests.filter((r) => r.url.includes('cognito') || r.url.includes('execute-api')).map((r) => `${r.method} ${r.status} ${r.url.replace(/\?.*/, '').slice(0, 90)}`).join(' | ')}`);

  // Back in: does Cognito remember the session (no password), or ask again?
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL(/amazoncognito\.com|vppillai\.github\.io\/chintan\/dev\/\?code=/, { timeout: 30_000 });
  await sleep(1_000);
  if (/amazoncognito/.test(page.url())) {
    note(info, `Cognito asked again: ${JSON.stringify((await text(page)).slice(0, 200))}`);
    const username = page.locator('input[name="username"], input[type="text"]').first();
    if (await username.isVisible().catch(() => false)) {
      await username.fill(CREDS.user);
      await page.getByRole('button', { name: /^(Next|Continue)$/ }).first().click();
    }
    const password = page.locator('input[type="password"]').first();
    await password.waitFor({ timeout: 20_000 });
    await password.fill(CREDS.pass);
    await page.getByRole('button', { name: /^(Continue|Sign in)$/ }).first().click();
  } else {
    note(info, 'Cognito session cookie still valid: signed straight back in without a password');
  }
  await page.waitForURL(/vppillai\.github\.io/, { timeout: 30_000 });
  await libraryReady(page);
  note(info, `signed back in; rows ${await page.locator('.note-row').count()}`);
  await shot(page, info, 'out-03-back-in');
  rec.dump(info, 'signout');
});
