import type { Page } from '@playwright/test';

import { expect, noteAction, test } from './fixtures.ts';

/**
 * Removing a note: delete (the archive), see the archive, undo or restore,
 * delete forever.
 *
 * All four operations the backend serves and `endpoints.ts` wraps must be
 * reachable from a control. Without them the app is append-only: every
 * mis-dictated note, every duplicate the router creates and every private
 * thing said by accident is permanent and always on the list, while the note
 * screen's "may have been archived or purged" describes two states the UI can
 * neither produce nor show.
 *
 * No typed word anywhere (owner, 2026-09-26: "I have to type delete. I don't
 * like that UX"). Delete on Home archives on the tap and offers Undo in a
 * toast; Delete forever, in the archive, is a plain confirm.
 *
 * These run against the stubbed API in `fixtures.ts`, which implements the same
 * four operations `openapi.yaml` declares.
 */

/** Tabs forward until the named button has focus, so Undo is reached as a keyboard would. */
async function tabTo(page: Page, name: string): Promise<void> {
  for (let i = 0; i < 40; i += 1) {
    if (await page.getByRole('button', { name }).evaluate((el) => el === document.activeElement)) return;
    await page.keyboard.press('Tab');
  }
  throw new Error(`Tab never reached "${name}"`);
}

test('Delete on a note archives it with no dialog, and Undo on the library restores it', async ({
  page,
  api,
}) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  await noteAction(page, 'Delete');

  // No dialog: back on the library, and the app is not left sitting on a note that is gone.
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole('button', { name: /roof repair/i })).toHaveCount(0);
  expect(api.notes['roof-repair']?.archived).toBe(true);

  // The toast says where it went and offers the way back, in a polite live region.
  const toast = page.locator('.toast');
  await expect(toast).toHaveAttribute('aria-live', 'polite');
  await expect(toast).toContainText('Deleted · kept in Archive for 30 days');
  await toast.getByRole('button', { name: 'Undo' }).click();

  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  expect(api.notes['roof-repair']?.archived).toBe(false);
  await expect(toast).toBeEmpty();
});

test('Delete on a row archives it, and Undo is reached from the keyboard', async ({ page, api }) => {
  await page.goto('/');
  const row = page.getByRole('button', { name: /roof repair/i });
  await row.hover();
  await page.locator('.note-row-wrap', { has: row }).getByRole('button', { name: 'More' }).click();
  // The row's ⋮ has Pin, Delete and Select — no separate Archive on Home.
  await expect(page.getByRole('menuitem')).toHaveText(['Pin', 'Delete', 'Select']);
  await page.getByRole('menuitem', { name: 'Delete' }).click();

  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(row).toHaveCount(0);
  expect(api.notes['roof-repair']?.archived).toBe(true);

  await tabTo(page, 'Undo');
  // The ring has to be seen on the ink card: the global ring is ink too.
  const ring = await page.getByRole('button', { name: 'Undo' }).evaluate((el) => ({
    style: getComputedStyle(el).outlineStyle,
    outline: getComputedStyle(el).outlineColor,
    card: getComputedStyle(el.closest('.toast__card')!).backgroundColor,
  }));
  expect(ring.style).not.toBe('none');
  expect(ring.outline).not.toBe(ring.card);

  await page.keyboard.press('Enter');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  expect(api.notes['roof-repair']?.archived).toBe(false);
});

test('the archive is a chip on the library, and says when each note is purged', async ({
  page,
}) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

  await page.getByRole('button', { name: /^Archived/ }).click();
  await expect(page).toHaveURL(/\?view=archived$/);
  await expect(page.getByRole('button', { name: /^Archived/ })).toHaveAttribute(
    'aria-pressed',
    'true',
  );

  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();
  await expect(page.getByRole('button', { name: /stray thought/i })).toBeVisible();

  // Active notes are not in here.
  await expect(page.getByRole('button', { name: /roof repair/i })).toHaveCount(0);

  // An absent `purge_after` — every note archived before retention was
  // configured — must read as "no deletion date", never "Deletes in NaN days".
  await expect(page.locator('.screen')).not.toContainText('NaN');
  await expect(page.getByText(/deletes in/i).first()).toBeVisible();
  await expect(page.getByText(/no deletion date/i)).toBeVisible();
});

test('an archived note can be restored', async ({ page, api }) => {
  // The old address still works; it is the library with the archive chip on.
  await page.goto('/archive');
  await expect(page).toHaveURL(/\?view=archived$/);
  await page.getByRole('button', { name: /old fence/i }).click();

  await expect(page.getByText(/this note is archived/i)).toBeVisible();
  await noteAction(page, 'Restore');

  await expect(page).toHaveURL(/\/notes\/old-fence$/);
  await expect(page.getByText(/this note is archived/i)).toHaveCount(0);
  expect(api.notes['old-fence']?.archived).toBe(false);

  await page.goto('/');
  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();
});

test('delete forever is a plain confirm with focus on Cancel, and cascades', async ({ page, api }) => {
  await page.goto('/notes/old-fence');

  await noteAction(page, 'Delete forever');

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  // It says what else goes with it. The audio and the transcripts are not
  // recoverable either, and a dialog that only names the note is not consent.
  await expect(dialog).toContainText(/recordings and transcripts/i);

  // And which note: the title is in the sentence. There is nothing to type,
  // and Enter lands on Cancel.
  await expect(dialog).toContainText('Old fence');
  await expect(dialog.getByRole('textbox')).toHaveCount(0);
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  const confirm = dialog.getByRole('button', { name: 'Delete forever' });
  await expect(confirm).toBeEnabled();
  await confirm.click();

  await expect(page).toHaveURL(/\?view=archived$/);
  expect(api.purged).toEqual(['old-fence']);
  await expect(page.getByRole('button', { name: /old fence/i })).toHaveCount(0);
});

/**
 * Bulk select, the same bar in both views: Delete from the library (the
 * archive, with Undo), Restore and Delete forever from the archive.
 *
 * There is no Select button and no hover checkbox (owner, 2026-09-24: "not
 * clean UX"). A press-and-hold on a row starts the selection with a mouse
 * as with a finger (the last two tests here), and so does Select in the
 * row's ⋮ menu, which a mouse reveals by resting on the row. The bar sits
 * above the tab bar rather than at the end of the list (backlog U2, Q6).
 */
async function startSelecting(page: Page, title: RegExp): Promise<void> {
  const row = page.getByRole('button', { name: title });
  await row.hover();
  // The ⋮ is named "More" and described by the title, so it is found from its row.
  await page.locator('.note-row-wrap', { has: row }).getByRole('button', { name: 'More' }).click();
  await page.getByRole('menuitem', { name: 'Select' }).click();
  await expect(page.getByRole('toolbar', { name: 'Bulk actions' })).toBeVisible();
}

test('several notes can be deleted at once from the library, and Undo brings them all back', async ({
  page,
  api,
}) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Select' })).toHaveCount(0);

  await startSelecting(page, /roof repair/i);
  await expect(page.getByText('1 selected')).toBeVisible();
  await page.getByRole('button', { name: 'Select all' }).click();
  await expect(page.getByText('2 selected')).toBeVisible();

  // The bar is directly above the tab bar, not at the end of the list.
  const bar = await page.locator('.selection-bar').boundingBox();
  const tabs = await page.locator('.tab-bar').boundingBox();
  expect(bar!.y + bar!.height).toBeLessThanOrEqual(tabs!.y + 1);

  // One destructive action, no dialog.
  const toolbar = page.getByRole('toolbar', { name: 'Bulk actions' });
  await expect(toolbar.getByRole('button')).toHaveText(['Deselect all', 'Delete', 'Cancel']);
  await toolbar.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByRole('dialog')).toHaveCount(0);

  await expect(page.getByText(/tap PTT to record your first note/i)).toBeVisible();
  expect(api.notes['roof-repair']?.archived).toBe(true);
  expect(api.notes['reading-list']?.archived).toBe(true);
  // And the chip now counts them.
  await expect(page.getByRole('button', { name: 'Archived · 4' })).toBeVisible();

  const toast = page.locator('.toast');
  await expect(toast).toContainText('2 notes deleted · kept in Archive for 30 days');
  await toast.getByRole('button', { name: 'Undo' }).click();
  await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();
  await expect(page.getByRole('button', { name: /reading list/i })).toBeVisible();
  expect(api.notes['roof-repair']?.archived).toBe(false);
  expect(api.notes['reading-list']?.archived).toBe(false);
});

test.describe('on a phone', () => {
  test.use({ viewport: { width: 412, height: 915 } });

  test('a reload of the archive keeps its pressed chip on screen once the tag chips land', async ({
    page,
    api,
  }) => {
    // Enough tags to push the Archived chip past a 412 px row. On a reload
    // they reach the row late, from IndexedDB, after the row had already
    // scrolled its pressed chip in — and pushed it back out (finding 6).
    api.notes['roof-repair']!.tags = ['house', 'garden', 'insurance', 'roofer quotes'];
    api.notes['reading-list']!.tags = ['books', 'to read'];
    await page.goto('/');
    await expect(page.getByRole('button', { name: /roof repair/i })).toBeVisible();

    await page.goto('/?view=archived');
    await expect(page.getByRole('button', { name: 'roofer quotes' })).toBeVisible();
    const archived = page.getByRole('button', { name: /^Archived/ });
    await expect(archived).toHaveAttribute('aria-pressed', 'true');
    await expect(archived).toBeInViewport();
  });
});

test('the archive can be emptied: select all, delete forever, confirm', async ({ page, api }) => {
  await page.goto('/?view=archived');
  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();

  await startSelecting(page, /old fence/i);
  await page.getByRole('button', { name: 'Select all' }).click();
  await page.getByRole('button', { name: 'Delete forever' }).click();

  // A handful of notes: a plain confirm. More than ten would be a held press.
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('textbox')).toHaveCount(0);
  await dialog.getByRole('button', { name: 'Delete them forever' }).click();

  await expect(page.getByText(/nothing is archived/i)).toBeVisible();
  expect(api.notes['old-fence']).toBeUndefined();
  expect(api.notes['stray-thought']).toBeUndefined();
  // The active notes were never touched.
  expect(api.notes['roof-repair']).toBeDefined();
});

test('emptying an archive of more than ten notes takes a held press: a click does nothing, a second held does', async ({
  page,
  api,
}) => {
  for (let i = 0; i < 11; i += 1) {
    api.notes[`stale-${String(i)}`] = {
      id: `stale-${String(i)}`,
      title: `Stale ${String(i)}`,
      body: 'Nothing came of it.',
      snippet: 'Nothing came of it.',
      tags: [],
      aliases: [],
      updated_at: new Date(Date.UTC(2026, 5, 1 + i)).toISOString(),
      version: 1,
      archived: true,
      captures: [],
    };
  }
  await page.goto('/?view=archived');
  await expect(page.getByRole('button', { name: /old fence/i })).toBeVisible();

  await startSelecting(page, /old fence/i);
  await page.getByRole('button', { name: 'Select all' }).click();
  await expect(page.getByText('13 selected')).toBeVisible();
  await page.getByRole('button', { name: 'Delete forever' }).click();

  const dialog = page.getByRole('dialog');
  const confirm = dialog.getByRole('button', { name: 'Hold to delete 13 notes' });
  // A click is a tap, and a tap is not a hold.
  await confirm.click();
  await expect(dialog).toBeVisible();
  expect(api.purged).toEqual([]);

  // A real mouse button, held for the second: `touch-action`, the CSS
  // variable the fill runs on and the pointer's click afterwards, all in a
  // browser rather than jsdom.
  const box = (await confirm.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await expect(confirm).toHaveAttribute('data-holding', 'true');
  await page.waitForTimeout(1100);
  await page.mouse.up();

  await expect(dialog).toHaveCount(0);
  await expect(page.getByText(/nothing is archived/i)).toBeVisible();
  expect(api.purged).toHaveLength(13);
  expect(api.notes['roof-repair']).toBeDefined();
});

test('escape closes the delete dialog without deleting anything', async ({ page, api }) => {
  await page.goto('/notes/old-fence');

  await noteAction(page, 'Delete forever');
  await expect(page.getByRole('dialog')).toBeVisible();

  await page.keyboard.press('Escape');

  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(api.purged).toEqual([]);
  expect(api.notes['old-fence']).toBeDefined();
});

test('with a mouse, pressing and holding a row starts the selection; a click still opens it', async ({
  page,
}) => {
  await page.goto('/');
  const row = page.getByRole('button', { name: /reading list/i });
  await expect(row).toBeVisible();
  // No checkbox waits at the row's edge for the pointer any more.
  await row.hover();
  await expect(page.getByRole('checkbox')).toHaveCount(0);

  const box = (await row.boundingBox())!;
  await page.mouse.move(box.x + 40, box.y + 20);
  await page.mouse.down();
  await page.waitForTimeout(650);
  await page.mouse.up();

  await expect(page.getByRole('toolbar', { name: 'Bulk actions' })).toBeVisible();
  await expect(page.getByText('1 selected')).toBeVisible();
  await expect(page).toHaveURL(/\/$/);
  await page.keyboard.press('Escape');
  await expect(page.getByRole('toolbar', { name: 'Bulk actions' })).toHaveCount(0);

  // A plain click is still the way into the note.
  await page.getByRole('button', { name: /reading list/i }).click();
  await expect(page).toHaveURL(/\/notes\/reading-list$/);
});

test('on a phone, a long press on a row starts the selection', async ({ page }) => {
  await page.setViewportSize({ width: 412, height: 915 });
  await page.goto('/');
  await expect(page.getByRole('button', { name: /reading list/i })).toBeVisible();
  // By class and text rather than role: the row is a button until the press
  // fires and a labelled checkbox afterwards, and the finger is still on it.
  const row = page.locator('.note-row', { hasText: 'Reading list' });

  // A finger, held: pointer events with a touch pointer type, no click until
  // it lifts. `page.touchscreen` can only tap, so the gesture is dispatched.
  await row.dispatchEvent('pointerdown', {
    pointerType: 'touch',
    clientX: 40,
    clientY: 40,
    isPrimary: true,
    bubbles: true,
  });
  await page.waitForTimeout(650);
  await row.dispatchEvent('pointerup', { pointerType: 'touch', bubbles: true });
  await row.dispatchEvent('click', { bubbles: true });

  await expect(page.getByRole('toolbar', { name: 'Bulk actions' })).toBeVisible();
  await expect(page.getByText('1 selected')).toBeVisible();
  // The press selected the row; it did not open the note.
  await expect(page).toHaveURL(/\/$/);

  await page.keyboard.press('Escape');
  await expect(page.getByRole('toolbar', { name: 'Bulk actions' })).toHaveCount(0);
});
