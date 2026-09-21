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

  await page.getByRole('button', { name: /^record$/i }).click();
  await expect(page.locator('.capture__state')).toHaveText('Recording');
  await page.waitForTimeout(1_200);
  await page.getByRole('button', { name: 'Stop' }).click();
  await expect(page.getByText('Ready to send')).toBeVisible();

  await page.getByRole('button', { name: 'Send' }).click();

  // Send hands off to the library at once; the failure lands in the upload's
  // row there, honestly, and says where the recording is.
  await expect(page).toHaveURL(/\/$/);
  const filing = page.getByRole('region', { name: /recordings being filed/i });
  await expect(filing.getByText(/safe on this device/i)).toBeVisible({ timeout: 15_000 });
  await expect(filing.getByRole('button', { name: 'Retry' })).toBeVisible();
  // Not doubled by the "unsent recording" prompt: this row is the offer.
  await expect(page.getByRole('region', { name: 'Unsent recording' })).toHaveCount(0);

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
  await filing.getByRole('button', { name: 'Retry' }).click();

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

  await page.getByRole('button', { name: /^record$/i }).click();
  await expect(page.locator('.capture__state')).toHaveText('Recording');
  await page.waitForTimeout(1_000);
  await page.getByRole('button', { name: 'Stop' }).click();
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.getByText(/safe on this device/i)).toBeVisible({ timeout: 15_000 });

  api.offline = false;
  /*
   * Reopen the app at its manifest `start_url`, which is what launching the
   * installed icon does — a completely fresh document with no JS state carried
   * over. A capture id held in a module-level variable would not survive this:
   * the audio would become unreachable the moment the page went away.
   *
   * Deliberately not a reload of /capture: that screen is a live recording
   * surface rather than a place to triage old ones, and the prompt lives at
   * the top of the library.
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
 * IndexedDB holds the note corpus for offline reading and instant search, and a
 * queue of pending mutations is flushed on reconnect. Without the notes store,
 * opening a note the user was looking at one screen earlier reports that it
 * "may have been archived or purged" and searching for it says nothing matched;
 * a queue with no caller drops every offline edit on the floor.
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

/**
 * A deep link is answered from the precache, without asking the network first.
 *
 * Workbox's precache route served `/` cache-first, but `/capture` (the
 * manifest's shortcut) and `/notes/{id}` ran `networkFirst` and awaited the
 * host's answer — GitHub Pages' 404, 890 ms on Fast 3G — before falling back to
 * the same shell. Chromium reports a worker's own fetches on the context with
 * `request.serviceWorker()` set, which is how this sees whether the worker
 * went to the network for the document at all.
 */
test('a deep link is served from the precached shell without a network round trip', async ({
  page,
  context,
}) => {
  await withServiceWorker(page);

  const fetchedByWorker: string[] = [];
  context.on('request', (request) => {
    if (request.serviceWorker()) fetchedByWorker.push(new URL(request.url()).pathname);
  });

  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  expect(fetchedByWorker.filter((path) => path === '/notes/roof-repair')).toEqual([]);
});

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

/** Whether the device holds `noteId` in full — body and captures — not as a list row. */
async function bodyOnDevice(page: Page, noteId: string): Promise<boolean> {
  return page.evaluate(async (id) => {
    const open = indexedDB.open('chintan');
    const db = await new Promise<IDBDatabase>((resolve, reject) => {
      open.onsuccess = () => resolve(open.result);
      open.onerror = () => reject(open.error);
    });
    return new Promise<boolean>((resolve) => {
      const request = db.transaction('notes').objectStore('notes').get(id);
      request.onsuccess = () => resolve(Boolean((request.result as { detail?: boolean } | undefined)?.detail));
      request.onerror = () => resolve(false);
    });
  }, noteId);
}

test('a note never opened on this device is still readable offline once the list has been seen', async ({
  page,
  context,
  api,
}) => {
  // A list row carries no body, so an unopened note used to be a dead end
  // offline. Seeing the library is now enough: the first page's bodies are
  // fetched while the app is idle.
  await withServiceWorker(page);
  await page.goto('/');
  await expect(page.getByRole('button', { name: /reading list/i })).toBeVisible();
  await expect.poll(() => bodyOnDevice(page, 'reading-list'), { timeout: 15_000 }).toBe(true);

  api.offline = true;
  await context.setOffline(true);
  await page.goto('/notes/reading-list');

  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Reading list');
  await expect(page.getByRole('heading', { name: 'Not on this device' })).toHaveCount(0);

  await context.setOffline(false);
});

test('every first-page body lands even when a one-row list page arrives first', async ({
  page,
  api,
}) => {
  /*
   * Home fires four list GETs at mount, and the Checklists chip's
   * `kind=checklist` page is the smallest. When it landed first the prefetch
   * began on its one row, the other pages landed during that body's GET, and
   * their re-triggers were dropped: five cold loads of the live site stored
   * 20, 1, 1, 20 and 1 bodies (QA 2026-09-21, finding 5). Forced here: the
   * checklist page answers at once, every other list page and every body take
   * long enough that the order cannot come out any other way.
   */
  api.notes['shopping'] = {
    id: 'shopping',
    kind: 'checklist',
    title: 'Shopping',
    body: '- [ ] Milk',
    snippet: '- [ ] Milk',
    tags: [],
    aliases: [],
    updated_at: '2026-08-05T10:00:00.000Z',
    version: 1,
    archived: false,
    captures: [],
  };
  await page.route(
    (url) => url.pathname.endsWith('/api/v1/notes') && !url.searchParams.has('kind'),
    async (route) => {
      await new Promise((resolve) => setTimeout(resolve, 800));
      await route.fallback();
    },
  );
  await page.route(
    (url) => /\/api\/v1\/notes\/[^/]+$/.test(url.pathname),
    async (route) => {
      if (route.request().method() === 'GET') await new Promise((resolve) => setTimeout(resolve, 1_500));
      await route.fallback();
    },
  );

  await page.goto('/');
  await expect(page.getByRole('button', { name: /shopping/i })).toBeVisible();

  for (const id of ['shopping', 'roof-repair', 'reading-list']) {
    await expect.poll(() => bodyOnDevice(page, id), { timeout: 15_000 }).toBe(true);
  }
});

test('a note never opened on this device says so, rather than claiming it was purged', async ({
  page,
  context,
  api,
}) => {
  // The idle prefetch would fetch this body with the rest of the first page;
  // refusing that one request leaves the device with the list row alone,
  // which is the state a note beyond the first page is in.
  await page.route('**/api/v1/notes/reading-list', (route) =>
    route.request().method() === 'GET' ? route.fulfill({ status: 404, body: '' }) : route.fallback(),
  );
  await withServiceWorker(page);
  await page.goto('/');
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
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  api.offline = true;
  await context.setOffline(true);

  /*
   * Typed into the library that is already on screen rather than reached by
   * `page.goto`, which is both the real scenario — the connection drops, the
   * user searches — and the only way to keep `navigator.onLine` false:
   * Chromium re-initialises it as true on a document loaded from the service
   * worker while offline emulation is already on. Everything below comes off
   * the device.
   */
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
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  // Real offline, not a synthetic event: the banner reads navigator.onLine,
  // which only the browser context can change.
  api.offline = true;
  await context.setOffline(true);

  await expect(page.getByText(/showing saved notes/i)).toBeVisible();

  await context.setOffline(false);
});

/**
 * What happens to the "will sync" banner once the sync happens.
 *
 * `saveQueued` set `state: 'queued'` and nothing ever transitioned out of it:
 * there was no event meaning "the queued mutation reached the server", and the
 * flush path notified the editor of nothing. So the note screen said "Saved on
 * this device — will sync" until a reload, whether the flush had succeeded,
 * failed permanently, or lost a conflict.
 *
 * That is the same untruth as the blanket offline sentence, one layer up: a
 * user watching it cannot tell "not yet" from "never".
 */
test('the queued banner clears once the edit reaches the server', async ({
  page,
  context,
  api,
}) => {
  await withServiceWorker(page);
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  api.offline = true;
  await context.setOffline(true);

  await page.getByRole('textbox', { name: 'Note body' }).fill('Ellis quoted nine hundred.');
  await expect(page.getByText(/saved on this device — will sync/i)).toBeVisible();

  api.offline = false;
  await context.setOffline(false);

  await expect
    .poll(() => api.notes['roof-repair']?.body, { timeout: 20_000 })
    .toContain('Ellis quoted nine hundred.');

  // The claim is now false, so it must stop being made.
  await expect(page.getByText(/saved on this device — will sync/i)).toHaveCount(0);
  await expect(page.getByText('Saved')).toBeVisible();
  // And nothing is left waiting.
  await expect(page.getByText(/waiting to sync/i)).toHaveCount(0);
});

test('a queued edit the server refuses says so, instead of promising a sync', async ({
  page,
  context,
  api,
}) => {
  await withServiceWorker(page);
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  api.offline = true;
  await context.setOffline(true);

  await page.getByRole('textbox', { name: 'Note body' }).fill('Ellis quoted nine hundred.');
  await expect(page.getByText(/saved on this device — will sync/i)).toBeVisible();

  // The server rejects it on its merits when it finally sees it. Replaying will
  // never help, and neither will telling the user it is about to sync.
  api.rejectPatch = { status: 400, detail: 'That title is too long to store.' };
  api.offline = false;
  await context.setOffline(false);

  await expect(page.getByText(/saved on this device — will sync/i)).toHaveCount(0, {
    timeout: 20_000,
  });
  await expect(page.getByText(/too long to store/i)).toBeVisible();
});
