import process from 'node:process';

import { devices, type Locator, type Page } from '@playwright/test';

import { expect, noteAction, seedChecklist as seedShopping, test } from './fixtures.ts';

/** The text of every field in a list of items, in order — the add row's empty one last. */
function values(list: Locator): Promise<string[]> {
  return list
    .getByRole('textbox')
    .evaluateAll((inputs) => inputs.map((input) => (input as HTMLInputElement).value));
}

/**
 * Checklist notes, end to end against the stubbed API: the Checklists chip
 * on Home, the Items tab with its open and done rows, the Details switch
 * that converts a note and sends `kind` with the body, and Tidy up list in
 * the ⋮, whose one mode the server applies unasked.
 */

test('Home has a Checklists chip after All that filters the library through the URL', async ({
  page,
  api,
}) => {
  seedShopping(api);
  await page.goto('/');
  const chips = page.getByRole('group', { name: 'Filter notes' }).getByRole('button');
  await expect(chips.nth(0)).toHaveText('All');
  await expect(chips.nth(1)).toHaveAccessibleName('Checklists · 1');

  // The row shows the open items plainly and how far along the list is.
  const row = page.getByRole('button', { name: /shopping/i });
  await expect(row).toContainText('Milk · Bread and butter');
  await expect(row).toContainText('1 of 3 done');
  await expect(row).not.toContainText('[ ]');

  await chips.nth(1).click();
  await expect(page).toHaveURL(/\?kind=checklist$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toHaveCount(0);
  await expect(row).toBeVisible();
  expect(
    api.requests.some((r) => r.method === 'GET' && r.url.startsWith('/v1/notes?') && r.url.includes('kind=checklist')),
  ).toBe(true);

  await chips.nth(0).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
});

test('the Items tab ticks, adds, edits and deletes items through the note’s own save', async ({
  page,
  api,
}) => {
  seedShopping(api);
  await page.goto('/notes/shopping');
  const tabs = page.getByRole('tablist', { name: 'Note views' }).getByRole('tab');
  await expect(tabs).toHaveText(['Items', 'Recordings (0)']);
  await expect(page.getByText('1 of 3 done')).toBeVisible();

  const items = page.getByRole('list', { name: 'Items' });
  await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);
  await expect(page.getByRole('heading', { name: 'Done (1)' })).toBeVisible();

  // Tick Milk: it moves down, the body's own line flips, the save goes out.
  // `click`, not `check`: the row leaves this list for Done, so the box that
  // was clicked is not the one that ends up checked.
  await items.getByRole('checkbox', { name: 'Milk' }).click();
  await expect(page.getByRole('heading', { name: 'Done (2)' })).toBeVisible();
  await expect(page.getByText('2 of 3 done')).toBeVisible();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [x] Milk\n- [x] Eggs\n- [ ] Bread and butter');

  // Enter under an item starts the next one; the add row appends.
  await items.getByRole('textbox', { name: 'Item 1' }).press('Enter');
  await expect(items.getByRole('textbox', { name: 'Item 2' })).toBeFocused();
  await page.keyboard.type('Jam');
  await items.getByRole('textbox', { name: 'Add an item' }).fill('Tea');
  await page.keyboard.press('Enter');
  await expect.poll(() => values(items)).toEqual(['Bread and butter', 'Jam', 'Tea', '']);
  await page.getByRole('textbox', { name: 'Note title' }).click();
  // Body order is kept — Eggs stays where it was ticked; the new items follow
  // the row they were started from, and the add row's goes last.
  await expect.poll(() => api.notes['shopping']?.body).toBe(
    '- [x] Milk\n- [x] Eggs\n- [ ] Bread and butter\n- [ ] Jam\n- [ ] Tea',
  );

  // A done item can be reopened or deleted for good.
  await page.getByRole('button', { name: 'Delete Eggs' }).click();
  await expect(page.getByRole('heading', { name: 'Done (1)' })).toBeVisible();
  await expect.poll(() => api.notes['shopping']?.body).not.toContain('Eggs');

  // Rows are real targets, 44 px tall.
  const box = await items.getByRole('checkbox', { name: 'Bread and butter' }).locator('..').boundingBox();
  expect(box?.height).toBeGreaterThanOrEqual(44);
  expect(box?.width).toBeGreaterThanOrEqual(44);
});

test('Details turns a note into a checklist and back, sending kind with the converted body', async ({
  page,
  api,
}) => {
  api.notes['roof-repair']!.body = 'Ridge tiles on the south slope have slipped.\n\nGet two quotes.';
  await page.goto('/notes/roof-repair');
  await noteAction(page, 'Details');
  const toggle = page.getByRole('checkbox', { name: 'This note is a checklist' });
  await expect(toggle).not.toBeChecked();

  await toggle.check();
  await expect(page.getByRole('tab', { name: 'Items' })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Item 1' })).toHaveValue(
    'Ridge tiles on the south slope have slipped.',
  );
  await expect.poll(() => api.notes['roof-repair']?.kind).toBe('checklist');
  expect(api.notes['roof-repair']?.body).toBe(
    '- [ ] Ridge tiles on the south slope have slipped.\n- [ ] Get two quotes.',
  );
  const patch = api.requests.find((r) => r.method === 'PATCH' && r.url === '/v1/notes/roof-repair');
  expect(patch).toBeTruthy();

  // And back: every item a paragraph, the switch's state reloaded from the note.
  await page.reload();
  await noteAction(page, 'Details');
  await expect(page.getByRole('checkbox', { name: 'This note is a checklist' })).toBeChecked();
  await page.getByRole('checkbox', { name: 'This note is a checklist' }).uncheck();
  await expect(page.getByRole('tab', { name: 'Text' })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Note body' })).toHaveValue(
    'Ridge tiles on the south slope have slipped.\n\nGet two quotes.',
  );
  await expect.poll(() => api.notes['roof-repair']?.kind).toBe('note');
});

test('Tidy up list in the ⋮ writes the split list into the body, with Undo that puts the list back', async ({
  page,
  api,
}) => {
  seedShopping(api);
  // A remembered or linked Cleaned tab lands on Items: a checklist has none.
  await page.goto('/notes/shopping?tab=cleaned');
  await expect(page.getByRole('tab', { name: 'Items' })).toHaveAttribute('aria-selected', 'true');
  const items = page.getByRole('list', { name: 'Items' });
  await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);

  await noteAction(page, 'Tidy up list');
  await expect(page.getByText('Tidying the list…')).toBeVisible();
  // The request named no mode; the server applied `tasks` itself.
  const clean = api.requests.find((r) => r.method === 'POST' && r.url === '/v1/notes/shopping/clean');
  expect(clean).toBeTruthy();

  // The rows are replaced in place, Eggs still done, and the body saved once.
  await expect.poll(() => values(items), { timeout: 10_000 }).toEqual(['Milk', 'Bread', 'butter', '']);
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [x] Eggs\n- [ ] Bread\n- [ ] butter');
  expect(saves(api)).toBe(1);
  await expect(page.getByText('List tidied: 3 lines → 4 items.')).toBeVisible();
  await expect(page.getByText('Tidying the list…')).toHaveCount(0);

  await page.getByRole('button', { name: 'Undo' }).click();
  await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [x] Eggs\n- [ ] Bread and butter');
});

/** The PATCHes the note has received. */
function saves(api: { requests: { method: string; url: string }[] }): number {
  return api.requests.filter((r) => r.method === 'PATCH' && r.url === '/v1/notes/shopping').length;
}

test.describe('reordering by the grip', () => {
  test('a mouse drag on a grip re-sorts the rows under the pointer and writes the body once, on release', async ({
    page,
    api,
  }) => {
    seedShopping(api);
    await page.goto('/notes/shopping');
    const items = page.getByRole('list', { name: 'Items' });
    await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);

    // The grip is a real target, 44 px each way, before the box.
    const grip = page.getByRole('button', { name: 'Move Milk' });
    const from = (await grip.boundingBox())!;
    expect(from.width).toBeGreaterThanOrEqual(44);
    expect(from.height).toBeGreaterThanOrEqual(44);
    const below = (await items.locator('li').nth(1).boundingBox())!;

    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
    await page.mouse.down();
    // Past the next row's midpoint, in steps, as a hand moves.
    await page.mouse.move(from.x + from.width / 2, below.y + below.height - 2, { steps: 8 });
    // Lifted: the list has re-sorted, and nothing is saved yet.
    await expect.poll(() => values(items)).toEqual(['Bread and butter', 'Milk', '']);
    expect(saves(api)).toBe(0);
    await page.mouse.up();

    // One write: Milk takes Bread's slot; Eggs, done, keeps its line where it was.
    await expect.poll(() => api.notes['shopping']?.body).toBe('- [x] Eggs\n- [ ] Bread and butter\n- [ ] Milk');
    expect(saves(api)).toBe(1);
    // The release did not open the grip's menu.
    await expect(page.getByRole('menu')).toHaveCount(0);

    // The arrow keys on a grip move a row one slot, for a keyboard.
    await page.getByRole('button', { name: 'Move Milk' }).focus();
    await page.keyboard.press('ArrowUp');
    await expect.poll(() => api.notes['shopping']?.body).toBe('- [x] Eggs\n- [ ] Milk\n- [ ] Bread and butter');
    await expect(page.getByRole('button', { name: 'Move Milk' })).toBeFocused();
  });

  test('a vertical drag moves a three-level block as one, its levels intact', async ({ page, api }) => {
    seedShopping(api);
    const body = '- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n- [ ] Milk';
    Object.assign(api.notes['shopping']!, { body, snippet: body });
    await page.goto('/notes/shopping');
    const items = page.getByRole('list', { name: 'Items' });
    await expect.poll(() => values(items)).toEqual(['Party', 'Costco', 'Plates', 'Milk', '']);

    const from = (await page.getByRole('button', { name: 'Move Party' }).boundingBox())!;
    const milk = (await items.locator('li').nth(3).boundingBox())!;
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
    await page.mouse.down();
    await page.mouse.move(from.x + from.width / 2, milk.y + milk.height - 2, { steps: 10 });
    await page.mouse.up();

    // Party lands after Milk with both levels under it, as they were.
    await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [ ] Party\n  - [ ] Costco\n    - [ ] Plates');
    expect(saves(api)).toBe(1);
    await expect.poll(() => values(items)).toEqual(['Milk', 'Party', 'Costco', 'Plates', '']);
    await expect(items.locator('li').nth(3)).toHaveAttribute('data-depth', '2');
  });

  test('a tap on the grip opens the row’s menu, the path that needs no drag', async ({ page, api }) => {
    seedShopping(api);
    await page.goto('/notes/shopping');
    const items = page.getByRole('list', { name: 'Items' });
    await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);

    await page.getByRole('button', { name: 'Move Bread and butter' }).click();
    const menu = page.getByRole('menu');
    await expect(menu).toBeVisible();
    await expect(menu.getByRole('menuitem')).toHaveText([
      'Move up',
      'Move down',
      'Move to top',
      'Move to bottom',
      'Make a sub-item',
      'Move up a level',
      'Delete',
    ]);
    // The bottom row cannot move down.
    await expect(menu.getByRole('menuitem', { name: 'Move down' })).toBeDisabled();
    await menu.getByRole('menuitem', { name: 'Move to top' }).click();
    await expect.poll(() => values(items)).toEqual(['Bread and butter', 'Milk', '']);
    await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Bread and butter\n- [ ] Milk\n- [x] Eggs');
    await expect(menu).toHaveCount(0);
  });

  test.describe('with a finger', () => {
    // The descriptor's `defaultBrowserType` cannot be set inside a describe.
    const { defaultBrowserType: _browser, ...pixel } = devices['Pixel 7']!;
    test.use({ ...pixel, hasTouch: true, isMobile: true });
    // The finger here is a CDP touch session, which only Chromium has.
    test.skip(({ browserName }) => browserName === 'webkit', 'CDP touch');

    test('a touch drag on a grip lifts the row at once — no hold — and reorders', async ({ page, api }) => {
      seedShopping(api);
      await page.goto('/notes/shopping');
      const items = page.getByRole('list', { name: 'Items' });
      await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);

      const cdp = await page.context().newCDPSession(page);
      const grip = (await page.getByRole('button', { name: 'Move Milk' }).boundingBox())!;
      const below = (await items.locator('li').nth(1).boundingBox())!;
      const x = grip.x + grip.width / 2;
      const y = grip.y + grip.height / 2;
      const toY = below.y + below.height - 2;
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
      const steps = 10;
      for (let i = 1; i <= steps; i += 1) {
        await cdp.send('Input.dispatchTouchEvent', {
          type: 'touchMove',
          touchPoints: [{ x, y: y + ((toY - y) * i) / steps }],
        });
      }
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });

      await expect.poll(() => api.notes['shopping']?.body).toBe('- [x] Eggs\n- [ ] Bread and butter\n- [ ] Milk');
      // The finger lifting after the drag neither opened the menu nor ticked a box.
      await expect(page.getByRole('menu')).toHaveCount(0);
      await expect(page.getByText('1 of 3 done')).toBeVisible();
    });

    test('a touch drag sideways on a grip makes the row a sub-item, previewed in place and kept across a reload', async ({
      page,
      api,
    }) => {
      seedShopping(api);
      await page.goto('/notes/shopping');
      const items = page.getByRole('list', { name: 'Items' });
      await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);
      const row = items.locator('li').nth(1);

      const cdp = await page.context().newCDPSession(page);
      const grip = (await page.getByRole('button', { name: 'Move Bread and butter' }).boundingBox())!;
      const x = grip.x + grip.width / 2;
      const y = grip.y + grip.height / 2;
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
      // One indent step and a little, to the right, level: the first ten
      // pixels decide the axis, then every 24 px is a level.
      const steps = 6;
      for (let i = 1; i <= steps; i += 1) {
        await cdp.send('Input.dispatchTouchEvent', {
          type: 'touchMove',
          touchPoints: [{ x: x + (30 * i) / steps, y: y + 1 }],
        });
      }
      // In the air: the row keeps its slot, shows the level it would take, and nothing is saved.
      await expect(row).toHaveAttribute('data-preview-depth', '1');
      await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);
      expect(saves(api)).toBe(0);
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });

      // One write, saved at once: two spaces of indent, under the row shown
      // above — Milk — with the done Eggs line, which showed nowhere, below.
      await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n  - [ ] Bread and butter\n- [x] Eggs');
      expect(saves(api)).toBe(1);
      await expect(row).toHaveAttribute('data-depth', '1');
      await expect(row).not.toHaveAttribute('data-preview-depth');
      await expect(page.getByRole('menu')).toHaveCount(0);

      // The body is what is reloaded, and the indent is read back from it.
      await page.reload();
      await expect(page.getByRole('list', { name: 'Items' }).locator('li').nth(1)).toHaveAttribute('data-depth', '1');
      await expect(page.getByRole('textbox', { name: 'Sub-item 2' })).toHaveValue('Bread and butter');
    });

    test('a touch drag two levels in, in one release, previews the third level and keeps it across a reload', async ({
      page,
      api,
    }) => {
      seedShopping(api);
      const body = '- [ ] Party\n  - [ ] Costco\n- [ ] Plates';
      Object.assign(api.notes['shopping']!, { body, snippet: body });
      await page.goto('/notes/shopping');
      const items = page.getByRole('list', { name: 'Items' });
      await expect.poll(() => values(items)).toEqual(['Party', 'Costco', 'Plates', '']);
      const row = items.locator('li').nth(2);
      const indent = (): Promise<number> => row.evaluate((li) => Number.parseFloat(getComputedStyle(li).paddingInlineStart));
      const cdp = await page.context().newCDPSession(page);

      /** A finger on the grip, `dx` sideways in six moves; lifted only when `lift` says. */
      const slide = async (dx: number): Promise<void> => {
        const grip = (await row.locator('.checklist__grip').boundingBox())!;
        const x = grip.x + grip.width / 2;
        const y = grip.y + grip.height / 2;
        await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
        for (let i = 1; i <= 6; i += 1) {
          await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x + (dx * i) / 6, y: y + 1 }] });
        }
      };
      const lift = (): Promise<unknown> => cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });

      // Two indent steps and a little: the row is drawn at the third level in the air.
      await slide(55);
      await expect(row).toHaveAttribute('data-preview-depth', '2');
      await expect.poll(indent).toBe(48);
      expect(saves(api)).toBe(0);
      await lift();
      await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Plates');
      expect(saves(api)).toBe(1);

      await page.reload();
      const reloaded = page.getByRole('list', { name: 'Items' }).locator('li').nth(2);
      await expect(reloaded).toHaveAttribute('data-depth', '2');
      await expect(reloaded).toHaveAttribute('aria-level', '3');
      await expect(page.getByRole('textbox', { name: 'Sub-item 3, level 3' })).toHaveValue('Plates');

      // One step back out previews the second level, not the top: the
      // preview's depth rule outranks the row's own.
      await slide(-30);
      await expect(row).toHaveAttribute('data-preview-depth', '1');
      await expect.poll(indent).toBe(24);
      await lift();
      await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Party\n  - [ ] Costco\n  - [ ] Plates');
    });
  });
});

test('Tab in an item makes it a sub-item, set in by one step and kept across a reload; Shift+Tab brings it up', async ({
  page,
  api,
}) => {
  seedShopping(api);
  await page.goto('/notes/shopping');
  const items = page.getByRole('list', { name: 'Items' });
  await expect.poll(() => values(items)).toEqual(['Milk', 'Bread and butter', '']);
  const milkRow = items.locator('li').nth(0);
  const breadRow = items.locator('li').nth(1);
  const before = (await breadRow.locator('.checklist__grip').boundingBox())!;

  await items.getByRole('textbox', { name: 'Item 2' }).focus();
  await page.keyboard.press('Tab');
  // One write, saved at once: two spaces of indent, under the row shown
  // above — Milk — with the done Eggs line, which showed nowhere, below.
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n  - [ ] Bread and butter\n- [x] Eggs');
  // The field keeps focus and is named for what it is now.
  await expect(items.getByRole('textbox', { name: 'Sub-item 2' })).toBeFocused();
  // Set in by one step from where it stood; the row above did not move.
  const after = (await breadRow.locator('.checklist__grip').boundingBox())!;
  expect(after.x - before.x).toBeGreaterThanOrEqual(20);
  expect((await milkRow.locator('.checklist__grip').boundingBox())!.x).toBe(before.x);

  // The body is what is reloaded, and the indent is read back from it.
  await page.reload();
  await expect(page.getByRole('list', { name: 'Items' }).getByRole('textbox', { name: 'Sub-item 2' })).toHaveValue(
    'Bread and butter',
  );
  await expect(page.getByRole('list', { name: 'Items' }).locator('li').nth(1)).toHaveAttribute('data-depth', '1');

  // Shift+Tab brings it back up in place; the first item can never go in.
  await page.getByRole('textbox', { name: 'Sub-item 2' }).focus();
  await page.keyboard.press('Shift+Tab');
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [ ] Bread and butter\n- [x] Eggs');
  await expect(page.getByRole('textbox', { name: 'Item 2' })).toBeFocused();
  await page.getByRole('textbox', { name: 'Item 1' }).focus();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('textbox', { name: 'Item 1' })).not.toBeFocused();
  expect(api.notes['shopping']?.body).toBe('- [ ] Milk\n- [ ] Bread and butter\n- [x] Eggs');

  // The grip's menu is the path for a finger: Make a sub-item, then Move up a level.
  await page.getByRole('button', { name: 'Move Bread and butter' }).click();
  const menu = page.getByRole('menu');
  await expect(menu.getByRole('menuitem', { name: 'Move up a level' })).toBeDisabled();
  await menu.getByRole('menuitem', { name: 'Make a sub-item' }).click();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n  - [ ] Bread and butter\n- [x] Eggs');
  await page.getByRole('button', { name: 'Move Bread and butter' }).click();
  await expect(page.getByRole('menu').getByRole('menuitem', { name: 'Make a sub-item' })).toBeDisabled();
  await page.getByRole('menu').getByRole('menuitem', { name: 'Move up a level' }).click();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [ ] Bread and butter\n- [x] Eggs');

  // Ticking a parent ticks its sub-items and the block moves to Done as one.
  await page.getByRole('textbox', { name: 'Item 2' }).focus();
  await page.keyboard.press('Tab');
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n  - [ ] Bread and butter\n- [x] Eggs');
  await items.getByRole('checkbox', { name: 'Milk' }).click();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [x] Milk\n  - [x] Bread and butter\n- [x] Eggs');
  await expect(page.getByRole('heading', { name: 'Done (3)' })).toBeVisible();
  await expect(page.getByText('3 of 3 done')).toBeVisible();
  const doneList = page.getByRole('region', { name: 'Done (3)' }).getByRole('list');
  await expect(doneList.locator('li').nth(1)).toHaveAttribute('data-depth', '1');
});

test('Tab takes an item to the third level, one step at a time, kept across a reload', async ({ page, api }) => {
  seedShopping(api);
  const body = '- [ ] Party\n- [ ] Costco\n- [ ] Plates';
  Object.assign(api.notes['shopping']!, { body, snippet: body });
  await page.goto('/notes/shopping');
  const items = page.getByRole('list', { name: 'Items' });
  await expect.poll(() => values(items)).toEqual(['Party', 'Costco', 'Plates', '']);
  const grip = (n: number) => items.locator('li').nth(n).locator('.checklist__grip').boundingBox();
  const top = (await grip(0))!.x;

  await items.getByRole('textbox', { name: 'Item 2' }).focus();
  await page.keyboard.press('Tab');
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Party\n  - [ ] Costco\n- [ ] Plates');
  await items.getByRole('textbox', { name: 'Item 3' }).focus();
  // Under a sub-item one Tab is its sibling, a second its child.
  await page.keyboard.press('Tab');
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Party\n  - [ ] Costco\n  - [ ] Plates');
  await page.keyboard.press('Tab');
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Party\n  - [ ] Costco\n    - [ ] Plates');
  const field = items.getByRole('textbox', { name: 'Sub-item 3, level 3' });
  await expect(field).toBeFocused();
  // Two indent steps in from the top level's grip.
  await expect.poll(async () => (await grip(2))!.x - top).toBe(48);
  // The third level is the last: the key is the browser's again.
  await page.keyboard.press('Tab');
  await expect(field).not.toBeFocused();

  await page.reload();
  const row = page.getByRole('list', { name: 'Items' }).locator('li').nth(2);
  await expect(row).toHaveAttribute('data-depth', '2');
  await expect(row).toHaveAttribute('aria-level', '3');
  await expect(page.getByRole('textbox', { name: 'Sub-item 3, level 3' })).toHaveValue('Plates');
});

test('Done is a disclosure remembered for the session; Delete done is undone from the keyboard; Uncheck all reopens in place', async ({
  page,
  api,
}) => {
  seedShopping(api);
  await page.goto('/notes/shopping');
  const done = page.getByRole('region', { name: 'Done (1)' });
  const toggle = done.getByRole('button', { name: 'Done (1)' });
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(done.getByRole('checkbox', { name: 'Eggs' })).toBeVisible();

  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await expect(done.getByRole('checkbox', { name: 'Eggs' })).toBeHidden();
  // Remembered across a reload, for this tab.
  await page.reload();
  await expect(page.getByRole('button', { name: 'Done (1)' })).toHaveAttribute('aria-expanded', 'false');
  await page.getByRole('button', { name: 'Done (1)' }).click();
  await expect(page.getByRole('checkbox', { name: 'Eggs' })).toBeVisible();

  // Delete done asks nothing: the body loses its done line and Undo waits in
  // the shell's toast, a real button a keyboard reaches.
  await page.getByRole('button', { name: 'Delete done' }).click();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [ ] Bread and butter');
  await expect(page.getByRole('region', { name: /^Done/ })).toHaveCount(0);
  const undo = page.getByRole('button', { name: 'Undo' });
  await expect(undo).toBeVisible();
  await undo.focus();
  await page.keyboard.press('Enter');
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [x] Eggs\n- [ ] Bread and butter');
  await expect(page.getByRole('heading', { name: 'Done (1)' })).toBeVisible();

  await page.getByRole('button', { name: 'Uncheck all' }).click();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [ ] Eggs\n- [ ] Bread and butter');
  await expect(page.getByRole('region', { name: /^Done/ })).toHaveCount(0);
  await expect(page.getByText('0 of 3 done')).toBeVisible();
});

/*
 * Pictures of the three screens, in both themes, on a phone and a desktop.
 * Opt-in like the layout sweep: `CHECKLIST_SHOTS=1 bun run e2e checklist`
 * writes them under e2e/__screenshots__/sweep/, which is gitignored.
 */
const SHOTS = process.env['CHECKLIST_SHOTS'] === '1';

const SHOT_VIEWPORTS = [
  { name: 'pixel-8-pro', width: 448, height: 998 },
  { name: 'desktop', width: 1280, height: 800 },
] as const;

async function useTheme(page: Page, theme: string): Promise<void> {
  await page.addInitScript((value) => {
    localStorage.setItem('chintan.theme', value);
  }, theme);
}

for (const viewport of SHOT_VIEWPORTS) {
  for (const theme of ['ink', 'nocturne'] as const) {
    test(`screenshots · ${viewport.name} · ${theme}`, async ({ page, api }) => {
      test.skip(!SHOTS, 'CHECKLIST_SHOTS=1 to write the pictures');
      seedShopping(api);
      api.notes['shopping']!.body = '- [ ] Milk\n- [x] Eggs\n- [ ] Bread and butter\n- [x] Call the tiler\n- [ ] Pick up the quotes from Ellis';
      api.notes['shopping']!.snippet = api.notes['shopping']!.body;
      await useTheme(page, theme);
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      const shot = (name: string) =>
        page.screenshot({ path: `e2e/__screenshots__/sweep/checklist-${name}-${viewport.name}-${theme}.png` });

      await page.goto('/');
      await expect(page.getByRole('button', { name: /shopping/i })).toBeVisible();
      await shot('home');

      await page.goto('/notes/shopping');
      await expect(page.getByRole('heading', { name: 'Done (2)' })).toBeVisible();
      await shot('items');

      await noteAction(page, 'Tidy up list');
      await expect(page.getByText(/^List tidied:/)).toBeVisible({ timeout: 10_000 });
      await shot('tidied');
    });
  }
}

for (const theme of ['ink', 'nocturne'] as const) {
  test(`screenshots · three levels and the drag preview · ${theme}`, async ({ page, api }) => {
    test.skip(!SHOTS, 'CHECKLIST_SHOTS=1 to write the pictures');
    seedShopping(api);
    const body = '- [ ] Party\n  - [ ] Costco\n    - [ ] Plates\n    - [ ] Cups, the paper ones for twenty people\n  - [ ] Target\n  - [ ] Candles\n- [ ] Milk';
    Object.assign(api.notes['shopping']!, { body, snippet: body });
    await useTheme(page, theme);
    await page.setViewportSize({ width: 360, height: 800 });
    const shot = (name: string) => page.screenshot({ path: `e2e/__screenshots__/sweep/checklist-${name}-360-${theme}.png` });
    await page.goto('/notes/shopping');
    await expect(page.getByRole('textbox', { name: 'Sub-item 3, level 3' })).toBeVisible();
    await shot('depth');
    // Candles lifted a level in, under Target: the mockup's sideways preview.
    const grip = (await page.getByRole('button', { name: 'Move Candles' }).boundingBox())!;
    await page.mouse.move(grip.x + grip.width / 2, grip.y + grip.height / 2);
    await page.mouse.down();
    await page.mouse.move(grip.x + grip.width / 2 + 30, grip.y + grip.height / 2 + 1, { steps: 6 });
    const lifted = page.locator('[data-preview-depth="2"]');
    await expect(lifted).toHaveCount(1);
    // Past the indent's transition, so the picture shows where it lands.
    await expect
      .poll(() => lifted.evaluate((li) => Number.parseFloat(getComputedStyle(li).paddingInlineStart)))
      .toBe(48);
    await shot('depth-preview');
    await page.mouse.up();
  });
}

test('Show after a recording lands in a list puts focus on the row it added', async ({ page, api }) => {
  seedShopping(api);
  const note = api.notes['shopping']!;
  note.captures = [
    {
      id: 'cap-into-list',
      note_id: 'shopping',
      status: 'transcribing',
      created_at: new Date().toISOString(),
      version: 1,
      targeted: true,
    },
  ];
  await page.goto('/notes/shopping');
  const banner = page.getByRole('region', { name: 'Filing a recording' });
  await expect(banner).toContainText('Transcribing');

  note.captures[0]!.status = 'appended';
  note.body += '\n- [ ] Jam';
  note.version += 1;
  await expect(banner).toContainText('Added to the list', { timeout: 10_000 });
  await banner.getByRole('button', { name: 'Show' }).focus();
  await page.keyboard.press('Enter');

  const jam = page.getByRole('list', { name: 'Items' }).locator('li[data-flash]');
  await expect(jam).toHaveCount(1);
  await expect(jam.getByRole('textbox')).toBeFocused();
  await expect(jam.getByRole('textbox')).toHaveValue('Jam');
});
