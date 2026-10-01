import type { Page } from '@playwright/test';

import { expect, test } from './fixtures.ts';

/**
 * Back is an app's, not a browser's (R8, F2).
 *
 * The owner: "About → Notes → About → Back walks every screen. Want one linear
 * back path that always ends at the main screen." Home is entry 0 of the
 * app's history, You and the archive only ever sit at entry 1, and the Home
 * tab goes back to entry 0 rather than pushing a second Home.
 */

const depth = (page: Page): Promise<number> => page.evaluate(() => window.history.length);

async function openYou(page: Page): Promise<void> {
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'You' }).click();
  await expect(page).toHaveURL(/\/settings$/);
}

async function openAbout(page: Page): Promise<void> {
  await page.getByRole('link', { name: /About/ }).first().click();
  await expect(page).toHaveURL(/\/about$/);
}

async function tapHome(page: Page): Promise<void> {
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Home' }).click();
  await expect(page).toHaveURL(/\/$/);
}

test("the owner's walk: About, Home, About, then Back goes to You, Home and out", async ({ page }) => {
  // A page before the app, so "out" is a place Back can be seen to reach.
  await page.goto('about:blank');
  await page.goto('/');
  const start = await depth(page);

  await openYou(page);
  await openAbout(page);
  await tapHome(page);
  await openYou(page);
  await openAbout(page);
  // Two screens above Home, however many were visited.
  expect(await depth(page)).toBe(start + 2);

  await page.goBack();
  await expect(page).toHaveURL(/\/settings$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('heading', { name: /^Notes/ })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL('about:blank');
});

test('the "‹ You" link goes back to You rather than stacking another', async ({ page }) => {
  await page.goto('/');
  const start = await depth(page);
  await openYou(page);
  await openAbout(page);
  await page.getByRole('link', { name: /back to\s*you/i }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  // Forward is still there (nothing pushed past it), so the depth did not grow.
  expect(await depth(page)).toBe(start + 2);
});

test('switching tabs from a note keeps the stack two deep', async ({ page }) => {
  await page.goto('/');
  const start = await depth(page);
  await page.getByRole('button', { name: /roof repair/i }).click();
  await expect(page.getByRole('textbox', { name: 'Note title' })).toBeVisible();
  await openYou(page);
  await tapHome(page);
  await page.getByRole('link', { name: /^Archive/ }).click();
  await expect(page).toHaveURL(/\?view=archived$/);
  await openYou(page);
  expect(await depth(page)).toBeLessThanOrEqual(start + 2);
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
});

test('a cold start on the archive puts Home beneath it', async ({ page }) => {
  await page.goto('about:blank');
  await page.goto('/?view=archived');
  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL('about:blank');
});

test('a cold start on About: "‹ You" leaves [Home, You]', async ({ page }) => {
  await page.goto('/about');
  await page.getByRole('link', { name: /back to\s*you/i }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
});

test('a reload on About keeps the stack: "‹ You" still lands on You', async ({ page }) => {
  await page.goto('/');
  await openYou(page);
  await openAbout(page);
  // The tab's history and the router's index survive a reload; seeding Home
  // again would leave [Home, You, Home, About] and "‹ You" on Home.
  await page.reload();
  await expect(page.getByRole('link', { name: /back to\s*you/i })).toBeVisible();
  await page.getByRole('link', { name: /back to\s*you/i }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
});

test('the Archived chip opens the archive above Home, and Back is Home', async ({ page }) => {
  await page.goto('about:blank');
  await page.goto('/');
  const start = await depth(page);
  await page.getByRole('button', { name: /^Archived/ }).click();
  await expect(page).toHaveURL(/\?view=archived$/);
  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();
  expect(await depth(page)).toBe(start + 1);
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  // Turned off from the archive, the chip goes back down to Home too.
  await page.getByRole('button', { name: /^Archived/ }).click();
  await expect(page).toHaveURL(/\?view=archived$/);
  await page.getByRole('button', { name: /^Archived/ }).click();
  await expect(page).toHaveURL(/\/$/);
  await page.goBack();
  await expect(page).toHaveURL('about:blank');
});
