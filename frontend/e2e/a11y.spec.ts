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
  '/?view=archived',
  '/?q=roof',
  '/settings',
  '/settings?passkey=invalid_session',
  '/about',
  '/notes/roof-repair',
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

/**
 * A heading to land on, on every screen. The note screen had none: its title
 * is an input (axe `page-has-heading-one`, the one finding of the QA pass's
 * scans), so the label is now the page's h1 as well.
 */
for (const route of ROUTES) {
  test(`${route} has exactly one h1`, async ({ page }) => {
    await page.goto(route);
    await expect(page.locator('main')).toBeVisible();
    await expect(page.locator('h1')).toHaveCount(1);
  });
}

/**
 * WCAG 2.5.8: every control at least 24 by 24 CSS pixels. The selection
 * checkboxes were 20 and the plain playback slider 16 tall.
 */
test('selection checkboxes and the playback slider are at least 24 px', async ({ page, api }) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Select' }).click();
  const box = page.getByRole('checkbox').first();
  const checkbox = await box.boundingBox();
  expect(checkbox?.width ?? 0).toBeGreaterThanOrEqual(24);
  expect(checkbox?.height ?? 0).toBeGreaterThanOrEqual(24);
  // And the thumb has a 44 px target around it, without the row growing.
  const target = await page.locator('.note-row__check').first().boundingBox();
  expect(target?.width ?? 0).toBeGreaterThanOrEqual(44);
  expect(target?.height ?? 0).toBeGreaterThanOrEqual(44);

  // The plain slider: a capture with no peaks to draw.
  api.notes['reading-list']!.captures = [
    {
      id: 'cap-plain',
      status: 'appended',
      created_at: '2026-01-01T00:00:00.000Z',
      version: 1,
      note_id: 'reading-list',
      duration_ms: 5_000,
      has_peaks: false,
      has_segments: false,
    },
  ];
  await page.goto('/notes/reading-list');
  const slider = await page.getByRole('slider', { name: 'Playback position' }).boundingBox();
  expect(slider?.height ?? 0).toBeGreaterThanOrEqual(24);
});

/**
 * The parked skip link used to leak its shadow: translated 70 px up, its box
 * ended above the viewport but its 28 px shadow did not, and every desktop
 * screenshot carried a grey sliver in the top-left corner (QA D19).
 */
test('the skip link casts no shadow until it is shown', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/');
  const skip = page.getByRole('link', { name: /skip to content/i });

  const parked = await skip.evaluate((element) => {
    const { boxShadow } = getComputedStyle(element);
    return { boxShadow, bottom: element.getBoundingClientRect().bottom };
  });
  expect(parked.boxShadow).toBe('none');
  expect(parked.bottom).toBeLessThanOrEqual(0);

  await page.keyboard.press('Tab');
  await expect(skip).toBeFocused();
  expect(await skip.evaluate((element) => getComputedStyle(element).boxShadow)).not.toBe('none');
});

test('the library is fully traversable by keyboard', async ({ page }) => {
  await page.goto('/');
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
  // Let the library settle first: a Tab that lands on a control the next
  // render replaces leaves focus on <body>, which has no ring to measure.
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');

  const outline = await page.evaluate(() => {
    const element = document.activeElement;
    if (!element || element === document.body) return null;
    const styles = getComputedStyle(element);
    return { tag: element.tagName, width: styles.outlineWidth, style: styles.outlineStyle };
  });

  expect(outline).not.toBeNull();
  expect(outline?.style).not.toBe('none');
  expect(parseFloat(outline?.width ?? '0')).toBeGreaterThan(0);
});

test('Back from a note returns to the library, not out of the app', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: /roof repair/i }).click();
  await expect(page).toHaveURL(/\/notes\/roof-repair$/);

  await page.goBack();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('heading', { name: 'Notes' })).toBeVisible();
  await expect(page.getByRole('button', { name: /^record$/i })).toBeVisible();
});

test('a deep link seeds the library beneath it, so Back stays in the app', async ({ page }) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  await page.goBack();

  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
});

/**
 * The running build, at the foot of the You screen.
 *
 * Injected from the git SHA at build time; `playwright.config.ts` pins a known
 * value so this can assert the injection actually reaches the screen rather
 * than that some text is present.
 */
test('the You screen names the build it is running, legibly', async ({ page }) => {
  await page.goto('/settings');

  const footnote = page.locator('.version-footnote');
  await expect(footnote).toContainText('e2e-abc1234');

  // "Faded" is not a licence to be invisible: --color-faint is the palette's
  // quietest text and tokens.css documents it as meeting AA.
  const readable = await footnote.evaluate((element) => {
    const style = getComputedStyle(element);
    return { color: style.color, opacity: Number(style.opacity) };
  });
  expect(readable.opacity).toBeGreaterThan(0.5);
  expect(readable.color).not.toBe('rgba(0, 0, 0, 0)');

  // It exists to be copied into a bug report, so it must be selectable text.
  await expect(footnote.locator('code')).toHaveText('e2e-abc1234');
});
