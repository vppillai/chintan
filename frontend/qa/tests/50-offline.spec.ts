import { expect, test } from '@playwright/test';

import { listCaptures } from './api.ts';
import { libraryReady, note, record, shot, sleep, text } from './helpers.ts';

test('offline: library, note, recording, and reconnect', async ({ page }, info) => {
  const rec = record(page);
  // Warm up: library, and one note opened so it is cached.
  await page.goto('./');
  await libraryReady(page);
  await page.locator('.note-row').filter({ hasText: 'staging smoke' }).first().click();
  await page.locator('#note-body').waitFor();
  await sleep(1_500);
  await page.getByRole('button', { name: /Back to Notes/ }).click();
  await libraryReady(page);
  const sw = await page.evaluate(async () => {
    const regs = await navigator.serviceWorker.getRegistrations();
    return regs.map((r) => ({ scope: r.scope, active: Boolean(r.active), state: r.active?.state }));
  });
  note(info, `service worker: ${JSON.stringify(sw)}`);

  await page.context().setOffline(true);
  await sleep(1_500);
  note(info, `offline (no reload): banner ${JSON.stringify(await page.locator('.offline-banner').innerText().catch(() => '(none)'))}`);
  await shot(page, info, 'off-01-library-offline');
  // The URL the app itself left us on after "Back to Notes" — note whether it has the trailing slash.
  note(info, `URL before offline reload: ${page.url()}`);
  await page.reload().catch((e: Error) => note(info, `reload offline threw: ${e.message.split('\n')[0]}`));
  await sleep(2_500);
  note(info, `after offline reload: URL ${page.url()}; text ${JSON.stringify((await text(page).catch(() => '(no body)')).slice(0, 400))}`);
  await shot(page, info, 'off-02-library-offline-reload');
  if ((await page.locator('.note-row').count()) === 0) {
    // Try again at the canonical start_url, with the trailing slash.
    await page.goto('./').catch((e: Error) => note(info, `goto ./ offline threw: ${e.message.split('\n')[0]}`));
    await sleep(2_500);
    note(info, `offline load of the start_url (${page.url()}): ${JSON.stringify((await text(page).catch(() => '(no body)')).slice(0, 300))}`);
    await shot(page, info, 'off-02b-start-url-offline');
  }
  const rowCount = await page.locator('.note-row').count();
  note(info, `rows shown offline: ${rowCount}`);

  // Open the cached note, then one never opened.
  if (rowCount > 0) {
    await page.locator('.note-row').filter({ hasText: 'staging smoke' }).first().click();
    await sleep(1_500);
    note(info, `cached note offline: ${JSON.stringify((await text(page)).slice(0, 300))}; recordings body: ${JSON.stringify(await page.locator('.recording__body').innerText().catch(() => '(none)'))}`);
    await shot(page, info, 'off-03-note-cached');
    // Edit offline.
    await page.locator('#note-body').click();
    await page.locator('#note-body').press('End');
    await page.locator('#note-body').pressSequentially(' Offline edit.', { delay: 50 });
    await sleep(2_500);
    note(info, `offline edit indicator: ${JSON.stringify(await page.locator('.save-indicator').innerText().catch(() => ''))}; banner ${JSON.stringify(await page.locator('.offline-banner').innerText().catch(() => ''))}`);
    await shot(page, info, 'off-04-note-offline-edit');
    await page.getByRole('button', { name: /Back to Notes/ }).click();
    await libraryReady(page);
    await page.locator('.note-row').filter({ hasText: 'Piano lessons' }).first().click();
    await sleep(1_500);
    note(info, `never-opened note offline: ${JSON.stringify((await text(page)).slice(0, 300))}`);
    await shot(page, info, 'off-05-note-uncached');
    await page.getByRole('button', { name: /Back to Notes/ }).click();
    await libraryReady(page);
  }

  // The URL most in-app navigations produce has no trailing slash; is it served offline?
  await page.goto('https://vppillai.github.io/chintan/dev?view=archived').catch((e: Error) => note(info, `offline goto /chintan/dev?view=archived threw: ${e.message.split('\n')[0]}`));
  await sleep(1_500);
  note(info, `offline at /chintan/dev?view=archived (no trailing slash): URL ${page.url()}; text ${JSON.stringify((await text(page).catch(() => '(no body)')).slice(0, 200))}`);
  await shot(page, info, 'off-05b-no-trailing-slash-offline');
  await page.goto('./').catch(() => undefined);
  await libraryReady(page).catch(() => undefined);

  // Record offline.
  const before = (await listCaptures(page).catch(() => [])).length;
  await page.locator('.record-button').click();
  const state = page.locator('.capture__state');
  await expect(state).toHaveText('Recording', { timeout: 15_000 });
  note(info, `offline capture: pill ${JSON.stringify(await page.locator('.target-chooser__pill').innerText())}`);
  await sleep(4_000);
  await page.getByRole('button', { name: 'Stop' }).click();
  await expect(state).toHaveText('Ready to send', { timeout: 15_000 });
  await page.getByRole('button', { name: 'Send' }).click();
  await sleep(6_000);
  note(info, `offline Send → state ${JSON.stringify(await state.innerText().catch(() => ''))}; URL ${page.url()}; text ${JSON.stringify((await text(page)).slice(0, 400))}`);
  await shot(page, info, 'off-06-send-offline');
  if (page.url().includes('/capture')) {
    const done = page.getByRole('button', { name: /Done|Close|Discard/ }).first();
    if (await done.isVisible().catch(() => false)) {
      note(info, `leaving capture via ${await done.innerText()}`);
      if ((await done.innerText()) !== 'Discard') await done.click();
      else await page.goto('./');
    }
  }
  await sleep(1_000);
  note(info, `library while offline after the attempt: ${JSON.stringify((await text(page)).slice(0, 400))}`);
  await shot(page, info, 'off-07-library-after-offline-send');

  // Back online.
  await page.context().setOffline(false);
  const t0 = Date.now();
  let uploaded = false;
  while (Date.now() - t0 < 60_000) {
    const now = (await listCaptures(page).catch(() => [])).length;
    if (now > before) {
      uploaded = true;
      break;
    }
    await sleep(2_000);
  }
  note(info, `back online: capture reached the server on its own within 60 s: ${uploaded} (${Date.now() - t0} ms); screen ${JSON.stringify((await text(page)).slice(0, 400))}`);
  await shot(page, info, 'off-08-back-online');
  if (!uploaded) {
    const send = page.getByRole('button', { name: 'Send', exact: true }).first();
    if (await send.isVisible().catch(() => false)) {
      await send.click();
      await sleep(8_000);
      note(info, `after manual Send on the resume prompt: ${JSON.stringify((await text(page)).slice(0, 300))}; captures now ${(await listCaptures(page)).length} (was ${before})`);
      await shot(page, info, 'off-09-resume-sent');
    }
  }
  // Did the offline edit sync?
  await page.reload();
  await libraryReady(page);
  note(info, `banner after reload online: ${JSON.stringify(await page.locator('.offline-banner').innerText().catch(() => '(none)'))}`);
  await page.locator('.note-row').filter({ hasText: 'staging smoke' }).first().click();
  await page.locator('#note-body').waitFor();
  await sleep(1_500);
  note(info, `staging smoke body contains "Offline edit.": ${(await page.locator('#note-body').inputValue()).includes('Offline edit.')}`);
  rec.dump(info, 'offline');
});
