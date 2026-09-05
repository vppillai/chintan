import type { Page } from '@playwright/test';

import { ARTIFACT_ORIGIN, expect, test } from './fixtures.ts';

/**
 * Inline playback with tap-to-seek, and the raw/cleaned distinction.
 *
 * The scrubber canvas is only reachable here — jsdom has no `getContext`.
 *
 * The recordings are the note's Recordings tab, reached here by its deep link
 * (`?tab=recordings`); the newest is open on arrival, so the player and
 * transcript these specs reach for are on screen without a further tap.
 * `exact: true` on the region: the row's player is "Recording", the section
 * around it is "Recordings", and a substring match would find both.
 */

const RECORDINGS = '/notes/roof-repair?tab=recordings';

test('plays inline, never in a new tab', async ({ page, context }) => {
  await page.goto(RECORDINGS);

  await expect(page.getByRole('region', { name: 'Recording', exact: true })).toBeVisible();

  const pagesBefore = context.pages().length;
  await page.getByRole('button', { name: 'Play' }).click();

  // Nothing here may hand the presigned S3 URL to window.open or a new tab.
  expect(context.pages()).toHaveLength(pagesBefore);
  await expect(page).toHaveURL(/\/notes\/roof-repair\?tab=recordings$/);
  await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible();
});

/**
 * "Download audio" never worked on the live app. The bucket is another origin;
 * the `<audio>` element fetched the presigned URL in no-cors mode, S3 answered
 * without `Access-Control-Allow-Origin`, and Chromium handed that cached
 * response to the download's CORS `fetch`, which failed its check every time.
 * The stub bucket behaves the same way (see `ARTIFACT_ORIGIN`), so this only
 * passes if both requests are made as CORS requests and the download does not
 * read from the element's cache.
 */
test('downloads the recording it has just loaded, from the cross-origin bucket', async ({
  page,
  browserName,
}) => {
  test.skip(browserName !== 'chromium', 'the download event is asserted in Chromium only');
  await page.goto(RECORDINGS);

  const audio = page.locator('audio');
  await expect(audio).toHaveAttribute('src', new RegExp(`^${ARTIFACT_ORIGIN}/`));
  await expect(audio).toHaveAttribute('crossorigin', 'anonymous');
  // The element has fetched the media: the cache now holds its answer.
  await waitForAudioReady(page);

  const download = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download audio' }).click();
  expect((await download).suggestedFilename()).toBe('chintan-cap-old.webm');
  await expect(page.getByText('Downloaded')).toBeVisible();
});

test('renders the peaks waveform to canvas', async ({ page }) => {
  await page.goto(RECORDINGS);

  const canvas = page.locator('.scrubber__canvas');
  await expect(canvas).toBeVisible();

  const painted = await canvas.evaluate((element) => {
    const source = element as HTMLCanvasElement;
    const context = source.getContext('2d');
    if (!context || source.width === 0) return false;
    const { data } = context.getImageData(0, 0, source.width, source.height);
    for (let index = 3; index < data.length; index += 4) {
      if ((data[index] ?? 0) > 0) return true;
    }
    return false;
  });
  expect(painted).toBe(true);
});

/** The audio element must have metadata before a seek means anything. */
async function waitForAudioReady(page: Page): Promise<void> {
  await page
    .locator('audio')
    .evaluate(
      (element) =>
        new Promise<void>((resolve) => {
          const audio = element as HTMLAudioElement;
          if (audio.readyState >= 1) {
            resolve();
            return;
          }
          audio.addEventListener('loadedmetadata', () => resolve(), { once: true });
        }),
    );
}

test('tapping a transcript line seeks the audio', async ({ page }) => {
  await page.goto(RECORDINGS);
  await waitForAudioReady(page);

  const line = page.getByRole('button', { name: /Ellis quoted nine hundred/ });
  await expect(line).toBeVisible();
  await line.click();

  const currentTime = await page.locator('audio').evaluate(
    (element) => (element as HTMLAudioElement).currentTime,
  );
  // Segment 3 begins at 8s.
  expect(currentTime).toBeGreaterThanOrEqual(7.5);
});

test('the scrubber is keyboard-operable', async ({ page }) => {
  await page.goto(RECORDINGS);
  await waitForAudioReady(page);

  const slider = page.getByRole('slider', { name: 'Playback position' });
  await slider.focus();
  await slider.press('ArrowRight');

  await expect
    .poll(async () =>
      page.locator('audio').evaluate((element) => (element as HTMLAudioElement).currentTime),
    )
    .toBeGreaterThan(0);
});

test('the cleaned view drops timestamps and says why', async ({ page }) => {
  await page.goto(RECORDINGS);

  // Raw view offers seeking.
  await expect(page.getByText(/tap any line to jump/i)).toBeVisible();

  await page.getByRole('button', { name: 'Cleaned' }).click();

  // The constraint is stated, not hidden — and there is nothing to tap.
  await expect(page.getByText(/no reliable timestamps/i)).toBeVisible();
  await expect(page.getByRole('button', { name: /Ellis quoted nine hundred/ })).toHaveCount(0);

  /*
   * The cleaned text is actually there. It used to be hard-coded to `''` at the
   * call site, so this tab read "No cleaned text for this capture." on every
   * capture in the app — including ones the pipeline had cleaned perfectly
   * well, which reads as "cleanup failed" or "my text was lost".
   */
  await expect(page.getByText(/Get two quotes before the autumn rain/)).toBeVisible();
  await expect(page.getByText(/no cleaned text for this capture/i)).toHaveCount(0);
});

test('the Cleaned toggle is not offered when there is no cleaned text', async ({ page }) => {
  // The presigned URL is still minted; the object behind it is gone, which is
  // what a capture that stopped at `no_content` looks like.
  await page.route('**/artifact/*/clean', (route) => route.fulfill({ status: 404, body: '' }));

  await page.goto(RECORDINGS);

  await expect(page.getByRole('button', { name: /Ellis quoted nine hundred/ })).toBeVisible();
  // No control whose only possible outcome is an empty panel.
  await expect(page.getByRole('button', { name: 'Cleaned' })).toHaveCount(0);
});

test('a capture with no artifacts falls back to a plain player', async ({ page, api }) => {
  api.notes['reading-list']!.captures = [
    {
      id: 'cap-legacy',
      status: 'appended',
      created_at: '2026-01-01T00:00:00.000Z',
      version: 1,
      note_id: 'reading-list',
      duration_ms: 5_000,
      has_peaks: false,
      has_segments: false,
    },
  ];

  await page.goto('/notes/reading-list?tab=recordings');

  await expect(page.getByRole('slider', { name: 'Playback position' })).toBeVisible();
  await expect(page.locator('.scrubber__canvas')).toHaveCount(0);
  await expect(page.getByText(/no timestamps are available/i)).toBeVisible();
});

test('an edit conflict is surfaced rather than clobbering', async ({ page, api }) => {
  api.conflictOnce = true;
  await page.goto('/notes/roof-repair');

  const body = page.getByLabel('Note body');
  await body.fill('My local edit.');
  await body.blur();

  await expect(page.getByText(/changed elsewhere/i)).toBeVisible({ timeout: 10_000 });
  await expect(page.getByRole('button', { name: 'Use the newer version' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Keep my edits' })).toBeVisible();
});

/**
 * Copying a note's text out in one action.
 *
 * The owner asked for this: it is a voice-capture app whose point is getting a
 * thought out and using it elsewhere, and the only route was select-all inside
 * a textarea, which one-handed on a phone is miserable.
 */
test('copies the note as its title and body', async ({ page, context, browserName }) => {
  // Playwright grants clipboard permissions in Chromium only; the control is
  // the same component on every engine, so the check is not repeated.
  test.skip(browserName !== 'chromium', 'clipboard permissions are Chromium-only in Playwright');
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  // Copy and download live behind Share in the action bar, a clear distance
  // from Archive, so a stray thumb cannot reach both.
  await page.getByRole('button', { name: 'Share' }).click();
  await page.getByRole('button', { name: 'Copy note' }).click();

  // A copy with no confirmation cannot be told from one that failed: the
  // button itself says so for a moment.
  await expect(page.getByRole('button', { name: 'Copied' })).toBeVisible();

  const clipboard = await page.evaluate(() => navigator.clipboard.readText());
  // Title first: a body pasted elsewhere with no title loses what it was about.
  expect(clipboard).toContain('Roof repair');
  expect(clipboard).toContain('Ridge tiles on the south slope have slipped.');
  expect(clipboard.indexOf('Roof repair')).toBeLessThan(
    clipboard.indexOf('Ridge tiles'),
  );
});

test('copies the transcript separately, and names which one', async ({
  page,
  context,
  browserName,
}) => {
  test.skip(browserName !== 'chromium', 'clipboard permissions are Chromium-only in Playwright');
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto(RECORDINGS);

  // Three different things could be meant by "copy" on this screen, so no
  // control is allowed to be called just "Copy" — and the one inside a
  // recording's row says it copies that recording, not the note.
  await page.getByRole('button', { name: 'Share' }).click();
  await expect(page.getByRole('button', { name: 'Copy note' })).toBeVisible();
  await page.getByRole('button', { name: 'Copy this transcript' }).click();
  await expect(page.getByRole('button', { name: 'Copied' })).toBeVisible();

  const raw = await page.evaluate(() => navigator.clipboard.readText());
  expect(raw).toContain('Get two quotes before the autumn rain.');
  // The raw transcript, not the rewritten note body.
  expect(raw).not.toContain('Ellis quoted nine hundred.\n\n');

  await page.getByRole('button', { name: 'Cleaned' }).click();
  await page.getByRole('button', { name: 'Copy this cleaned text' }).click();

  const cleaned = await page.evaluate(() => navigator.clipboard.readText());
  expect(cleaned).toContain('Ellis quoted nine hundred');
});
