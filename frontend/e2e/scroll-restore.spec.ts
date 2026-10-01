import { devices, type CDPSession, type Page } from '@playwright/test';

import { expect, test, type ApiState } from './fixtures.ts';

/**
 * Back returns to the place the user left (R7-6a).
 *
 * The library scrolls inside `.app__main`, which the browser's own scroll
 * restoration does not know about, so Back from a note used to land the list
 * at the top.
 */

/** Forty notes in each list, so both scroll on any screen. */
function manyNotes(api: ApiState): void {
  for (let index = 1; index <= 40; index += 1) {
    for (const archived of [false, true]) {
      const id = `${archived ? 'gone' : 'note'}-${String(index)}`;
      const title = `${archived ? 'Gone' : 'Filler'} ${String(index)}`;
      api.notes[id] = {
        id,
        title,
        body: `${title} body.`,
        snippet: `${title} body.`,
        tags: [],
        aliases: [],
        // Oldest last, so "Filler 30" is well down the list.
        updated_at: new Date(Date.UTC(2026, 8, 1, 0, 60 - index)).toISOString(),
        version: 1,
        archived,
        captures: [],
      };
    }
  }
}

function mainTop(page: Page): Promise<number> {
  return page.locator('.app__main').evaluate((main) => main.scrollTop);
}

async function scrollRowIntoView(page: Page, name: RegExp): Promise<number> {
  const row = page.getByRole('button', { name }).first();
  await row.scrollIntoViewIfNeeded();
  const top = await mainTop(page);
  expect(top).toBeGreaterThan(200);
  return top;
}

async function expectBackRestores(page: Page, list: string, row: RegExp): Promise<void> {
  await page.goto(list);
  const before = await scrollRowIntoView(page, row);
  await page.getByRole('button', { name: row }).first().click();
  await expect(page.getByRole('textbox', { name: 'Note title' })).toBeVisible();
  await page.goBack();
  await expect(page.getByRole('button', { name: row }).first()).toBeVisible();
  await expect.poll(() => mainTop(page)).toBeGreaterThan(before - 4);
  expect(Math.abs((await mainTop(page)) - before)).toBeLessThanOrEqual(4);
}

for (const [label, use] of [
  ['on a desktop', {}],
  ['on a phone', (({ defaultBrowserType: _browser, ...pixel }) => pixel)(devices['Pixel 7']!)],
] as const) {
  test.describe(label, () => {
    test.use(use);

    test('Back from a note returns Home to where the list was', async ({ page, api }) => {
      manyNotes(api);
      await expectBackRestores(page, '/', /^Filler 30\b/);
    });

    test('Back from a note returns the archive to where it was', async ({ page, api }) => {
      manyNotes(api);
      await expectBackRestores(page, '/?view=archived', /^Gone 30\b/);
    });
  });
}

test("a note returned to by Forward is at its own place", async ({ page, api }) => {
  api.notes['roof-repair']!.body = Array.from(
    { length: 80 },
    (_, index) => `Paragraph ${String(index + 1)}. The flashing around the chimney needs replacing.`,
  ).join('\n\n');
  await page.setViewportSize({ width: 412, height: 915 });
  await page.goto('/');
  await page.getByRole('button', { name: /roof repair/i }).click();
  await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();
  await page.locator('.app__main').evaluate((main) => {
    main.scrollTop = 1_500;
    main.dispatchEvent(new Event('scroll'));
  });
  const before = await mainTop(page);
  expect(before).toBeGreaterThan(1_000);

  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await page.goForward();
  await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();
  await expect.poll(() => mainTop(page)).toBeGreaterThan(before - 4);
  expect(Math.abs((await mainTop(page)) - before)).toBeLessThanOrEqual(4);
});

test('a scroll of the person\'s own during the restore is never pulled back', async ({ page, api }) => {
  manyNotes(api);
  await page.goto('/');
  await expect(page.getByRole('button', { name: /^Filler 30\b/ }).first()).toBeVisible();
  const homeKey = await page.evaluate(() => (history.state as { key: string }).key);
  await page.getByRole('button', { name: /^Filler 3\b/ }).first().click();
  await expect(page.getByRole('textbox', { name: 'Note title' })).toBeVisible();
  // An offset the list can never reach keeps the restore retrying for its
  // whole second, which is the window a person's scroll must win in.
  await page.evaluate((key) => {
    sessionStorage.setItem(`chintan.scroll.${key}`, '999999');
  }, homeKey);

  await page.goBack();
  await expect(page.getByRole('button', { name: /^Filler 30\b/ }).first()).toBeVisible();
  await page.locator('.app__main').evaluate((main) => {
    main.dispatchEvent(new WheelEvent('wheel', { deltaY: -100 }));
    main.scrollTop = 100;
  });
  await page.waitForTimeout(400);
  expect(await mainTop(page)).toBe(100);
});

test.describe('a swipe between a note\'s tabs', () => {
  const { defaultBrowserType: _browser, ...pixel } = devices['Pixel 7']!;
  test.use({ ...pixel, hasTouch: true, isMobile: true });

  /** A left finger drag of 180 px across the note's panel (as `note-tabs.spec.ts`). */
  async function swipeLeft(page: Page, cdp: CDPSession): Promise<void> {
    const y = 600;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: 300, y }] });
    for (let i = 1; i <= 12; i += 1) {
      await cdp.send('Input.dispatchTouchEvent', {
        type: 'touchMove',
        touchPoints: [{ x: 300 - (180 * i) / 12, y }],
      });
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    await expect(page.getByRole('tab', { name: 'Cleaned' })).toHaveAttribute('aria-selected', 'true');
  }

  test('Back after a swipe still returns Home to where the list was', async ({ page, api }) => {
    manyNotes(api);
    await page.goto('/');
    const before = await scrollRowIntoView(page, /^Filler 30\b/);
    await page.getByRole('button', { name: /^Filler 30\b/ }).first().click();
    await expect(page.getByRole('textbox', { name: 'Note title' })).toBeVisible();
    await swipeLeft(page, await page.context().newCDPSession(page));
    await page.goBack();
    await expect(page.getByRole('button', { name: /^Filler 30\b/ }).first()).toBeVisible();
    await expect.poll(() => mainTop(page)).toBeGreaterThan(before - 4);
    expect(Math.abs((await mainTop(page)) - before)).toBeLessThanOrEqual(4);
  });

  test('from deep in a note a swipe starts the new tab under the strip, and Back then Forward keeps it', async ({
    page,
    api,
  }) => {
    const long = Array.from(
      { length: 80 },
      (_, index) => `Paragraph ${String(index + 1)}. The flashing around the chimney needs replacing.`,
    ).join('\n\n');
    const note = api.notes['roof-repair']!;
    note.body = long;
    // Cleaned as long, so the line under the strip is reachable, not clamped.
    note.cleaned = { body: long, mode: 'structured', generated_at: new Date().toISOString(), stale: false };
    await page.goto('/');
    await page.getByRole('button', { name: /roof repair/i }).click();
    await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();
    await page.locator('.app__main').evaluate((main) => {
      main.scrollTop = 1_500;
      main.dispatchEvent(new Event('scroll'));
    });
    await swipeLeft(page, await page.context().newCDPSession(page));
    // The region's top is the scroll region's: the strip sits where it was
    // stuck and the panel starts under it, at its own top.
    const gap = () =>
      page.evaluate(
        () =>
          document.querySelector('.note-views')!.getBoundingClientRect().top -
          document.querySelector('.app__main')!.getBoundingClientRect().top,
      );
    expect(Math.abs(await gap())).toBeLessThanOrEqual(1);
    const line = await mainTop(page);
    expect(line).toBeGreaterThan(0);
    expect(line).toBeLessThan(1_500);

    await page.goBack();
    await expect(page).toHaveURL(/\/$/);
    await page.goForward();
    await expect(page.getByRole('tab', { name: 'Cleaned' })).toHaveAttribute('aria-selected', 'true');
    await expect.poll(() => mainTop(page)).toBeGreaterThan(line - 4);
    expect(Math.abs((await mainTop(page)) - line)).toBeLessThanOrEqual(4);
  });
});
