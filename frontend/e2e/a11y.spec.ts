import AxeBuilder from '@axe-core/playwright';

import { expect, test } from './fixtures.ts';

/**
 * Accessibility, in both themes.
 *
 * v1 contained zero `aria-*`, `role`, `tabindex`, or `:focus-visible`, and its
 * muted text failed AA. The contrast rule is included deliberately: it is the
 * one that would have caught the palette.
 */

const ROUTES = [
  '/',
  '/notes',
  '/search',
  '/settings',
  '/notes/roof-repair',
  '/archive',
] as const;
const THEMES = ['ink', 'nocturne'] as const;

for (const theme of THEMES) {
  for (const route of ROUTES) {
    test(`${route} has no critical axe violations in ${theme}`, async ({ page }) => {
      await page.addInitScript((value) => {
        localStorage.setItem('chintan.theme', value);
      }, theme);

      await page.goto(route);
      await expect(page.locator('main')).toBeVisible();
      // Let async content settle so the scan sees the real screen.
      await page.waitForTimeout(700);

      const results = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
        .analyze();

      const serious = results.violations.filter(
        (violation) => violation.impact === 'critical' || violation.impact === 'serious',
      );

      expect(
        serious,
        serious
          .map(
            (violation) =>
              `${violation.id} (${violation.impact}): ${violation.nodes
                .map((node) => node.target.join(' '))
                .join(', ')}`,
          )
          .join('\n'),
      ).toEqual([]);
    });
  }
}

test('the library is fully traversable by keyboard', async ({ page }) => {
  await page.goto('/notes');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  // Tab until a note row has focus, proving rows are real buttons rather than
  // clickable divs — the defect that made v1's library keyboard-unreachable.
  let reached = false;
  for (let step = 0; step < 25 && !reached; step += 1) {
    await page.keyboard.press('Tab');
    reached = await page.evaluate(() =>
      Boolean(document.activeElement?.classList.contains('note-row')),
    );
  }
  expect(reached).toBe(true);

  // Enter opens it.
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/notes\/[a-z-]+$/);
});

test('the skip link moves focus to main', async ({ page }) => {
  await page.goto('/');

  await page.keyboard.press('Tab');
  const skip = page.getByRole('link', { name: /skip to content/i });
  await expect(skip).toBeFocused();

  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#main$/);
});

test('every interactive element shows a visible focus ring', async ({ page }) => {
  await page.goto('/');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');

  const outline = await page.evaluate(() => {
    const element = document.activeElement;
    if (!element) return null;
    const styles = getComputedStyle(element);
    return { width: styles.outlineWidth, style: styles.outlineStyle };
  });

  expect(outline).not.toBeNull();
  expect(outline?.style).not.toBe('none');
  expect(parseFloat(outline?.width ?? '0')).toBeGreaterThan(0);
});

test('Back collapses the sheet instead of leaving the app', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('link', { name: 'Notes' }).click();
  await expect(page).toHaveURL(/\/notes$/);

  await page.goBack();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /record/i })).toBeVisible();
});

test('a deep link seeds home beneath it, so Back stays in the app', async ({ page }) => {
  await page.goto('/notes');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  await page.goBack();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /record/i })).toBeVisible();
});
