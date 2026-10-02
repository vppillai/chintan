import { devices, type CDPSession, type Locator } from '@playwright/test';

import { expect, noteAction, test } from './fixtures.ts';

/**
 * Dragging the drawer's head down closes it, with a real touch through the
 * browser's own pipeline (`Input.dispatchTouchEvent`, as `swipe.spec.ts`);
 * the × and Escape are proven elsewhere and stay.
 */

test.use({ ...devices['Pixel 7'], hasTouch: true, isMobile: true });

/**
 * A finger from the head's centre, `dy` down, in `steps` moves `stepMs`
 * apart on the clock the events carry: the `timestamp` the protocol takes is
 * what the page reads as `event.timeStamp`, so the velocity the hook sees is
 * the one written here, not the CDP round trip's.
 */
async function dragDown(
  cdp: CDPSession,
  head: Locator,
  dy: number,
  { stepMs, steps = 12 }: { stepMs: number; steps?: number },
): Promise<void> {
  const box = await head.boundingBox();
  if (!box) throw new Error('the head has no box');
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;
  let at = Date.now() / 1000;
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }], timestamp: at });
  for (let i = 1; i <= steps; i += 1) {
    at += stepMs / 1000;
    await cdp.send('Input.dispatchTouchEvent', {
      type: 'touchMove',
      touchPoints: [{ x, y: y + (dy * i) / steps }],
      timestamp: at,
    });
  }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [], timestamp: at });
}

for (const reduced of [false, true]) {
  test(`a drag down past the threshold closes the Share sheet and hands focus back to the ⋮${reduced ? ' (reduced motion)' : ''}`, async ({
    page,
  }) => {
    if (reduced) await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('/notes/roof-repair');
    await noteAction(page, 'Share');
    const dialog = page.getByRole('dialog', { name: 'Share' });
    await expect(dialog).toBeVisible();
    const height = (await dialog.boundingBox())!.height;
    const cdp = await page.context().newCDPSession(page);

    // Half the height over 1.2 s: far, and nowhere near a flick.
    await dragDown(cdp, page.locator('.note-panel__head'), height * 0.5, { stepMs: 100 });
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('button', { name: 'Note actions' })).toBeFocused();
  });
}

test('a short drag settles back and a flick closes', async ({ page }) => {
  await page.goto('/notes/roof-repair');
  await noteAction(page, 'Share');
  const dialog = page.getByRole('dialog', { name: 'Share' });
  await expect(dialog).toBeVisible();
  const cdp = await page.context().newCDPSession(page);
  const head = page.locator('.note-panel__head');

  await dragDown(cdp, head, 40, { stepMs: 100 });
  await expect(dialog).toBeVisible();
  await expect.poll(() => dialog.evaluate((el) => el.style.translate)).toBe('');
  // The × still works after a drag that settled.
  await expect(page.getByRole('button', { name: 'Close share' })).toBeVisible();

  // Short of the line but fast: a quarter of the height in two moves 20 ms
  // apart — 0.125·H per 20 ms, five times the flick speed on any phone.
  const height = (await dialog.boundingBox())!.height;
  await dragDown(cdp, head, height * 0.25, { stepMs: 20, steps: 2 });
  await expect(dialog).toBeHidden();
});
