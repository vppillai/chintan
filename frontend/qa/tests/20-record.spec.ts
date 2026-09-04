import { expect, test, type Page } from '@playwright/test';

import { getNote, listCaptures } from './api.ts';
import { libraryReady, note, record, shot, sleep, text } from './helpers.ts';

/** Samples the waveform canvas and the status line at a high rate from inside the page. */
async function armWaveformProbe(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as { __probe: { t: number; status: string; painted: number; timer: string }[] };
    w.__probe = [];
    const t0 = performance.now();
    const tick = (): void => {
      const canvas = document.querySelector<HTMLCanvasElement>('canvas.waveform');
      const status = document.querySelector('.capture__state')?.textContent ?? '';
      const timer = document.querySelector('.capture__timer')?.textContent ?? '';
      let painted = 0;
      if (canvas && canvas.width > 0) {
        const ctx = canvas.getContext('2d');
        const data = ctx?.getImageData(0, 0, canvas.width, canvas.height).data;
        if (data) {
          for (let i = 3; i < data.length; i += 4 * 7) if (data[i]! > 0) painted += 1;
        }
      }
      w.__probe.push({ t: Math.round(performance.now() - t0), status, painted, timer });
      if (performance.now() - t0 < 4_000) requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
}

async function readProbe(page: Page) {
  return page.evaluate(() => (window as unknown as { __probe: { t: number; status: string; painted: number; timer: string }[] }).__probe);
}

test.describe('record flow', () => {
  test('record → stop → send → filing row → open the note; then record again', async ({ page }, info) => {
    const rec = record(page);
    await page.goto('./');
    await libraryReady(page);
    const capturesBefore = (await listCaptures(page)).length;

    // ---- first recording ----
    await page.locator('.record-button').click();
    await expect(page).toHaveURL(/\/capture/);
    const state = page.locator('.capture__state');
    await expect(state).toHaveText('Recording', { timeout: 15_000 });
    note(info, `capture screen: target pill ${JSON.stringify(await page.locator('.target-chooser__pill').innerText())}; tab bar present: ${await page.locator('.tab-bar').count()}`);
    await sleep(2_000);
    await shot(page, info, 'rec-01-recording');
    await sleep(6_000);
    note(info, `timer before stop: ${await page.locator('.capture__timer').innerText()}`);
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(state).toHaveText('Ready to send', { timeout: 15_000 });
    await shot(page, info, 'rec-02-ready-to-send');
    const sentAt = Date.now();
    await page.getByRole('button', { name: 'Send' }).click();
    // Watch the states go by.
    const seen: string[] = [];
    const watch = setInterval(() => {
      void state.innerText().then((t) => {
        if (seen[seen.length - 1] !== t) seen.push(t);
      }).catch(() => undefined);
    }, 50);
    await page.waitForURL((u) => !u.pathname.endsWith('/capture'), { timeout: 60_000 });
    clearInterval(watch);
    const backAt = Date.now();
    note(info, `capture states after Send: ${seen.join(' → ')}; back on library after ${backAt - sentAt} ms; ${await text(page).then((t) => JSON.stringify(t.slice(0, 200)))}`);
    await shot(page, info, 'rec-03-filing-row-start');

    const filing = page.locator('.filing-row').first();
    const stages: string[] = [];
    const deadline = Date.now() + 120_000;
    let status = '';
    while (Date.now() < deadline) {
      status = (await filing.getAttribute('data-status').catch(() => null)) ?? '';
      const label = (await filing.locator('.filing-row__status').innerText().catch(() => '')).trim();
      const title = (await filing.locator('.filing-row__title').innerText().catch(() => '')).trim();
      const marker = `${status}/${label}/${title}`;
      if (stages[stages.length - 1] !== marker) stages.push(`${Date.now() - sentAt}ms ${marker}`);
      if (['appended', 'needs_target', 'no_content', 'failed', 'spend_capped'].includes(status)) break;
      await sleep(250);
    }
    const filedAt = Date.now();
    note(info, `filing row timeline: ${stages.join(' | ')}`);
    note(info, `Send → terminal status "${status}" in ${filedAt - sentAt} ms`);
    await shot(page, info, 'rec-04-filing-row-done');
    const captures = await listCaptures(page);
    const mine = captures.find((c) => !capturesBefore || c['created_at']! > new Date(sentAt - 60_000).toISOString());
    note(info, `capture record: ${JSON.stringify(mine)}`);
    note(info, `captures polled: ${rec.apiCalls('GET', '/v1/captures').length} GET /v1/captures between Send and Filed`);

    if (status === 'appended') {
      const noteId = String(mine?.['note_id'] ?? '');
      const beforeOpen = noteId ? await getNote(page, noteId) : null;
      await filing.getByRole('button', { name: 'Open the note' }).click();
      await expect(page).toHaveURL(/\/notes\//);
      await sleep(800);
      const title = await page.locator('#note-title').inputValue();
      const body = await page.locator('#note-body').inputValue();
      note(info, `opened note "${title}": body (${body.length} chars) = ${JSON.stringify(body.slice(0, 400))}`);
      note(info, `server body at that moment: ${JSON.stringify(String(beforeOpen?.['body'] ?? '').slice(0, 400))}`);
      note(info, `recordings rows on the note: ${await page.locator('.recording').count()}; newest label: ${JSON.stringify(await page.locator('.recording__summary').first().innerText().catch(() => ''))}`);
      await shot(page, info, 'rec-05-opened-note');
      await page.getByRole('button', { name: /Back to Notes/ }).click();
      await libraryReady(page);
      note(info, `back on library: filing rows now ${await page.locator('.filing-row').count()}`);
      await shot(page, info, 'rec-06-library-after-open');
    } else if (status === 'needs_target') {
      note(info, `needs_target prompt: ${JSON.stringify(await filing.innerText())}`);
      await shot(page, info, 'rec-05-needs-target');
    }

    // ---- second recording without a reload ----
    await page.locator('.record-button').click();
    await armWaveformProbe(page);
    await expect(state).toHaveText('Recording', { timeout: 15_000 });
    await sleep(600);
    const probe = await readProbe(page);
    const beforeRecording = probe.filter((p) => p.status !== 'Recording');
    const paintedEarly = beforeRecording.filter((p) => p.painted > 40);
    note(info, `second entry: ${probe.length} frames sampled; frames before "Recording": ${beforeRecording.length}; of those with a painted waveform: ${paintedEarly.length}; first frames: ${JSON.stringify(probe.slice(0, 6))}`);
    note(info, `second entry timer at first frame: ${JSON.stringify(probe[0]?.timer)}; status at first frame: ${JSON.stringify(probe[0]?.status)}`);
    await sleep(4_000);
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(state).toHaveText('Ready to send', { timeout: 15_000 });
    const sent2 = Date.now();
    await page.getByRole('button', { name: 'Send' }).click();
    await page.waitForURL((u) => !u.pathname.endsWith('/capture'), { timeout: 60_000 });
    const row2 = page.locator('.filing-row').first();
    let status2 = '';
    const deadline2 = Date.now() + 120_000;
    while (Date.now() < deadline2) {
      status2 = (await row2.getAttribute('data-status').catch(() => null)) ?? '';
      if (['appended', 'needs_target', 'no_content', 'failed', 'spend_capped'].includes(status2)) break;
      await sleep(250);
    }
    note(info, `second recording: Send → "${status2}" in ${Date.now() - sent2} ms; filing rows visible: ${await page.locator('.filing-row').count()}; text: ${JSON.stringify((await text(page)).slice(0, 260))}`);
    await shot(page, info, 'rec-07-second-filed');
    const errors = rec.console.filter((l) => l.startsWith('[error]') || l.startsWith('[pageerror]'));
    note(info, `console errors during record flow: ${errors.length} ${JSON.stringify(errors.slice(0, 5))}`);
    rec.dump(info, 'record');
  });

  test('cancel and discard paths', async ({ page }, info) => {
    await page.goto('./capture');
    const state = page.locator('.capture__state');
    await expect(state).toHaveText('Recording', { timeout: 15_000 });
    note(info, `direct /capture load (PWA shortcut): tab bar count ${await page.locator('.tab-bar').count()}, controls: ${(await page.locator('.capture__control').allInnerTexts()).join('/')}`);
    await page.getByRole('button', { name: 'Pause' }).click();
    await expect(state).toHaveText('Paused');
    await shot(page, info, 'rec-08-paused');
    await page.getByRole('button', { name: 'Resume' }).click();
    await expect(state).toHaveText('Recording');
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(state).toHaveText('Ready to send');
    await page.getByRole('button', { name: 'Discard' }).click();
    await sleep(1_000);
    note(info, `after Discard: URL ${page.url()}; history length ${await page.evaluate(() => window.history.length)}`);
    await libraryReady(page);
    // Back from the library after a discard: where does it go?
    await page.goBack().catch(() => undefined);
    await sleep(800);
    note(info, `Back after discard → ${page.url()}`);
  });
});
