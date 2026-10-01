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
   * that stops before it lifts — a pause after the last move, which fires no
   * move of its own, so the swipe must read no flick from the moves before
   * it (DB6-23); `beforeEnd` reads the screen while the finger is still down.
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
      await cdp.send('Input.dispatchTouchEvent', {
        type: 'touchMove',
        touchPoints: [{ x: x + (dx * i) / steps, y: y + (dy * i) / steps }],
      });
    }
    if (rest) await new Promise((resolve) => setTimeout(resolve, 150));
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

    interface Frame {
      t: number;
      x: number;
      /** The tab the panel on screen belongs to, from its id. */
      panel: string;
      /** `data-tab-enter` on the region: the frame is a step's start pose. */
      enter: boolean;
    }

    /**
     * Every frame's panel `translate` (0 for none), which panel it is, and
     * whether it is posed, from now until `ms` later, read back by `frames`.
     * Started before the lift, so the first frame after it is in the record.
     */
    async function sample(page: Page, ms: number): Promise<void> {
      await page.evaluate((duration) => {
        const record: Frame[] = [];
        (window as unknown as { frames_: Frame[] }).frames_ = record;
        delete document.documentElement.dataset.sampled;
        const start = performance.now();
        const tick = (): void => {
          const panel = document.querySelector('.note-tabpanel');
          const raw = panel ? getComputedStyle(panel).translate : 'none';
          record.push({
            t: performance.now(),
            x: raw === 'none' ? 0 : parseFloat(raw),
            panel: panel?.id.split('-').pop() ?? '',
            enter: document.querySelector('.note-views')?.hasAttribute('data-tab-enter') ?? false,
          });
          if (performance.now() - start < duration) requestAnimationFrame(tick);
          else document.documentElement.dataset.sampled = '';
        };
        requestAnimationFrame(tick);
      }, ms);
    }

    async function frames(page: Page): Promise<Frame[]> {
      await page.locator('html[data-sampled]').waitFor({ state: 'attached' });
      return page.evaluate(() => (window as unknown as { frames_: Frame[] }).frames_);
    }

    /**
     * A 180 px drag across the panel, then what the frames showed: the first
     * posed frame is the NEW panel — never the old one at the enter pose — on
     * the side the finger left, and it comes to rest without crossing over,
     * within 400 ms.
     */
    async function expectStep(page: Page, cdp: CDPSession, dx: number, to: string): Promise<void> {
      const box = (await page.locator('.note-tabpanel').boundingBox())!;
      await drag(cdp, dx < 0 ? 300 : 100, box.y + 150, dx, 4, { beforeEnd: () => sample(page, 600) });
      const record = await frames(page);
      const posed = record.find((frame) => frame.enter);
      expect(posed, 'a posed frame').toBeDefined();
      expect(posed!.panel).toBe(to);
      const arrived = record.filter((frame) => frame.panel === to);
      const side = Math.sign(-dx);
      expect(Math.sign(arrived[0]!.x)).toBe(side);
      expect(arrived.every((frame) => frame.x * side >= 0)).toBe(true);
      const rest = arrived.find((frame) => frame.x === 0);
      expect(rest).toBeDefined();
      expect(rest!.t - arrived[0]!.t).toBeLessThan(400);
    }

    test('the stepped-to panel arrives the way the finger went, and the pill lands on its tab', async ({
      page,
      api,
    }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      const cdp = await page.context().newCDPSession(page);
      // A left drag: Cleaned comes in from the right, moving left. The defect
      // was the reverse — the new panel drawn at the old offset, sliding right.
      await expectStep(page, cdp, -180, 'cleaned');

      // The pill ends centred on the selected tab.
      const centres = async () =>
        page.evaluate(() => {
          const centre = (selector: string) => {
            const rect = document.querySelector(selector)!.getBoundingClientRect();
            return rect.left + rect.width / 2;
          };
          return {
            pill: centre('.note-tabs__indicator'),
            tab: centre('[role="tab"][aria-selected="true"]'),
          };
        });
      await expect
        .poll(async () => {
          const { pill, tab } = await centres();
          return Math.abs(pill - tab);
        })
        .toBeLessThanOrEqual(1);

      // Back from ?tab=cleaned: Text comes in from the left.
      await expectStep(page, cdp, 180, 'text');
    });

    test('a step from a tab the URL names poses the new panel, not the old', async ({ page, api }) => {
      // `?tab=` changes through the router, which commits a frame late; the
      // old panel used to be shown at the enter pose for that frame.
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair?tab=cleaned');
      const cdp = await page.context().newCDPSession(page);
      await expectStep(page, cdp, -180, 'recordings');
      await expectStep(page, cdp, 180, 'cleaned');
    });

    test('right on Text rubber-bands short of 15 % of the width and snaps back', async ({
      page,
      api,
    }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      const cdp = await page.context().newCDPSession(page);
      const panel = page.locator('.note-tabpanel');
      const box = (await panel.boundingBox())!;
      let held = Number.NaN;
      await drag(cdp, 60, box.y + 200, 300, 0, {
        beforeEnd: async () => {
          held = await panel.evaluate((element) => parseFloat(getComputedStyle(element).translate));
        },
      });
      expect(held).toBeGreaterThan(0);
      expect(held).toBeLessThan(0.15 * 412);
      await expect.poll(() => panel.evaluate((element) => getComputedStyle(element).translate)).toBe('none');
      expect(await selected(page)).toBe('Text');
    });

    test('under reduced motion the new panel is at rest straight after the lift', async ({ page, api }) => {
      longBody(api);
      await page.emulateMedia({ reducedMotion: 'reduce' });
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair');
      const cdp = await page.context().newCDPSession(page);
      const box = (await page.locator('.note-tabpanel').boundingBox())!;
      await drag(cdp, 300, box.y + 200, -180, 4, { beforeEnd: () => sample(page, 300) });
      const arrived = (await frames(page)).filter((frame) => frame.panel === 'cleaned');
      const rest = arrived.find((frame) => frame.x === 0);
      expect(rest).toBeDefined();
      expect(rest!.t - arrived[0]!.t).toBeLessThan(50);
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

    test('on Recordings a left drag on a head is its tray; a right drag on the open head closes it; a right drag on the closed head goes to Cleaned', async ({
      page,
      api,
    }) => {
      longBody(api);
      await page.setViewportSize({ width: 412, height: 915 });
      await page.goto('/notes/roof-repair?tab=recordings');
      const cdp = await page.context().newCDPSession(page);
      const head = page.locator('.recording__swipe').first();
      await expect(head).toBeVisible();
      const box = (await head.boundingBox())!;
      const y = box.y + box.height / 2;
      await drag(cdp, box.x + box.width * 0.6, y, -200, 0);
      await expect(head).toHaveAttribute('data-open');
      expect(await selected(page)).toContain('Recordings');
      // An open row owns both directions: a right drag on it is its own close
      // gesture. The swipe took it (review 2026-09-29): its capture fired the
      // row's `lostpointercapture`, the tray stayed open and the tab flipped.
      // x = 200 is on the row however far its tray has shifted it.
      await drag(cdp, 200, y, 200, 0);
      await expect(head).not.toHaveAttribute('data-open');
      await page.waitForTimeout(300);
      expect(await selected(page)).toContain('Recordings');
      // Closed again, and trays open leftwards only: a right drag on the head
      // is the swipe's.
      await drag(cdp, 200, y, 200, 0);
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

/**
 * Record while typing (R8, F3): with the keyboard up, a mic at the banner's
 * right end, in the banner's own row, so it covers nothing. CDP cannot raise
 * an on-screen keyboard (R6-NAV-2), so this sets what `useKeyboardInset`
 * would: the inset and `data-keyboard` on `<html>`.
 */
test('with the keyboard up, the banner mic records into the note being typed in', async ({
  page,
  api,
}) => {
  await page.setViewportSize({ width: 412, height: 915 });
  await page.goto('/notes/roof-repair');
  const body = page.getByRole('textbox', { name: 'Note body' });
  await expect(body).toBeVisible();
  const banner = page.locator('.app__banner');
  const mic = banner.getByRole('button', { name: 'Record into this note (while typing)' });
  const bannerBox = await banner.boundingBox();
  await expect(mic).toBeHidden();

  // A keyboard is not enough on its own: a note field must have focus.
  await page.evaluate(() => {
    document.documentElement.style.setProperty('--keyboard-inset', '400px');
    document.documentElement.setAttribute('data-keyboard', '');
  });
  await expect(mic).toBeHidden();

  await body.focus();
  await expect(mic).toBeVisible();
  const micBox = (await mic.boundingBox())!;
  const after = (await banner.boundingBox())!;
  // In the banner's row, at its right end, and the row did not grow.
  expect(after.height).toBe(bannerBox!.height);
  expect(micBox.y).toBeGreaterThanOrEqual(after.y);
  expect(micBox.y + micBox.height).toBeLessThanOrEqual(after.y + after.height + 0.5);
  expect(micBox.width).toBeGreaterThanOrEqual(44);
  expect(412 - (micBox.x + micBox.width)).toBeCloseTo(16, 0);

  await body.blur();
  await expect(mic).toBeHidden();

  // Typed words are saved before the recording starts: the editor's
  // debounced save flushes as the note screen unmounts.
  await body.focus();
  await body.press('End');
  await page.keyboard.type(' Check the gutters too.');
  await mic.click();
  await expect(page).toHaveURL(/\/capture\?note=roof-repair$/);
  await expect(page.locator('.capture__state')).toHaveText('Recording');
  const patchAt = () =>
    api.requests.findIndex((request) => request.method === 'PATCH' && request.url === '/v1/notes/roof-repair');
  await expect.poll(patchAt, { message: 'the body was saved' }).toBeGreaterThanOrEqual(0);
  expect(api.notes['roof-repair']!.body).toContain('Check the gutters too.');

  await expect(page.locator('.capture__timer')).toHaveText('00:01');
  await page.getByRole('button', { name: 'Stop' }).click();
  await page.getByRole('button', { name: 'Send' }).click();
  const postAt = () =>
    api.requests.findIndex((request) => request.method === 'POST' && request.url === '/v1/captures');
  await expect.poll(postAt, { message: 'the recording was sent' }).toBeGreaterThanOrEqual(0);
  expect(patchAt()).toBeLessThan(postAt());
});

test('offline on a 360 px phone, the banner mic leaves the offline pill on one line', async ({
  page,
  context,
}) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await page.goto('/notes/roof-repair');
  const body = page.getByRole('textbox', { name: 'Note body' });
  await expect(body).toBeVisible();
  await context.setOffline(true);
  const banner = page.locator('.app__banner');
  const pill = banner.locator('.offline-banner');
  await expect(pill).toBeVisible();
  const bannerBefore = (await banner.boundingBox())!.height;
  const pillBefore = (await pill.boundingBox())!.height;

  await page.evaluate(() => {
    document.documentElement.style.setProperty('--keyboard-inset', '400px');
    document.documentElement.setAttribute('data-keyboard', '');
  });
  await body.focus();
  const mic = banner.getByRole('button', { name: 'Record into this note (while typing)' });
  await expect(mic).toBeVisible();
  // The mic squeezed the pill onto two lines at 360–393 px, and the banner grew.
  expect((await pill.boundingBox())!.height).toBe(pillBefore);
  expect((await banner.boundingBox())!.height).toBe(bannerBefore);
  const micBox = (await mic.boundingBox())!;
  const pillBox = (await pill.boundingBox())!;
  expect(pillBox.x + pillBox.width).toBeLessThanOrEqual(micBox.x);
  await context.setOffline(false);
});
