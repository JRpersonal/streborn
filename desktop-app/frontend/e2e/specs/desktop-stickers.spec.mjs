// desktop-stickers: the sticker sheet for the speakers (src/stickers/).
//
// The dialog reads every STR speaker, paints the sheet as it will print and
// hands the pages to SaveStickerPDF. Covered: the button by the keys in both
// layouts, the link under the phone QR code in Settings, who is on the sheet
// and who is named as left off, the white/dark switch and that it is
// remembered, what Save sends, the out-of-date marker after a key changes,
// and the one-time hint after the first saved preset.

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';

const btn = (page) => page.locator('#stickerPrintBtn');
const modal = (page) => page.locator('#stickerModal');
const pages = (page) => page.locator('#stickerPreview canvas.sticker-page');
const saveBtn = (page) => page.locator('#stickerSave');

async function openFromKeys(page) {
  await btn(page).click();
  await expect(modal(page)).toBeVisible();
  await expect(saveBtn(page)).toBeEnabled();
}

// Puts the first station of the top list on key 3, the way a user does.
async function saveAPreset(page, mock) {
  await mock.resetCalls();
  const row = page.locator('#searchResults .result-row').first();
  // The list stays open after a save; ask for it only when it is not there.
  if (!(await row.count())) await page.locator('#topBtn').click();
  await expect(row).toBeVisible();
  await row.locator('.pick').click();
  await page.locator('#pickGrid .pick-slot').nth(2).click();
  await mock.expectCalled('SetPreset', { times: 1 });
  await expect(page.locator('#pickModal')).toBeHidden();
}

// inkAt reads one pixel of the first page, at a position given in mm.
const inkAt = (page, xMm, yMm) => page.evaluate(([x, y]) => {
  const c = document.querySelector('#stickerPreview canvas.sticker-page');
  const k = c.width / 210;
  return [...c.getContext('2d').getImageData(Math.round(x * k), Math.round(y * k), 1, 1).data].slice(0, 3);
}, [xMm, yMm]);

for (const ui2 of [false, true]) {
  test.describe(ui2 ? 'UI 2.0 preview' : 'classic layout', () => {
    test.use({ ui2 });

    test('the button by the keys opens the sheet for every speaker that answers', async ({ app, mock }) => {
      const { page } = app;
      await expect(btn(page)).toBeVisible();
      await expect(btn(page)).toHaveAccessibleName(en('stickers.btn'));
      await openFromKeys(page);

      await expect(modal(page).getByRole('dialog')).toHaveAccessibleName(en('stickers.title'));
      // Bedroom and Living Room run STR and answer. The office speaker is
      // offline and named; the Bose original is not a candidate at all.
      await expect(page.locator('.sticker-speaker strong')).toHaveText(['Bedroom', 'Living Room']);
      await expect(page.locator('.sticker-skipped')).toHaveText([en('stickers.skipped', { name: 'Office' })]);
      await expect(modal(page)).not.toContainText('Kitchen');
      await mock.expectCalled('GetPresets', { pred: (c) => c.args[0] === '192.0.2.52' });
      expect(await mock.count('GetPresets', (c) => c.args[0] === '192.0.2.53')).toBe(0);

      // An ST10 and an ST20 sticker fit one page, painted at A4 shape.
      await expect(pages(page)).toHaveCount(1);
      await expect(pages(page).first()).toHaveAccessibleName(en('stickers.pageN', { n: 1, total: 1 }));
      const shape = await pages(page).first().evaluate((c) => c.width / c.height);
      expect(shape).toBeCloseTo(210 / 297, 2);
    });
  });
}

test('the housing colour switch repaints the sticker and is remembered', async ({ app }) => {
  const { page } = app;
  await openFromKeys(page);
  // The first speaker is the bedroom's SoundTouch 10.
  const first = page.locator('.sticker-speaker').first();
  await expect(first.getByRole('radio', { name: en('stickers.dark') })).toBeChecked();
  // A point on its sticker's background, left of the first key.
  const dark = await inkAt(page, 19, 40);
  expect(dark).toEqual([0x14, 0x15, 0x18]);

  await first.getByRole('radio', { name: en('stickers.white') }).check();
  await expect.poll(() => inkAt(page, 19, 40)).toEqual([0xE5, 0xDC, 0xC8]);

  await page.locator('#stickerClose').click();
  await expect(modal(page)).toBeHidden();
  await openFromKeys(page);
  await expect(page.locator('.sticker-speaker').first().getByRole('radio', { name: en('stickers.white') })).toBeChecked();
  await expect(page.locator('.sticker-speaker').nth(1).getByRole('radio', { name: en('stickers.dark') })).toBeChecked();
});

test('Save sends one A4 page image per page and says where the file went', async ({ app, mock }) => {
  const { page } = app;
  await openFromKeys(page);
  await mock.resetCalls();
  await saveBtn(page).click();
  await mock.expectCalled('SaveStickerPDF', { times: 1 });
  const call = await mock.last('SaveStickerPDF');
  expect(call.args[0]).toHaveLength(1);
  expect(call.args[0][0].startsWith('data:image/png;base64,')).toBe(true);
  // A4 at 300 dpi, read from the PNG header.
  const size = await page.evaluate(async (uri) => {
    const img = new Image();
    img.src = uri;
    await img.decode();
    return [img.naturalWidth, img.naturalHeight];
  }, call.args[0][0]);
  expect(size).toEqual([2480, 3508]);
  await expect(page.locator('#stickerStatus')).toHaveText(en('stickers.saved', { path: '/home/demo/Downloads/st-reborn-stickers.pdf' }));
});

test('a cancelled save dialog leaves nothing behind', async ({ app, mock }) => {
  const { page } = app;
  await mock.set('SaveStickerPDF', { value: '' });
  await openFromKeys(page);
  await saveBtn(page).click();
  await mock.expectCalled('SaveStickerPDF', { times: 1 });
  await expect(saveBtn(page)).toBeEnabled();
  await expect(page.locator('#stickerStatus')).toHaveText('');
  expect(await page.evaluate(() => localStorage.getItem('str.stickerPrinted'))).toBeNull();
});

test('a save that fails says so and can be tried again', async ({ app, mock }) => {
  const { page } = app;
  await mock.set('SaveStickerPDF', { error: 'disk full' });
  await openFromKeys(page);
  await saveBtn(page).click();
  await expect(page.locator('#stickerStatus')).toContainText('disk full');
  await expect(saveBtn(page)).toBeEnabled();
});

test('the button is marked once a key differs from the saved sheet', async ({ app, mock }) => {
  const { page } = app;
  const dot = page.locator('#stickerStaleDot');
  await expect(dot).toBeHidden();
  await openFromKeys(page);
  await saveBtn(page).click();
  await expect(page.locator('#stickerStatus')).toContainText('st-reborn-stickers.pdf');
  await page.locator('#stickerClose').click();
  await expect(dot).toBeHidden();

  // Another station goes on key 3; the read of the keys after it notices.
  await saveAPreset(page, mock);
  await expect(dot).toBeVisible();
  await expect(btn(page)).toHaveAccessibleName(new RegExp(en('stickers.stale')));

  // Saving the sheet again clears it.
  await openFromKeys(page);
  await saveBtn(page).click();
  await expect(page.locator('#stickerStatus')).toContainText('st-reborn-stickers.pdf');
  await page.locator('#stickerClose').click();
  await expect(dot).toBeHidden();
});

test('Escape closes the dialog and gives the focus back to the button', async ({ app }) => {
  const { page } = app;
  await openFromKeys(page);
  await page.keyboard.press('Escape');
  await expect(modal(page)).toBeHidden();
  await expect(btn(page)).toBeFocused();
});

test('the link under the phone QR code in Settings opens the same sheet', async ({ app }) => {
  const { page } = app;
  await page.locator('.tab-btn[data-view="settings"]').click();
  const link = page.locator('#phoneStickersLink');
  await expect(link).toHaveText(en('stickers.settingsLink'));
  await link.click();
  await expect(modal(page)).toBeVisible();
  await expect(pages(page)).toHaveCount(1);
});

test.describe('the one-time hint', () => {
  const hint = (page) => page.locator('#stickerHint');

  test('is not there before a preset was saved', async ({ app }) => {
    await expect(hint(app.page)).toBeHidden();
  });

  test('shows after the first saved preset and stays away once closed', async ({ app, mock }) => {
    const { page } = app;
    await saveAPreset(page, mock);
    await expect(hint(page)).toBeVisible();
    await expect(hint(page)).toContainText(en('stickers.hint'));
    await hint(page).getByRole('button', { name: en('common.close') }).click();
    await expect(hint(page)).toBeHidden();
    expect(await page.evaluate(() => localStorage.getItem('str.stickerHint'))).toBe('done');
    // The next save does not bring it back.
    await saveAPreset(page, mock);
    await expect(page.locator('#toast')).toBeVisible();
    await expect(hint(page)).toBeHidden();
  });

  test('opening the stickers from it counts as seen', async ({ app, mock }) => {
    const { page } = app;
    await saveAPreset(page, mock);
    await hint(page).getByRole('button', { name: en('stickers.btn') }).click();
    await expect(modal(page)).toBeVisible();
    await expect(hint(page)).toBeHidden();
    expect(await page.evaluate(() => localStorage.getItem('str.stickerHint'))).toBe('done');
  });

  test('is never offered to someone who already opened the stickers', async ({ app, mock }) => {
    const { page } = app;
    await openFromKeys(page);
    await page.locator('#stickerClose').click();
    await saveAPreset(page, mock);
    await expect(page.locator('#toast')).toBeVisible();
    await expect(hint(page)).toBeHidden();
  });
});
