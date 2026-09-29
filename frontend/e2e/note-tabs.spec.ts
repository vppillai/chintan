import { devices, type CDPSession, type Page } from '@playwright/test';

import { expect, noteAction, seedChecklist, test, type ApiState } from './fixtures.ts';

/**
 * The note as panels under one strip: Text · Cleaned · Recordings (N).
 *
 * The note used to be one page, and the recordings of a long note were a
 * scroll past every paragraph. The strip sticks under the banner, the chosen
 * tab is remembered for the session and named in the URL, and a deep link can
 * open a note on its recordings.
 */

/** Eighty paragraphs: a note that scrolls on any phone. */
function longBody(api: ApiState): void {
  api.notes['roof-repair']!.body = Array.from(
    { length: 80 },
    (_, index) => `Paragraph ${String(index + 1)}. The flashing around the chimney needs replacing.`,
  ).join('\n\n');
}

test('opens on the text, and Recordings is one tap away with its count', async ({ page }) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  const tabs = page.getByRole('tablist', { name: 'Note views' }).getByRole('tab');
  await expect(tabs).toHaveText(['Text', 'Cleaned', 'Recordings (1)']);
  await expect(page.getByRole('tab', { name: 'Text' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();
  await expect(page.getByRole('region', { name: 'Recordings' })).toHaveCount(0);

  await page.getByRole('tab', { name: /^Recordings/ }).click();
  await expect(page).toHaveURL(/\/notes\/roof-repair\?tab=recordings$/);
  await expect(page.getByRole('region', { name: 'Recording', exact: true })).toBeVisible();
  await expect(page.getByRole('textbox', { name: 'Note body' })).toHaveCount(0);
  // The tab bar's mic records into this note on every tab.
  await expect(page.getByRole('button', { name: 'Record into this note', exact: true })).toBeVisible();

  // Remembered for the session: a reload lands on the same tab, and so does
  // reopening the note from the library.
  await page.reload();
  await expect(page.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
  await page.goto('/');
  await page.getByRole('button', { name: /roof repair/i }).click();
  await expect(page.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
  // Back leaves the note; it does not step back through tabs.
  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
});

test('the arrow keys move between segments', async ({ page }) => {
  await page.goto('/notes/roof-repair');
  await page.getByRole('tab', { name: 'Text' }).focus();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('tab', { name: 'Cleaned' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('tab', { name: 'Cleaned' })).toBeFocused();
  await expect(page.getByText('No cleaned view yet')).toBeVisible();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('tab', { name: /^Recordings/ })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('tab', { name: /^Recordings/ })).toBeFocused();
  await page.keyboard.press('Home');
  await expect(page.getByRole('tab', { name: 'Text' })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();
});

test('the strip sticks under the banner while a long note scrolls', async ({ page, api }) => {
  longBody(api);
  await page.setViewportSize({ width: 412, height: 915 });
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note body' })).toBeVisible();

  const measured = await page.evaluate(() => {
    const main = document.querySelector('.app__main');
    const strip = document.querySelector('.note-tabs');
    if (!main || !strip) return null;
    const before = strip.getBoundingClientRect().top;
    main.scrollTop = 1_200;
    const after = strip.getBoundingClientRect();
    return {
      before: Math.round(before),
      top: Math.round(after.top),
      mainTop: Math.round(main.getBoundingClientRect().top),
      scrolled: main.scrollTop,
    };
  });
  expect(measured).not.toBeNull();
  expect(measured!.scrolled).toBeGreaterThan(0);
  // It was lower on the page before the scroll, and now rests on the line
  // under the banner — the top of the scroll region.
  expect(measured!.before).toBeGreaterThan(measured!.top);
  expect(Math.abs(measured!.top - measured!.mainTop)).toBeLessThanOrEqual(1);
});

test('the Details sheet keeps Close in reach while its content scrolls', async ({ page }) => {
  // Short enough that the sheet's 60dvh cap is less than Details' content, so
  // the sheet scrolls inside itself; Close scrolled away with the first
  // section until the head was made sticky (QA 2026-09-21, finding 7).
  await page.setViewportSize({ width: 412, height: 560 });
  await page.goto('/notes/roof-repair');
  await noteAction(page, 'Details');
  const close = page.getByRole('button', { name: 'Close details' });
  await expect(close).toBeVisible();

  const scrolled = await page.locator('.note-panel').evaluate((panel) => {
    panel.scrollTop = panel.scrollHeight;
    return panel.scrollTop;
  });
  expect(scrolled).toBeGreaterThan(0);
  await expect(close).toBeInViewport();
});

test.describe('on a phone', () => {
  // The descriptor's `defaultBrowserType` cannot be set inside a describe.
  const { defaultBrowserType: _browser, ...pixel } = devices['Pixel 7']!;
  test.use({ ...pixel, hasTouch: true, isMobile: true });

  /**
   * A finger from (x, y), `dx` across and `dy` down, in a dozen moves through
   * the browser's own touch pipeline (as `swipe.spec.ts`). `rest` is a finger
   * that stops before it lifts — a pause before the last move, so the swipe
   * reads no flick from it; `beforeEnd` reads the screen while the finger is
   * still down.
   */
  async function drag(
    cdp: CDPSession,
    x: number,
    y: number,
    dx: number,
    dy: number,
    { rest = false, beforeEnd }: { rest?: boolean; beforeEnd?: () => Promise<void> } = {},
  ): Promise<void> {
    const steps = 12;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
    for (let i = 1; i <= steps; i += 1) {
      if (rest && i === steps) await new Promise((resolve) => setTimeout(resolve, 150));
      await cdp.send('Input.dispatchTouchEvent', {
        type: 'touchMove',
        touchPoints: [{ x: x + (dx * i) / steps, y: y + (dy * i) / steps }],
      });
    }
    if (beforeEnd) await beforeEnd();
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  }

  test('a drag down on a scrolled Details sheet scrolls the sheet, not the note, and pulls nothing', async ({
    page,
    api,
  }) => {
    // The note at its top, the sheet scrolled to its end: the one arrangement
    // where pull-to-refresh used to claim the sheet's drag (R6-NAV-3, T1 —
    // armed at 90 px with the sheet unmoved, then the note refetched).
    longBody(api);
    await page.setViewportSize({ width: 412, height: 700 });
    await page.goto('/notes/roof-repair');
    await noteAction(page, 'Details');
    const panel = page.locator('.note-panel');
    const before = await panel.evaluate((sheet) => {
      sheet.scrollTop = sheet.scrollHeight;
      return sheet.scrollTop;
    });
    expect(before, 'the sheet must scroll for this to mean anything').toBeGreaterThan(0);

    const box = (await panel.boundingBox())!;
    const cdp = await page.context().newCDPSession(page);
    let midPhase: string | null = null;
    await drag(cdp, box.x + box.width / 2, box.y + box.height * 0.6, 0, 150, {
      beforeEnd: async () => {
        midPhase = await page.locator('.pull-refresh').getAttribute('data-phase');
      },
    });
    expect(midPhase).toBe('idle');
    await expect.poll(() => panel.evaluate((sheet) => sheet.scrollTop)).toBeLessThan(before);
    expect(await page.locator('.app__main').evaluate((main) => main.scrollTop)).toBe(0);
  });

  test.describe('a swipe between the segments', () => {
    const selected = (page: Page) =>
      page.getByRole('tablist', { name: 'Note views' }).locator('[aria-selected="true"]').textContent();

    test('left opens Cleaned and names it in the URL; right comes back; right on Text and a short drag stay', async ({
      page,
      api,
    }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      const cdp = await page.context().newCDPSession(page);
      const panel = page.locator('.note-tabpanel');
      let box = (await panel.boundingBox())!;
      await drag(cdp, 300, box.y + 200, -180, 4);
      await expect.poll(() => selected(page)).toBe('Cleaned');
      await expect(page).toHaveURL(/tab=cleaned/);
      // Lifting over the new panel focused nothing.
      expect(await page.evaluate(() => document.activeElement?.tagName)).not.toBe('TEXTAREA');
      box = (await panel.boundingBox())!;
      await drag(cdp, 100, box.y + 100, 180, 4);
      await expect.poll(() => selected(page)).toBe('Text');
      // Nothing to the right of Text: the panel rubber-bands and stays.
      await drag(cdp, 100, box.y + 100, 180, 4);
      await page.waitForTimeout(300);
      expect(await selected(page)).toBe('Text');
      // Short of 30 % of the width, and the finger at rest before it lifts: a snap back.
      await drag(cdp, 300, box.y + 200, -60, 0, { rest: true });
      await page.waitForTimeout(300);
      expect(await selected(page)).toBe('Text');
    });

    test('a vertical drag scrolls the note and switches nothing', async ({ page, api }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      const cdp = await page.context().newCDPSession(page);
      const main = page.locator('.app__main');
      await drag(cdp, 200, 700, 8, -300);
      await expect.poll(() => main.evaluate((region) => region.scrollTop)).toBeGreaterThan(0);
      expect(await selected(page)).toBe('Text');
    });

    test('on Recordings a left drag on a head is its tray; a right drag on the head goes to Cleaned', async ({
      page,
      api,
    }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair?tab=recordings');
      const cdp = await page.context().newCDPSession(page);
      const head = page.locator('.recording__swipe').first();
      await expect(head).toBeVisible();
      let box = (await head.boundingBox())!;
      await drag(cdp, box.x + box.width * 0.6, box.y + box.height / 2, -200, 0);
      await expect(head).toHaveAttribute('data-open');
      expect(await selected(page)).toContain('Recordings');
      // A tap elsewhere closes the tray; trays open leftwards only, so a
      // right drag on the same head is the swipe's.
      await page.mouse.click(200, 150);
      await expect(head).not.toHaveAttribute('data-open');
      box = (await head.boundingBox())!;
      await drag(cdp, box.x + 60, box.y + box.height / 2, 200, 0);
      await expect.poll(() => selected(page)).toBe('Cleaned');
    });

    test('a drag from the screen edge is left to the system', async ({ page, api }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      const cdp = await page.context().newCDPSession(page);
      await drag(cdp, 10, 500, 220, 0);
      await page.waitForTimeout(300);
      expect(await selected(page)).toBe('Text');
    });

    test('on Items a left drag on a grip is the row’s, never a tab switch', async ({ page, api }) => {
      seedChecklist(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/shopping');
      const cdp = await page.context().newCDPSession(page);
      const grip = (await page.getByRole('button', { name: 'Move Milk' }).boundingBox())!;
      await drag(cdp, grip.x + grip.width / 2, grip.y + grip.height / 2, -180, 0);
      await page.waitForTimeout(300);
      expect(await selected(page)).toBe('Items');
    });
  });

  test.describe('with the keyboard up', () => {
    /*
     * CDP cannot raise a keyboard — `visualViewport` ignores the emulated
     * metrics — so the tests write what `useKeyboardInset` would: a 400 px
     * keyboard on a 915 px phone (R6-NAV-2). What is measured is what the
     * property does to the scroll container and the drawer.
     */
    const INSET = 400;

    async function raiseKeyboard(page: Page): Promise<void> {
      await page.evaluate((inset) => {
        document.documentElement.style.setProperty('--keyboard-inset', `${String(inset)}px`);
      }, INSET);
    }

    /** The active element's bottom edge and the scroll region's, in viewport pixels. */
    async function edges(page: Page): Promise<{ active: number; main: number }> {
      return page.evaluate(() => {
        const active = document.activeElement?.getBoundingClientRect().bottom ?? 0;
        const main = document.querySelector('.app__main')!.getBoundingClientRect().bottom;
        return { active: Math.round(active), main: Math.round(main) };
      });
    }

    test('typing at the end of a long note keeps the caret line above the keyboard', async ({
      page,
      api,
    }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      await raiseKeyboard(page);
      const body = page.getByRole('textbox', { name: 'Note body' });
      await body.evaluate((textarea: HTMLTextAreaElement) => {
        textarea.focus();
        textarea.setSelectionRange(textarea.value.length, textarea.value.length);
      });
      // The end of the note 700 px below the fold: the reveal has to scroll.
      await page.locator('.app__main').evaluate((main) => {
        main.scrollTop = main.scrollHeight - main.clientHeight - 700;
      });
      await page.keyboard.press('End');
      await page.keyboard.type('x');
      await page.keyboard.press('Enter');
      await page.keyboard.type('the last line');
      // The textarea's bottom is the caret line plus 13 px of padding; the
      // caret must sit above the keyboard's top edge. Without the inset it
      // rests at the region's edge, 13 px under the keyboard (E1: 835).
      await expect
        .poll(async () => {
          const { active, main } = await edges(page);
          return active - 13 <= main - INSET;
        })
        .toBe(true);
    });

    test('Enter in the last item of a long checklist puts the new item above the keyboard', async ({
      page,
      api,
    }) => {
      const note = api.notes['roof-repair']!;
      note.kind = 'checklist';
      note.body = Array.from({ length: 40 }, (_, index) => `- [ ] Item ${String(index + 1)}`).join(
        '\n',
      );
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      await raiseKeyboard(page);
      const last = page.getByRole('list', { name: 'Items' }).locator('textarea.checklist__text').nth(39);
      await last.evaluate((textarea: HTMLTextAreaElement) => {
        textarea.focus();
        textarea.setSelectionRange(textarea.value.length, textarea.value.length);
      });
      await page.locator('.app__main').evaluate((main) => {
        main.scrollTop = main.scrollHeight - main.clientHeight - 600;
      });
      await page.keyboard.press('Enter');
      await page.keyboard.type('new one');
      await expect
        .poll(async () => {
          const { active, main } = await edges(page);
          return active <= main - INSET;
        })
        .toBe(true);
    });

    test('the Details sheet rises above the keyboard', async ({ page, api }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      await raiseKeyboard(page);
      await noteAction(page, 'Details');
      const panel = page.locator('.note-panel');
      await expect(panel).toBeVisible();
      const bottom = await panel.evaluate((sheet) => Math.round(sheet.getBoundingClientRect().bottom));
      const main = await page
        .locator('.app__main')
        .evaluate((region) => Math.round(region.getBoundingClientRect().bottom));
      expect(bottom).toBeLessThanOrEqual(main - INSET);
      // Lifted by the inset, not twice: the padding is the region's, not the sheet's.
      expect(bottom).toBeGreaterThan(main - INSET - 2);
    });
  });
});
