// Hold a key to change it, in the UI 2.0 preview (views/music/ui2keyedit.js):
// a short press plays the key, a hold of about 700 ms opens its choices under
// the grid instead (and never also plays), an empty key opens them on a short
// press, and the keyboard gets there with Shift+Enter or the context-menu
// key. Every choice is the flow the key already had: the rename and clear
// icons, the save of what plays, and the station rows' "Put on key" picker,
// which then starts on the key the user came from. The last block checks that
// the current layout kept its hold-to-save.
//
//   E2E_PORT=4184 npx playwright test specs/desktop-ui2-hold-to-edit-key.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { PRESETS, ROSTER, STATIONS } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const JAZZ = STATIONS[3];

const tile = (page, slot) => page.locator(`#presets .preset[data-slot="${slot}"]`);
const body = (page, slot) => tile(page, slot).locator('.preset-body');
const panel = (page) => page.locator('#ui2KeyEdit');
const choice = (page, id) => panel(page).locator(`[data-ke="${id}"]`);
const choices = (page) => panel(page).locator('.ui2-key-edit-act');
const ids = (page) => choices(page).evaluateAll((els) => els.map((el) => el.dataset.ke));
const row = (page, name) => page.locator('#searchResults .ui2-st').filter({ has: page.locator('.ui2-st-name', { hasText: name }) });
const barWidth = (page, slot) => tile(page, slot).locator('.long-press-bar').evaluate((el) => parseFloat(el.style.width || '0'));
const focusedKey = (page) => page.evaluate(() => {
  const a = document.activeElement;
  const t = a && a.closest('#presets .preset');
  return t ? `${t.dataset.slot}:${a.classList.contains('preset-body') ? 'body' : a.className}` : (a && a.id) || '';
});

// Key 5 empty: the rest as the demo speaker has them.
const EMPTY5 = { presets: { livingRoom: PRESETS.filter((p) => p.slot !== 5) } };

test.describe('UI 2.0: hold a key to change it', () => {
  test.use({ ui2: true });

  test.describe('on the page clock', () => {
    test.use({ fakeClock: true });

    test('1. a short press plays the key and opens nothing', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await mock.resetCalls();
      await body(page, 4).hover();
      await page.mouse.down();
      await runFor(page, 100);
      await page.mouse.up();
      await mock.expectCalled('PlaySlot', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 4, times: 1 });
      await expect(panel(page)).toBeHidden();
    });

    test('2. a hold opens the key\'s choices, named with key and speaker, and never also plays', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await expect(body(page, 4)).toContainText('Jazz24');
      await mock.resetCalls();
      await body(page, 4).hover();
      await page.mouse.down();
      await runFor(page, 400);
      // Held, not yet open. The run asks for less motion, so the bar stands
      // full at once instead of growing.
      await expect(tile(page, 4)).toHaveClass(/long-press/);
      expect(await barWidth(page, 4)).toBe(100);
      await expect(panel(page)).toBeHidden();
      await runFor(page, 400);
      await expect(panel(page)).toBeVisible();
      await page.mouse.up();
      await expect(panel(page)).toHaveAttribute('aria-labelledby', 'ui2KeyEditTitle');
      await expect(panel(page).locator('#ui2KeyEditTitle')).toHaveText(en('ui2.radio.keyEditTitle', { n: 4, speaker: LR.friendlyName }));
      await expect(panel(page).locator('.ui2-key-edit-now')).toHaveText(en('ui2.radio.keyEditNow', { name: 'Jazz24' }));
      // Living Room plays, so saving what plays is offered first and has the
      // keyboard; the key's own rename and clear follow.
      expect(await ids(page)).toEqual(['save', 'find', 'favorites', 'rename', 'clear']);
      await expect(choice(page, 'save')).toBeFocused();
      await expect(choice(page, 'save')).toHaveClass(/u-btn--primary/);
      await expect(choice(page, 'close')).toHaveAttribute('aria-label', en('ui2.popover.close'));
      await runFor(page, 2_000);
      expect(await mock.count('PlaySlot')).toBe(0);
      expect(await mock.count('SetPreset')).toBe(0);
      await expect(tile(page, 4)).not.toHaveClass(/long-press/);
      // Escape closes it and puts the keyboard back on the key.
      await page.keyboard.press('Escape');
      await expect(panel(page)).toBeHidden();
      await expect.poll(() => focusedKey(page)).toBe('4:body');
    });

    test('3. with motion allowed the bar fills while the key is held, and a release before the end plays', async ({ app, mock }) => {
      const { page } = app;
      await page.emulateMedia({ reducedMotion: 'no-preference' });
      await settle(page, 9_000);
      await mock.resetCalls();
      await body(page, 4).hover();
      await page.mouse.down();
      await runFor(page, 440);
      const mid = await barWidth(page, 4);
      expect(mid).toBeGreaterThan(20);
      expect(mid).toBeLessThan(80);
      await page.mouse.up();
      await mock.expectCalled('PlaySlot', { pred: (c) => c.args[2] === 4, times: 1 });
      await expect(panel(page)).toBeHidden();
    });
  });

  test('4. Shift+Enter opens the choices from the keyboard; Rename is the key\'s own rename', async ({ app, mock }) => {
    const { page } = app;
    await expect(page.locator('#ui2KeysHint')).toContainText(en('ui2.radio.keysEditHint'));
    await expect(body(page, 3)).toHaveAttribute('aria-keyshortcuts', 'Shift+Enter');
    await expect(body(page, 3)).toHaveAttribute('aria-describedby', 'ui2KeysHint');
    await mock.resetCalls();
    await body(page, 3).focus();
    await page.keyboard.press('Shift+Enter');
    await expect(panel(page)).toBeVisible();
    await expect(panel(page).locator('#ui2KeyEditTitle')).toHaveText(en('ui2.radio.keyEditTitle', { n: 3, speaker: LR.friendlyName }));
    expect(await mock.count('PlaySlot')).toBe(0);
    await choice(page, 'rename').focus();
    await page.keyboard.press('Enter');
    await expect(panel(page)).toBeHidden();
    const input = page.locator('#presetRenameInput');
    await expect(input).toBeFocused();
    await expect(input).toHaveValue('NPR News');
    await page.locator('#warnCancel').click();
    await expect.poll(() => focusedKey(page)).toBe('3:body');
    expect(await mock.count('RenamePreset')).toBe(0);
  });

  test('5. Clear asks first, through the key\'s own confirmation', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await body(page, 6).focus();
    await page.keyboard.press('Shift+Enter');
    await choice(page, 'clear').click();
    await expect(page.locator('#warnConfirm')).toBeVisible();
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('DeletePreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 6, times: 1 });
    await expect(tile(page, 6).locator('.ui2-key-status')).toHaveText(en('ui2.feedback.keyCleared', { n: 6, speaker: LR.friendlyName }));
  });

  test('6. the context-menu key and a right click open the choices too', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await body(page, 2).focus();
    await page.keyboard.press('ContextMenu');
    await expect(panel(page)).toBeVisible();
    await expect(panel(page).locator('#ui2KeyEditTitle')).toHaveText(en('ui2.radio.keyEditTitle', { n: 2, speaker: LR.friendlyName }));
    await choice(page, 'close').click();
    await expect(panel(page)).toBeHidden();
    await expect.poll(() => focusedKey(page)).toBe('2:body');
    await body(page, 1).click({ button: 'right' });
    await expect(panel(page).locator('#ui2KeyEditTitle')).toHaveText(en('ui2.radio.keyEditTitle', { n: 1, speaker: LR.friendlyName }));
    expect(await mock.count('PlaySlot')).toBe(0);
  });

  test.describe('an empty key', () => {
    test.use({ scenario: EMPTY5 });

    test('7. opens its assignment on a short press, by mouse or keyboard', async ({ app }) => {
      const { page } = app;
      const key5 = tile(page, 5);
      await expect(key5).toHaveClass(/empty/);
      await expect(key5).toHaveAttribute('role', 'button');
      await expect(key5).toHaveAttribute('aria-label', en('ui2.radio.keyEmpty', { n: 5, speaker: LR.friendlyName }));
      await expect(key5).toHaveAttribute('aria-controls', 'ui2KeyEdit');
      await expect(key5).toHaveAttribute('aria-expanded', 'false');
      await key5.focus();
      await page.keyboard.press('Enter');
      await expect(panel(page)).toBeVisible();
      await expect(panel(page).locator('#ui2KeyEditTitle')).toHaveText(en('ui2.radio.keyAssignTitle', { n: 5, speaker: LR.friendlyName }));
      await expect(panel(page).locator('.ui2-key-edit-now')).toHaveText(en('ui2.radio.keyEditEmpty'));
      await expect(panel(page).locator('.ui2-key-edit-note')).toHaveText(en('ui2.radio.keyEditNote', { n: 5 }));
      expect(await ids(page)).toEqual(['save', 'find', 'favorites']);
      await expect(tile(page, 5)).toHaveAttribute('aria-expanded', 'true');
      await page.keyboard.press('Escape');
      await expect(panel(page)).toBeHidden();
      await expect(tile(page, 5)).toHaveAttribute('aria-expanded', 'false');
      await expect.poll(() => page.evaluate(() => document.activeElement && document.activeElement.dataset.slot)).toBe('5');
      await tile(page, 5).click();
      await expect(panel(page)).toBeVisible();
      await expect(choice(page, 'save')).toBeFocused();
    });

    test('8. Find a station leads to Put on key, which starts on that key', async ({ app, mock }) => {
      const { page } = app;
      await tile(page, 5).click();
      await choice(page, 'find').click();
      await expect(panel(page)).toBeHidden();
      await expect(page.locator('.ui2-radio-tab[data-tab="discover"]')).toHaveAttribute('aria-selected', 'true');
      await expect(tile(page, 5).locator('.ui2-key-status')).toHaveText(en('ui2.radio.keyAssignPending', { n: 5 }));
      await expect(page.locator('#searchQ')).toBeFocused();
      await mock.resetCalls();
      await row(page, JAZZ.name).locator('[data-act="key"]').click();
      await expect(page.locator('#pickModal')).toBeVisible();
      await expect(page.locator('#pickGrid .pick-slot[data-slot="5"]')).toBeFocused();
      await page.keyboard.press('Enter');
      await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 && c.args[3] === JAZZ.name, times: 1 });
      await expect(tile(page, 5).locator('.name')).toHaveText(JAZZ.name);
      await expect(tile(page, 5).locator('.ui2-key-status')).not.toHaveText(en('ui2.radio.keyAssignPending', { n: 5 }));
      // Used once: the next picker starts on the first key again.
      await row(page, JAZZ.name).locator('[data-act="key"]').click();
      await expect(page.locator('#pickGrid .pick-slot').first()).toBeFocused();
    });
  });
});

test.describe('current layout', () => {
  test.use({ fakeClock: true });

  test('9. keeps its hold-to-save: no choices panel, and a release before 1.1 s still plays', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await mock.resetCalls();
    await body(page, 4).hover();
    await page.mouse.down();
    await runFor(page, 800);
    await page.mouse.up();
    await expect(page.locator('#ui2KeyEdit')).toHaveCount(0);
    await mock.expectCalled('PlaySlot', { pred: (c) => c.args[2] === 4, times: 1 });
    expect(await mock.count('SetPreset')).toBe(0);
  });
});
