import { expect, test } from './fixtures.ts';

/**
 * Record → stop → send → progress card → filed.
 *
 * Chromium's fake media device gives MediaRecorder a real encoded stream, so
 * this exercises the actual recorder, the real IndexedDB buffer, and the real
 * upload path — including the canvas waveform, which no unit test can reach
 * because jsdom has no `getContext`.
 */

test('records, uploads, and hands off to the progress card', async ({ page, api }) => {
  await page.goto('/');

  await page.getByRole('button', { name: /record/i }).click();
  await expect(page).toHaveURL(/\/capture$/);

  // The claim the machine has to earn: not "Recording" until the stream is live.
  await expect(page.locator('.capture__state')).toHaveText('Recording');

  // The waveform is a real canvas being painted from AnalyserNode data.
  const canvas = page.locator('canvas.waveform');
  await expect(canvas).toBeVisible();
  await page.waitForTimeout(1_200);
  const painted = await canvas.evaluate((element) => {
    const source = element as HTMLCanvasElement;
    const context = source.getContext('2d');
    if (!context || source.width === 0) return false;
    const { data } = context.getImageData(0, 0, source.width, source.height);
    // Any non-transparent pixel means bars were drawn.
    for (let index = 3; index < data.length; index += 4) {
      if ((data[index] ?? 0) > 0) return true;
    }
    return false;
  });
  expect(painted).toBe(true);

  // The timer counts up with tabular numerals.
  await expect(page.locator('.capture__timer')).not.toHaveText('00:00');

  await page.getByRole('button', { name: 'Stop' }).click();
  await expect(page.getByText('Ready to send')).toBeVisible();

  await page.getByRole('button', { name: 'Send' }).click();

  await expect
    .poll(() => api.captures.length, { message: 'capture created' })
    .toBeGreaterThan(0);

  // The create is keyed so a resumed upload replays rather than duplicating.
  const create = api.requests.find(
    (request) => request.method === 'POST' && request.url === '/v1/captures',
  );
  expect(create?.headers['idempotency-key']).toBeTruthy();
  expect(create?.headers['authorization']).toBe('Bearer e2e-id-token');

  // Handed off: the progress card lives in the shell and survives this screen.
  api.captures[0]!.status = 'transcribing';
  await expect(page).toHaveURL(/\/$/, { timeout: 10_000 });
  await expect(page.getByRole('region', { name: /captures in progress/i })).toBeVisible();
});

/**
 * The app must record more than once per page load.
 *
 * After a successful send the machine sits in the terminal `uploaded` state.
 * Nothing reset it, so the mount effect's `idle` guard never fired again: the
 * second tap of Record showed "Sent" and the previous elapsed time, bounced
 * back home, and never opened the microphone. Only a reload recovered.
 */
test('records twice in one page load, without a reload', async ({ page, api }) => {
  await page.goto('/');

  for (const attempt of [1, 2]) {
    await page.getByRole('button', { name: /record/i }).click();
    await expect(page).toHaveURL(/\/capture$/);

    // Never "Sent", and never the previous recording's clock.
    await expect(page.locator('.capture__state')).toHaveText('Recording');
    await expect(page.locator('.capture__timer')).toHaveText('00:00');

    await page.waitForTimeout(1_100);
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(page.getByText('Ready to send')).toBeVisible();
    await page.getByRole('button', { name: 'Send' }).click();

    await expect
      .poll(() => api.captures.length, { message: `capture ${attempt} created`, timeout: 15_000 })
      .toBe(attempt);

    // Filed, so the progress card clears and Home is back to the record hero.
    api.captures.at(-1)!.status = 'appended';
    await expect(page).toHaveURL(/\/$/, { timeout: 10_000 });
  }
});

test('pause and stop are distinct controls', async ({ page }) => {
  await page.goto('/capture');

  await expect(page.locator('.capture__state')).toHaveText('Recording');
  await page.getByRole('button', { name: 'Pause' }).click();
  await expect(page.locator('.capture__state')).toHaveText('Paused');

  const frozen = await page.locator('.capture__timer').textContent();
  await page.waitForTimeout(800);
  expect(await page.locator('.capture__timer').textContent()).toBe(frozen);

  await page.getByRole('button', { name: 'Resume' }).click();
  await expect(page.locator('.capture__state')).toHaveText('Recording');
});

test('a failed capture offers a retry that reaches the API', async ({ page, api }) => {
  api.captures.push({
    id: 'cap-failed',
    status: 'failed',
    created_at: new Date().toISOString(),
    version: 1,
    error: 'Transcription timed out',
  });

  await page.goto('/');

  await expect(page.getByText('Transcription timed out')).toBeVisible();
  await page.getByRole('button', { name: 'Retry' }).click();

  await expect
    .poll(() =>
      api.requests.some(
        (request) => request.method === 'POST' && request.url === '/v1/captures/cap-failed/retry',
      ),
    )
    .toBe(true);
});
