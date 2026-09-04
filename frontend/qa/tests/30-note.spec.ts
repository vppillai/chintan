import { expect, test, type Page } from '@playwright/test';

import { createNote, getNote, listNotes } from './api.ts';
import { libraryReady, note, record, shot, sleep, text } from './helpers.ts';

async function openNote(page: Page, title: string): Promise<void> {
  await page.goto('./');
  await libraryReady(page);
  await page.locator('.note-row').filter({ has: page.locator('.note-row__title', { hasText: title }) }).first().click();
  await expect(page).toHaveURL(/\/notes\//);
  await page.locator('#note-body').waitFor();
}

test.describe('note screen', () => {
  test('recordings: transcript, play, copy transcript, download audio', async ({ page }, info) => {
    const rec = record(page);
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    await openNote(page, 'staging smoke');
    await sleep(2_500);
    await shot(page, info, 'note-01-open', true);
    note(info, `meta: ${JSON.stringify(await page.locator('.note-meta').innerText())}`);
    const rows = page.locator('.recording');
    note(info, `recording rows: ${await rows.count()}; first summary: ${JSON.stringify(await page.locator('.recording__summary').first().innerText())}; expanded by default: ${await rows.first().getAttribute('data-expanded')}`);
    const body = rows.first().locator('.recording__body');
    await expect(body).toBeVisible();
    await sleep(1_500);
    const lines = await page.locator('.transcript__line').count();
    const empty = await page.locator('.transcript__empty').innerText().catch(() => '');
    const toggle = await page.locator('.transcript__toggle').count();
    note(info, `transcript lines: ${lines}; empty-state text: ${JSON.stringify(empty)}; Timestamped/Cleaned toggle present: ${toggle}; scrubber: waveform=${await page.locator('.waveform-scrubber, .scrubber').count()} range=${await page.locator('.player__range').count()}`);
    note(info, `artifact requests: ${rec.apiCalls('GET', '/download').map((c) => `${c.status} kind=${c.url.split('kind=')[1]}`).join(', ')}; S3 fetches: ${rec.requests.filter((r) => r.url.includes('s3') || r.url.includes('amazonaws.com/') && !r.url.includes('execute-api')).map((r) => `${r.status} ${r.type} ${r.size}B`).join(', ')}`);

    // Play.
    const play = rows.first().locator('.recording__play');
    note(info, `play button label: ${await play.getAttribute('aria-label')}, disabled: ${await play.isDisabled()}`);
    await play.click();
    await sleep(2_500);
    const audio = await page.evaluate(() => {
      const a = document.querySelector('audio');
      return a ? { paused: a.paused, currentTime: a.currentTime, duration: a.duration, readyState: a.readyState, error: a.error?.code ?? null, src: a.currentSrc.slice(0, 80) } : null;
    });
    note(info, `after Play 2.5s: ${JSON.stringify(audio)}; button now: ${await play.getAttribute('aria-label')}; player error text: ${JSON.stringify(await page.locator('.player__error').innerText().catch(() => ''))}`);
    const active = await page.locator('.transcript__line[data-active]').innerText().catch(() => '');
    note(info, `active transcript line while playing: ${JSON.stringify(active)}`);
    await shot(page, info, 'note-02-playing');
    await play.click();

    // Tap a transcript line to seek.
    if (lines > 1) {
      await page.locator('.transcript__line').nth(1).click();
      await sleep(600);
      const after = await page.evaluate(() => ({ t: document.querySelector('audio')?.currentTime, paused: document.querySelector('audio')?.paused }));
      note(info, `tapped 2nd transcript line → ${JSON.stringify(after)}`);
      await page.evaluate(() => document.querySelector('audio')?.pause());
    }

    // Copy transcript.
    await page.evaluate(() => navigator.clipboard.writeText('SENTINEL'));
    const copy = page.locator('.transcript button', { hasText: /Copy/ }).first();
    note(info, `copy button label: ${JSON.stringify(await copy.innerText().catch(() => '(no Copy button in the transcript panel)'))}; transcript header: ${JSON.stringify(await page.locator('.transcript__header').innerText().catch(() => ''))}`);
    await copy.click();
    await sleep(500);
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    const result = await page.locator('.copy__result').innerText().catch(() => '');
    note(info, `copy transcript → feedback ${JSON.stringify(result)}; clipboard: ${JSON.stringify(clip.slice(0, 200))} (${clip.length} chars)`);
    await shot(page, info, 'note-03-copied');

    // Cleaned view, if offered.
    if (toggle > 0) {
      await page.getByRole('button', { name: 'Cleaned' }).click();
      await sleep(300);
      note(info, `cleaned view: ${JSON.stringify((await page.locator('.transcript__cleaned').innerText()).slice(0, 300))}; copy label now ${await page.getByRole('button', { name: /Copy/ }).first().innerText()}`);
      await shot(page, info, 'note-04-cleaned-view');
      await page.getByRole('button', { name: 'Timestamped' }).click();
    }

    // Download audio. Watch the inline feedback closely: it settles back to idle after 2.5 s.
    const consoleBefore = rec.console.length;
    const dl = page.waitForEvent('download', { timeout: 8_000 }).catch(() => null);
    await page.getByRole('button', { name: 'Download audio' }).click();
    const feedback: string[] = [];
    for (let i = 0; i < 40; i += 1) {
      const t = await page.evaluate(() => (document.querySelector('.download__result')?.textContent ?? '') + ' | ' + (document.querySelector('.player__actions button')?.textContent ?? ''));
      if (feedback[feedback.length - 1] !== t) feedback.push(t);
      await sleep(100);
    }
    const download = await dl;
    const path = download ? await download.path().catch(() => null) : null;
    const size = path ? (await import('node:fs')).statSync(path).size : -1;
    note(info, `Download audio → event ${download ? 'fired' : 'NOT fired'}; filename ${download?.suggestedFilename()}; ${size} bytes; feedback sequence ${JSON.stringify(feedback)}`);
    note(info, `console during download: ${JSON.stringify(rec.console.slice(consoleBefore).map((l) => l.slice(0, 220)))}`);
    await shot(page, info, 'note-04b-download-audio');
    rec.dump(info, 'note-recordings');
  });

  test('editing: how many PATCH requests does typing produce; tags; share', async ({ page }, info) => {
    const rec = record(page);
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    const seeded = await createNote(page, { title: `Edit me ${info.project.name}`, body: 'Original body.' });
    await openNote(page, seeded.title);
    // Watch the save indicator.
    await page.evaluate(() => {
      const w = window as unknown as { __saves: string[] };
      w.__saves = [];
      const push = (): void => {
        const t = document.querySelector('.save-indicator')?.textContent?.trim() ?? '';
        if (w.__saves[w.__saves.length - 1] !== t) w.__saves.push(t);
      };
      new MutationObserver(push).observe(document.body, { subtree: true, childList: true, characterData: true });
    });

    const patchesBefore = rec.apiCalls('PATCH').length;
    const bodyField = page.locator('#note-body');
    await bodyField.click();
    await bodyField.press('End');
    await bodyField.pressSequentially(' The quick brown fox jumps over the lazy dog while typing.', { delay: 70 });
    await sleep(3_000);
    const afterSentence = rec.apiCalls('PATCH').length - patchesBefore;
    note(info, `typed one 58-char sentence at 70 ms/key (~4 s) → ${afterSentence} PATCH request(s)`);
    await bodyField.pressSequentially(' Second sentence,', { delay: 70 });
    await sleep(1_600);
    await bodyField.pressSequentially(' after a pause.', { delay: 70 });
    await sleep(3_000);
    const afterSecond = rec.apiCalls('PATCH').length - patchesBefore;
    note(info, `two more bursts with a 1.6 s gap → ${afterSecond} PATCH total`);
    const title = page.locator('#note-title');
    await title.click();
    await title.press('End');
    await title.pressSequentially(' (edited)', { delay: 70 });
    await title.blur();
    await sleep(2_500);
    const afterTitle = rec.apiCalls('PATCH').length - patchesBefore;
    note(info, `title edit + blur → ${afterTitle} PATCH total; statuses: ${rec.apiCalls('PATCH').map((c) => c.status).join(',')}`);
    const saves = await page.evaluate(() => (window as unknown as { __saves: string[] }).__saves);
    note(info, `save indicator sequence: ${JSON.stringify(saves)}`);
    await shot(page, info, 'note-05-edited');
    const server = await getNote(page, seeded.id);
    note(info, `server after edits: title ${JSON.stringify(server['title'])}, version ${String(server['version'])}, body ${JSON.stringify(String(server['body']).slice(0, 160))}`);
    // Does the library row reflect it without a reload?
    await page.getByRole('button', { name: /Back to Notes/ }).click();
    await libraryReady(page);
    const row = page.locator('.note-row').filter({ hasText: 'Edit me' }).first();
    note(info, `library row after edit (no reload): ${JSON.stringify(await row.innerText())}`);

    // Tags and aliases.
    await row.click();
    await page.locator('#note-body').waitFor();
    await page.getByRole('button', { name: 'Tags' }).click();
    const tagInput = page.getByPlaceholder('Add a tag');
    await tagInput.fill('qa-tag');
    await tagInput.press('Enter');
    await sleep(2_000);
    note(info, `tag added → PATCH bodies: ${rec.apiCalls('PATCH').length - patchesBefore} total; meta ${JSON.stringify(await page.locator('.note-meta').innerText())}; chips ${JSON.stringify(await page.locator('.tag-list').first().innerText())}`);
    const alias = page.getByPlaceholder('Add another name');
    await alias.fill('edit alias');
    await alias.press('Enter');
    await sleep(2_000);
    await shot(page, info, 'note-06-tags-panel');
    const server2 = await getNote(page, seeded.id);
    note(info, `server tags ${JSON.stringify(server2['tags'])} aliases ${JSON.stringify(server2['aliases'])}`);
    // Remove the tag again via its chip.
    const chip = page.locator('.tag-list button').first();
    note(info, `tag chip accessible name: ${JSON.stringify(await chip.getAttribute('aria-label') ?? await chip.innerText())}`);

    // Share.
    await page.getByRole('button', { name: 'Share' }).click();
    note(info, `Share opens: ${JSON.stringify(await page.locator('.note-copy').innerText())}; Tags panel still open: ${await page.locator('.tag-editor__input').count()}`);
    await page.evaluate(() => navigator.clipboard.writeText('SENTINEL'));
    await page.getByRole('button', { name: 'Copy note' }).click();
    await sleep(500);
    note(info, `Copy note → clipboard ${JSON.stringify((await page.evaluate(() => navigator.clipboard.readText())).slice(0, 120))}; feedback ${JSON.stringify(await page.locator('.copy__result').innerText().catch(() => ''))}`);
    const dl = page.waitForEvent('download', { timeout: 15_000 }).catch(() => null);
    await page.getByRole('button', { name: 'Download note' }).click();
    const download = await dl;
    const path = download ? await download.path().catch(() => null) : null;
    const content = path ? (await import('node:fs')).readFileSync(path, 'utf8') : '';
    note(info, `Download note → ${download ? download.suggestedFilename() : 'NO download event'}; content ${JSON.stringify(content.slice(0, 120))}`);
    await shot(page, info, 'note-07-share-panel');

    // Body with 3000 chars: does the textarea grow or scroll inside?
    await page.locator('#note-body').fill('Line of text that goes on. '.repeat(120));
    await sleep(2_000);
    const ta = await page.locator('#note-body').evaluate((el) => ({ clientHeight: el.clientHeight, scrollHeight: el.scrollHeight, overflow: getComputedStyle(el).overflowY }));
    note(info, `3.2k-char body: textarea ${JSON.stringify(ta)}`);
    await shot(page, info, 'note-08-long-body', true);
    rec.dump(info, 'note-edit');
  });

  test('record into this note: does the filed text appear without a reload?', async ({ page }, info) => {
    const rec = record(page);
    const seeded = await createNote(page, { title: `Record target ${info.project.name}`, body: 'Existing first paragraph.' });
    await openNote(page, seeded.title);
    await page.getByRole('button', { name: 'Record into this' }).click();
    await expect(page).toHaveURL(/\/capture\?note=/);
    const state = page.locator('.capture__state');
    await expect(state).toHaveText('Recording', { timeout: 15_000 });
    const pill = await page.locator('.target-chooser__pill').innerText();
    note(info, `target pill reads: ${JSON.stringify(pill)}`);
    await shot(page, info, 'note-09-record-into');
    // Open the chooser to see what it offers.
    await page.locator('.target-chooser__pill').click();
    await sleep(400);
    const options = await page.locator('.target-chooser__option').allInnerTexts();
    note(info, `chooser options (${options.length}): ${options.slice(0, 6).join(' | ')}…; pressed: ${JSON.stringify(await page.locator('.target-chooser__option[aria-pressed="true"]').allInnerTexts())}`);
    await shot(page, info, 'note-10-target-chooser');
    await page.locator('.target-chooser__pill').click();
    await sleep(6_000);
    await page.getByRole('button', { name: 'Stop' }).click();
    await expect(state).toHaveText('Ready to send', { timeout: 15_000 });
    const sentAt = Date.now();
    await page.getByRole('button', { name: 'Send' }).click();
    await page.waitForURL((u) => !u.pathname.endsWith('/capture'), { timeout: 60_000 });
    note(info, `after Send the app went to: ${page.url()} (${Date.now() - sentAt} ms)`);
    // Go straight to the note while it is still filing, and watch the body.
    await page.locator('.note-row').filter({ hasText: seeded.title }).first().click();
    await page.locator('#note-body').waitFor();
    const initial = await page.locator('#note-body').inputValue();
    let changed = false;
    let bodyNow = initial;
    const deadline = Date.now() + 120_000;
    while (Date.now() < deadline) {
      bodyNow = await page.locator('#note-body').inputValue();
      if (bodyNow !== initial) {
        changed = true;
        break;
      }
      await sleep(500);
    }
    note(info, `on the note while filing: body changed without reload: ${changed} after ${Date.now() - sentAt} ms since Send; body now ${JSON.stringify(bodyNow.slice(0, 300))}`);
    note(info, `GET /v1/notes/<id> calls while waiting: ${rec.apiCalls('GET', seeded.id).length}; GET /v1/captures: ${rec.apiCalls('GET', '/v1/captures').length}`);
    const server = await getNote(page, seeded.id);
    note(info, `server body: ${JSON.stringify(String(server['body']).slice(0, 300))}; captures on note: ${(server['captures'] as unknown[] | undefined)?.length}`);
    note(info, `recordings section rows: ${await page.locator('.recording').count()}; meta ${JSON.stringify(await page.locator('.note-meta').innerText())}`);
    await shot(page, info, 'note-11-after-record-into', true);
    if (!changed) {
      await page.reload();
      await page.locator('#note-body').waitFor();
      note(info, `after reload body: ${JSON.stringify((await page.locator('#note-body').inputValue()).slice(0, 300))}; recordings rows ${await page.locator('.recording').count()}`);
      await shot(page, info, 'note-12-after-reload', true);
    }
    rec.dump(info, 'note-record-into');
  });

  test('archive, restore, delete forever from the note screen', async ({ page }, info) => {
    const rec = record(page);
    const seeded = await createNote(page, { title: `Archive me ${info.project.name}`, body: 'to be archived' });
    await openNote(page, seeded.title);
    await page.getByRole('button', { name: 'Archive', exact: true }).click();
    const dialog = page.getByRole('dialog');
    note(info, `archive dialog: ${JSON.stringify(await dialog.innerText())}`);
    await dialog.getByRole('button', { name: 'Archive it' }).click();
    await sleep(1_500);
    note(info, `after Archive it → ${page.url()}; chips ${(await page.locator('.chip').allInnerTexts()).join(' | ')}`);
    await page.getByRole('button', { name: /^Archived/ }).click();
    await sleep(1_000);
    await page.locator('.note-row').filter({ hasText: seeded.title }).first().click();
    await page.locator('#note-body').waitFor();
    note(info, `archived note screen: state ${JSON.stringify(await page.locator('.note-actions__state').innerText().catch(() => ''))}; actions ${(await page.locator('.note-bar__action').allInnerTexts()).join(' / ')}; Record into this present: ${await page.getByRole('button', { name: 'Record into this' }).count()}`);
    note(info, `can the archived note still be edited? title readonly=${await page.locator('#note-title').getAttribute('readonly')} disabled=${await page.locator('#note-title').isDisabled()}`);
    await shot(page, info, 'note-13-archived');
    await page.getByRole('button', { name: 'Delete forever' }).click();
    const gate = page.getByRole('dialog');
    const confirm = gate.getByRole('button', { name: 'Delete forever' });
    note(info, `purge dialog: ${JSON.stringify(await gate.innerText())}; confirm disabled before typing: ${await confirm.isDisabled()}`);
    await gate.locator('input').fill(seeded.title.toLowerCase());
    note(info, `after typing the title in lower case: confirm disabled ${await confirm.isDisabled()}`);
    await shot(page, info, 'note-14-purge-dialog');
    await confirm.click();
    await sleep(2_000);
    note(info, `after purge → ${page.url()}; ${JSON.stringify((await text(page)).slice(0, 200))}; requests ${rec.apiCalls('DELETE').map((c) => `${c.status} ${c.url.split('/v1')[1]}`).join(' ; ')}`);
    const gone = (await listNotes(page, 'archived')).some((n) => n.id === seeded.id);
    note(info, `note still in archive list: ${gone}`);
    // Not found path.
    await page.goto(`./notes/${seeded.id}`);
    await sleep(2_000);
    note(info, `visiting the purged note's URL: ${JSON.stringify((await text(page)).slice(0, 200))}`);
    await shot(page, info, 'note-15-not-found');
  });
});
