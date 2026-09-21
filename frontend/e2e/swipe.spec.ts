import { devices, type CDPSession, type Locator, type Page } from '@playwright/test';

import { expect, test, type ApiState } from './fixtures.ts';

/**
 * The swipe-to-reveal gesture, driven with a real touch in a real Chromium.
 *
 * The desktop descriptors match `(hover: hover) and (pointer: fine)`, so under
 * them `SwipeRow` never renders a tray and no swipe was ever exercised outside
 * jsdom — which has no pointer capture, the mechanism the gesture turned out
 * to break on (review 2026-09-21 T1, T31). Touch emulation gives the coarse
 * pointer; `Input.dispatchTouchEvent` gives the browser's own touch pipeline,
 * implicit capture and `touch-action: pan-y` scrolling included, which
 * `page.mouse` and `page.touchscreen.tap` do not.
 */

test.use({ ...devices['Pixel 7'], hasTouch: true, isMobile: true });

/** A finger from the row's centre, `dx` across and `dy` down, in a dozen moves. */
async function drag(cdp: CDPSession, row: Locator, dx: number, dy: number): Promise<void> {
  const box = await row.boundingBox();
  if (!box) throw new Error('the row has no box');
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  const steps = 12;
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
  for (let i = 1; i <= steps; i += 1) {
    await cdp.send('Input.dispatchTouchEvent', {
      type: 'touchMove',
      touchPoints: [{ x: x + (dx * i) / steps, y: y + (dy * i) / steps }],
    });
  }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
}

function swipeX(row: Locator): Promise<string> {
  return row.evaluate((el) => el.style.getPropertyValue('--swipe-x'));
}

/** The tray's full width, which is what the settled row's offset must equal. */
function trayWidth(row: Locator): Promise<number> {
  return row.locator('.swipe__actions').evaluate((el) => (el as HTMLElement).offsetWidth);
}

/** Enough rows that the library scrolls on a phone. */
function fillTheLibrary(api: ApiState): void {
  for (let i = 0; i < 30; i += 1) {
    api.notes[`filler-${String(i)}`] = {
      id: `filler-${String(i)}`,
      title: `Filler ${String(i)}`,
      body: 'Padding.',
      snippet: 'Padding.',
      tags: [],
      aliases: [],
      updated_at: new Date(Date.UTC(2026, 0, 1 + i)).toISOString(),
      version: 1,
      archived: false,
      captures: [],
    };
  }
}

async function openLibrary(page: Page): Promise<CDPSession> {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  return page.context().newCDPSession(page);
}

test('a sideways drag opens exactly one tray, to the tray width, and is not a tap', async ({
  page,
}) => {
  const cdp = await openLibrary(page);
  const rows = page.locator('.swipe');
  const first = rows.first();

  await drag(cdp, first.locator('.note-row'), -220, 0);

  await expect(first).toHaveAttribute('data-open');
  await expect(page.locator('.swipe[data-open]')).toHaveCount(1);
  expect(await swipeX(first)).toBe(`-${String(await trayWidth(first))}px`);
  await expect(first.getByRole('button', { name: 'Archive' })).toBeVisible();
  await expect(first.getByRole('button', { name: 'Delete' })).toBeVisible();
  // The finger lifting fires a click on the row underneath; it must not open the note.
  await expect(page).toHaveURL(/\/$/);
});

test('a drag down scrolls the library and opens nothing', async ({ page, api }) => {
  fillTheLibrary(api);
  const cdp = await openLibrary(page);
  const main = page.locator('.app__main');
  expect(await main.evaluate((el) => el.scrollTop)).toBe(0);

  await drag(cdp, page.locator('.note-row').first(), 0, -200);

  await expect.poll(() => main.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
  await expect(page.locator('.swipe[data-open]')).toHaveCount(0);
  expect(await swipeX(page.locator('.swipe').first())).toBe('0px');
});

test('swiping a second row closes the first', async ({ page }) => {
  const cdp = await openLibrary(page);
  const rows = page.locator('.swipe');

  await drag(cdp, rows.nth(0).locator('.note-row'), -220, 0);
  await expect(rows.nth(0)).toHaveAttribute('data-open');

  await drag(cdp, rows.nth(1).locator('.note-row'), -220, 0);

  await expect(rows.nth(1)).toHaveAttribute('data-open');
  await expect(rows.nth(0)).not.toHaveAttribute('data-open');
  expect(await swipeX(rows.nth(0))).toBe('0px');
  await expect(page.locator('.swipe[data-open]')).toHaveCount(1);
});
