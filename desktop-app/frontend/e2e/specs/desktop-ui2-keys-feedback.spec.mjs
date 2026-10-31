// The key tiles of the UI 2.0 preview report in their own line (#1286 phase 5,
// docs/UI-2.0.md rules 2 and 3): a recall the speaker refused, a hold-to-save,
// a rename and the Transfer say what happened where it happened, named with
// the key and the speaker, done only after the speaker's read-back, and the
// keyboard stays on the key. The last block checks the current layout.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-keys-feedback.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { PRESETS, ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const tile = (page, slot) => page.locator(`#presets .preset[data-slot="${slot}"]`);
const line = (page, slot) => tile(page, slot).locator('.ui2-key-status');

// holdKey presses a key tile for longer than the 1.1 s hold that saves what
// plays, on the page clock (the fakeClock option).
async function holdKey(page, slot) {
  await tile(page, slot).locator('.preset-body').hover();
  await page.mouse.down();
  await runFor(page, 1_300);
  await page.mouse.up();
}

// Key 6 is not one of STR's keys but the speaker's own (a Deezer mix), which
// a press recalls through the speaker's hardware key.
const NATIVE = {
  presets: { livingRoom: PRESETS.filter((p) => p.slot !== 6) },
  bindings: {
    BoxPresets: { value: [{ slot: 6, name: 'Deezer Mix', source: 'DEEZER', location: 'deezer:mix' }] },
    RecallBoxPreset: { error: 'status 503: speaker busy' },
  },
};

test.describe('UI 2.0 key tiles', () => {
  test.use({ ui2: true });

  test.describe('a recall the speaker refuses', () => {
    test.use({ scenario: NATIVE });

    test('1. says so in the key, not in the error dialog, and the keyboard stays on the key', async ({ app, mock }) => {
      const { page } = app;
      const body = tile(page, 6).locator('.preset-body');
      await expect(body).toHaveAttribute('role', 'button');
      await expect(line(page, 6)).toHaveAttribute('role', 'status');
      await expect(line(page, 6)).toHaveText('');
      await body.focus();
      await page.keyboard.press('Enter');
      await mock.expectCalled('RecallBoxPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 6 });
      await expect(line(page, 6)).toContainText(`Key 6 did not start on ${LR.friendlyName}.`);
      await expect(line(page, 6)).toHaveClass(/is-error/);
      await expect(page.locator('#errorModal')).toBeHidden();
      await expect.poll(() => page.evaluate(() => {
        const a = document.activeElement;
        return a && a.closest('.preset') ? a.closest('.preset').dataset.slot : '';
      })).toBe('6');
      // The other keys say nothing.
      await expect(line(page, 3)).toHaveText('');
    });
  });

  test.describe('hold-to-save', () => {
    test.use({ fakeClock: true });

    test('2. says done only after the speaker read the key back', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await expect(tile(page, 4)).toContainText('Jazz24');
      // The read-back takes a moment, so the line can be seen before it.
      await mock.set('GetPresets', { delayMs: 1500 });
      await mock.resetCalls();
      await holdKey(page, 4);
      // Living Room plays its key 2, so the hold copies key 2 onto key 4.
      await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 4 });
      await expect(line(page, 4)).toHaveText(en('ui2.radio.saving', { n: 4, speaker: LR.friendlyName }));
      await runFor(page, 2_000);
      await expect(line(page, 4)).toHaveText(en('ui2.radio.saved', { n: 4, speaker: LR.friendlyName }));
      await expect(line(page, 4)).toHaveClass(/is-ok/);
      await expect(tile(page, 4)).toContainText('Best Of Rock.FM');
      await expect(page.locator('#toast')).not.toHaveClass(/show/);
    });
  });

  test.describe('a save the speaker does not keep', () => {
    // The speaker answers the save but its list still has the old station.
    test.use({ fakeClock: true, scenario: { bindings: { SetPreset: { value: null } } } });

    test('3. is not called done', async ({ app }) => {
      const { page } = app;
      await settle(page, 9_000);
      await holdKey(page, 4);
      await runFor(page, 1_000);
      await expect(line(page, 4)).toHaveText(en('ui2.radio.notConfirmed', { n: 4, speaker: LR.friendlyName }));
      await expect(line(page, 4)).toHaveClass(/is-warn/);
      await expect(page.locator('#toast')).not.toHaveClass(/show/);
    });
  });

  test('4. a rename from the keyboard is confirmed in the key, and the keyboard is back on its rename icon', async ({ app, mock }) => {
    const { page } = app;
    // The speaker renames the key while the call is out (the mock has no
    // rename of its own).
    await mock.set('RenamePreset', { delayMs: 600, value: null });
    const ren = tile(page, 3).locator('.ren');
    await ren.focus();
    await page.keyboard.press('Enter');
    const input = page.locator('#presetRenameInput');
    await expect(input).toBeFocused();
    await input.fill('Morning News');
    await page.evaluate(([host]) => {
      const list = window.__strMock.state.presets[host];
      const p = list && list.find((x) => x.slot === 3);
      if (p) p.name = 'Morning News';
    }, [LR.host]);
    await page.keyboard.press('Enter');
    await mock.expectCalled('RenamePreset', { pred: (c) => c.args[2] === 3 && c.args[3] === 'Morning News' });
    await expect(line(page, 3)).toHaveText(en('ui2.feedback.keyRenamed', { n: 3, speaker: LR.friendlyName, name: 'Morning News' }), { timeout: 10_000 });
    await expect(page.locator('#toast')).not.toHaveClass(/show/);
    await expect.poll(() => page.evaluate(() => {
      const a = document.activeElement;
      return a && a.closest('.preset') ? `${a.closest('.preset').dataset.slot}:${a.classList.contains('ren') ? 'ren' : a.className}` : '';
    })).toBe('3:ren');
  });

  test('5. Transfer reports under its button, naming both speakers', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await page.locator('#presetTransferBtn').click();
    await page.locator('#presetXferTarget').selectOption(`${BR.host}|${BR.port}`);
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('CopyPresetsAcrossBoxes', { pred: (c) => c.args[0] === LR.host && c.args[2] === BR.host });
    const status = page.locator('#ui2XferStatus');
    await expect(status).toHaveAttribute('role', 'status');
    await expect(status).toHaveText(new RegExp(`^${BR.friendlyName} took \\d+ keys of ${LR.friendlyName}\\.`));
    await expect(status).toHaveClass(/is-ok/);
    await expect(page.locator('#toast')).not.toHaveClass(/show/);
  });
});

test.describe('current layout', () => {
  test.describe('a recall the speaker refuses', () => {
    test.use({ scenario: NATIVE });

    test('6. keeps its error dialog and draws no key line', async ({ app }) => {
      const { page } = app;
      const body = tile(page, 6).locator('.preset-body');
      await body.click();
      await expect(page.locator('#errorModal')).toBeVisible();
      await expect(page.locator('#presets .ui2-key-status')).toHaveCount(0);
    });
  });

  test.describe('hold-to-save', () => {
    test.use({ fakeClock: true });

    test('7. keeps its toast', async ({ app }) => {
      const { page } = app;
      await settle(page, 9_000);
      await holdKey(page, 4);
      await runFor(page, 500);
      await expect(page.locator('#toast')).toHaveClass(/show/);
      await expect(page.locator('#presets .ui2-key-status')).toHaveCount(0);
    });
  });
});
