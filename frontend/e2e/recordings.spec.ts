import type { Page } from '@playwright/test';

import { LONG_PRESS_MS } from '../src/hooks/useLongPress.ts';

import { expect, seedStuckCapture, test, type ApiState } from './fixtures.ts';

/**
 * Doing things to a recording other than playing it: moving it to another
 * note, deleting it with its paragraph, downloading several as one archive.
 *
 * Each row has a More menu; a long press, or its Select item, starts a
 * selection with a bar at the foot of the screen. The endpoints behind these
 * have been live since v0.5.3 (backlog D2, D3, D4); this is their first UI.
 */

const ROW = /more for recording from/i;

/** The note, opened on its Recordings tab — where every row below lives. */
const RECORDINGS = '/notes/roof-repair?tab=recordings';

/** A second recording on the roof note, so there is something to select together. */
function twoRecordings(api: ApiState): void {
  api.notes['roof-repair']!.captures!.push({
    id: 'cap-newer',
    status: 'appended',
    created_at: '2026-08-06T09:12:00.000Z',
    version: 1,
    note_id: 'roof-repair',
    duration_ms: 8_000,
    has_peaks: false,
    has_segments: false,
  });
  api.notes['roof-repair']!.body += '\n\nGet two quotes before the autumn rain.';
}

/** Press and hold, as a finger does: pointer events rather than a mouse click. */
async function longPress(page: Page, selector: string): Promise<void> {
  const target = page.locator(selector).first();
  await target.dispatchEvent('pointerdown', { pointerType: 'touch', clientX: 20, clientY: 20, isPrimary: true, bubbles: true });
  await page.waitForTimeout(LONG_PRESS_MS + 100);
  await target.dispatchEvent('pointerup', { pointerType: 'touch', bubbles: true });
}

test('a recording can be deleted with its paragraph, behind a plain confirm', async ({ page, api }) => {
  await page.goto(RECORDINGS);
  await expect(page.getByRole('button', { name: ROW })).toBeVisible();

  await page.getByRole('button', { name: ROW }).click();
  await page.getByRole('menuitem', { name: 'Delete recording' }).click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText(/paragraph it dictated/i);
  // Nothing to type (owner, 2026-09-26): the sentence is the warning.
  await expect(dialog.getByRole('textbox')).toHaveCount(0);
  await dialog.getByRole('button', { name: 'Delete it' }).click();

  await expect(page.getByRole('button', { name: ROW })).toHaveCount(0);
  await expect(page.getByText('Recording deleted')).toBeVisible();
  expect(api.deletedCaptures).toEqual(['cap-old']);
  expect(api.notes['roof-repair']?.captures).toEqual([]);
  // The body was rewritten by the server and the text shows what it holds now.
  await page.getByRole('tab', { name: 'Text' }).click();
  await expect(page.getByLabel('Note body')).toHaveValue(api.notes['roof-repair']!.body);
});

test('a recording that is still filing cannot be deleted yet, and the screen says so', async ({
  page,
  api,
}) => {
  // Still filing: the worker wrote the row a moment ago. Without the stamp an
  // August row at `transcribing` is one the server has given up on, and the
  // stub lets it go as the server would.
  api.notes['roof-repair']!.captures![0]!.status = 'transcribing';
  api.notes['roof-repair']!.captures![0]!.last_progress_at = new Date().toISOString();
  await page.goto(RECORDINGS);

  await page.getByRole('button', { name: ROW }).click();
  await page.getByRole('menuitem', { name: 'Delete recording' }).click();
  await page.getByRole('button', { name: 'Delete it' }).click();

  await expect(page.getByText('Wait until it has finished filing.')).toBeVisible();
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(1);
});

test('a recording can be moved to another note, which is then one tap away', async ({
  page,
  api,
}) => {
  await page.goto(RECORDINGS);
  await page.getByRole('button', { name: ROW }).click();
  await page.getByRole('menuitem', { name: 'Move to…' }).click();

  const sheet = page.getByRole('dialog', { name: /move this recording to/i });
  await expect(sheet).toBeVisible();
  // Only other active notes: not this one, not the archive, and no "new note".
  await expect(sheet.getByRole('button', { name: /reading list/i })).toBeVisible();
  await expect(sheet.getByRole('button', { name: /roof repair/i })).toHaveCount(0);
  await expect(sheet.getByRole('button', { name: /old fence/i })).toHaveCount(0);
  await expect(sheet.getByPlaceholder(/new note/i)).toHaveCount(0);

  await sheet.getByRole('searchbox').fill('read');
  await sheet.getByRole('button', { name: /reading list/i }).click();

  await expect(sheet).toHaveCount(0);
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(0);
  await expect(page.getByText(/recording moved to “reading list”/i)).toBeVisible();
  expect(api.notes['reading-list']?.captures?.map((c) => c.id)).toEqual(['cap-old']);
  expect(api.notes['roof-repair']?.captures).toEqual([]);

  await page.getByRole('button', { name: 'Open Reading list' }).click();
  await expect(page).toHaveURL(/\/notes\/reading-list$/);
  // A note never opened this session arrives on its text; its recordings
  // are one segment over.
  await page.getByRole('tab', { name: /^Recordings/ }).click();
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(1);
});

test('several recordings download as one archive, with progress', async ({
  page,
  api,
  browserName,
}) => {
  test.skip(browserName !== 'chromium', 'the download event is asserted in Chromium only');
  twoRecordings(api);
  await page.goto(RECORDINGS);
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(2);

  await page.getByRole('button', { name: ROW }).first().click();
  await page.getByRole('menuitem', { name: 'Select' }).click();

  const bar = page.getByRole('toolbar', { name: 'Recording actions' });
  await expect(bar).toBeVisible();
  await expect(page.getByText('1 selected')).toBeVisible();
  await bar.getByRole('button', { name: 'Select all' }).click();
  await expect(page.getByText('2 selected')).toBeVisible();

  // The bar sits directly above the tab bar, not at the end of the list.
  const barBox = await page.locator('.selection-bar').boundingBox();
  const tabBox = await page.locator('.tab-bar').boundingBox();
  expect(barBox!.y + barBox!.height).toBeLessThanOrEqual(tabBox!.y + 1);

  const download = page.waitForEvent('download');
  await bar.getByRole('button', { name: 'Download' }).click();
  expect((await download).suggestedFilename()).toBe('roof-repair-recordings.zip');
  await expect(page.getByText(/downloaded 2 recordings as one archive/i)).toBeVisible();
  // The manifest was asked for once; each file came from the bucket.
  expect(api.requests.filter((r) => r.url === '/v1/notes/roof-repair/recordings/urls')).toHaveLength(1);
});

test('a long press on a row starts selecting it', async ({ page, api }) => {
  twoRecordings(api);
  await page.goto(RECORDINGS);
  await expect(page.getByRole('button', { name: ROW })).toHaveCount(2);

  await longPress(page, '.recording__summary');

  await expect(page.getByRole('toolbar', { name: 'Recording actions' })).toBeVisible();
  await expect(page.getByText('1 selected')).toBeVisible();
  await expect(page.getByRole('checkbox').first()).toBeChecked();
  // The player closed with the mode change, and Escape leaves it.
  await expect(page.getByRole('region', { name: 'Recording', exact: true })).toHaveCount(0);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('toolbar', { name: 'Recording actions' })).toHaveCount(0);
});

/**
 * Transcribe again (T7): a recording that came back in the wrong script is
 * sent through the pipeline once more from its audio, in the note's
 * language, and the row follows the run as it does a new recording.
 */
test('a settled recording can be transcribed again, and the row follows the run', async ({
  page,
  api,
}) => {
  api.notes['roof-repair']!.language = 'ml';
  await page.goto(RECORDINGS);
  await page.getByRole('button', { name: ROW }).click();
  await page.getByRole('menuitem', { name: 'Transcribe again in Malayalam' }).click();

  await expect(page.getByRole('list', { name: 'Filing progress' })).toBeVisible();
  const posted = api.requests.find(
    (r) => r.method === 'POST' && r.url === '/v1/captures/cap-old/retranscribe',
  );
  expect(posted).toBeTruthy();
  expect(api.notes['roof-repair']?.captures?.[0]?.status).toBe('transcribing');
});

/**
 * A recording the pipeline stopped moving without saying so. It sat at
 * "Filing your recording" over a lit strip for ten minutes, then said
 * something might have gone wrong with nothing to tap, and Delete came back
 * 409. Both surfaces take the failed row's shape instead: the age in the
 * title, no strip, Retry when the server will take it, and a way to delete.
 */
test('a stuck recording offers Retry and Delete on the banner and the Recordings tab, aged and without a strip', async ({
  page,
  api,
}) => {
  seedStuckCapture(api);

  // Home's tray: one the server will take a Retry on, one it still holds.
  await page.goto('/');
  const tray = page.getByRole('region', { name: 'Filing' });
  const rows = tray.locator('.filing-row');
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText('Still not done. Retry, or dismiss it.');
  await expect(rows.nth(0)).toContainText('· 16 min');
  await expect(rows.nth(0).getByRole('button', { name: 'Retry' })).toBeVisible();
  await expect(rows.nth(1)).toContainText('Still not done. You can dismiss it.');
  await expect(rows.nth(1)).toContainText('· 12 min');
  await expect(rows.nth(1).getByRole('button', { name: 'Retry' })).toHaveCount(0);
  await expect(tray.getByRole('list', { name: 'Filing progress' })).toHaveCount(0);

  await page.goto('/notes/roof-repair');
  const banner = page.getByRole('region', { name: 'Filing a recording' });
  await expect(banner).toContainText('Still not done. Retry, or dismiss it.');
  await expect(banner).toContainText('· 16 min');
  await expect(banner.getByRole('list', { name: 'Filing progress' })).toHaveCount(0);
  await expect(banner.getByRole('button', { name: 'Retry' })).toBeVisible();
  await expect(banner.getByRole('button', { name: 'Dismiss' })).toBeVisible();

  await page.getByRole('tab', { name: /^Recordings/ }).click();
  const row = page.getByRole('region', { name: 'Recordings' }).getByRole('listitem').first();
  await expect(row).toContainText('Still not done after 16 min');
  await expect(row.getByRole('list', { name: 'Filing progress' })).toHaveCount(0);
  await row.getByRole('button', { name: ROW }).click();
  await expect(page.getByRole('menuitem', { name: 'Retry' })).toBeVisible();
  await expect(page.getByRole('menuitem', { name: 'Delete recording' })).toBeVisible();
  // Its transcript may well exist; Retry resumes from it. Transcribe again is for a settled row.
  await expect(page.getByRole('menuitem', { name: /transcribe again/i })).toHaveCount(0);
  await page.keyboard.press('Escape');

  // The banner's × is a real delete once the server will let the capture go.
  await page.getByRole('tab', { name: 'Text' }).click();
  await banner.getByRole('button', { name: 'Dismiss' }).click();
  await expect.poll(() => api.deletedCaptures).toEqual(['cap-stuck']);
  await expect(banner).toHaveCount(0);
  await expect(page.getByRole('tab', { name: 'Recordings (1)' })).toBeVisible();
});
