import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page, type TestInfo } from '@playwright/test';

import { libraryReady, note, sleep } from './helpers.ts';

async function audit(page: Page, info: TestInfo, name: string): Promise<void> {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa', 'best-practice']).analyze();
  const summary = results.violations.map((v) => ({
    id: v.id,
    impact: v.impact,
    nodes: v.nodes.length,
    targets: v.nodes.slice(0, 3).map((n) => n.target.join(' ')),
    help: v.help,
  }));
  note(info, `axe ${name}: ${results.violations.length} violation rule(s), ${results.incomplete.length} incomplete: ${JSON.stringify(summary)}`);
  // Tap targets under 24 px on the phone.
  const small = await page.evaluate(() =>
    Array.from(document.querySelectorAll<HTMLElement>('button, a, input, [role="button"]'))
      .filter((el) => el.offsetParent !== null)
      .map((el) => {
        const r = el.getBoundingClientRect();
        return { w: Math.round(r.width), h: Math.round(r.height), name: (el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 30), cls: el.className.toString().slice(0, 40) };
      })
      .filter((t) => t.w > 0 && (t.w < 24 || t.h < 24)),
  );
  note(info, `${name}: interactive targets smaller than 24 px: ${JSON.stringify(small)}`);
  const focusable = await page.evaluate(() => {
    const els = Array.from(document.querySelectorAll<HTMLElement>('button, a, input, textarea, [tabindex]'));
    return els.filter((el) => el.offsetParent !== null).length;
  });
  note(info, `${name}: focusable elements ${focusable}`);
}

test('axe on library, note, settings, capture', async ({ page }, info) => {
  await page.goto('./');
  await libraryReady(page);
  await audit(page, info, 'library');
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  await audit(page, info, 'library-selecting');
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await page.locator('.note-row').filter({ hasText: 'staging smoke' }).first().click();
  await page.locator('#note-body').waitFor();
  await sleep(2_000);
  await audit(page, info, 'note');
  await page.getByRole('button', { name: 'Tags' }).click();
  await audit(page, info, 'note-tags-open');
  await page.goto('./settings');
  await page.getByRole('heading', { name: 'You' }).waitFor();
  await sleep(1_000);
  await audit(page, info, 'settings');
  await page.goto('./capture');
  await expect(page.locator('.capture__state')).toHaveText('Recording', { timeout: 15_000 });
  await audit(page, info, 'capture');
  await page.getByRole('button', { name: 'Cancel' }).click();
  // Keyboard: Tab order on the library.
  await page.goto('./');
  await libraryReady(page);
  const order: string[] = [];
  for (let i = 0; i < 8; i += 1) {
    await page.keyboard.press('Tab');
    order.push(await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null;
      return el ? `${el.tagName.toLowerCase()}${el.className ? '.' + el.className.toString().split(' ')[0] : ''}:${(el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 20)}` : 'none';
    }));
  }
  note(info, `library tab order: ${order.join(' → ')}`);
  const ring = await page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null;
    if (!el) return null;
    const s = getComputedStyle(el);
    return { outline: s.outlineStyle + ' ' + s.outlineWidth + ' ' + s.outlineColor, boxShadow: s.boxShadow.slice(0, 60) };
  });
  note(info, `focus ring on the last focused element: ${JSON.stringify(ring)}`);
});
