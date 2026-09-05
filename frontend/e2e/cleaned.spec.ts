import { CLEAN_WORKER_MS, expect, test } from './fixtures.ts';

/**
 * The Cleaned tab: the worker's rewrite of the whole note, read-only, with
 * Generate and Regenerate, a stale notice, a mode switch and the per-note
 * toggle that keeps it updated after each recording.
 *
 * The backend answers the regeneration with 202 and writes the view a moment
 * later; the stub does the same, so what these prove is that the app polls
 * for the answer rather than expecting one in the response.
 */

const CLEANED = '/notes/roof-repair?tab=cleaned';

test('starts empty, generates on demand, and renders the Markdown it gets back', async ({
  page,
  api,
}) => {
  await page.goto(CLEANED);
  await expect(page.getByRole('tab', { name: 'Cleaned' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByText('No cleaned view yet')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Structured' })).toHaveAttribute('aria-pressed', 'true');

  await page.getByRole('button', { name: 'Generate' }).click();
  await expect(page.getByRole('status').filter({ hasText: /rewriting the note/i })).toBeVisible();

  // The answer arrives on the note, found by the poll, not in the response.
  const view = page.locator('.cleaned__body');
  await expect(view.getByRole('heading', { name: 'Roof repair' })).toBeVisible({ timeout: 10_000 });
  await expect(view.getByRole('heading', { name: 'Summary' })).toBeVisible();
  await expect(view.getByRole('listitem').first()).toContainText('Ridge tiles on the south slope');
  await expect(view.locator('strong')).toHaveText('Next:');
  await expect(page.getByText(/generated just now · structured/i)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Regenerate' })).toBeEnabled();
  expect(api.requests.filter((r) => r.method === 'POST' && r.url === '/v1/notes/roof-repair/clean')).toHaveLength(1);

  // Read-only: no field, no way to type into it.
  await expect(view.getByRole('textbox')).toHaveCount(0);

  // Share offers the view alongside the note.
  await page.getByRole('button', { name: 'Share' }).click();
  await expect(page.getByRole('button', { name: 'Copy cleaned view' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Download cleaned view' })).toBeVisible();
});

test('says when the note has moved on, and the mode switch regenerates in that mode', async ({
  page,
  api,
}) => {
  api.notes['roof-repair']!.cleaned = {
    body: '# Roof repair\n\n- Ridge tiles have slipped.',
    mode: 'structured',
    generated_at: new Date(Date.now() - 3 * 60_000).toISOString(),
    stale: true,
  };
  await page.goto(CLEANED);

  await expect(page.getByText(/generated 3 minutes ago · structured/i)).toBeVisible();
  const stale = page.getByRole('status').filter({ hasText: /note changed since/i });
  await expect(stale).toBeVisible();

  await page.getByRole('button', { name: 'Polished' }).click();
  await expect(page.getByRole('button', { name: /regenerating/i })).toBeDisabled();
  await page.waitForTimeout(CLEAN_WORKER_MS);
  await expect(page.getByText(/generated just now · polished/i)).toBeVisible({ timeout: 10_000 });
  await expect(stale).toHaveCount(0);
  // The polished view is the note's prose, as a paragraph rather than a list.
  await expect(page.locator('.cleaned__body p').first()).toContainText('Ridge tiles on the south slope');

  const clean = api.requests.filter((r) => r.method === 'POST' && r.url === '/v1/notes/roof-repair/clean');
  expect(clean).toHaveLength(1);
  // And the choice was recorded on the note, through its own PATCH.
  await expect.poll(() => api.notes['roof-repair']?.cleaned_mode).toBe('polished');
});

test('the toggle asks the worker to keep the view updated, through the note’s own save', async ({
  page,
  api,
}) => {
  await page.goto(CLEANED);
  const toggle = page.getByRole('checkbox', { name: /keep it updated after each recording/i });
  await expect(toggle).not.toBeChecked();

  await toggle.check();
  await expect(toggle).toBeChecked();
  await expect.poll(() => api.notes['roof-repair']?.auto_clean).toBe(true);
  const patch = api.requests.find((r) => r.method === 'PATCH' && r.url === '/v1/notes/roof-repair');
  expect(patch).toBeTruthy();

  // Still on after a reload: it is the note's, not the screen's.
  await page.reload();
  await expect(
    page.getByRole('checkbox', { name: /keep it updated after each recording/i }),
  ).toBeChecked();
});
