import type { Page } from '@playwright/test';

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

/**
 * Reading, searching and editing with no connection.
 *
 * Spec §5.5 promised "IndexedDB holds … the note corpus for offline reading and
 * instant search" and "a queue of pending mutations … flushed on reconnect".
 * None of the three existed: there was no notes store at all, and
 * `offline/queue.ts` had no caller outside its own test. Driven offline, opening
 * a note the user had been looking at one screen earlier reported that it "may
 * have been archived or purged", and searching for it said nothing matched.
 *
 * Each case reloads the document before going offline, on purpose. Without the
 * reload the in-memory query cache answers and the test proves nothing about
 * what is on the device.
 */

/** One visit is enough to install and take control; the shell is precached. */
async function withServiceWorker(page: Page): Promise<void> {
  await page.goto('/');
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  await expect
    .poll(() => page.evaluate(() => Boolean(navigator.serviceWorker.controller)))
    .toBe(true);
}

test('a note read once can be read again offline, from a cold start', async ({
  page,
  context,
  api,
}) => {
  await withServiceWorker(page);
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  api.offline = true;
  await context.setOffline(true);

  // A completely fresh document: nothing survives but what is on disk.
  await page.goto('/notes/roof-repair');

  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');
  await expect(page.getByRole('textbox', { name: 'Note body' })).toHaveValue(
    /Ridge tiles/,
  );
  await expect(page.getByText(/may have been archived or purged/i)).toHaveCount(0);

  await context.setOffline(false);
});

test('a note never opened on this device says so, rather than claiming it was purged', async ({
  page,
  context,
  api,
}) => {
  await withServiceWorker(page);
  await page.goto('/notes');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  api.offline = true;
  await context.setOffline(true);
  await page.goto('/notes/reading-list');

  // The list row is on the device; the note's body is not. Rendering a real
  // title over an empty textarea would invite an edit that erases the note.
  await expect(page.getByRole('heading', { name: 'Not on this device' })).toBeVisible();
  await expect(page.getByText(/may have been archived or purged/i)).toHaveCount(0);

  await context.setOffline(false);
});

test('search offline finds a cached note instead of saying it does not exist', async ({
  page,
  context,
  api,
}) => {
  await page.goto('/notes');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  api.offline = true;
  await context.setOffline(true);

  /*
   * Navigated within the app rather than by `page.goto`, which is both the real
   * scenario — the library is on screen, the connection drops, the user
   * searches — and the only way to keep `navigator.onLine` false: Chromium
   * re-initialises it as true on a document loaded from the service worker
   * while offline emulation is already on.
   *
   * Search's corpus query is fetched only when this screen opens, so it has
   * never run. Everything below comes off the device.
   */
  await page.getByRole('link', { name: 'Search' }).click();
  await page.getByRole('searchbox', { name: 'Search notes' }).fill('roof');

  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await expect(page.getByText(/nothing matches/i)).toHaveCount(0);
  await expect(page.getByText(/searching offline/i)).toBeVisible();

  await context.setOffline(false);
});

test('an edit made offline is queued on the device and flushed on reconnect', async ({
  page,
  context,
  api,
}) => {
  await withServiceWorker(page);
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  api.offline = true;
  await context.setOffline(true);

  await page
    .getByRole('textbox', { name: 'Note body' })
    .fill('Ridge tiles slipped. Ellis quoted nine hundred.');

  // Not "Couldn't save": nothing has gone wrong, and the edit is somewhere
  // durable. This is the sentence the client used to assert on every offline
  // failure while writing nothing anywhere.
  await expect(page.getByText(/saved on this device — will sync/i)).toBeVisible();

  // Durable in fact, not in copy.
  const queued = await page.evaluate(async () => {
    const open = indexedDB.open('chintan');
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      open.onsuccess = () => resolve(open.result);
      open.onerror = () => reject(open.error);
    });
    return new Promise<number>((resolve) => {
      const request = db.transaction('mutations').objectStore('mutations').count();
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => resolve(-1);
    });
  });
  expect(queued).toBe(1);

  await expect(page.getByText(/waiting to sync/i)).toBeVisible();

  api.offline = false;
  await context.setOffline(false);

  await expect
    .poll(() => api.notes['roof-repair']?.body, { timeout: 20_000 })
    .toContain('Ellis quoted nine hundred.');
});

test('three offline edits to one note are one queued write, not three', async ({
  page,
  context,
  api,
}) => {
  await withServiceWorker(page);
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  api.offline = true;
  await context.setOffline(true);

  const body = page.getByRole('textbox', { name: 'Note body' });
  for (const text of ['One.', 'One. Two.', 'One. Two. Three.']) {
    await body.fill(text);
    await expect(page.getByText(/saved on this device — will sync/i)).toBeVisible();
  }

  const queued = await page.evaluate(async () => {
    const open = indexedDB.open('chintan');
    const db = await new Promise<IDBDatabase>((resolve) => {
      open.onsuccess = () => resolve(open.result);
    });
    return new Promise<number>((resolve) => {
      const request = db.transaction('mutations').objectStore('mutations').count();
      request.onsuccess = () => resolve(request.result);
    });
  });
  // A note PATCH carries the whole note, so three edits are one write made
  // three times. Queueing all three would replay two supersedes at the server.
  expect(queued).toBe(1);

  api.offline = false;
  await context.setOffline(false);

  await expect
    .poll(() => api.notes['roof-repair']?.body, { timeout: 20_000 })
    .toBe('One. Two. Three.');
});

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
