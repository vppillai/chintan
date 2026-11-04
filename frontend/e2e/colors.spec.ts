import AxeBuilder from '@axe-core/playwright';
import type { Locator, Page } from '@playwright/test';

import { NOTE_COLORS } from '../src/api/schema.ts';

import { expect, noteAction, test } from './fixtures.ts';

/**
 * Note colour: a tint set from Home's row (⋮ → Colour…) or from the note's
 * Details, sent as `PATCH {version, color}`, worn by the row and the note
 * screen's header, and still there after a reload. The tints are held to AA
 * by axe's contrast rule in both themes, every tint on screen at once.
 */

function rowFor(page: Page, title: string): Locator {
  return page.locator('.note-row', { has: page.locator('.note-row__title', { hasText: title }) });
}

/** Opens the row's Colour… dialog: the ⋮ shows on hover for a mouse. */
async function openColour(page: Page, title: string): Promise<Locator> {
  await rowFor(page, title).hover();
  await page
    .locator('.note-row-wrap', { has: page.locator('.note-row__title', { hasText: title }) })
    .getByRole('button', { name: 'More' })
    .click();
  await page.getByRole('menuitem', { name: 'Colour…' }).click();
  return page.getByRole('dialog', { name: `Colour for “${title}”` });
}

test('a colour chosen from the Home row tints it and survives a reload', async ({ page, api }) => {
  await page.goto('/');
  const dialog = await openColour(page, 'Roof repair');
  await expect(dialog.getByRole('radio', { name: 'No colour' })).toBeChecked();
  await dialog.getByRole('radio', { name: 'Rose' }).check();
  await expect(rowFor(page, 'Roof repair')).toHaveAttribute('data-color', 'rose');
  await expect(page.getByRole('status').filter({ hasText: 'Colour: Rose' })).toBeAttached();
  await dialog.getByRole('button', { name: 'Done' }).click();
  await expect(dialog).toBeHidden();

  await expect.poll(() => api.notes['roof-repair']?.color).toBe('rose');
  await page.reload();
  await expect(rowFor(page, 'Roof repair')).toHaveAttribute('data-color', 'rose');
});

test('a colour chosen in the note’s Details washes its header and survives a reload', async ({
  page,
  api,
}) => {
  await page.goto('/notes/roof-repair');
  await noteAction(page, 'Details');
  await page.getByRole('radio', { name: 'Mint' }).check();
  await expect(page.locator('.note-screen')).toHaveAttribute('data-color', 'mint');
  await expect.poll(() => api.notes['roof-repair']?.color).toBe('mint');

  await page.reload();
  await expect(page.locator('.note-screen')).toHaveAttribute('data-color', 'mint');
  await page.goto('/');
  await expect(rowFor(page, 'Roof repair')).toHaveAttribute('data-color', 'mint');
});

for (const theme of ['ink', 'nocturne'] as const) {
  test(`every tint and the open picker have no serious axe violations in ${theme}`, async ({
    page,
    api,
  }) => {
    // One note per tint, so the contrast rule reads every tint's title,
    // snippet, time, tags and meta.
    const base = api.notes['reading-list']!;
    for (const color of NOTE_COLORS.slice(1)) {
      api.notes[`tint-${color}`] = { ...base, id: `tint-${color}`, title: `Tint ${color}`, color };
    }
    await page.addInitScript((value) => {
      localStorage.setItem('chintan.theme', value);
    }, theme);
    await page.goto('/');
    await expect(rowFor(page, 'Tint slate')).toHaveAttribute('data-color', 'slate');
    await expect(await openColour(page, 'Tint rose')).toBeVisible();
    await page.waitForLoadState('networkidle');

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();
    const serious = results.violations.filter(
      (violation) => violation.impact === 'critical' || violation.impact === 'serious',
    );
    expect(serious.map((violation) => `${violation.id}: ${violation.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([]);
  });
}
