import { expect, test } from '@playwright/test';

import { createNote, getNote } from './api.ts';
import { libraryReady, note, record, shot, sleep, text } from './helpers.ts';

/** Follow-ups on what the first note pass turned up, with request bodies on record. */
test.describe('note follow-ups', () => {
  test('tags and aliases: what the PATCH carries and what the server keeps', async ({ page }, info) => {
    const rec = record(page);
    const seeded = await createNote(page, { title: `Tag me ${info.project.name}`, body: 'tag body' });
    await page.goto(`./notes/${seeded.id}`);
    await page.locator('#note-body').waitFor();
    await page.getByRole('button', { name: 'Tags' }).click();
    const tagInput = page.getByPlaceholder('Add a tag');
    await tagInput.fill('qa-tag');
    await tagInput.press('Enter');
    await sleep(3_000);
    const alias = page.getByPlaceholder('Add another name');
    await alias.fill('nickname');
    await alias.press('Enter');
    await sleep(3_000);
    const patches = rec.apiCalls('PATCH');
    note(info, `PATCH requests: ${patches.map((p) => `${p.status} body=${p.body ?? ''}`).join(' || ')}`);
    note(info, `panel now: ${JSON.stringify(await page.locator('.note-panel').innerText())}; save indicator ${JSON.stringify(await page.locator('.save-indicator').innerText().catch(() => ''))}`);
    const server = await getNote(page, seeded.id);
    note(info, `server: tags ${JSON.stringify(server['tags'])} aliases ${JSON.stringify(server['aliases'])} version ${String(server['version'])}`);
    await page.reload();
    await page.locator('#note-body').waitFor();
    await sleep(1_500);
    note(info, `after reload: meta ${JSON.stringify(await page.locator('.note-meta').innerText())}`);
    await page.getByRole('button', { name: 'Tags' }).click();
    note(info, `after reload, Tags panel: ${JSON.stringify(await page.locator('.note-panel').innerText())}`);
    await shot(page, info, 'notedetail-01-tags-after-reload');
    await page.goto('./');
    await libraryReady(page);
    note(info, `library chips: ${(await page.locator('.chip').allInnerTexts()).join(' | ')}`);
    rec.dump(info, 'notedetail-tags');
  });

  test('record into this, then read the note: no reload, then reload', async ({ page }, info) => {
    const rec = record(page);
    const seeded = await createNote(page, { title: `Stale check ${info.project.name}`, body: 'Existing first paragraph.' });
    await page.goto(`./notes/${seeded.id}`);
    await page.locator('#note-body').waitFor();
    await sleep(1_000);
    await page.getByRole('button', { name: 'Record into this' }).click();
    const state = page.locator('.capture__state');
    await expect(state).toHaveText('Recording', { timeout: 15_000 });
    await sleep(5_000);
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(state).toHaveText('Ready to send', { timeout: 15_000 });
    const sentAt = Date.now();
    await page.getByRole('button', { name: 'Send' }).click();
    await page.waitForURL((u) => !u.pathname.endsWith('/capture'), { timeout: 60_000 });
    // Wait on the library until the row for this capture is terminal.
    const deadline = Date.now() + 90_000;
    let rows = '';
    while (Date.now() < deadline) {
      rows = await page.evaluate(() => Array.from(document.querySelectorAll('.filing-row')).map((r) => `${r.getAttribute('data-status')}`).join(','));
      if (rows && !/uploaded|transcribing|routing|cleaning|appending/.test(rows)) break;
      await sleep(300);
    }
    note(info, `filing rows terminal after ${Date.now() - sentAt} ms: ${rows}`);
    const serverNow = await getNote(page, seeded.id);
    note(info, `server body now (v${String(serverNow['version'])}): ${JSON.stringify(String(serverNow['body']).slice(0, 200))}`);
    // The library row for this note — updated?
    const row = page.locator('.note-row').filter({ hasText: seeded.title }).first();
    note(info, `library row (no reload): ${JSON.stringify(await row.innerText())}`);
    // Open the note by its row (not the filing row).
    const getsBefore = rec.apiCalls('GET', seeded.id).length;
    await row.click();
    await page.locator('#note-body').waitFor();
    await sleep(3_000);
    note(info, `opened by row: GET note calls ${rec.apiCalls('GET', seeded.id).length - getsBefore} (${rec.apiCalls('GET', seeded.id).slice(getsBefore).map((c) => `${c.status} sw=${String(c.fromSw)}`).join(',')}); body ${JSON.stringify((await page.locator('#note-body').inputValue()).slice(0, 200))}; recordings rows ${await page.locator('.recording').count()}; meta ${JSON.stringify(await page.locator('.note-meta').innerText())}`);
    await shot(page, info, 'notedetail-02-note-after-record-into', true);
    await page.reload();
    await page.locator('#note-body').waitFor();
    await sleep(3_000);
    note(info, `after reload: GET note ${rec.apiCalls('GET', seeded.id).slice(-1).map((c) => `${c.status} sw=${String(c.fromSw)} ${c.size}B`).join(',')}; body ${JSON.stringify((await page.locator('#note-body').inputValue()).slice(0, 200))}; recordings rows ${await page.locator('.recording').count()}; meta ${JSON.stringify(await page.locator('.note-meta').innerText())}`);
    await shot(page, info, 'notedetail-03-note-after-reload', true);
    // Open via the filing row's button instead.
    await page.goto('./');
    await libraryReady(page);
    const open = page.locator('.filing-row').filter({ hasText: 'Open the note' }).first().getByRole('button', { name: 'Open the note' });
    if (await open.isVisible().catch(() => false)) {
      await open.click();
      await page.locator('#note-body').waitFor();
      await sleep(2_000);
      note(info, `opened via "Open the note": ${JSON.stringify(await page.locator('#note-title').inputValue())} body ${JSON.stringify((await page.locator('#note-body').inputValue()).slice(0, 200))}; recordings ${await page.locator('.recording').count()}`);
    }
    rec.dump(info, 'notedetail-stale');
  });

  test('cancel while recording: is anything offered back as unsent?', async ({ page }, info) => {
    await page.goto('./');
    await libraryReady(page);
    note(info, `resume prompt before: ${await page.locator('.resume-prompt').count()}`);
    await page.locator('.record-button').click();
    await expect(page.locator('.capture__state')).toHaveText('Recording', { timeout: 15_000 });
    await sleep(3_000);
    await page.getByRole('button', { name: 'Cancel' }).click();
    await libraryReady(page);
    await sleep(1_500);
    note(info, `after Cancel during recording: URL ${page.url()}; resume prompt ${JSON.stringify(await page.locator('.resume-prompt').innerText().catch(() => '(none)'))}`);
    await shot(page, info, 'notedetail-04-after-cancel');
    await page.reload();
    await libraryReady(page);
    await sleep(1_500);
    note(info, `after reload: resume prompt ${JSON.stringify(await page.locator('.resume-prompt').innerText().catch(() => '(none)'))}; IndexedDB stores: ${JSON.stringify(await page.evaluate(async () => {
      const dbs = await indexedDB.databases();
      const out: Record<string, number> = {};
      for (const d of dbs) {
        if (!d.name) continue;
        const db = await new Promise<IDBDatabase>((res, rej) => { const r = indexedDB.open(d.name!); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
        for (const store of Array.from(db.objectStoreNames)) {
          out[`${d.name}/${store}`] = await new Promise<number>((res) => { const c = db.transaction(store).objectStore(store).count(); c.onsuccess = () => res(c.result); });
        }
        db.close();
      }
      return out;
    }))}`);
    await shot(page, info, 'notedetail-05-after-cancel-reload');
    const discard = page.locator('.resume-prompt').getByRole('button', { name: 'Discard' });
    if (await discard.isVisible().catch(() => false)) {
      await discard.click();
      await page.getByRole('dialog').getByRole('button', { name: 'Discard recording' }).click();
      await sleep(800);
      note(info, `after discarding the leftover: resume prompt ${await page.locator('.resume-prompt').count()}`);
    }
  });

  test('library freshness after editing a title', async ({ page }, info) => {
    const rec = record(page);
    const seeded = await createNote(page, { title: `Rename me ${info.project.name}`, body: 'body' });
    await page.goto('./');
    await libraryReady(page);
    await page.locator('.note-row').filter({ hasText: seeded.title }).first().click();
    const title = page.locator('#note-title');
    await title.fill(`Renamed ${info.project.name}`);
    await title.blur();
    await expect(page.locator('.save-indicator')).toHaveText('Saved', { timeout: 10_000 });
    const listsBefore = rec.apiCalls('GET', '/v1/notes?').length;
    await page.getByRole('button', { name: /Back to Notes/ }).click();
    await libraryReady(page);
    await sleep(1_000);
    const titles = await page.locator('.note-row__title').allInnerTexts();
    note(info, `after rename + Back: list refetched ${rec.apiCalls('GET', '/v1/notes?').length - listsBefore} time(s); row shows ${JSON.stringify(titles.find((t) => t.includes('me ' + info.project.name) || t.includes('Renamed')))}`);
    await shot(page, info, 'notedetail-06-library-after-rename');
    note(info, `screen text head: ${JSON.stringify((await text(page)).slice(0, 200))}`);
  });
});
