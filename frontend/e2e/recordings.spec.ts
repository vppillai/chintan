import type { Page } from '@playwright/test';

import { expect, test, type ApiState } from './fixtures.ts';

/**
 * Doing things to a recording other than playing it: moving it to another
 * note, deleting it with its paragraph, downloading several as one archive.
 *
 * Each row has a More menu; a long press, or its Select item, starts a
 * selection with a bar at the foot of the screen. The endpoints behind these
 * have been live since v0.5.3 (backlog D2, D3, D4); this is their first UI.
 */

const ROW = /more for recording from/i;

/** A second recording on the roof note, so there is something to select together. */
function twoRecordings(api: ApiState): void {
  api.notes['roof-repair']!.captures!.push({
    id: 'cap-newer',
    status: 'appended',
    created_at: '2026-08-06T09:12:00.000Z',
    version: 1,
    note_id: 'roof-repair',
    duration_ms: 8_000,
    has_peaks: false,
    has_segments: false,
  });
  api.notes['roof-repair']!.body += '\n\nGet two quotes before the autumn rain.';
}

/** Press and hold, as a finger does: pointer events rather than a mouse click. */
async function longPress(page: Page, selector: string): Promise<void> {
  const target = page.locator(selector).first();
  await target.dispatchEvent('pointerdown', { pointerType: 'touch', clientX: 20, clientY: 20, isPrimary: true, bubbles: true });
  await page.waitForTimeout(650);
  await target.dispatchEvent('pointerup', { pointerType: 'touch', bubbles: true });
}

test('a recording can be deleted with its paragraph, behind a typed word', async ({ page, api }) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('button', { name: ROW })).toBeVisible();

  await page.getByRole('button', { name: ROW }).click();
  await page.getByRole('menuitem', { name: 'Delete recording' }).click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText(/paragraph it dictated/i);
  const confirm = dialog.getByRole('button', { name: 'Delete it' });
  await expect(confirm).toBeDisabled();
  await dialog.getByRole('textbox').fill('delete');
  await confirm.click();

  await expect(page.getByRole('button', { name: ROW })).toHaveCount(0);
  await expect(page.getByText('Recording deleted')).toBeVisible();
  expect(api.deletedCaptures).toEqual(['cap-old']);
  expect(api.notes['roof-repair']?.captures).toEqual([]);
  // The body was rewritten by the server and the screen shows what it holds now.
  await expect(page.getByLabel('Note body')).toHaveValue(api.notes['roof-repair']!.body);
});

test('a recording that is still filing cannot be deleted yet, and the screen says so', async ({
  page,
  api,
}) => {
  api.notes['roof-repair']!.captures![0]!.status = 'transcribing';
  await page.goto('/notes/roof-repair');

  await page.getByRole('button', { name: ROW }).click();
  await page.getByRole('menuitem', { name: 'Delete recording' }).click();
  await page.getByRole('dialog').getByRole('textbox').fill('delete');
  await page.getByRole('button', { name: 'Delete it' }).click();

  await expect(page.getByText('Wait until it has finished filing.')).toBeVisible();
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(1);
});

test('a recording can be moved to another note, which is then one tap away', async ({
  page,
  api,
}) => {
  await page.goto('/notes/roof-repair');
  await page.getByRole('button', { name: ROW }).click();
  await page.getByRole('menuitem', { name: 'Move to…' }).click();

  const sheet = page.getByRole('dialog', { name: /move this recording to/i });
  await expect(sheet).toBeVisible();
  // Only other active notes: not this one, not the archive, and no "new note".
  await expect(sheet.getByRole('button', { name: /reading list/i })).toBeVisible();
  await expect(sheet.getByRole('button', { name: /roof repair/i })).toHaveCount(0);
  await expect(sheet.getByRole('button', { name: /old fence/i })).toHaveCount(0);
  await expect(sheet.getByPlaceholder(/new note/i)).toHaveCount(0);

  await sheet.getByRole('searchbox').fill('read');
  await sheet.getByRole('button', { name: /reading list/i }).click();

  await expect(sheet).toHaveCount(0);
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(0);
  await expect(page.getByText(/recording moved to “reading list”/i)).toBeVisible();
  expect(api.notes['reading-list']?.captures?.map((c) => c.id)).toEqual(['cap-old']);
  expect(api.notes['roof-repair']?.captures).toEqual([]);

  await page.getByRole('button', { name: 'Open Reading list' }).click();
  await expect(page).toHaveURL(/\/notes\/reading-list$/);
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(1);
});

test('several recordings download as one archive, with progress', async ({
  page,
  api,
  browserName,
}) => {
  test.skip(browserName !== 'chromium', 'the download event is asserted in Chromium only');
  twoRecordings(api);
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(2);

  await page.getByRole('button', { name: ROW }).first().click();
  await page.getByRole('menuitem', { name: 'Select' }).click();

  const bar = page.getByRole('toolbar', { name: 'Recording actions' });
  await expect(bar).toBeVisible();
  await expect(page.getByText('1 selected')).toBeVisible();
  await bar.getByRole('button', { name: 'Select all' }).click();
  await expect(page.getByText('2 selected')).toBeVisible();

  // The bar sits directly above the tab bar, not at the end of the list.
  const barBox = await page.locator('.selection-bar').boundingBox();
  const tabBox = await page.locator('.tab-bar').boundingBox();
  expect(barBox!.y + barBox!.height).toBeLessThanOrEqual(tabBox!.y + 1);
  // And the note's own action bar has stepped aside.
  await expect(page.getByRole('toolbar', { name: 'Note actions' })).toBeHidden();

  const download = page.waitForEvent('download');
  await bar.getByRole('button', { name: 'Download' }).click();
  expect((await download).suggestedFilename()).toBe('roof-repair-recordings.zip');
  await expect(page.getByText(/downloaded 2 recordings as one archive/i)).toBeVisible();
  // The manifest was asked for once; each file came from the bucket.
  expect(api.requests.filter((r) => r.url === '/v1/notes/roof-repair/recordings/urls')).toHaveLength(1);
});

test('a long press on a row starts selecting it', async ({ page, api }) => {
  twoRecordings(api);
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(2);

  await longPress(page, '.recording__summary');

  await expect(page.getByRole('toolbar', { name: 'Recording actions' })).toBeVisible();
  await expect(page.getByText('1 selected')).toBeVisible();
  await expect(page.getByRole('checkbox').first()).toBeChecked();
  // The player closed with the mode change, and Escape leaves it.
  await expect(page.getByRole('region', { name: 'Recording', exact: true })).toHaveCount(0);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('toolbar', { name: 'Recording actions' })).toHaveCount(0);
});
