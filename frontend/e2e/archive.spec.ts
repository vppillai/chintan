import type { Page } from '@playwright/test';

import { LONG_PRESS_MS } from '../src/hooks/useLongPress.ts';

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
 * like that UX"). Delete on Home asks first — "Delete “<title>”?", Cancel
 * under Enter (owner, 2026-09-27: "ask are you sure, don't directly
 * archive") — then archives and offers Undo in a toast; Delete forever, in
 * the archive, is a plain confirm of its own.
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

test('Delete on a note asks first, archives on the answer, and Undo on the library restores it', async ({
  page,
  api,
}) => {
  await page.goto('/notes/roof-repair');
  await expect(page.getByRole('textbox', { name: 'Note title' })).toHaveValue('Roof repair');

  await noteAction(page, 'Delete');

  // The question names the note and says where it goes; Enter lands on
  // Cancel, so the accidental Delete backs out with nothing gone.
  const dialog = page.getByRole('dialog', { name: 'Delete “Roof repair”?' });
  await expect(dialog).toContainText('It is kept in the Archive for 30 days, then gone for good.');
  await expect(dialog.getByRole('textbox')).toHaveCount(0);
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(dialog).toHaveCount(0);
  await expect(page).toHaveURL(/\/notes\/roof-repair$/);
  expect(api.notes['roof-repair']?.archived).toBe(false);

  await noteAction(page, 'Delete');
  await dialog.getByRole('button', { name: 'Delete' }).click();

  // Back on the library: the app is not left sitting on a note that is gone.
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
  // The row's ⋮ has Pin and Delete — no separate Archive on Home, and no Select.
  await expect(page.getByRole('menuitem')).toHaveText(['Pin', 'Delete']);
  await page.getByRole('menuitem', { name: 'Delete' }).click();
  await page.getByRole('dialog', { name: 'Delete “Roof repair”?' }).getByRole('button', { name: 'Delete' }).click();

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

test('escape closes the delete dialog without deleting anything', async ({ page, api }) => {
  await page.goto('/notes/old-fence');

  await noteAction(page, 'Delete forever');
  await expect(page.getByRole('dialog')).toBeVisible();

  await page.keyboard.press('Escape');

  await expect(page.getByRole('dialog')).toHaveCount(0);
  expect(api.purged).toEqual([]);
  expect(api.notes['old-fence']).toBeDefined();
});

/**
 * There is no selection mode (owner, 2026-09-29): a row is a plain button on
 * every pointer, and the actions are on the row — its ⋮, its swipe tray. A
 * hold on it is not a gesture the app answers, so its release is a click.
 */
test('a row is a plain button: a held mouse press opens it, and no checkbox appears', async ({
  page,
}) => {
  await page.goto('/');
  const row = page.getByRole('button', { name: /reading list/i });
  await expect(row).toBeVisible();
  await row.hover();
  await expect(page.getByRole('checkbox')).toHaveCount(0);

  const box = (await row.boundingBox())!;
  await page.mouse.move(box.x + 40, box.y + 20);
  await page.mouse.down();
  await page.waitForTimeout(LONG_PRESS_MS + 100);
  // Held past the recordings' long-press duration, the row is still a row.
  await expect(page.getByRole('checkbox')).toHaveCount(0);
  await expect(page.getByRole('toolbar')).toHaveCount(0);
  await page.mouse.up();

  await expect(page).toHaveURL(/\/notes\/reading-list$/);
});
