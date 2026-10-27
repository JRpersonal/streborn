// desktop-preset-row2: the second row of six keys (#1110), played by pressing
// a hardware key twice quickly. Slot 6+n is key n pressed twice; the app never
// shows 7 to 12, it says "Key 1 · 2×".
//
// Covered in the classic layout (the UI 2.0 preview wraps the same grid):
// the Once | Twice tabs and their keyboard model, the explainer of an empty
// second row and its way to the station search, the "New" marker, putting a
// station on "Key 2 · 2×" from the search, the lit key and the now-playing
// line while a row-2 key plays, copying both rows to another speaker (and row
// 1 only to one without the second row), and a speaker whose agent predates
// the second row showing exactly the classic grid.

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { en } from '../helpers/i18n.mjs';
import { PRESETS, PRESETS_ROW2, STATIONS } from '../../testkit/demo-data.mjs';

const LIVING = 'demobox.local';
const SPEAKER = 'Living Room';

test.use({ ui2: false });

const tabs = (page) => page.locator('#presetRowTabs [role="tab"]');
const onceTab = (page) => page.locator('#presetRowTab1');
const twiceTab = (page) => page.locator('#presetRowTab2');
const names = (page) => page.locator('#presets .preset .name');

test.describe('a speaker with the second row of keys', () => {
  test('Once | Twice are tabs named with the speaker, and the keyboard moves between them', async ({ app }) => {
    const { page } = app;
    const list = page.getByRole('tablist', { name: en('preset.row2.tablistName', { speaker: SPEAKER }) });
    await expect(list).toBeVisible();
    await expect(tabs(page)).toHaveCount(2);
    await expect(onceTab(page)).toHaveAttribute('aria-selected', 'true');
    await expect(onceTab(page)).toHaveAttribute('tabindex', '0');
    await expect(twiceTab(page)).toHaveAttribute('tabindex', '-1');
    await expect(twiceTab(page)).toHaveAccessibleName(new RegExp(en('preset.row2.tabTwiceName', { speaker: SPEAKER })));
    // Two keys pressed twice hold something: the tab says how many.
    await expect(twiceTab(page).locator('.prt-count')).toHaveText('2');
    await expect(page.locator('#presets')).toHaveAttribute('role', 'tabpanel');
    await expect(names(page)).toHaveText(PRESETS.map((p) => p.name));

    await onceTab(page).focus();
    await page.keyboard.press('ArrowRight');
    await expect(twiceTab(page)).toBeFocused();
    await expect(twiceTab(page)).toHaveAttribute('aria-selected', 'true');
    await expect(onceTab(page)).toHaveAttribute('tabindex', '-1');
    await expect(page.locator('#presets')).toHaveAttribute('aria-labelledby', 'presetRowTab2');
    await expect(page.locator('#presets .preset')).toHaveCount(6);
    await expect(page.locator('#presets .preset .num').first()).toHaveText(/Key 1/);
    await expect(page.locator('#presets .preset').first()).toContainText(en('preset.row2.key', { n: 1 }));
    await expect(page.locator('#presets')).toContainText(PRESETS_ROW2[0].name);
    await expect(page.locator('#presets')).not.toContainText(/Key (7|8|9|10|11|12)\b/);

    await page.keyboard.press('Home');
    await expect(onceTab(page)).toBeFocused();
    await expect(onceTab(page)).toHaveAttribute('aria-selected', 'true');
    await page.keyboard.press('End');
    await expect(twiceTab(page)).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(onceTab(page)).toBeFocused();
    await expect(names(page)).toHaveText(PRESETS.map((p) => p.name));
  });

  test('a row-2 key plays slot 6+n, lights in the Twice tab, and the line names it', async ({ app, mock }) => {
    const { page } = app;
    await twiceTab(page).click();
    await expect(page.locator('#presets')).toContainText(PRESETS_ROW2[0].name);
    await mock.resetCalls();
    await page.locator('#presets .preset-body').first().click();
    await mock.expectCalled('PlaySlot', { times: 1, pred: onHost(LIVING) });
    expect((await mock.last('PlaySlot')).args).toEqual([LIVING, 8888, 7]);
    await expect(page.locator('#presets .preset.playing')).toHaveCount(1);
    await expect(page.locator('#presets .preset.playing')).toContainText(PRESETS_ROW2[0].name);
    await expect(page.locator('#statusMain .now-key')).toHaveText(en('preset.row2.key', { n: 1 }));
    // Back on Once, the Twice tab carries the playing marker, in words too.
    await onceTab(page).click();
    await expect(page.locator('#presets .preset.playing')).toHaveCount(0);
    await expect(twiceTab(page).locator('.prt-playing')).toBeVisible();
    await expect(twiceTab(page)).toHaveAccessibleName(new RegExp(en('preset.row2.tabPlayingName')));
  });

  test('"Put on key" switches rows and puts a station on Key 2 · 2×', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#topBtn').click();
    const row = page.locator('#searchResults .result-row').first();
    await expect(row).toBeVisible();
    await mock.resetCalls();
    await row.locator('.pick').click();
    const dialog = page.getByRole('dialog', { name: en('preset.assignStationTitle') });
    await expect(dialog).toBeVisible();
    // Opens on the row the grid shows (Once), with the switch above the slots.
    const switcher = page.locator('#pickRows');
    await expect(switcher).toHaveAttribute('role', 'tablist');
    await expect(page.locator('#pickRowTab1')).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#pickGrid .pick-slot').first()).toBeFocused();
    await expect(page.locator('#pickGrid .pick-slot').first()).toContainText(PRESETS[0].name);
    await page.locator('#pickRowTab1').focus();
    await page.keyboard.press('ArrowRight');
    await expect(page.locator('#pickRowTab2')).toBeFocused();
    await expect(page.locator('#pickRowTab2')).toHaveAttribute('aria-selected', 'true');
    // Each slot shows what is on it: key 1 pressed twice holds a station, key 2 is empty.
    await expect(page.locator('#pickGrid .pick-slot').nth(0)).toContainText(PRESETS_ROW2[0].name);
    const key2 = page.locator('#pickGrid .pick-slot').nth(1);
    await expect(key2).toContainText(en('preset.row2.key', { n: 2 }));
    await expect(key2).toContainText(en('preset.pickEmpty'));
    await key2.click();
    await mock.expectCalled('SetPreset', { times: 1 });
    const call = await mock.last('SetPreset');
    expect(call.args[0]).toBe(LIVING);
    expect(call.args[2]).toBe(8);
    await expect(page.locator('#pickModal')).toBeHidden();
    await expect(page.locator('#toast')).toContainText(en('preset.savedToKey', { n: '2 · 2×', name: call.args[3] }));
    // The grid follows, on the Twice tab, with the count up by one.
    await twiceTab(page).click();
    await expect(page.locator('#presets .preset').nth(1)).toContainText(call.args[3]);
    await expect(twiceTab(page).locator('.prt-count')).toHaveText('3');
    expect(STATIONS.map((s) => s.name)).toContain(call.args[3]);
  });

});

test.describe('copying to a speaker with the second row', () => {
  test.use({ scenario: { boxPatches: { bedroom: { presetRows: '2' } } } });

  test('carries both rows and says both are replaced', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await page.locator('#presetTransferBtn').click();
    await expect(page.locator('#warnBody')).toContainText(en('settingsView.copyPresetsHelpAll'));
    await expect(page.locator('#warnBody')).toContainText(en('presets.transferReplacesBoth'));
    await page.locator('#presetXferTarget').selectOption('192.0.2.52|8888');
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('CopyPresetsAcrossBoxes', { times: 1 });
    expect((await mock.last('CopyPresetsAcrossBoxes')).args).toEqual([LIVING, 8888, '192.0.2.52', 8888]);
    await expect(page.locator('#toast')).toHaveText(en('settingsView.copyPresetsDone', { n: 8, target: 'Bedroom' }));
  });
});

test.describe('copying to a speaker without the second row', () => {
  test.use({ scenario: { boxPatches: { bedroom: { presetRows: '' } } } });

  test('copies row 1 only and says so', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#presetTransferBtn').click();
    await page.locator('#presetXferTarget').selectOption('192.0.2.52|8888');
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('CopyPresetsAcrossBoxes', { times: 1 });
    await expect(page.locator('#toast')).toHaveText(
      en('settingsView.copyPresetsDone', { n: 6, target: 'Bedroom' }) + ' ' + en('settingsView.copyPresetsRow1Only', { target: 'Bedroom' }),
    );
  });
});

test.describe('an empty second row', () => {
  test.use({ scenario: { presets: { livingRoom: PRESETS } } });

  test('the Twice tab is marked New until opened, explains itself and leads to the search', async ({ app }) => {
    const { page } = app;
    await expect(twiceTab(page).locator('.prt-new')).toHaveText(en('preset.row2.new'));
    await expect(twiceTab(page).locator('.prt-count')).toHaveCount(0);
    await expect(page.locator('#presetRow2Intro')).toBeHidden();
    await twiceTab(page).click();
    const intro = page.locator('#presetRow2Intro');
    await expect(intro).toBeVisible();
    await expect(intro).toContainText(en('preset.row2.introBody'));
    await expect(page.locator('#presets .preset.empty')).toHaveCount(6);
    // Opened once: the marker is gone, and the app remembers it.
    await expect(twiceTab(page).locator('.prt-new')).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem('str.presetRow2Seen'))).toBe('1');
    await intro.getByRole('button', { name: en('preset.row2.introSearch') }).click();
    await expect(page.locator('#searchQ')).toBeFocused();
  });
});

test.describe('a speaker whose agent predates the second row', () => {
  test.use({ scenario: { boxPatches: { livingRoom: { presetRows: '' } } } });

  test('shows the classic grid: no tabs, no explainer, no switch in "Put on key"', async ({ app }) => {
    const { page } = app;
    await expect(names(page)).toHaveText(PRESETS.map((p) => p.name));
    await expect(page.locator('#presetRowTabs')).toBeHidden();
    await expect(page.getByRole('tablist')).toHaveCount(0);
    await expect(page.locator('#presets')).not.toHaveAttribute('role', 'tabpanel');
    await expect(page.locator('#presetRow2Intro')).toBeHidden();
    await page.locator('#topBtn').click();
    await page.locator('#searchResults .result-row').first().locator('.pick').click();
    await expect(page.locator('#pickGrid .pick-slot')).toHaveCount(6);
    await expect(page.locator('#pickRows')).toBeHidden();
    await expect(page.locator('#pickGrid')).not.toContainText('2×');
  });
});
