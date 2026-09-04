import { SCOPED_BASE_URL } from '../playwright.config.ts';

import { expect, test } from './fixtures.ts';

/**
 * The app under its real shape: a sub-path of the host, which is also the
 * manifest's `scope` and the service worker's.
 *
 * QA D5, measured on the live app: every in-app navigation to the library
 * landed on `/chintan/dev` and `/chintan/dev?view=archived` — no trailing
 * slash — because the router's basename had it stripped and React Router
 * writes the home URL as the basename verbatim. Those URLs are outside the
 * installed app's scope and outside the worker's; reloading one offline was
 * `chrome-error://chromewebdata/`, while `/chintan/dev/` served the cached
 * library. GitHub Pages redirects the bare path online, which hid it.
 *
 * Runs against the second web server in `playwright.config.ts`, built with
 * `VITE_BASE=/chintan/dev/`; the root build has no slash to lose.
 */

test.use({ baseURL: SCOPED_BASE_URL });

const ORIGIN = new URL(SCOPED_BASE_URL).origin;

test('every in-app URL keeps the scope’s trailing slash', async ({ page, request }) => {
  await page.goto(SCOPED_BASE_URL);
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  // The scope the URLs below have to stay inside.
  const manifestHref = await page.locator('link[rel="manifest"]').getAttribute('href');
  const manifest = (await (await request.get(new URL(manifestHref ?? '', SCOPED_BASE_URL).href)).json()) as {
    scope: string;
    start_url: string;
  };
  expect(manifest.scope).toBe('/chintan/dev/');
  expect(manifest.start_url).toBe('/chintan/dev/');

  await page.getByRole('button', { name: /^Archived/ }).click();
  await expect(page).toHaveURL(`${ORIGIN}/chintan/dev/?view=archived`);

  await page.getByRole('button', { name: 'All' }).click();
  await expect(page).toHaveURL(`${ORIGIN}/chintan/dev/`);

  await page.getByRole('searchbox', { name: 'Search notes' }).fill('roof');
  await expect(page).toHaveURL(`${ORIGIN}/chintan/dev/?q=roof`);
  await page.getByRole('searchbox', { name: 'Search notes' }).fill('');

  await page.getByRole('button', { name: /roof repair/i }).click();
  await expect(page).toHaveURL(`${ORIGIN}/chintan/dev/notes/roof-repair`);

  await page.getByRole('button', { name: /back to notes/i }).click();
  await expect(page).toHaveURL(`${ORIGIN}/chintan/dev/`);

  await page.getByRole('link', { name: 'You' }).click();
  await expect(page).toHaveURL(`${ORIGIN}/chintan/dev/settings`);
});

test('the archive view reloads offline from the worker, at the URL the app itself wrote', async ({
  page,
  context,
  api,
}) => {
  await page.goto(SCOPED_BASE_URL);
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  await expect
    .poll(() => page.evaluate(() => Boolean(navigator.serviceWorker.controller)))
    .toBe(true);

  // Reached by pressing the chip, not by typing the URL: this is the address
  // the app puts in the bar, and the one that could not be reloaded.
  await page.getByRole('button', { name: /^Archived/ }).click();
  await expect(page).toHaveURL(`${ORIGIN}/chintan/dev/?view=archived`);

  api.offline = true;
  await context.setOffline(true);
  await page.reload();

  await expect(page.locator('.app')).toBeVisible();
  await expect(page.getByRole('banner')).toBeVisible();
  await expect(page.getByRole('button', { name: /^Archived/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await expect(page).toHaveURL(`${ORIGIN}/chintan/dev/?view=archived`);

  await context.setOffline(false);
});
