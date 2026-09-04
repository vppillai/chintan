import { expect, test } from './fixtures.ts';

/**
 * Removing a note: archive, see the archive, restore, delete forever.
 *
 * All four operations the backend serves and `endpoints.ts` wraps must be
 * reachable from a control. Without them the app is append-only: every
 * mis-dictated note, every duplicate the router creates and every private
 * thing said by accident is permanent and always on the list, while the note
 * screen's "may have been archived or purged" describes two states the UI can
 * neither produce nor show.
 *
 * These run against the stubbed API in `fixtures.ts`, which implements the same
 * four operations `openapi.yaml` declares.
 */

test('a note can be archived from its own screen', async ({ page, api }) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  await page.getByRole('button', { name: 'Archive' }).click();

  // Archiving is reversible, so it asks once and plainly rather than demanding
  // the title be typed — that discipline is reserved for the irreversible one.
  await page.getByRole('button', { name: 'Archive it' }).click();

  // Back on the library, and the app is not left sitting on a note that is gone.
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toHaveCount(0);
  expect(api.notes['roof-repair']?.archived).toBe(true);
});

test('the archive is a chip on the library, and says when each note is purged', async ({
  page,
}) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  await page.getByRole('button', { name: /^Archived/ }).click();
  await expect(page).toHaveURL(/\?view=archived$/);
  await expect(page.getByRole('button', { name: /^Archived/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );

  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();
  await expect(page.getByRole('button', { name: /stray thought/i })).toBeVisible();

  // Active notes are not in here.
  await expect(page.getByRole('button', { name: /roof repair/i })).toHaveCount(0);

  // An absent `purge_after` — every note archived before retention was
  // configured — must read as "no deletion date", never "Deletes in NaN days".
  await expect(page.locator('.screen')).not.toContainText('NaN');
  await expect(page.getByText(/deletes in/i).first()).toBeVisible();
  await expect(page.getByText(/no deletion date/i)).toBeVisible();
});

test('an archived note can be restored', async ({ page, api }) => {
  // The old address still works; it is the library with the archive chip on.
  await page.goto('/archive');
  await expect(page).toHaveURL(/\?view=archived$/);
  await page.getByRole('button', { name: /old fence/i }).click();

  await expect(page.getByText(/this note is archived/i)).toBeVisible();
  await page.getByRole('button', { name: 'Restore' }).click();

  await expect(page).toHaveURL(/\/notes\/old-fence$/);
  await expect(page.getByText(/this note is archived/i)).toHaveCount(0);
  expect(api.notes['old-fence']?.archived).toBe(false);

  await page.goto('/');
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

  await expect(page).toHaveURL(/\?view=archived$/);
  expect(api.purged).toEqual(['old-fence']);
  await expect(page.getByRole('button', { name: /old fence/i })).toHaveCount(0);
});

/**
 * Bulk select, the same control in both views: Archive from the library,
 * Restore from the archive, Delete forever in either behind a typed word.
 */
test('several notes can be archived at once from the library', async ({ page, api }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  await page.getByRole('button', { name: 'Select' }).click();
  await page.getByRole('button', { name: 'Select all' }).click();
  await expect(page.getByText('2 selected')).toBeVisible();

  await page.getByRole('toolbar', { name: 'Bulk actions' }).getByRole('button', { name: 'Archive' }).click();
  await page.getByRole('button', { name: 'Archive them' }).click();

  await expect(page.getByText(/tap record to make your first note/i)).toBeVisible();
  expect(api.notes['roof-repair']?.archived).toBe(true);
  expect(api.notes['reading-list']?.archived).toBe(true);
  // And the chip now counts them.
  await expect(page.getByRole('button', { name: 'Archived · 4' })).toBeVisible();
});

test('the archive can be emptied: select all, delete forever, type the word', async ({
  page,
  api,
}) => {
  await page.goto('/?view=archived');
  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();

  await page.getByRole('button', { name: 'Select' }).click();
  await page.getByRole('button', { name: 'Select all' }).click();
  await page.getByRole('button', { name: 'Delete forever' }).click();

  const dialog = page.getByRole('dialog');
  const confirm = dialog.getByRole('button', { name: 'Delete them forever' });
  await expect(confirm).toBeDisabled();
  await dialog.getByRole('textbox').fill('delete');
  await confirm.click();

  await expect(page.getByText(/nothing is archived/i)).toBeVisible();
  expect(api.notes['old-fence']).toBeUndefined();
  expect(api.notes['stray-thought']).toBeUndefined();
  // The active notes were never touched.
  expect(api.notes['roof-repair']).toBeDefined();
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
