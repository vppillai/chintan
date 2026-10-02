import { expect, test } from './fixtures.ts';

/**
 * A note without a recording: Home's + offers Note or Checklist, the pick is
 * one POST with an Idempotency-Key, and the note screen opens with the
 * placeholder title selected so typing replaces it.
 */

test('New note → Checklist creates the note and lands with the title selected', async ({ page, api }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  const plus = page.getByRole('button', { name: 'New note' });
  const box = await plus.boundingBox();
  expect(box?.width ?? 0).toBeGreaterThanOrEqual(44);
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
  await plus.click();
  await page.getByRole('menuitem', { name: 'Checklist' }).click();

  await expect(page).toHaveURL(/\/notes\/typed-\d+$/);
  const title = page.getByRole('textbox', { name: 'Note title' });
  await expect(title).toHaveValue('New checklist');
  await expect(title).toBeFocused();
  expect(
    await title.evaluate((el: HTMLInputElement) => (el.selectionEnd ?? 0) - (el.selectionStart ?? 0)),
  ).toBe('New checklist'.length);
  // A checklist from the first paint: the Items tab, not Text.
  await expect(page.getByRole('tab', { name: 'Items' })).toBeVisible();

  const posts = api.requests.filter((r) => r.method === 'POST' && r.url === '/v1/notes');
  expect(posts).toHaveLength(1);
  expect(posts[0]?.headers['idempotency-key']).toMatch(/\S/);

  // Typing replaces the placeholder.
  await page.keyboard.type('Hardware');
  await expect(title).toHaveValue('Hardware');
});

test('offline, the + says it needs a connection and sends nothing', async ({ page, context, api }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await context.setOffline(true);

  await page.getByRole('button', { name: 'New note' }).click();
  await page.getByRole('menuitem', { name: 'Note' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'New note needs a connection.' })).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
  expect(api.requests.filter((r) => r.method === 'POST' && r.url === '/v1/notes')).toHaveLength(0);
  await context.setOffline(false);
});

test('a placeholder left untouched is discarded on Back, so Home shows no new row', async ({ page, api }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await page.getByRole('button', { name: 'New note' }).click();
  await page.getByRole('menuitem', { name: 'Note' }).click();
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('New note');

  await page.goBack();
  await expect(page.getByRole('heading', { name: 'Notes' })).toBeVisible();
  // Archived, then purged — the id is the stub's count, so match its shape.
  await expect.poll(() => api.purged.length).toBe(1);
  expect(api.purged[0]).toMatch(/^typed-\d+$/);
  // No row for it; the + itself is also named "New note", so look at rows.
  await expect(page.locator('.note-row', { hasText: 'New note' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
});
