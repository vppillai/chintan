import process from 'node:process';

import { devices, type Locator, type Page } from '@playwright/test';

import { CLEAN_WORKER_MS, expect, noteAction, seedChecklist as seedShopping, test } from './fixtures.ts';

/** The text of every field in a list of items, in order — the add row's empty one last. */
function values(list: Locator): Promise<string[]> {
  return list
    .getByRole('textbox')
    .evaluateAll((inputs) => inputs.map((input) => (input as HTMLInputElement).value));
}

/**
 * Checklist notes, end to end against the stubbed API: the Checklists chip
 * on Home, the Items tab with its open and done rows, the Details switch
 * that converts a note and sends `kind` with the body, and the Split up tab
 * whose one mode the server applies unasked.
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
  await expect(tabs).toHaveText(['Items', 'Split up', 'Recordings (0)']);
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

test('Split up has no mode picker, and its list is live: the first tick adopts the split list, later ones edit it', async ({
  page,
  api,
}) => {
  seedShopping(api);
  await page.goto('/notes/shopping?tab=cleaned');
  await expect(page.getByRole('tab', { name: 'Split up' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('group', { name: 'Cleaned view mode' })).toHaveCount(0);
  await expect(page.getByText('Not split up yet')).toBeVisible();

  await page.getByRole('button', { name: 'Generate' }).click();
  await page.waitForTimeout(CLEAN_WORKER_MS);
  const preview = page.getByRole('list', { name: 'Split up items' });
  await expect(preview).toBeVisible({ timeout: 10_000 });
  await expect(preview.getByRole('checkbox')).toHaveCount(4);
  for (const box of await preview.getByRole('checkbox').all()) await expect(box).toBeEnabled();
  await expect(preview.getByRole('textbox')).toHaveCount(0);
  await expect(page.getByText(/generated just now · split up/i)).toBeVisible();
  const caption = page.getByText('Ticking here replaces your list with the split version.');
  await expect(caption).toBeVisible();
  // The request named no mode; the server applied `tasks` itself.
  const clean = api.requests.find((r) => r.method === 'POST' && r.url === '/v1/notes/shopping/clean');
  expect(clean).toBeTruthy();

  // No native box anywhere: the control is a real checkbox hidden under its
  // 44 px label, and the box beside it is drawn.
  const butter = preview.getByRole('checkbox', { name: 'butter' });
  await expect(butter).toHaveCSS('opacity', '0');
  const target = await butter.locator('..').boundingBox();
  expect(target?.height).toBeGreaterThanOrEqual(44);
  expect(target?.width).toBeGreaterThanOrEqual(44);

  // The first tick adopts the split list and ticks butter, in one PATCH.
  await butter.click();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [x] Eggs\n- [ ] Bread\n- [x] butter');
  expect(api.requests.filter((r) => r.method === 'PATCH' && r.url === '/v1/notes/shopping')).toHaveLength(1);
  await expect(butter).toBeChecked();
  await expect(caption).toHaveCount(0);
  await expect(page.getByText(/your list · split up just now/i)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Use this list' })).toHaveCount(0);

  // The next act edits the body it made, not another replacement: the ×
  // under a done item deletes it, and butter stays done.
  await preview.getByRole('button', { name: 'Delete Eggs' }).click();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [ ] Bread\n- [x] butter');

  // Away and back, the tab shows the body, still ticked; Regenerate stays and
  // Use this list does not: the body already is this list.
  await page.getByRole('tab', { name: 'Items' }).click();
  await expect.poll(() => values(page.getByRole('list', { name: 'Items' }))).toEqual(['Milk', 'Bread', '']);
  await page.getByRole('tab', { name: 'Split up' }).click();
  await expect(page.getByRole('list', { name: 'Split up items' }).getByRole('checkbox', { name: 'butter' })).toBeChecked();
  await expect(page.getByText('Ticking here replaces your list with the split version.')).toHaveCount(0);
  await expect(page.getByText('The note changed since this was generated.')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Use this list' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Regenerate' })).toBeVisible();
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
  });
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

      await page.getByRole('tab', { name: 'Split up' }).click();
      await page.getByRole('button', { name: 'Generate' }).click();
      await page.waitForTimeout(CLEAN_WORKER_MS);
      await expect(page.getByRole('list', { name: 'Split up items' })).toBeVisible({ timeout: 10_000 });
      await shot('split-up');
    });
  }
}
