import { expect, test } from './fixtures.ts';

/**
 * Recording with no connection, then reconnecting.
 *
 * The product cannot absorb losing a recording. This asserts the ordering that
 * guarantees it: the audio stays in IndexedDB until the server confirms, so a
 * failed upload is a Resend rather than a loss.
 */

test('a recording made offline survives and is sent on reconnect', async ({ page, api }) => {
  await page.goto('/');

  api.offline = true;

  await page.getByRole('button', { name: /record/i }).click();
  await expect(page.locator('.capture__state')).toHaveText('Recording');
  await page.waitForTimeout(1_200);
  await page.getByRole('button', { name: 'Stop' }).click();
  await expect(page.getByText('Ready to send')).toBeVisible();

  await page.getByRole('button', { name: 'Send' }).click();

  // Fails honestly, and says where the recording is.
  await expect(page.getByText(/safe on this device/i)).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole('button', { name: 'Try again' })).toBeVisible();

  // The audio is genuinely on disk, not just in a JS variable.
  const bufferedChunks = await page.evaluate(async () => {
    const open = indexedDB.open('chintan');
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      open.onsuccess = () => resolve(open.result);
      open.onerror = () => reject(open.error);
    });
    return new Promise<number>((resolve) => {
      const request = db.transaction('captureChunks').objectStore('captureChunks').count();
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => resolve(-1);
    });
  });
  expect(bufferedChunks).toBeGreaterThan(0);

  api.offline = false;
  await page.getByRole('button', { name: 'Try again' }).click();

  await expect.poll(() => api.captures.length, { timeout: 15_000 }).toBeGreaterThan(0);

  // Pruned only now that the server has it.
  await expect
    .poll(
      async () =>
        page.evaluate(async () => {
          const open = indexedDB.open('chintan');
          const db = await new Promise<IDBDatabase>((resolve) => {
            open.onsuccess = () => resolve(open.result);
          });
          return new Promise<number>((resolve) => {
            const request = db
              .transaction('captureChunks')
              .objectStore('captureChunks')
              .count();
            request.onsuccess = () => resolve(request.result);
          });
        }),
      { timeout: 15_000 },
    )
    .toBe(0);
});

test('a stranded recording is offered back after a reload', async ({ page, api }) => {
  await page.goto('/');
  api.offline = true;

  await page.getByRole('button', { name: /record/i }).click();
  await expect(page.locator('.capture__state')).toHaveText('Recording');
  await page.waitForTimeout(1_000);
  await page.getByRole('button', { name: 'Stop' }).click();
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByText(/safe on this device/i)).toBeVisible({ timeout: 15_000 });

  api.offline = false;
  /*
   * Reopen the app at its manifest `start_url`, which is what launching the
   * installed icon does — a completely fresh document with no JS state carried
   * over. This is the case v1 could not survive: the capture id lived in a
   * module-level variable, so the audio became unreachable the moment the page
   * went away.
   *
   * Deliberately not a reload of /capture: the sheet is locked there and the
   * prompt is correctly suppressed, because that screen is a live recording
   * surface rather than a place to triage old ones.
   */
  await page.goto('/');

  await expect(page.getByRole('region', { name: 'Unsent recording' })).toBeVisible();
  await expect(page.getByText(/unsent recording from/i)).toBeVisible();
});

/**
 * Opening the installed app offline at a URL this device has never visited.
 *
 * Workbox precaches the shell as `index.html?__WB_REVISION__=<hash>` and the
 * fallback was `caches.match('/index.html')` with no `ignoreSearch`, so it
 * missed and the worker answered the bare 503 `new Response('Offline')`: a
 * blank page containing one word, no app, no controls, no way back. Previously
 * visited URLs work off the runtime cache, which is exactly why casual testing
 * misses it.
 */
for (const path of ['/settings', '/capture']) {
  test(`opening ${path} offline serves the app shell, not the word "Offline"`, async ({
    page,
    context,
  }) => {
    // One visit to `/` installs and activates the worker. Nothing here has ever
    // fetched the URL under test.
    await page.goto('/');
    await page.evaluate(async () => {
      await navigator.serviceWorker.ready;
    });
    await expect.poll(() => page.evaluate(() => Boolean(navigator.serviceWorker.controller)))
      .toBe(true);

    await context.setOffline(true);
    await page.goto(path);

    await expect(page.locator('.app')).toBeVisible();
    await expect(page.getByRole('banner')).toBeVisible();
    await expect(page.locator('body')).not.toHaveText('Offline');

    await context.setOffline(false);
  });
}

test('the offline banner says the data is cached', async ({ page, context, api }) => {
  await page.goto('/notes');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  // Real offline, not a synthetic event: the banner reads navigator.onLine,
  // which only the browser context can change.
  api.offline = true;
  await context.setOffline(true);

  await expect(page.getByText(/showing saved notes/i)).toBeVisible();

  await context.setOffline(false);
});
