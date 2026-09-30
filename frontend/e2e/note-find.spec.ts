import { expect, test } from './fixtures.ts';

/**
 * Find in note draws its marks in a mirror of the body. The mirror once
 * inherited `.prose`'s grid, which broke the text into one row per match and
 * per run between matches (R7-5); these check it reads as flowing text.
 */
test('the find mirror is one block of text with the matches inline', async ({ page, api }) => {
  api.notes['roof-repair']!.body = 'The roof leaks by the roof vent, so the roof needs a look.';
  await page.goto('/notes/roof-repair');
  await page.getByRole('button', { name: 'Find in note' }).click();
  await page.getByRole('searchbox', { name: 'Find in note' }).fill('roof');

  const mirror = page.locator('.note-body-mirror');
  await expect(mirror.locator('.find-match')).toHaveCount(3);
  const layout = await mirror.evaluate((element) => {
    const marks = [...element.querySelectorAll('.find-match')];
    return {
      display: getComputedStyle(element).display,
      marks: marks.map((mark) => getComputedStyle(mark).display),
      // On one line, all three sit at the same top; in a grid each is a row.
      tops: new Set(marks.map((mark) => Math.round(mark.getBoundingClientRect().top))).size,
    };
  });
  expect(layout.display).toBe('block');
  expect(layout.marks).toEqual(['inline', 'inline', 'inline']);
  expect(layout.tops).toBe(1);
});
