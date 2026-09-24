import { devices, type CDPSession, type Locator, type Page } from '@playwright/test';

import { expect, noteAction, test, type ApiState } from './fixtures.ts';

/**
 * Pinned notes (2026-09-24 contract, B): a group of their own at the top of
 * Home, Pin and Unpin on the row's ⋮ and in the note's own menu, and the order
 * within the group set by dragging — a mouse on the grip, a finger by holding
 * the row — ending in one `POST /v1/notes/pins`.
 */

function pinsRequests(api: ApiState): { method: string; url: string }[] {
  return api.requests.filter((request) => request.url === '/v1/notes/pins');
}

/** The ⋮ of the row with this title: named "More", described by the title. */
function moreFor(page: Page, title: string): Locator {
  return page
    .locator('.note-row-wrap', { has: page.locator('.note-row__title', { hasText: title }) })
    .getByRole('button', { name: 'More' });
}

/** Titles of the rows inside the Pinned group, top to bottom. */
function pinnedTitles(page: Page): Promise<string[]> {
  return page
    .locator('.note-group--pinned .note-row__title')
    .evaluateAll((nodes) => nodes.map((node) => node.textContent?.trim() ?? ''));
}

test.describe('with a mouse', () => {
  test('pinned notes sit in their own group above the days, and Pin on the row moves a note there', async ({
    page,
    api,
  }) => {
    api.notes['reading-list']!.pinned = true;
    api.notes['reading-list']!.pin_rank = 0;
    await page.goto('/');

    const pinned = page.getByRole('region', { name: 'Pinned' });
    await expect(pinned).toBeVisible();
    await expect(pinned.getByRole('button', { name: /^reading list/i })).toBeVisible();
    // The group comes before the first day, and the pinned note is not in the days too.
    const groups = await page
      .locator('.note-group__label')
      .evaluateAll((nodes) => nodes.map((node) => node.textContent));
    expect(groups[0]).toBe('Pinned');
    await expect(page.getByRole('button', { name: /^reading list/i })).toHaveCount(1);

    // No checkbox appears on hover any more; the row's ⋮ does.
    const roof = page.getByRole('button', { name: /^roof repair/i });
    await roof.hover();
    await expect(page.getByRole('checkbox')).toHaveCount(0);
    await moreFor(page, 'Roof repair').click();
    await page.getByRole('menuitem', { name: 'Pin' }).click();

    await expect(pinned.getByRole('button', { name: /^roof repair/i })).toBeVisible();
    const patch = api.requests.find(
      (request) => request.method === 'PATCH' && request.url === '/v1/notes/roof-repair',
    );
    expect(patch).toBeDefined();
    expect(api.notes['roof-repair']?.pinned).toBe(true);
    // A new pin lands last among the pinned.
    expect(await pinnedTitles(page)).toEqual(['Reading list', 'Roof repair']);
  });

  test('dragging a grip reorders the group and sends the whole order once', async ({
    page,
    api,
  }) => {
    api.notes['reading-list']!.pinned = true;
    api.notes['reading-list']!.pin_rank = 0;
    api.notes['roof-repair']!.pinned = true;
    api.notes['roof-repair']!.pin_rank = 1000;
    await page.goto('/');
    await expect.poll(() => pinnedTitles(page)).toEqual(['Reading list', 'Roof repair']);

    const grip = page.getByRole('button', { name: 'Move Roof repair' });
    const from = (await grip.boundingBox())!;
    const target = (await page.getByRole('button', { name: /^reading list/i }).boundingBox())!;
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
    await page.mouse.down();
    // Past the other row's midpoint, in steps, as a hand moves.
    await page.mouse.move(from.x + from.width / 2, target.y + 4, { steps: 8 });
    await page.mouse.up();

    await expect
      .poll(() => pinnedTitles(page))
      .toEqual(['Roof repair', 'Reading list']);
    await expect.poll(() => pinsRequests(api).length).toBe(1);
    expect(api.notes['roof-repair']?.pin_rank).toBe(0);
    expect(api.notes['reading-list']?.pin_rank).toBe(1000);
    // The drag did not open the note it lifted.
    await expect(page).toHaveURL(/\/$/);

    // The arrow keys on a grip move a row one step, for a keyboard.
    await page.getByRole('button', { name: 'Move Roof repair' }).focus();
    await page.keyboard.press('ArrowDown');
    await expect
      .poll(() => pinnedTitles(page))
      .toEqual(['Reading list', 'Roof repair']);
    await expect.poll(() => pinsRequests(api).length).toBe(2);
  });

  test('Unpin is in the note’s own menu, and the row leaves the group', async ({ page, api }) => {
    api.notes['roof-repair']!.pinned = true;
    api.notes['roof-repair']!.pin_rank = 0;
    await page.goto('/notes/roof-repair');
    await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

    await noteAction(page, 'Unpin');
    await expect.poll(() => api.notes['roof-repair']?.pinned).toBe(false);

    await page.goto('/');
    await expect(page.getByRole('button', { name: /^roof repair/i })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Pinned' })).toHaveCount(0);
  });
});

test.describe('on a phone', () => {
  // The descriptor's `defaultBrowserType` cannot be set inside a describe.
  const { defaultBrowserType: _browser, ...pixel } = devices['Pixel 7']!;
  test.use({ ...pixel, hasTouch: true, isMobile: true });

  /** A finger that lands, waits, then travels — the hold-then-drag. */
  async function holdAndDrag(
    cdp: CDPSession,
    row: Locator,
    toY: number,
    holdMs: number,
  ): Promise<void> {
    const box = (await row.boundingBox())!;
    const x = box.x + box.width / 2;
    const y = box.y + box.height / 2;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
    await new Promise((resolve) => setTimeout(resolve, holdMs));
    const steps = 10;
    for (let i = 1; i <= steps; i += 1) {
      await cdp.send('Input.dispatchTouchEvent', {
        type: 'touchMove',
        touchPoints: [{ x, y: y + ((toY - y) * i) / steps }],
      });
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  }

  test('holding a pinned row lifts it, and dragging reorders; the ⋮ is always there', async ({
    page,
    api,
  }) => {
    api.notes['reading-list']!.pinned = true;
    api.notes['reading-list']!.pin_rank = 0;
    api.notes['roof-repair']!.pinned = true;
    api.notes['roof-repair']!.pin_rank = 1000;
    await page.goto('/');
    await expect.poll(() => pinnedTitles(page)).toEqual(['Reading list', 'Roof repair']);
    // No grip on a phone; the ⋮ is visible without a hover.
    await expect(page.getByRole('button', { name: 'Move Roof repair' })).toHaveCount(0);
    await expect(moreFor(page, 'Roof repair')).toBeVisible();

    const cdp = await page.context().newCDPSession(page);
    const roof = page.getByRole('button', { name: /^roof repair/i });
    const above = (await page.getByRole('button', { name: /^reading list/i }).boundingBox())!;
    await holdAndDrag(cdp, roof, above.y + 4, 650);

    await expect
      .poll(() => pinnedTitles(page))
      .toEqual(['Roof repair', 'Reading list']);
    await expect.poll(() => pinsRequests(api).length).toBe(1);
    // The finger lifting after the drag did not open the note.
    await expect(page).toHaveURL(/\/$/);
  });

  test('Pin is in the swipe tray', async ({ page, api }) => {
    await page.goto('/');
    const row = page.locator('.swipe', { hasText: 'Roof repair' });
    await expect(row).toBeVisible();
    const cdp = await page.context().newCDPSession(page);
    const box = (await row.boundingBox())!;
    const x = box.x + box.width / 2;
    const y = box.y + box.height / 2;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
    for (let i = 1; i <= 12; i += 1) {
      await cdp.send('Input.dispatchTouchEvent', {
        type: 'touchMove',
        touchPoints: [{ x: x - (240 * i) / 12, y }],
      });
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });

    const tray = page.getByRole('group', { name: 'Actions for Roof repair' });
    await expect(tray).toBeVisible();
    await expect(tray.getByRole('button')).toHaveText(['Pin', 'Archive', 'Delete']);
    await tray.getByRole('button', { name: 'Pin' }).click();
    await expect.poll(() => api.notes['roof-repair']?.pinned).toBe(true);
    await expect(page.getByRole('region', { name: 'Pinned' })).toBeVisible();
  });
});
