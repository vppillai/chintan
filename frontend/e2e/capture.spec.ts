import type { Page } from '@playwright/test';

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

  await page.getByRole('button', { name: /^PTT: tap to record/ }).click();
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
    await page.getByRole('button', { name: /^PTT: tap to record/ }).click();
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
  await page.getByRole('button', { name: /^PTT into this note/ }).click();
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

  // Send goes to the note it was aimed at — the one chosen last — on the tab it was left on.
  await expect(page).toHaveURL(/\/notes\/reading-list$/);

  await expect.poll(() => api.captures.length, { message: 'capture created' }).toBe(1);
  const create = api.requests.find(
    (request) => request.method === 'POST' && request.url === '/v1/captures',
  );
  expect(create).toBeTruthy();
  expect(api.captures[0]?.note_id).toBe('reading-list');
});

/**
 * Record into a note, Send, and be back on that note where you left it: the
 * filing banner under the meta line shows the upload, then the pipeline's
 * stages, and goes when the recording lands with the text updated under it.
 * Sending used to force the Recordings tab (owner feedback 2026-09-24: the
 * person reading the text was taken to a list of players to watch a strip);
 * before that it dropped the user on the library.
 */
test('Send returns to the note, where the filing banner follows the recording in', async ({
  page,
  api,
}) => {
  await page.goto('/notes/roof-repair');
  await page.getByRole('button', { name: /^PTT into this note/ }).click();
  await expect(page.locator('.capture__state')).toHaveText('Recording');
  await page.waitForTimeout(1_100);
  await page.getByRole('button', { name: 'Stop' }).click();

  // Hold the create so the local upload is observable in the banner.
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

  // Back on the note, on Text — no `?tab=` forced — with the banner up.
  await expect(page).toHaveURL(/\/notes\/roof-repair$/);
  await expect(page.getByRole('tab', { name: 'Text' })).toHaveAttribute('aria-selected', 'true');
  const banner = page.getByRole('region', { name: 'Filing a recording' });
  await expect(banner).toContainText(/uploading… \d+%/i);
  // Counted on the Recordings tab as well: the row is there too.
  await expect(page.getByRole('tab', { name: 'Recordings (2)' })).toBeVisible();
  // The mic still records into this note, to keep adding.
  await expect(page.getByRole('button', { name: /^PTT into this note/ })).toBeVisible();

  releaseCreate();
  await expect.poll(() => api.captures.length, { message: 'capture created' }).toBe(1);
  expect(api.captures[0]?.note_id).toBe('roof-repair');

  // The server's row takes over in the banner and follows the pipeline.
  api.captures[0]!.status = 'transcribing';
  await expect(banner).toContainText('Filing your recording', { timeout: 10_000 });
  await expect(banner.getByRole('list', { name: 'Filing progress' })).toBeVisible();
  await expect(banner).toContainText('Transcribing');

  // It lands: the worker wrote the paragraph, the banner goes, the text shows it.
  api.captures[0]!.status = 'appended';
  api.captures[0]!.duration_ms = 1_100;
  api.notes['roof-repair']!.body += '\n\nThe gutter is leaking again.';
  api.notes['roof-repair']!.version += 1;
  await expect(banner).toHaveCount(0, { timeout: 10_000 });
  await expect(page.getByRole('textbox', { name: 'Note body' })).toHaveValue(
    /The gutter is leaking again\.$/,
  );

  // And the Recordings tab has it as an ordinary recording with its length.
  await page.getByRole('tab', { name: /^Recordings/ }).click();
  const rows = page.getByRole('region', { name: 'Recordings' }).getByRole('listitem');
  await expect(rows.first()).toContainText('0:01');
  await expect(rows.first().getByRole('list', { name: 'Filing progress' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /more for recording from/i })).toHaveCount(2);

  await page.goBack();
  await expect(page).not.toHaveURL(/\/capture/);
});

/**
 * Send while recording: one tap where there were two.
 *
 * The real recorder hands over its last chunk after `stop()` returns, so the
 * thing to prove here — and only here, with a real MediaRecorder — is that the
 * create the server sees was made after the stop settled: a length and a size.
 */
test('Send while recording stops the recorder, then uploads', async ({ page, api }) => {
  await page.goto('/notes/roof-repair');
  await page.getByRole('button', { name: /^PTT into this note/ }).click();
  await expect(page.locator('.capture__state')).toHaveText('Recording');
  await page.waitForTimeout(1_100);

  const create = page.waitForRequest(
    (request) => request.method() === 'POST' && request.url().endsWith('/api/v1/captures'),
  );
  await page.getByRole('button', { name: 'Send' }).click();

  // Gone at once, to where a Send from review goes.
  await expect(page).toHaveURL(/\/notes\/roof-repair$/);
  const body = (await create).postDataJSON() as { duration_ms: number; size_bytes: number };
  expect(body.duration_ms).toBeGreaterThan(900);
  expect(body.size_bytes).toBeGreaterThan(0);

  await expect.poll(() => api.captures.length, { message: 'capture created' }).toBe(1);
  expect(api.captures[0]?.note_id).toBe('roof-repair');
  api.captures[0]!.status = 'appended';
  // Landed: the banner goes, and the row on the Recordings tab says nothing
  // about being filed (T19).
  await expect(page.getByRole('region', { name: 'Filing a recording' })).toHaveCount(0, {
    timeout: 10_000,
  });
  await page.getByRole('tab', { name: /^Recordings/ }).click();
  await expect(page.getByRole('list', { name: 'Filing progress' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /more for recording from/i })).toHaveCount(2);
});

/**
 * Push-to-talk on the tab-bar mic (owner feedback 2026-09-24): held, it
 * records in place with a card above the bar; released, it sends; slid away,
 * it cancels. A tap is still the capture screen, which every other spec here
 * proves. Driven with the real mouse so the button's pointer capture and the
 * click that follows the release are the browser's own.
 */
async function holdTheMic(page: Page, name: RegExp, ms: number, slide = 0): Promise<void> {
  const mic = page.getByRole('button', { name });
  const box = await mic.boundingBox();
  if (!box) throw new Error('the mic has no box');
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await expect(page.locator('.hold-overlay')).toContainText(/release to send/i);
  await page.waitForTimeout(ms);
  if (slide) await page.mouse.move(x + slide, y, { steps: 6 });
  await page.mouse.up();
}

test('holding the mic on Home records in place and sends on release', async ({ page, api }) => {
  await page.goto('/');
  await holdTheMic(page, /^PTT: tap to record/, 1_500);

  // Sent, not opened: still Home, the card gone, the upload in the filing row.
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator('.hold-overlay')).toHaveCount(0);
  await expect(page.getByRole('region', { name: /recordings being filed/i })).toBeVisible();
  await expect.poll(() => api.captures.length, { message: 'capture created' }).toBe(1);
  expect(api.captures[0]?.note_id).toBeNull();
  // The same create the capture screen makes: keyed, so a resume replays it.
  const create = api.requests.find(
    (request) => request.method === 'POST' && request.url === '/v1/captures',
  );
  expect(create?.headers['idempotency-key']).toBeTruthy();
});

test('holding the mic on a note records into it, and the banner shows it filing', async ({
  page,
  api,
}) => {
  await page.goto('/notes/roof-repair');
  await holdTheMic(page, /^PTT into this note/, 1_500);

  await expect(page).toHaveURL(/\/notes\/roof-repair$/);
  await expect(page.getByRole('region', { name: 'Filing a recording' })).toBeVisible();
  await expect.poll(() => api.captures.length, { message: 'capture created' }).toBe(1);
  expect(api.captures[0]?.note_id).toBe('roof-repair');
});

test('sliding away before release cancels, and a hold too short is discarded with a hint', async ({
  page,
  api,
}) => {
  await page.goto('/');

  await holdTheMic(page, /^PTT: tap to record/, 1_200, 160);
  await expect(page.locator('.hold-overlay')).toHaveCount(0);
  await expect(page).toHaveURL(/\/$/);

  await holdTheMic(page, /^PTT: tap to record/, 50);
  await expect(page.locator('.hold-overlay--hint')).toHaveText('Too short — hold to talk');
  await expect(page).toHaveURL(/\/$/);

  // Neither reached the server.
  await page.waitForTimeout(1_000);
  expect(api.captures).toHaveLength(0);
  await expect(page.getByRole('region', { name: /recordings being filed/i })).toHaveCount(0);
});

/**
 * The narrowest phone still in use, held in a hand: four round targets in one
 * row, none of them narrower than the finger. Touch emulation gives the coarse
 * pointer that selects the 64px size; the viewport is the iPhone SE's.
 */
test.describe('the control row on a 320px phone', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 320, height: 568 } });

  test('keeps Cancel, Pause, Stop and Send on one row at 64px', async ({ page }) => {
    await page.goto('/capture');
    await expect(page.locator('.capture__state')).toHaveText('Recording');

    const row = async () =>
      page.locator('.capture__control').evaluateAll((controls) =>
        controls.map((control) => {
          const box = control.getBoundingClientRect();
          const disc = control.querySelector('.capture__control-disc')!.getBoundingClientRect();
          return { top: Math.round(box.top), left: box.left, right: box.right, disc: disc.width };
        }),
      );

    let controls = await row();
    expect(controls).toHaveLength(4);
    expect(new Set(controls.map((control) => control.top)).size, 'the row wrapped').toBe(1);
    for (const control of controls) {
      expect(control.left).toBeGreaterThanOrEqual(0);
      expect(control.right).toBeLessThanOrEqual(320);
      expect(control.disc).toBeGreaterThanOrEqual(64);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(320);

    // And the words are there to read, not only to be announced.
    for (const label of ['Cancel', 'Pause', 'Stop', 'Send']) {
      await expect(page.getByText(label, { exact: true })).toBeVisible();
    }

    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(page.locator('.capture__state')).toHaveText('Ready to send');
    controls = await row();
    expect(controls).toHaveLength(3);
    expect(new Set(controls.map((control) => control.top)).size, 'the review row wrapped').toBe(1);
  });
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
