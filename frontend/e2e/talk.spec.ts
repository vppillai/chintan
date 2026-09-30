import type { Page } from '@playwright/test';

import { MIN_TALK_MS } from '../src/features/capture/holdTiming.ts';

import { expect, test } from './fixtures.ts';

/**
 * `/talk`: hold, speak, release, and be ready for the next one (owner
 * feedback 2026-09-24). Chromium's fake microphone feeds the real recorder,
 * so a hold produces real bytes and the release a real upload; the button's
 * pointer capture and the Space bar are the browser's own.
 */

async function holdTheButton(page: Page, ms: number): Promise<void> {
  const button = page.getByRole('button', { name: /^PTT: hold to talk/ });
  const box = await button.boundingBox();
  if (!box) throw new Error('the button has no box');
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.waitForTimeout(ms);
  await page.mouse.up();
}

test('is one giant button, and the manifest offers it as a shortcut', async ({ page, request }) => {
  await page.goto('/talk');
  const button = page.getByRole('button', { name: /^PTT: hold to talk/ });
  await expect(button).toBeVisible();
  const viewport = page.viewportSize()!;
  const box = (await button.boundingBox())!;
  // At least two fifths of the viewport's height, and round.
  expect(box.height).toBeGreaterThanOrEqual(viewport.height * 0.4);
  expect(Math.abs(box.width - box.height)).toBeLessThan(2);
  // The target pill sits above it; with no note chosen, Chintan decides (R7-14).
  await expect(page.getByRole('button', { name: 'Chintan decides' })).toBeVisible();

  const manifestHref = (await page.locator('link[rel="manifest"]').getAttribute('href'))!;
  const manifest = (await (await request.get(manifestHref)).json()) as {
    shortcuts: { name: string; description: string; url: string }[];
  };
  const talk = manifest.shortcuts.find((shortcut) => shortcut.name === 'PTT');
  expect(talk?.url.endsWith('/talk')).toBe(true);
  expect(talk?.description).toBe('Hold to talk, release to send');
});

test('hold sends, a tap is too short, Space is the button, and Back leaves to Home', async ({
  page,
  api,
}) => {
  await page.goto('/talk');

  // Held: recording from the first frame, the button says what release does.
  await holdTheButton(page, MIN_TALK_MS + 100);
  await expect(page.locator('.talk__status')).toHaveText('Sent · filing');
  await expect(page).toHaveURL(/\/talk$/);
  await expect.poll(() => api.captures.length, { message: 'capture created' }).toBe(1);
  expect(api.captures[0]?.note_id).toBeNull();
  // Ready again, in place. The status keeps saying Sent while the machine
  // holds the landed upload; the next hold resets it.
  await expect(page.getByRole('button', { name: /^PTT: hold to talk/ })).toBeVisible();

  // A tap is not a message: once the hint has cleared, nothing more was sent.
  await page.getByRole('button', { name: /^PTT: hold to talk/ }).click();
  await expect(page.locator('.talk__status')).toHaveText('Too short — hold to talk');
  await expect(page.locator('.talk__status')).toHaveText('', { timeout: 5_000 });
  expect(api.captures).toHaveLength(1);

  // The Space bar, from the page.
  await page.keyboard.down('Space');
  await expect(page.getByRole('button', { name: 'Release to send' })).toBeVisible();
  await page.waitForTimeout(MIN_TALK_MS + 100);
  await page.keyboard.up('Space');
  await expect(page.locator('.talk__status')).toHaveText('Sent · filing');
  await expect.poll(() => api.captures.length, { message: 'second capture created' }).toBe(2);

  // Back is Home, not the previous recording.
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
});

test('records into the note the URL names, as the capture screen is seeded', async ({ page, api }) => {
  // The one hold surface left in the app (owner feedback 2026-09-27): a
  // recording aimed at a note goes there from here too, not only from `/capture`.
  await page.goto('/talk?note=roof-repair');
  await expect(page.getByRole('button', { name: /^into roof repair/i })).toBeVisible();
  await holdTheButton(page, MIN_TALK_MS + 100);
  await expect(page.locator('.talk__status')).toHaveText('Sent · filing');
  await expect.poll(() => api.captures.length, { message: 'capture created' }).toBe(1);
  expect(api.captures[0]?.note_id).toBe('roof-repair');
});

test('slides away to cancel', async ({ page, api }) => {
  await page.goto('/talk');
  const button = page.getByRole('button', { name: /^PTT: hold to talk/ });
  const box = (await button.boundingBox())!;
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await expect(page.getByRole('button', { name: 'Release to send' })).toBeVisible();
  await page.waitForTimeout(MIN_TALK_MS + 100);
  // Away is measured from the disc's edge, not the press point: halfway to
  // the edge is still a send, and clear of it by more than the margin is not.
  await page.mouse.move(x + box.width / 4, y, { steps: 4 });
  await expect(page.getByRole('button', { name: 'Release to send' })).toBeVisible();
  await page.mouse.move(box.x + box.width + 120, y, { steps: 8 });
  await expect(page.getByRole('button', { name: 'Release to cancel' })).toBeVisible();
  await page.mouse.up();

  await expect(page.getByRole('button', { name: /^PTT: hold to talk/ })).toBeVisible();
  // Cancelled: the status says nothing, and nothing was sent.
  await expect(page.locator('.talk__status')).toHaveText('');
  expect(api.captures).toHaveLength(0);
});
