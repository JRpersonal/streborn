// desktop-preset-copy-keys: copying CHOSEN keys to other speakers (#1551).
//
// The Transfer dialog lists every key of this speaker that holds something,
// all ticked. All ticked is the whole-set copy it has always been; a choice of
// keys copies only those and leaves the other keys of the target alone. A
// station already on another key of the target is left out and named (the
// duplicate guard of a save, #836), per speaker. Classic layout and UI 2.0.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-preset-copy-keys.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { PRESETS, ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const keyBoxes = (page) => page.locator('#presetXferKeys input[type="checkbox"]');
const keyBox = (page, slot) => page.locator(`#presetXferKeys input[type="checkbox"][value="${slot}"]`);

async function openTransfer(page) {
  await page.locator('#presetTransferBtn').click();
  await expect(page.locator('#presetXferKeys')).toBeVisible();
}

// chooseOnly unticks everything with None, then ticks the given keys.
async function chooseOnly(page, slots) {
  await page.locator('#presetXferKeys [data-xfer-keys="none"]').click();
  for (const s of slots) await keyBox(page, s).check();
}

test.describe('classic layout', () => {
  test.use({ ui2: false });

  test('lists every key ticked, and all ticked is the whole copy', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await openTransfer(page);
    await expect(page.locator('#presetXferKeys legend')).toHaveText(en('presets.transferKeysLegend'));
    // Living Room keeps both rows: six keys pressed once and two pressed twice.
    await expect(keyBoxes(page)).toHaveCount(8);
    for (const box of await keyBoxes(page).all()) await expect(box).toBeChecked();
    await expect(page.locator('#presetXferKeys')).toContainText(en('preset.row2.key', { n: 1 }));
    await expect(page.locator('#presetXferKeys')).toContainText(en('presets.transferKeysNote'));
    await page.locator('#presetXferTarget').selectOption(`${BR.host}|${BR.port}`);
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('CopyPresetsAcrossBoxes', { times: 1 });
    expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
  });

  test('copies one key and leaves the others alone', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await openTransfer(page);
    await chooseOnly(page, [2]);
    await page.locator('#presetXferTarget').selectOption(`${BR.host}|${BR.port}`);
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('CopyPresetKeysAcrossBoxes', { times: 1 });
    expect((await mock.last('CopyPresetKeysAcrossBoxes')).args).toEqual([LR.host, LR.port, BR.host, BR.port, [2]]);
    expect(await mock.count('CopyPresetsAcrossBoxes')).toBe(0);
    await expect(page.locator('#toast')).toHaveText(en('presets.keyCopyCopied', { target: BR.friendlyName, keys: en('preset.key', { n: 2 }) }));
  });

  test.describe('a station already on another key of the target', () => {
    // Bedroom holds BBC Radio 1 (Living Room's key 1) on its key 5.
    test.use({ scenario: { presets: { bedroom: [{ ...PRESETS[0], slot: 5 }] } } });

    test('is left out and named, and the other chosen key still goes', async ({ app, mock }) => {
      const { page } = app;
      await openTransfer(page);
      await chooseOnly(page, [1, 3]);
      await page.locator('#presetXferTarget').selectOption(`${BR.host}|${BR.port}`);
      await page.locator('#warnConfirm').click();
      await mock.expectCalled('CopyPresetKeysAcrossBoxes', { times: 1 });
      await expect(page.locator('#errorModal')).toBeVisible();
      const text = await page.locator('#errorText').inputValue();
      expect(text).toContain(en('presets.keyCopyCopied', { target: BR.friendlyName, keys: en('preset.key', { n: 3 }) }));
      expect(text).toContain(en('presets.keyCopyOnOtherKey', {
        target: BR.friendlyName, key: en('preset.key', { n: 1 }), name: PRESETS[0].name, n: 5,
      }));
      const bedroom = (await mock.state()).presets[BR.host];
      expect(bedroom.find((p) => p.slot === 5).name).toBe(PRESETS[0].name);
      expect(bedroom.find((p) => p.slot === 1)).toBeUndefined();
    });
  });

  test('refuses to copy when no key is ticked', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await openTransfer(page);
    await page.locator('#presetXferKeys [data-xfer-keys="none"]').click();
    for (const box of await keyBoxes(page).all()) await expect(box).not.toBeChecked();
    await page.locator('#warnConfirm').click();
    await expect(page.locator('#toast')).toHaveText(en('presets.transferKeysNone'));
    expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
    expect(await mock.count('CopyPresetsAcrossBoxes')).toBe(0);
  });

  test('copies the chosen key to every other speaker, one answer each', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await openTransfer(page);
    await chooseOnly(page, [4]);
    await page.locator('#presetXferTarget').selectOption('__ALL__');
    await page.locator('#warnConfirm').click();
    const n = await page.locator('#presetXferTarget option').count();
    await mock.expectCalled('CopyPresetKeysAcrossBoxes', { times: n - 1 });
    await expect(page.locator('#toast')).toContainText(en('presets.keyCopyCopied', { target: BR.friendlyName, keys: en('preset.key', { n: 4 }) }));
  });
});

test.describe('UI 2.0', () => {
  test.use({ ui2: true });

  test('the line under Transfer names the speaker and the key', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await openTransfer(page);
    await chooseOnly(page, [2, 7]);
    await page.locator('#presetXferTarget').selectOption(`${BR.host}|${BR.port}`);
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('CopyPresetKeysAcrossBoxes', { pred: (c) => c.args[0] === LR.host && c.args[2] === BR.host });
    const status = page.locator('#ui2XferStatus');
    await expect(status).toHaveAttribute('role', 'status');
    // Bedroom keeps one row here, so the key pressed twice cannot go.
    await expect(status).toHaveText(
      en('presets.keyCopyCopied', { target: BR.friendlyName, keys: en('preset.key', { n: 2 }) }) + ' '
      + en('presets.keyCopyNotKept', { target: BR.friendlyName, keys: en('preset.row2.key', { n: 1 }) }),
    );
    await expect(status).toHaveClass(/is-warn/);
    await expect(page.locator('#toast')).not.toHaveClass(/show/);
  });
});
