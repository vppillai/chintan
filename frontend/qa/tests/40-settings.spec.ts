import { expect, test } from '@playwright/test';

import { note, record, shot, sleep, text } from './helpers.ts';

test('settings ("You") screen', async ({ page }, info) => {
  const rec = record(page);
  await page.goto('./settings');
  await page.getByRole('heading', { name: 'You' }).waitFor();
  await sleep(1_500);
  await shot(page, info, 'you-01', true);
  const body = await text(page);
  note(info, `settings text: ${JSON.stringify(body)}`);
  const version = await page.locator('.version-footnote code').innerText();
  note(info, `version footnote: ${JSON.stringify(version)}; footnote full: ${JSON.stringify(await page.locator('.version-footnote').innerText())}`);
  note(info, `status line: ${JSON.stringify(await page.locator('.settings-status__text').innerText())}`);

  // Theme: applied immediately, and part of the saved draft?
  await page.getByRole('button', { name: /Nocturne/ }).click();
  await sleep(500);
  const theme = await page.evaluate(() => ({
    dataTheme: document.documentElement.getAttribute('data-theme'),
    colorScheme: getComputedStyle(document.documentElement).colorScheme,
    bg: getComputedStyle(document.body).backgroundColor,
  }));
  note(info, `after Nocturne: ${JSON.stringify(theme)}; status ${JSON.stringify(await page.locator('.settings-status__text').innerText())}; note ${JSON.stringify(await page.locator('.settings-group__note').first().innerText())}`);
  await shot(page, info, 'you-02-nocturne', true);
  // Leave without saving: does the theme survive a reload?
  await page.reload();
  await page.getByRole('heading', { name: 'You' }).waitFor();
  await sleep(1_500);
  note(info, `after reload without Save: theme pressed = ${JSON.stringify(await page.locator('.option[aria-pressed="true"]').first().innerText())}; status ${JSON.stringify(await page.locator('.settings-status__text').innerText())}`);
  await page.getByRole('button', { name: /Ink/ }).click();
  await sleep(300);
  const dirty = await page.locator('.settings-status__text').innerText();
  note(info, `back to Ink: status ${JSON.stringify(dirty)}`);
  if (dirty !== 'All changes saved') {
    await page.getByRole('button', { name: 'Save', exact: true }).click();
    await sleep(1_500);
  }
  note(info, `PUT /v1/settings so far: ${rec.apiCalls('PUT', '/v1/settings').map((c) => c.status).join(',')}`);

  // Retention: change, see the note change, discard.
  const retention = page.getByLabel('Days to keep source audio');
  await retention.fill('30');
  await sleep(300);
  note(info, `retention 30 → ${JSON.stringify(await page.locator('.settings-group').nth(2).locator('.settings-group__note').innerText())}; status ${JSON.stringify(await page.locator('.settings-status__text').innerText())}`);
  await shot(page, info, 'you-03-dirty', true);
  await page.getByRole('button', { name: 'Discard', exact: true }).click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await page.getByRole('dialog').getByRole('button', { name: 'Discard changes' }).click();
  await sleep(300);
  note(info, `after discard: retention ${await retention.inputValue()}; status ${JSON.stringify(await page.locator('.settings-status__text').innerText())}`);
  // Navigate away dirty: is there any warning?
  await retention.fill('7');
  note(info, `tab bar links on You: ${(await page.locator('.tab-bar__tab').allInnerTexts()).join(' | ')}`);
  await page.locator('.tab-bar__tab').first().click();
  await sleep(800);
  note(info, `navigated away with unsaved settings → ${page.url()}; dialog shown: ${await page.getByRole('dialog').count()}`);
  await page.goto('./settings');
  await sleep(1_500);
  note(info, `back on You: retention now ${await page.getByLabel('Days to keep source audio').inputValue()} (edit silently lost if 0)`);
  rec.dump(info, 'settings');
});
