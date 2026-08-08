import { expect, test } from './fixtures.ts';

/**
 * Removing a note: archive, see the archive, restore, delete forever.
 *
 * v2 shipped append-only. The backend served all four operations, `endpoints.ts`
 * wrapped all four, and not one was wired to a control — so every mis-dictated
 * note, every duplicate the router created and every private thing said by
 * accident was permanent and always on the list. The note screen even told the
 * user a note "may have been archived or purged", describing two states the UI
 * could neither produce nor show.
 *
 * These run against the stubbed API in `fixtures.ts`, which now implements the
 * same four operations `openapi.yaml` declares.
 */

test('a note can be archived from its own screen', async ({ page, api }) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  await page.getByRole('button', { name: 'Archive' }).click();

  // Archiving is reversible, so it asks once and plainly rather than demanding
  // the title be typed — that discipline is reserved for the irreversible one.
  await page.getByRole('button', { name: 'Archive it' }).click();

  // Off the library, and the app is not left sitting on a note that is gone.
  await expect(page).toHaveURL(/\/notes$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toHaveCount(0);
  expect(api.notes['roof-repair']?.archived).toBe(true);
});

test('the archive lists archived notes and says when each is purged', async ({ page }) => {
  await page.goto('/notes');

  await page.getByRole('link', { name: /archive/i }).click();
  await expect(page).toHaveURL(/\/archive$/);

  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();
  await expect(page.getByRole('button', { name: /stray thought/i })).toBeVisible();

  // Active notes are not in here.
  await expect(page.getByRole('button', { name: /roof repair/i })).toHaveCount(0);

  // v1 rendered "Deletes in NaN days" whenever `purge_after` was absent, which
  // is every note archived before retention was configured.
  await expect(page.locator('.screen')).not.toContainText('NaN');
  await expect(page.getByText(/deletes in/i).first()).toBeVisible();
  await expect(page.getByText(/no deletion date/i)).toBeVisible();
});

test('an archived note can be restored', async ({ page, api }) => {
  await page.goto('/archive');
  await page.getByRole('button', { name: /old fence/i }).click();

  await expect(page.getByText(/this note is archived/i)).toBeVisible();
  await page.getByRole('button', { name: 'Restore' }).click();

  await expect(page).toHaveURL(/\/notes\/old-fence$/);
  await expect(page.getByText(/this note is archived/i)).toHaveCount(0);
  expect(api.notes['old-fence']?.archived).toBe(false);

  await page.goto('/notes');
  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();
});

test('delete forever is gated by typing the title, and cascades', async ({ page, api }) => {
  await page.goto('/notes/old-fence');

  await page.getByRole('button', { name: 'Delete forever' }).click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  // It says what else goes with it. The audio and the transcripts are not
  // recoverable either, and a dialog that only names the note is not consent.
  await expect(dialog).toContainText(/recordings and transcripts/i);

  const confirm = dialog.getByRole('button', { name: 'Delete forever' });
  await expect(confirm).toBeDisabled();

  await dialog.getByRole('textbox').fill('Old fenc');
  await expect(confirm).toBeDisabled();

  await dialog.getByRole('textbox').fill('Old fence');
  await expect(confirm).toBeEnabled();
  await confirm.click();

  await expect(page).toHaveURL(/\/archive$/);
  expect(api.purged).toEqual(['old-fence']);
  await expect(page.getByRole('button', { name: /old fence/i })).toHaveCount(0);
});

test('escape closes the delete dialog without deleting anything', async ({ page, api }) => {
  await page.goto('/notes/old-fence');

  await page.getByRole('button', { name: 'Delete forever' }).click();
  await expect(page.getByRole('dialog')).toBeVisible();

  await page.keyboard.press('Escape');

  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(api.purged).toEqual([]);
  expect(api.notes['old-fence']).toBeDefined();
});
