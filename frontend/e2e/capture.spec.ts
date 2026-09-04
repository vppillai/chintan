import { expect, test } from './fixtures.ts';

/**
 * Record → stop → send → filing row → filed.
 *
 * Chromium's fake media device gives MediaRecorder a real encoded stream, so
 * this exercises the actual recorder, the real IndexedDB buffer, and the real
 * upload path — including the canvas waveform, which no unit test can reach
 * because jsdom has no `getContext`.
 */

test('records, uploads, and hands off to the filing row', async ({ page, api }) => {
  await page.goto('/');

  await page.getByRole('button', { name: /^record$/i }).click();
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

  // Review: the recording itself, playable and seekable, before it goes anywhere.
  const slider = page.getByRole('slider', { name: 'Playback position' });
  await expect(slider).toBeVisible();
  await expect(slider).toHaveAttribute('aria-valuemax', /^[1-9]\d*$/);
  // Playable: the button enables once the chunks are reassembled, playback
  // runs, and — the clip being about a second long — ends by itself.
  await page.getByRole('button', { name: 'Play recording' }).click();
  await expect(page.getByRole('button', { name: 'Pause recording' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Play recording' })).toBeVisible({
    timeout: 10_000,
  });
  await expect(page.getByRole('button', { name: 'Re-record' })).toBeVisible();

  /*
   * Send must not wait. The create is held back so the hand-off is observable
   * before the server has answered anything: the library, with the upload's
   * own row at the top of it.
   */
  let releaseCreate: () => void = () => {};
  const held = new Promise<void>((resolve) => {
    releaseCreate = resolve;
  });
  await page.route('**/api/v1/captures', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.fallback();
      return;
    }
    await held;
    await route.fallback();
  });

  await page.getByRole('button', { name: 'Send' }).click();

  await expect(page).toHaveURL(/\/$/);
  const filing = page.getByRole('region', { name: /recordings being filed/i });
  await expect(filing).toBeVisible();
  await expect(filing.getByText(/uploading… \d+%/i)).toBeVisible();
  expect(api.captures).toHaveLength(0);
  // Not offered back as "unsent" while it is being sent one row below.
  await expect(page.getByRole('region', { name: 'Unsent recording' })).toHaveCount(0);

  releaseCreate();
  await expect
    .poll(() => api.captures.length, { message: 'capture created' })
    .toBeGreaterThan(0);

  // The create is keyed so a resumed upload replays rather than duplicating.
  const create = api.requests.find(
    (request) => request.method === 'POST' && request.url === '/v1/captures',
  );
  expect(create?.headers['idempotency-key']).toBeTruthy();
  expect(create?.headers['authorization']).toBe('Bearer e2e-id-token');

  // The server's row takes over from the local one, and follows the pipeline.
  api.captures[0]!.status = 'transcribing';
  await expect(filing.getByText('Filing your recording')).toBeVisible({ timeout: 10_000 });
  await expect(filing.locator('.filing-row')).toHaveCount(1);
  api.captures[0]!.status = 'appended';
  api.captures[0]!.note_id = 'roof-repair';
  await expect(filing.getByText('Filed')).toBeVisible({ timeout: 10_000 });
  // And Back does not walk into a fresh recording: the capture entry was replaced.
  await page.goBack();
  await expect(page).not.toHaveURL(/\/capture$/);
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
    await page.getByRole('button', { name: /^record$/i }).click();
    await expect(page).toHaveURL(/\/capture$/);

    // Never "Sent", and never the previous recording's clock.
    await expect(page.locator('.capture__state')).toHaveText('Recording');
    await expect(page.locator('.capture__timer')).toHaveText('00:00');

    await page.waitForTimeout(1_100);
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(page.getByText('Ready to send')).toBeVisible();
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page).toHaveURL(/\/$/, { timeout: 10_000 });

    await expect
      .poll(() => api.captures.length, { message: `capture ${attempt} created`, timeout: 15_000 })
      .toBe(attempt);

    // Filed, so the row settles and the machine is released for the next take.
    api.captures.at(-1)!.status = 'appended';
    await expect(page.getByText('Filed').first()).toBeVisible({ timeout: 10_000 });
  }
});

/**
 * Filing into a note you are already reading, and changing your mind on the
 * record screen. `POST /v1/captures` has accepted `note_id` since the contract
 * was written; until now nothing in the UI sent it, so the only ways to file
 * into a particular note were to say its name and hope, or to fix it afterwards
 * from the "needs a target" row.
 */
test('records into the note it was opened from, and the target can be changed before Send', async ({
  page,
  api,
}) => {
  await page.goto('/notes/roof-repair');
  await page.getByRole('button', { name: /record into this/i }).click();
  await expect(page).toHaveURL(/\/capture\?note=roof-repair$/);
  await expect(page.locator('.capture__state')).toHaveText('Recording');

  // Where it will file is stated before a word is spoken.
  const pill = page.getByRole('button', { name: /into roof repair/i });
  await expect(pill).toBeVisible();

  // And it can be changed: to another note, from the list the app holds.
  await pill.click();
  await page.getByRole('button', { name: 'Reading list' }).click();
  await expect(page.getByRole('button', { name: /into reading list/i })).toBeVisible();

  await page.waitForTimeout(1_100);
  await page.getByRole('button', { name: 'Stop' }).click();
  await page.getByRole('button', { name: 'Send' }).click();

  await expect.poll(() => api.captures.length, { message: 'capture created' }).toBe(1);
  const create = api.requests.find(
    (request) => request.method === 'POST' && request.url === '/v1/captures',
  );
  expect(create).toBeTruthy();
  expect(api.captures[0]?.note_id).toBe('reading-list');
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

/**
 * Where the app thinks a recording goes.
 *
 * The pipeline pays for an LLM routing call and stores its answer on the
 * capture as `suggested_note_id` / `suggested_title`, both declared in
 * `openapi.yaml`. The "where should this go?" prompt leads with that answer
 * rather than an unranked list of every note the user owns.
 */
test('leads with the note the router picked, and files into it', async ({ page, api }) => {
  api.captures.push({
    id: 'cap-routed',
    status: 'needs_target',
    created_at: new Date().toISOString(),
    version: 1,
    note_id: null,
    suggested_note_id: 'roof-repair',
  });

  await page.goto('/');

  const filing = page.getByRole('region', { name: /recordings being filed/i });
  await expect(filing.getByText(/which note should this go in/i)).toBeVisible();
  const add = filing.getByRole('button', { name: /add to .*roof repair/i });
  await expect(add).toBeVisible();

  // Not an unranked list of everything the user owns. (The library beneath
  // lists the notes too, so the check is scoped to the row.)
  await expect(filing.getByRole('button', { name: 'Reading list' })).toHaveCount(0);

  await add.click();

  await expect
    .poll(() =>
      api.requests.some(
        (request) =>
          request.method === 'POST' && request.url === '/v1/captures/cap-routed/target',
      ),
    )
    .toBe(true);
});

test('a suggested new note is offered by the title the router chose', async ({ page, api }) => {
  api.captures.push({
    id: 'cap-new',
    status: 'needs_target',
    created_at: new Date().toISOString(),
    version: 1,
    note_id: null,
    suggested_title: 'Kitchen rebuild',
  });

  await page.goto('/');

  const filing = page.getByRole('region', { name: /recordings being filed/i });
  await expect(filing.getByRole('button', { name: /start .*kitchen rebuild/i })).toBeVisible();

  // And disagreeing is one tap, so the suggestion is never a trap.
  await filing.getByRole('button', { name: /choose another note/i }).click();
  await expect(filing.getByRole('button', { name: 'Roof repair' })).toBeVisible();
  await expect(filing.getByRole('button', { name: 'Reading list' })).toBeVisible();
  await expect(filing.getByRole('button', { name: /back to the suggestion/i })).toBeVisible();
});
