import { expect, test } from '@playwright/test';

import { libraryReady, note, record, shot, sleep } from './helpers.ts';

/**
 * A second look at the filing row with a sampler that never auto-waits, and a
 * log of every GET /v1/captures body, so the time from Send to "Filed" and the
 * row's behaviour across two recordings can be read off the evidence.
 */
test('filing row timeline across two recordings', async ({ page }, info) => {
  const rec = record(page);
  const polls: { t: number; items: string }[] = [];
  const t0 = Date.now();
  page.on('response', (res) => {
    if (res.url().includes('/v1/captures?')) {
      void res.json().then((body: { items: { id: string; status: string; duration_ms: number }[] }) => {
        polls.push({ t: Date.now() - t0, items: body.items.map((c) => `${c.id.slice(-6)}:${c.status}`).join(',') });
      }).catch(() => undefined);
    }
  });
  const sample = (): Promise<string> =>
    page.evaluate(() => {
      const rows = Array.from(document.querySelectorAll('.filing-row'));
      return rows.map((r) => `[${r.getAttribute('data-status')}] ${r.textContent?.replace(/\s+/g, ' ').trim().slice(0, 90)}`).join(' || ') || '(no filing row)';
    });

  await page.goto('./');
  await libraryReady(page);
  // Dismiss any leftover rows so this run starts clean.
  for (const b of await page.getByRole('button', { name: 'Dismiss' }).all()) await b.click().catch(() => undefined);
  note(info, `filing section at start: ${await sample()}`);

  const runOnce = async (label: string, seconds: number): Promise<string> => {
    await page.locator('.record-button').click();
    const state = page.locator('.capture__state');
    await expect(state).toHaveText('Recording', { timeout: 15_000 });
    await sleep(seconds * 1000);
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(state).toHaveText('Ready to send', { timeout: 15_000 });
    const sentAt = Date.now();
    await page.getByRole('button', { name: 'Send' }).click();
    await page.waitForURL((u) => !u.pathname.endsWith('/capture'), { timeout: 60_000 });
    const timeline: string[] = [];
    let last = '';
    let terminalAt = -1;
    const deadline = Date.now() + 90_000;
    while (Date.now() < deadline) {
      const s = await sample();
      if (s !== last) {
        timeline.push(`${Date.now() - sentAt}ms ${s}`);
        last = s;
      }
      if (/\[(appended|needs_target|no_content|failed|spend_capped)\]/.test(s) && !/\[(uploaded|transcribing|routing|cleaning|appending)\]/.test(s)) {
        terminalAt = Date.now() - sentAt;
        break;
      }
      await sleep(200);
    }
    note(info, `${label}: Send → all rows terminal at ${terminalAt} ms; timeline:\n      ${timeline.join('\n      ')}`);
    await shot(page, info, `recdetail-${label}-done`);
    return last;
  };

  const first = await runOnce('first', 8);
  note(info, `first: rows after filing: ${first}`);
  // Open the note from the row, come back, and see whether the row is still there.
  const open = page.getByRole('button', { name: 'Open the note' }).first();
  if (await open.isVisible().catch(() => false)) {
    await open.click();
    await page.locator('#note-body').waitFor();
    await sleep(2_000);
    const lines = await page.locator('.transcript__line').count();
    note(info, `opened note ${JSON.stringify(await page.locator('#note-title').inputValue())}: recordings ${await page.locator('.recording').count()}; newest expanded: ${await page.locator('.recording').first().getAttribute('data-expanded')}; transcript lines ${lines}; empty text ${JSON.stringify(await page.locator('.transcript__empty').innerText().catch(() => ''))}; toggle ${await page.locator('.transcript__toggle').count()}; peaks scrubber ${await page.locator('canvas').count()}`);
    await shot(page, info, 'recdetail-note-after-first', true);
    await page.getByRole('button', { name: /Back to Notes/ }).click();
    await libraryReady(page);
    note(info, `after Open + Back: ${await sample()}`);
  }
  const second = await runOnce('second', 4);
  note(info, `second: rows after filing: ${second}`);
  note(info, `GET /v1/captures responses (t from test start): ${polls.map((p) => `${p.t}ms → ${p.items}`).join(' | ')}`);
  // Leave it a while: does the Filed row go away on its own?
  await sleep(20_000);
  note(info, `20 s later, no interaction: ${await sample()}`);
  await page.reload();
  await libraryReady(page);
  await sleep(1_000);
  note(info, `after a reload: ${await sample()}`);
  rec.dump(info, 'record-detail');
});
