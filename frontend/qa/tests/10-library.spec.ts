import { expect, test } from '@playwright/test';

import { createNote, listNotes } from './api.ts';
import { libraryReady, note, record, shot, sleep, text } from './helpers.ts';

test.describe('library', () => {
  test('list with 26 notes: pagination, scrolling, record button vs last row', async ({ page }, info) => {
    const rec = record(page);
    await page.goto('./');
    await libraryReady(page);
    await shot(page, info, 'lib-01-top');

    const rows = page.locator('.note-row');
    const count = await rows.count();
    const loadMore = page.locator('.load-more');
    note(info, `rows rendered: ${count}; "Load more" present: ${await loadMore.count()}`);
    const listCalls = rec.apiCalls('GET', '/v1/notes');
    note(info, `GET /v1/notes calls on first load: ${listCalls.map((c) => c.url.replace(/^.*execute-api[^/]*/, '')).join(' | ')}`);

    // Group headings and the meta line of the first row.
    const groups = await page.locator('.note-group__label').allInnerTexts();
    note(info, `day groups: ${groups.join(', ')}`);

    // Scroll to the bottom and see where the last row sits relative to the tab bar.
    const last = rows.last();
    await last.scrollIntoViewIfNeeded();
    await sleep(300);
    const lastBox = await last.boundingBox();
    const bar = await page.locator('.tab-bar').boundingBox();
    const recBtn = await page.locator('.record-button').boundingBox();
    const main = await page.locator('main').boundingBox();
    note(info, `last row box ${JSON.stringify(lastBox)}; tab bar ${JSON.stringify(bar)}; record ${JSON.stringify(recBtn)}; main ${JSON.stringify(main)}`);
    const overlaps = lastBox && bar ? lastBox.y + lastBox.height > bar.y + 1 : null;
    note(info, `last row overlaps tab bar: ${String(overlaps)}`);
    await shot(page, info, 'lib-02-bottom');
    const scroller = await page.evaluate(() => {
      const main = document.querySelector('main');
      return {
        mainScrollHeight: main?.scrollHeight,
        mainClientHeight: main?.clientHeight,
        mainOverflowY: main ? getComputedStyle(main).overflowY : null,
        docScrollHeight: document.documentElement.scrollHeight,
        innerHeight: window.innerHeight,
        docScrollY: window.scrollY,
      };
    });
    note(info, `scroll container: ${JSON.stringify(scroller)}`);
    // Where does the bulk bar land with a long list? Select from the top of the list.
    await page.evaluate(() => document.querySelector('main')?.scrollTo(0, 0));
    await page.getByRole('button', { name: 'Select', exact: true }).click();
    await sleep(300);
    const bulk = await page.locator('.bulk-bar').boundingBox();
    const vh = page.viewportSize()?.height ?? 0;
    note(info, `bulk bar after Select (list scrolled to top): box ${JSON.stringify(bulk)}; viewport height ${vh}; visible without scrolling: ${bulk ? bulk.y + bulk.height <= vh : 'n/a'}; position: ${await page.locator('.bulk-bar').evaluate((el) => getComputedStyle(el).position)}`);
    await shot(page, info, 'lib-02b-select-top');
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    rec.dump(info, 'lib-list');
  });

  test('search: a word that only occurs in a note body', async ({ page }, info) => {
    const rec = record(page);
    await page.goto('./');
    await libraryReady(page);
    const search = page.getByRole('searchbox', { name: 'Search notes' });
    await search.click();
    await search.pressSequentially('flashing', { delay: 60 });
    // Give the server search time to answer.
    await sleep(2_500);
    const countLine = await page.locator('.screen__count').first().innerText().catch(() => '');
    const titles = await page.locator('.note-row__title').allInnerTexts();
    const marks = await page.locator('mark.search-hit').allInnerTexts();
    note(info, `"flashing" → ${countLine}; rows: ${titles.join(' | ')}; highlighted: ${marks.join(',')}`);
    note(info, `URL after typing: ${page.url()}`);
    const searchCalls = rec.apiCalls('GET', '/v1/search');
    note(info, `GET /v1/search calls while typing 8 chars: ${searchCalls.length} → ${searchCalls.map((c) => decodeURIComponent(c.url.split('q=')[1] ?? '')).join(',')}`);
    await shot(page, info, 'lib-03-search-flashing');

    // Body-only word from a seeded note, not in the existing corpus snippet.
    await search.fill('');
    await sleep(400);
    note(info, `after clearing the field with fill(''): value ${JSON.stringify(await search.inputValue())}; URL ${page.url()}; count line ${JSON.stringify(await page.locator('.screen__count').first().innerText().catch(() => '(none)'))}`);
    if ((await search.inputValue()) !== '') {
      await search.press('ControlOrMeta+a');
      await search.press('Backspace');
      await sleep(400);
      note(info, `after select-all + Backspace: value ${JSON.stringify(await search.inputValue())}; URL ${page.url()}`);
    }
    await search.pressSequentially('zebra7', { delay: 60 });
    await sleep(2_500);
    const titles2 = await page.locator('.note-row__title').allInnerTexts();
    note(info, `"zebra7" → ${await page.locator('.screen__count').first().innerText().catch(() => '')}; rows: ${titles2.join(' | ')}`);
    await shot(page, info, 'lib-04-search-zebra7');

    // Title word, then a miss.
    await search.fill('Bike');
    await sleep(1_500);
    note(info, `"Bike" → ${await page.locator('.screen__count').first().innerText().catch(() => '')}; rows: ${(await page.locator('.note-row__title').allInnerTexts()).join(' | ')}`);
    await search.fill('xyzzyq');
    await sleep(2_000);
    note(info, `"xyzzyq" → ${(await text(page)).match(/Nothing matches[^\n]*/)?.[0] ?? '(no empty state)'}`);
    await shot(page, info, 'lib-05-search-miss');
    // Does the search field persist across a reload (it lives in the URL)?
    await page.reload();
    await libraryReady(page);
    note(info, `after reload with ?q= in URL the field reads: ${JSON.stringify(await search.inputValue())}`);
    rec.dump(info, 'lib-search');
  });

  test('tag chips and the Archived chip', async ({ page }, info) => {
    await page.goto('./');
    await libraryReady(page);
    const chips = await page.locator('.chip').allInnerTexts();
    note(info, `chips: ${chips.join(' | ')}`);
    await page.getByRole('button', { name: 'house', exact: true }).click();
    await sleep(1_200);
    const titles = await page.locator('.note-row__title').allInnerTexts();
    const tagSets = await page.locator('.note-row').evaluateAll((rows) =>
      rows.map((r) => Array.from(r.querySelectorAll('.note-row__tag')).map((t) => t.textContent)),
    );
    note(info, `house → ${titles.length} rows: ${titles.join(' | ')}; every row tagged house: ${tagSets.every((t) => t.includes('house'))}; URL ${page.url()}`);
    await shot(page, info, 'lib-06-tag-house');
    expect(tagSets.every((t) => t.includes('house'))).toBeTruthy();

    // Tag + search together.
    await page.getByRole('searchbox', { name: 'Search notes' }).fill('roof');
    await sleep(2_000);
    note(info, `house + "roof" → ${(await page.locator('.note-row__title').allInnerTexts()).join(' | ')} (${await page.locator('.screen__count').first().innerText().catch(() => '')})`);
    await page.getByRole('searchbox', { name: 'Search notes' }).fill('');
    await page.getByRole('button', { name: 'house', exact: true }).click();
    await sleep(600);

    await page.getByRole('button', { name: /^Archived/ }).click();
    await sleep(1_200);
    note(info, `Archived → URL ${page.url()}; body: ${JSON.stringify((await text(page)).slice(0, 300))}`);
    await shot(page, info, 'lib-07-archived-empty');
    await page.getByRole('button', { name: 'All', exact: true }).click();
    await sleep(600);
    expect(page.url()).not.toContain('view=');
  });

  test('bulk select → archive, restore, delete forever', async ({ page }, info) => {
    const rec = record(page);
    await page.goto('./');
    await libraryReady(page);
    const tag = `bulk${info.project.name}`;
    await createNote(page, { title: `Bulk A ${info.project.name}`, body: 'first of two', tags: [tag] });
    await createNote(page, { title: `Bulk B ${info.project.name}`, body: 'second of two', tags: [tag] });
    await page.reload();
    await libraryReady(page);
    await page.getByRole('button', { name: tag, exact: true }).click();
    await expect(page.locator('.note-row')).toHaveCount(2);

    await page.getByRole('button', { name: 'Select', exact: true }).click();
    await shot(page, info, 'lib-08-selecting');
    await page.getByRole('button', { name: 'Select all' }).click();
    note(info, `bulk bar: ${JSON.stringify(await page.locator('.bulk-bar').innerText())}`);
    await page.getByRole('button', { name: 'Archive', exact: true }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog).toBeVisible();
    note(info, `archive dialog: ${JSON.stringify(await dialog.innerText())}`);
    await shot(page, info, 'lib-09-archive-dialog');
    await dialog.getByRole('button', { name: 'Archive them' }).click();
    await sleep(2_000);
    note(info, `after archive: rows ${await page.locator('.note-row').count()}; chips ${(await page.locator('.chip').allInnerTexts()).join(' | ')}; selecting still on: ${await page.locator('.bulk-bar').count()}`);
    note(info, `archive requests: ${rec.apiCalls('DELETE').map((c) => `${c.status} ${c.url.split('/v1')[1]}`).join(' ; ')}`);
    await shot(page, info, 'lib-10-after-archive');

    // The archive: both notes there, with a purge countdown.
    await page.getByRole('button', { name: /^Archived/ }).click();
    await sleep(1_500);
    const archivedTitles = await page.locator('.note-row__title').allInnerTexts();
    const purge = await page.locator('.note-row__purge').allInnerTexts();
    note(info, `archive view rows: ${archivedTitles.join(' | ')}; purge labels: ${purge.join(' | ')}`);
    await shot(page, info, 'lib-11-archive-view');

    // Restore one, then delete the other forever with the typed gate.
    await page.getByRole('button', { name: 'Select', exact: true }).click();
    await page.locator('.note-row--selectable').filter({ hasText: 'Bulk A' }).locator('input').check();
    await page.getByRole('button', { name: 'Restore', exact: true }).click();
    await page.getByRole('dialog').getByRole('button', { name: 'Restore them' }).click();
    await sleep(2_000);
    note(info, `after restore: archive rows ${(await page.locator('.note-row__title').allInnerTexts()).join(' | ')}`);

    await page.getByRole('button', { name: 'Select', exact: true }).click();
    await page.getByRole('button', { name: 'Select all' }).click();
    await page.getByRole('button', { name: 'Delete forever' }).click();
    const gate = page.getByRole('dialog');
    await expect(gate).toBeVisible();
    const confirm = gate.getByRole('button', { name: 'Delete them forever' });
    note(info, `delete dialog: ${JSON.stringify(await gate.innerText())}; confirm disabled before typing: ${await confirm.isDisabled()}`);
    await gate.locator('input').fill('DELETE');
    note(info, `confirm disabled after typing "DELETE" (upper case): ${await confirm.isDisabled()}`);
    await shot(page, info, 'lib-12-delete-dialog');
    await confirm.click();
    await sleep(2_500);
    note(info, `after purge: ${JSON.stringify((await text(page)).slice(0, 400))}`);
    note(info, `purge requests: ${rec.apiCalls('POST', 'purge').concat(rec.apiCalls('DELETE', 'permanent')).map((c) => `${c.status} ${c.url.split('/v1')[1]}`).join(' ; ')}`);
    await shot(page, info, 'lib-13-after-purge');

    // Delete forever straight from the active list (archive + purge behind one gate).
    await page.getByRole('button', { name: 'All', exact: true }).click();
    await page.getByRole('button', { name: tag, exact: true }).click();
    await sleep(1_200);
    await page.getByRole('button', { name: 'Select', exact: true }).click();
    await page.getByRole('button', { name: 'Select all' }).click();
    await page.getByRole('button', { name: 'Delete forever' }).click();
    await page.getByRole('dialog').locator('input').fill('delete');
    await page.getByRole('dialog').getByRole('button', { name: 'Delete them forever' }).click();
    await sleep(2_500);
    const remaining = (await listNotes(page, 'active')).filter((n) => n.title.startsWith('Bulk '));
    const remainingArchived = (await listNotes(page, 'archived')).filter((n) => n.title.startsWith('Bulk '));
    note(info, `after active-list delete: active Bulk notes ${remaining.length}, archived ${remainingArchived.length}; screen: ${JSON.stringify((await text(page)).slice(0, 300))}; chips ${(await page.locator('.chip').allInnerTexts()).join(' | ')}`);
    await shot(page, info, 'lib-14-after-active-delete');
    rec.dump(info, 'lib-bulk');
  });

  test('long-press and pull-to-refresh on a row', async ({ page }, info) => {
    test.skip(!info.project.name.includes('mobile'), 'touch gestures are the phone story');
    const rec = record(page);
    await page.goto('./');
    await libraryReady(page);
    const row = page.locator('.note-row').first();
    const box = (await row.boundingBox())!;
    const cdp = await page.context().newCDPSession(page);
    const before = page.url();
    const x = box.x + box.width / 2;
    const y = box.y + box.height / 2;
    // Long-press: touch down, hold 900 ms, release.
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
    await sleep(900);
    await shot(page, info, 'lib-15-longpress-held');
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    await sleep(800);
    note(info, `long-press: URL before ${before} → after ${page.url()}; bulk bar present: ${await page.locator('.bulk-bar').count()}; selection mode: ${await page.locator('.note-row--selectable').count()}`);
    if (page.url() !== before) {
      await page.goBack();
      await libraryReady(page);
    }

    // Pull-to-refresh: a slow downward drag from the top of the list.
    const listCallsBefore = rec.apiCalls('GET', '/v1/notes').length;
    await page.evaluate(() => window.scrollTo(0, 0));
    const startY = (await page.locator('.note-list').first().boundingBox())!.y + 10;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: 200, y: startY }] });
    for (let step = 1; step <= 12; step += 1) {
      await cdp.send('Input.dispatchTouchEvent', {
        type: 'touchMove',
        touchPoints: [{ x: 200, y: startY + step * 30 }],
      });
      await sleep(40);
    }
    await shot(page, info, 'lib-16-pull-held');
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    await sleep(2_000);
    const listCallsAfter = rec.apiCalls('GET', '/v1/notes').length;
    note(info, `pull-to-refresh: GET /v1/notes before ${listCallsBefore} → after ${listCallsAfter}; any spinner/status text: ${JSON.stringify((await text(page)).slice(0, 160))}`);
    rec.dump(info, 'lib-gestures');
  });
});
