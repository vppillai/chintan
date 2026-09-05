import { expect, test } from './fixtures.ts';

/**
 * The note as panels under one strip: Text · Recordings (N).
 *
 * The note used to be one page, and the recordings of a long note were a
 * scroll past every paragraph. The strip sticks under the banner, the chosen
 * tab is remembered for the session and named in the URL, and a deep link can
 * open a note on its recordings.
 */

test('opens on the text, and Recordings is one tap away with its count', async ({ page }) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  const tabs = page.getByRole('tablist', { name: 'Note views' }).getByRole('tab');
  await expect(tabs).toHaveText(['Text', 'Recordings (1)']);
  await expect(page.getByRole('tab', { name: 'Text' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Recordings' })).toHaveCount(0);

  await page.getByRole('tab', { name: /^Recordings/ }).click();
  await expect(page).toHaveURL(/\/notes\/roof-repair\?tab=recordings$/);
  await expect(page.getByRole('region', { name: 'Recording', exact: true })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Note body' })).toHaveCount(0);
  // The action bar is on every tab.
  await expect(page.getByRole('button', { name: /record into this/i })).toBeVisible();

  // Remembered for the session: a reload lands on the same tab, and so does
  // reopening the note from the library.
  await page.reload();
  await expect(page.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
  await page.goto('/');
  await page.getByRole('button', { name: /roof repair/i }).click();
  await expect(page.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
  // Back leaves the note; it does not step back through tabs.
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
});

test('the arrow keys move between segments', async ({ page }) => {
  await page.goto('/notes/roof-repair');
  await page.getByRole('tab', { name: 'Text' }).focus();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('tab', { name: /^Recordings/ })).toBeFocused();
  await page.keyboard.press('ArrowLeft');
  await expect(page.getByRole('tab', { name: 'Text' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();
});

test('the strip sticks under the banner while a long note scrolls', async ({ page, api }) => {
  api.notes['roof-repair']!.body = Array.from(
    { length: 80 },
    (_, index) => `Paragraph ${String(index + 1)}. The flashing around the chimney needs replacing.`,
  ).join('\n\n');
  await page.setViewportSize({ width: 412, height: 915 });
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();

  const measured = await page.evaluate(() => {
    const main = document.querySelector('.app__main');
    const strip = document.querySelector('.note-tabs');
    if (!main || !strip) return null;
    const before = strip.getBoundingClientRect().top;
    main.scrollTop = 1_200;
    const after = strip.getBoundingClientRect();
    return {
      before: Math.round(before),
      top: Math.round(after.top),
      mainTop: Math.round(main.getBoundingClientRect().top),
      scrolled: main.scrollTop,
    };
  });
  expect(measured).not.toBeNull();
  expect(measured!.scrolled).toBeGreaterThan(0);
  // It was lower on the page before the scroll, and now rests on the line
  // under the banner — the top of the scroll region.
  expect(measured!.before).toBeGreaterThan(measured!.top);
  expect(Math.abs(measured!.top - measured!.mainTop)).toBeLessThanOrEqual(1);
});
