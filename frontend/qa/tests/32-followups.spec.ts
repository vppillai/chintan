import { expect, test } from '@playwright/test';

import { createNote, getNote } from './api.ts';
import { libraryReady, note, record, shot, sleep, text } from './helpers.ts';

test.describe('follow-ups', () => {
  test('edit, back, re-open, add a tag: which version does the PATCH carry?', async ({ page }, info) => {
    const rec = record(page);
    const seeded = await createNote(page, { title: `Version check ${info.project.name}`, body: 'v1 body' });
    await page.goto('./');
    await libraryReady(page);
    await page.locator('.note-row').filter({ hasText: seeded.title }).first().click();
    const body = page.locator('#note-body');
    await body.click();
    await body.press('End');
    await body.pressSequentially(' more words here.', { delay: 60 });
    await expect(page.locator('.save-indicator')).toHaveText('Saved', { timeout: 10_000 });
    await page.getByRole('button', { name: /Back to Notes/ }).click();
    await libraryReady(page);
    await page.locator('.note-row').filter({ hasText: seeded.title }).first().click();
    await page.locator('#note-body').waitFor();
    await sleep(500);
    note(info, `re-opened: body reads ${JSON.stringify(await page.locator('#note-body').inputValue())}`);
    await page.getByRole('button', { name: 'Tags' }).click();
    const tagInput = page.getByPlaceholder('Add a tag');
    await tagInput.fill('vtag');
    await tagInput.press('Enter');
    await sleep(3_500);
    note(info, `PATCHes: ${rec.apiCalls('PATCH').map((p) => `${p.status} ${p.body ?? ''}`).join(' || ')}`);
    note(info, `save indicator: ${JSON.stringify(await page.locator('.save-indicator').innerText().catch(() => ''))}; conflict panel: ${JSON.stringify(await page.locator('.save-conflict').innerText().catch(() => '(none)'))}`);
    await shot(page, info, 'follow-01-after-tag');
    const server = await getNote(page, seeded.id);
    note(info, `server: v${String(server['version'])} tags ${JSON.stringify(server['tags'])} body ${JSON.stringify(server['body'])}`);
    rec.dump(info, 'follow-version');
  });

  test('open the note while it is still filing, then wait, then reload', async ({ page }, info) => {
    const rec = record(page);
    const seeded = await createNote(page, { title: `Mid-filing ${info.project.name}`, body: 'Only paragraph.' });
    await page.goto(`./notes/${seeded.id}`);
    await page.locator('#note-body').waitFor();
    await page.getByRole('button', { name: 'Record into this' }).click();
    const state = page.locator('.capture__state');
    await expect(state).toHaveText('Recording', { timeout: 15_000 });
    await sleep(5_000);
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(state).toHaveText('Ready to send', { timeout: 15_000 });
    const sentAt = Date.now();
    await page.getByRole('button', { name: 'Send' }).click();
    await page.waitForURL((u) => !u.pathname.endsWith('/capture'), { timeout: 60_000 });
    await page.locator('.note-row').filter({ hasText: seeded.title }).first().click();
    await page.locator('#note-body').waitFor();
    note(info, `on the note ${Date.now() - sentAt} ms after Send; body ${JSON.stringify(await page.locator('#note-body').inputValue())}`);
    // Wait for the server to finish.
    let server = await getNote(page, seeded.id);
    const deadline = Date.now() + 90_000;
    while (Date.now() < deadline && Number(server['version']) < 2) {
      await sleep(1_000);
      server = await getNote(page, seeded.id);
    }
    note(info, `server v${String(server['version'])} at ${Date.now() - sentAt} ms: ${JSON.stringify(String(server['body']).slice(0, 120))}`);
    await sleep(10_000);
    note(info, `10 s later on the note, no reload: body ${JSON.stringify((await page.locator('#note-body').inputValue()).slice(0, 120))}; recordings ${await page.locator('.recording').count()}; GET note calls ${rec.apiCalls('GET', seeded.id).length}`);
    await shot(page, info, 'follow-02-note-mid-filing');
    const before = rec.apiCalls('GET', seeded.id).length;
    await page.reload();
    await page.locator('#note-body').waitFor();
    await sleep(3_000);
    const after = rec.apiCalls('GET', seeded.id).slice(before);
    note(info, `after reload: GET note ${after.map((c) => `${c.status} sw=${String(c.fromSw)} ${c.size}B`).join(',')}; body ${JSON.stringify((await page.locator('#note-body').inputValue()).slice(0, 120))}; recordings ${await page.locator('.recording').count()}; meta ${JSON.stringify(await page.locator('.note-meta').innerText())}`);
    await shot(page, info, 'follow-03-note-after-reload');
    // Blur/focus: does a window focus refetch bring it in?
    await page.evaluate(() => { document.dispatchEvent(new Event('visibilitychange')); window.dispatchEvent(new Event('focus')); });
    await sleep(3_000);
    note(info, `after a focus event: body ${JSON.stringify((await page.locator('#note-body').inputValue()).slice(0, 120))}; recordings ${await page.locator('.recording').count()}; GET note calls total ${rec.apiCalls('GET', seeded.id).length}`);
    rec.dump(info, 'follow-midfiling');
  });

  test('cancel timing: does a cancelled recording ever come back as unsent?', async ({ page }, info) => {
    const results: string[] = [];
    for (const delay of [300, 1_000, 2_000, 3_500]) {
      await page.goto('./');
      await libraryReady(page);
      for (const d of await page.locator('.resume-prompt').getByRole('button', { name: 'Discard' }).all()) {
        await d.click();
        await page.getByRole('dialog').getByRole('button', { name: 'Discard recording' }).click();
        await sleep(500);
      }
      await page.locator('.record-button').click();
      await expect(page.locator('.capture__state')).toHaveText('Recording', { timeout: 15_000 });
      await sleep(delay);
      await page.getByRole('button', { name: 'Cancel' }).click();
      await libraryReady(page);
      await sleep(1_500);
      const now = await page.locator('.resume-prompt').innerText().catch(() => '(none)');
      await page.reload();
      await libraryReady(page);
      await sleep(1_500);
      const afterReload = await page.locator('.resume-prompt').innerText().catch(() => '(none)');
      const stores = await page.evaluate(async () => {
        const db = await new Promise<IDBDatabase>((res, rej) => { const r = indexedDB.open('chintan'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
        const out: Record<string, number> = {};
        for (const store of Array.from(db.objectStoreNames)) {
          out[store] = await new Promise<number>((res) => { const c = db.transaction(store).objectStore(store).count(); c.onsuccess = () => res(c.result); });
        }
        db.close();
        return out;
      });
      results.push(`cancel after ${delay} ms → prompt now ${JSON.stringify(now.split('\n')[0])}, after reload ${JSON.stringify(afterReload.split('\n')[0])}, idb ${JSON.stringify(stores)}`);
      if (afterReload !== '(none)') await shot(page, info, `follow-04-cancel-${delay}`);
    }
    note(info, results.join('\n      '));
    note(info, `library head: ${JSON.stringify((await text(page)).slice(0, 200))}`);
  });
});
