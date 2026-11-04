import { readFile } from 'node:fs/promises';

import { expect, test } from './fixtures.ts';

/**
 * Download format MP4 (You → Your data): a recording is converted on the
 * device before it is saved. The file must be an MP4 — an `ftyp` box first —
 * and as long as the recording (the stub serves thirteen seconds). Chromium
 * here has no AAC encoder, so this is the Opus-in-MP4 path; the container
 * writer is the same for AAC (`toMp4.test.ts`).
 */
test('a recording downloads as MP4 when MP4 is the chosen format', async ({ page, browserName }) => {
  test.skip(browserName !== 'chromium', 'the download event is asserted in Chromium only');
  await page.addInitScript(() => {
    localStorage.setItem('chintan.downloadFormat', 'mp4');
  });
  await page.goto('/notes/roof-repair?tab=recordings');
  await page.getByRole('button', { name: /more for recording from/i }).first().click();
  const download = page.waitForEvent('download');
  await page.getByRole('menuitem', { name: 'Download audio' }).click();
  const saved = await download;
  expect(saved.suggestedFilename()).toMatch(/\.(m4a|mp4)$/);

  const bytes = await readFile((await saved.path())!);
  expect(bytes.subarray(4, 8).toString('latin1')).toBe('ftyp');
  const mvhd = bytes.indexOf('mvhd', 0, 'latin1');
  expect(mvhd).toBeGreaterThan(0);
  const timescale = bytes.readUInt32BE(mvhd + 16);
  const seconds = bytes.readUInt32BE(mvhd + 20) / timescale;
  expect(seconds).toBeGreaterThan(12.5);
  expect(seconds).toBeLessThan(13.5);
});

test('As recorded, the default, saves the file untouched', async ({ page, browserName }) => {
  test.skip(browserName !== 'chromium', 'the download event is asserted in Chromium only');
  await page.goto('/notes/roof-repair?tab=recordings');
  await page.getByRole('button', { name: /more for recording from/i }).first().click();
  const download = page.waitForEvent('download');
  await page.getByRole('menuitem', { name: 'Download audio' }).click();
  expect((await download).suggestedFilename()).toMatch(/\.wav$/);
});
