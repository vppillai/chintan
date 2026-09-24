import process from 'node:process';

import type { Locator, Page } from '@playwright/test';

import { CLEAN_WORKER_MS, expect, noteAction, test, type ApiState } from './fixtures.ts';

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

function seedShopping(api: ApiState): void {
  api.notes['shopping'] = {
    id: 'shopping',
    kind: 'checklist',
    title: 'Shopping',
    body: '- [ ] Milk\n- [x] Eggs\n- [ ] Bread and butter',
    snippet: '- [ ] Milk\n- [x] Eggs\n- [ ] Bread and butter',
    tags: ['house'],
    aliases: [],
    updated_at: '2026-08-07T08:00:00.000Z',
    version: 1,
    archived: false,
    captures: [],
  };
}

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

  // The next act edits the body it made, not another replacement: the ×
  // under a done item deletes it, and butter stays done.
  await preview.getByRole('button', { name: 'Delete Eggs' }).click();
  await expect.poll(() => api.notes['shopping']?.body).toBe('- [ ] Milk\n- [ ] Bread\n- [x] butter');

  // Away and back, the tab shows the body, still ticked; Use this list stays.
  await page.getByRole('tab', { name: 'Items' }).click();
  await expect.poll(() => values(page.getByRole('list', { name: 'Items' }))).toEqual(['Milk', 'Bread', '']);
  await page.getByRole('tab', { name: 'Split up' }).click();
  await expect(page.getByRole('list', { name: 'Split up items' }).getByRole('checkbox', { name: 'butter' })).toBeChecked();
  await expect(page.getByText('Ticking here replaces your list with the split version.')).toHaveCount(0);
  await expect(page.getByText('The note changed since this was generated.')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Use this list' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Regenerate' })).toBeVisible();
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
