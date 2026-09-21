import process from 'node:process';

import { expect, test } from './fixtures.ts';

/**
 * The install sheet's pictures — `public/screenshots/home-{narrow,wide}.jpg`,
 * which `manifest.config.ts` declares (round-3 T57).
 *
 * Taken from the real build over the stub API, so they are the app and not a
 * mock-up, and re-taken by hand when Home changes shape:
 *
 *   SCREENSHOTS=1 bunx playwright test --project=chromium e2e/screenshots.spec.ts
 *
 * Opt-in, like `launch-latency.spec.ts`: a run that rewrites files in
 * `public/` is not a check, and the sizes here must match the manifest's
 * `sizes` by hand. JPEG because the service worker precaches every PNG.
 */
const ENABLED = process.env['SCREENSHOTS'] === '1';

const SHOTS = [
  { name: 'home-narrow', width: 390, height: 844 },
  { name: 'home-wide', width: 1280, height: 800 },
] as const;

for (const shot of SHOTS) {
  test(`takes ${shot.name} at ${String(shot.width)}×${String(shot.height)}`, async ({ page }) => {
    test.skip(!ENABLED, 'set SCREENSHOTS=1 to rewrite public/screenshots');
    await page.setViewportSize({ width: shot.width, height: shot.height });
    await page.goto('/');
    await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
    // The chips read the device's copy of the notes; wait for the first one.
    await expect(page.getByRole('button', { name: 'house', exact: true })).toBeVisible();
    await page.screenshot({
      path: `public/screenshots/${shot.name}.jpg`,
      type: 'jpeg',
      quality: 85,
    });
  });
}
