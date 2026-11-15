// desktop-ui2-copy-keys-review: in the UI 2.0 preview, copying keys to other
// speakers first shows what will happen, per speaker and per key (copied,
// already there exactly like this so only the speaker's button is set again,
// replacing another station, skipped because the station is on another key
// there, not possible on a speaker with one row of keys), and copies only
// after Copy. Cancel copies nothing, and so does leaving the review behind
// (another speaker, another view). The review sits in the page under the
// keys' heading, not in a dialog, and works by keyboard. The classic layout
// copies as before.
//
//   E2E_PORT=4186 npx playwright test specs/desktop-ui2-copy-keys-review.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { PRESETS, ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;

// Bedroom, as the review should find it for Living Room's keys 1, 2, 3, 4 and
// key 1 pressed twice (slot 7): key 1 empty, key 2 already the same station,
// key 3 another station, Jazz24 (Living Room's key 4) already on key 5, and
// one row of keys only.
const BEDROOM = [
  { ...PRESETS[1] },
  { slot: 3, name: 'Old Station', stream_url: 'http://example.com/old', type: 'radio' },
  { ...PRESETS[3], slot: 5 },
];
const CHOSEN = [1, 2, 3, 4, 7];

const keyBox = (page, slot) => page.locator(`#presetXferKeys input[type="checkbox"][value="${slot}"]`);
const review = (page) => page.locator('#ui2XferReview');
const line = (page, slot) => review(page).locator(`.ui2-xfer-review-line[data-slot="${slot}"]`);

// startCopy opens Transfer, ticks only the given keys (none for all) and the
// target, and presses Transfer in the dialog.
async function startCopy(page, { slots = null, target = `${BR.host}|${BR.port}` } = {}) {
  await page.locator('#presetTransferBtn').click();
  await expect(page.locator('#presetXferKeys')).toBeVisible();
  if (slots) {
    await page.locator('#presetXferKeys [data-xfer-keys="none"]').click();
    for (const s of slots) await keyBox(page, s).check();
  }
  await page.locator('#presetXferTarget').selectOption(target);
  await page.locator('#warnConfirm').click();
}

test.describe('UI 2.0', () => {
  test.use({ ui2: true });

  test.describe('a copy with every kind of outcome', () => {
    test.use({ scenario: { presets: { bedroom: BEDROOM } } });

    test('1. says per key what will happen before anything is copied', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: CHOSEN });
      await expect(review(page)).toBeVisible();
      await expect(review(page)).toHaveAttribute('aria-labelledby', 'ui2XferReviewTitle');
      await expect(page.getByRole('region', { name: en('ui2.keyReview.title') })).toBeVisible();
      await expect(page.locator('#ui2XferReviewTitle')).toBeFocused();
      await expect(review(page)).toContainText(en('ui2.keyReview.intro', { source: LR.friendlyName }));
      await expect(review(page).locator('h4')).toHaveText([BR.friendlyName]);

      await expect(line(page, 1)).toHaveText(en('ui2.keyReview.copy', { key: en('preset.key', { n: 1 }), name: PRESETS[0].name }));
      await expect(line(page, 2)).toHaveText(en('ui2.keyReview.same', { key: en('preset.key', { n: 2 }), name: PRESETS[1].name }));
      await expect(line(page, 3)).toHaveText(en('ui2.keyReview.replace', { key: en('preset.key', { n: 3 }), name: PRESETS[2].name, old: 'Old Station' }));
      await expect(line(page, 3)).toHaveClass(/is-replace/);
      await expect(line(page, 4)).toHaveText(en('ui2.keyReview.onOtherKey', { key: en('preset.key', { n: 4 }), name: PRESETS[3].name, n: 5 }));
      await expect(line(page, 7)).toHaveText(en('ui2.keyReview.notKept', { key: en('preset.row2.key', { n: 1 }), name: 'Radio Swiss Jazz' }));

      // Only read so far: the preview with the copy's own arguments, no copy.
      await mock.expectCalled('PreviewPresetKeyCopy', { times: 1 });
      expect((await mock.last('PreviewPresetKeyCopy')).args).toEqual([LR.host, LR.port, BR.host, BR.port, CHOSEN]);
      expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
      expect(await mock.count('CopyPresetsAcrossBoxes')).toBe(0);
      const bedroom = (await mock.state()).presets[BR.host];
      expect(bedroom.map((p) => p.slot)).toEqual([2, 3, 5]);
      await expect(page.locator('#toast')).not.toHaveClass(/show/);
    });

    test('2. Copy, from the keyboard, does exactly what the review said', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: CHOSEN });
      await expect(page.locator('#ui2XferReviewTitle')).toBeFocused();
      await page.keyboard.press('Tab');
      const copy = page.getByRole('button', { name: en('ui2.keyReview.copyBtn'), exact: true });
      await expect(copy).toBeFocused();
      await page.keyboard.press('Enter');

      await mock.expectCalled('CopyPresetKeysAcrossBoxes', { times: 1 });
      expect((await mock.last('CopyPresetKeysAcrossBoxes')).args).toEqual([LR.host, LR.port, BR.host, BR.port, CHOSEN]);
      await expect(review(page)).toHaveCount(0);
      const status = page.locator('#ui2XferStatus');
      await expect(status).toHaveText([
        en('presets.keyCopyCopied', { target: BR.friendlyName, keys: `${en('preset.key', { n: 1 })}, ${en('preset.key', { n: 3 })}` }),
        en('presets.keyCopySame', { target: BR.friendlyName, keys: en('preset.key', { n: 2 }) }),
        en('presets.keyCopyOnOtherKey', { target: BR.friendlyName, key: en('preset.key', { n: 4 }), name: PRESETS[3].name, n: 5 }),
        en('presets.keyCopyNotKept', { target: BR.friendlyName, keys: en('preset.row2.key', { n: 1 }) }),
      ].join(' '));
      await expect(status).toHaveClass(/is-warn/);
      const bedroom = (await mock.state()).presets[BR.host];
      expect(Object.fromEntries(bedroom.map((p) => [p.slot, p.name]))).toEqual({
        1: PRESETS[0].name, 2: PRESETS[1].name, 3: PRESETS[2].name, 5: PRESETS[3].name,
      });
      // The keyboard is back where the copy started.
      await expect(page.locator('#presetTransferBtn')).toBeFocused();
      await expect(page.locator('#toast')).not.toHaveClass(/show/);
    });

    test('3. Escape cancels: nothing is copied and the keyboard is back on Transfer', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: CHOSEN });
      await expect(line(page, 1)).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(review(page)).toHaveCount(0);
      await expect(page.locator('#presetTransferBtn')).toBeFocused();
      await expect(page.locator('#ui2XferStatus')).toHaveText(en('ui2.keyReview.cancelled'));
      expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
      expect(await mock.count('CopyPresetsAcrossBoxes')).toBe(0);
      expect((await mock.state()).presets[BR.host].map((p) => p.slot)).toEqual([2, 3, 5]);
    });

    test('4. every key ticked and every speaker: one block per speaker, Cancel copies nothing', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { target: '__ALL__' });
      const n = (await page.locator('#presetXferTarget option').count()) - 1;
      await expect(review(page).locator('h4')).toHaveCount(n);
      await expect(review(page).locator('h4').first()).toBeVisible();
      await mock.expectCalled('PreviewPresetKeyCopy', { times: n });
      // Bedroom differs, so there is something to copy.
      await expect(page.locator('#ui2XferReviewCopy')).toBeVisible();
      await review(page).getByRole('button', { name: en('common.cancel'), exact: true }).click();
      await expect(review(page)).toHaveCount(0);
      expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
      expect(await mock.count('CopyPresetsAcrossBoxes')).toBe(0);
    });

    test('4b. choosing another speaker closes the review and copies nothing', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: CHOSEN });
      await expect(line(page, 1)).toBeVisible();
      await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR.host}"]`).click();
      await expect(page.locator('#ui2KeysTitle')).toHaveText(en('ui2.radio.keysOn', { name: BR.friendlyName }));
      await expect(review(page)).toHaveCount(0);
      expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
      expect(await mock.count('CopyPresetsAcrossBoxes')).toBe(0);
      expect((await mock.state()).presets[BR.host].map((p) => p.slot)).toEqual([2, 3, 5]);
    });

    test('4c. leaving Radio closes the review and copies nothing', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: CHOSEN });
      await expect(line(page, 1)).toBeVisible();
      await page.locator('#ui2Tabs .ui2-nav-item[data-view="library"]').click();
      await expect(review(page)).toHaveCount(0);
      await page.locator('#ui2Tabs .ui2-nav-item[data-view="box"]').click();
      await expect(page.locator('#presetTransferBtn')).toBeEnabled();
      await expect(review(page)).toHaveCount(0);
      expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
    });
  });

  test.describe('a speaker that already has the chosen keys', () => {
    test.use({ scenario: { presets: { bedroom: PRESETS.slice(0, 2) } } });

    // The speaker's own buttons can be stale while its keys in STR are right;
    // copying the keys again is how a user repairs that, so Copy stays.
    test('5. says the keys are already there and still offers Copy, which sets the buttons again', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: [1, 2] });
      await expect(line(page, 1)).toHaveText(en('ui2.keyReview.same', { key: en('preset.key', { n: 1 }), name: PRESETS[0].name }));
      await expect(review(page).locator('.ui2-xfer-review-status')).toHaveText('');
      const copy = page.locator('#ui2XferReviewCopy');
      await expect(copy).toBeVisible();
      await copy.click();
      await mock.expectCalled('CopyPresetKeysAcrossBoxes', { times: 1 });
      await expect(page.locator('#ui2XferStatus')).toHaveText(en('presets.keyCopySame', {
        target: BR.friendlyName, keys: `${en('preset.key', { n: 1 })}, ${en('preset.key', { n: 2 })}`,
      }));
      await expect(page.locator('#ui2XferStatus')).toHaveClass(/is-ok/);
    });
  });

  test.describe('a copy where nothing can go', () => {
    // Living Room's key 1 station already sits on Bedroom's key 6.
    test.use({ scenario: { presets: { bedroom: [{ ...PRESETS[0], slot: 6 }] } } });

    test('5b. says nothing would change and offers no Copy', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: [1] });
      await expect(line(page, 1)).toHaveText(en('ui2.keyReview.onOtherKey', { key: en('preset.key', { n: 1 }), name: PRESETS[0].name, n: 6 }));
      await expect(review(page).locator('.ui2-xfer-review-status')).toHaveText(en('ui2.keyReview.nothing'));
      await expect(page.locator('#ui2XferReviewCopy')).toBeHidden();
      await review(page).getByRole('button', { name: en('common.close'), exact: true }).click();
      await expect(review(page)).toHaveCount(0);
      expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
    });
  });

  test.describe('a source speaker that cannot be read', () => {
    test.use({ scenario: { bindings: { PreviewPresetKeyCopy: { error: 'read source presets: timeout' } } } });

    test('6b. says so once about the source, asks no other speaker and offers no Copy', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { target: '__ALL__' });
      await expect(review(page).locator('.ui2-xfer-review-source')).toHaveText(en('ui2.keyReview.sourceUnchecked', {
        source: LR.friendlyName, reason: 'read source presets: timeout',
      }));
      await expect(review(page).locator('h4')).toHaveCount(0);
      await expect(review(page).locator('.ui2-xfer-review-status')).toHaveText('');
      await expect(page.locator('#ui2XferReviewCopy')).toBeHidden();
      expect(await mock.count('PreviewPresetKeyCopy')).toBe(1);
      await review(page).getByRole('button', { name: en('common.close'), exact: true }).click();
      expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
      expect(await mock.count('CopyPresetsAcrossBoxes')).toBe(0);
    });
  });

  test.describe('while the speakers are still being checked', () => {
    test.use({ scenario: { bindings: { PreviewPresetKeyCopy: { never: true } } } });

    test('6c. the keyboard is in the review, Transfer stays off, and Escape cancels', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: [1] });
      await expect(review(page).locator('.ui2-xfer-review-status')).toHaveText(en('ui2.keyReview.checking', { target: BR.friendlyName }));
      await expect(page.locator('#ui2XferReviewTitle')).toBeFocused();
      await expect(page.locator('#presetTransferBtn')).toBeDisabled();
      await page.keyboard.press('Escape');
      await expect(review(page)).toHaveCount(0);
      await expect(page.locator('#presetTransferBtn')).toBeEnabled();
      await expect(page.locator('#presetTransferBtn')).toBeFocused();
      await expect(page.locator('#ui2XferStatus')).toHaveText(en('ui2.keyReview.cancelled'));
      expect(await mock.count('CopyPresetKeysAcrossBoxes')).toBe(0);
      // Transfer works again: the dialog opens on the next press.
      await page.keyboard.press('Enter');
      await expect(page.locator('#presetXferKeys')).toBeVisible();
    });
  });

  test.describe('a speaker that cannot be checked', () => {
    test.use({ scenario: { bindings: { PreviewPresetKeyCopy: { error: 'read target presets: timeout' } } } });

    test('6. is named with the reason, and Copy still tries it', async ({ app, mock }) => {
      const { page } = app;
      await mock.resetCalls();
      await startCopy(page, { slots: [3] });
      await expect(review(page).locator('.is-unchecked')).toHaveText(en('ui2.keyReview.unchecked', { reason: 'read target presets: timeout' }));
      await page.locator('#ui2XferReviewCopy').click();
      await mock.expectCalled('CopyPresetKeysAcrossBoxes', { times: 1 });
      expect((await mock.last('CopyPresetKeysAcrossBoxes')).args[4]).toEqual([3]);
    });
  });
});

test.describe('classic layout', () => {
  test.use({ ui2: false, scenario: { presets: { bedroom: BEDROOM } } });

  test('7. copies right away, without a review', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await startCopy(page, { slots: [1, 2] });
    await mock.expectCalled('CopyPresetKeysAcrossBoxes', { times: 1 });
    expect(await mock.count('PreviewPresetKeyCopy')).toBe(0);
    await expect(review(page)).toHaveCount(0);
    // A key the speaker already had still reads as copied there.
    await expect(page.locator('#toast')).toHaveText(en('presets.keyCopyCopied', {
      target: BR.friendlyName, keys: `${en('preset.key', { n: 1 })}, ${en('preset.key', { n: 2 })}`,
    }));
  });
});
