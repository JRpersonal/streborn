// Hold a key to save onto it, and a key's choices, in the UI 2.0 preview
// (views/music/ui2keyedit.js): a short press plays the key; a hold of 1.1 s
// saves what is playing onto it, as in the classic layout (since 25 November
// 2026, discussion #1077), and opens its choices under the grid when there is
// nothing to save (nothing plays, or the speaker's own key); a hold never also
// plays. A right click, a long touch and macOS Ctrl+click open the choices and
// never save, an empty key opens them on a short press, and the keyboard gets
// there with Shift+Enter or the context-menu key. The quick choices stay in
// the card (the save of what plays, the rename and clear icons); "Find a
// station" and "Choose a favorite" open the one key editor, Speakers > the
// speaker > Keys and alarms (shell2/speakers/keyeditor.js), with that key
// chosen and the station search or the favourites in front. The last block
// checks that the current layout kept its hold-to-save.
//
//   E2E_PORT=4184 npx playwright test specs/desktop-ui2-hold-to-edit-key.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { openTopList } from '../helpers/ui2radio.mjs';
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

// Key 6 is not one of STR's keys with a station: the speaker's own (a Deezer
// mix, which a press recalls) or one STR wrote that its store lost.
const KEY6 = {
  native: { name: 'Deezer Mix', source: 'DEEZER', location: 'deezer:mix' },
  lost: { name: 'Old Station', source: 'INTERNET_RADIO', location: 'http://demobox.local:8888/stream/6', strOrigin: true, lost: true },
};
const key6Scenario = (bp) => ({
  presets: { livingRoom: PRESETS.filter((p) => p.slot !== 6) },
  bindings: { BoxPresets: { value: [{ slot: 6, ...bp }] }, RecallBoxPreset: { value: null } },
});

// fire sends synthetic events to a key's body in one go, in the order a
// browser sends them for a gesture the run cannot make with a real pointer (a
// long touch, macOS Ctrl+click): each entry is [type, init].
const fire = (page, slot, events) => page.evaluate(([s, list]) => {
  const el = document.querySelector(`#presets .preset[data-slot="${s}"] .preset-body`);
  for (const [type, init] of list) {
    let ev;
    if (type.startsWith('touch')) {
      ev = typeof TouchEvent === 'function'
        ? new TouchEvent(type, { bubbles: true, cancelable: true })
        : new Event(type, { bubbles: true, cancelable: true });
    } else {
      ev = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, ...init });
    }
    el.dispatchEvent(ev);
  }
}, [slot, events]);

// setPlaying makes the mock speaker play (or stand by) from the next poll on.
const setPlaying = (page, host, ps) => page.evaluate(([h, p]) => { window.__strMock.state.playing[h] = p; }, [host, ps]);
const keyLine = (page, slot) => tile(page, slot).locator('.ui2-key-status');
const pick = (page, host) => page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${host}"]`);
// The key editor on Keys and alarms.
const editor = (page) => page.locator('#ui2KeyEditor');
const editorKey = (page, slot) => editor(page).locator(`.kae-key[data-slot="${slot}"]`);
const KE = (k, v) => en(`ui2.speakers.keyEditor.${k}`, v);

// onKeysTab: the hold card handed over to the speaker's Keys and alarms, with
// key slot chosen in its editor and the Radio source open.
async function onKeysTab(page, slot) {
  await expect(page.locator('#ui2Main')).toHaveAttribute('data-view', 'settings');
  await expect(page.locator('#ui2SpkPageName')).toHaveText(LR.friendlyName);
  await expect(page.locator('#ui2SpkTab-keys')).toHaveAttribute('aria-selected', 'true');
  await expect(editorKey(page, slot)).toHaveAttribute('aria-pressed', 'true');
  await expect(editor(page).locator('.kae-key[aria-pressed="true"]')).toHaveCount(1);
  await expect(page.locator('#kaeTab-radio')).toHaveAttribute('aria-selected', 'true');
}

test.describe('UI 2.0: hold a key to save onto it, and its choices', () => {
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

    test('2. a hold while something plays saves it onto the key, opens nothing and never also plays', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await expect(body(page, 4)).toContainText('Jazz24');
      await mock.resetCalls();
      await body(page, 4).hover();
      await page.mouse.down();
      await runFor(page, 400);
      // Held, not yet saved. The run asks for less motion, so the bar stands
      // full at once instead of growing.
      await expect(tile(page, 4)).toHaveClass(/long-press/);
      expect(await barWidth(page, 4)).toBe(100);
      // The 0.7 s of the choices hold is gone: at 0.8 s nothing happened yet.
      await runFor(page, 400);
      expect(await mock.count('SetPreset')).toBe(0);
      await expect(panel(page)).toBeHidden();
      await runFor(page, 400);
      // Living Room plays its key 2, so the hold copies key 2 onto key 4.
      await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 4, times: 1 });
      await page.mouse.up();
      await runFor(page, 2_000);
      await expect(keyLine(page, 4)).toHaveText(en('ui2.radio.saved', { n: 4, speaker: LR.friendlyName }));
      await expect(tile(page, 4)).toContainText('Best Of Rock.FM');
      await expect(panel(page)).toBeHidden();
      expect(await mock.count('PlaySlot')).toBe(0);
      expect(await mock.count('SetPreset')).toBe(1);
      await expect(tile(page, 4)).not.toHaveClass(/long-press/);
    });

    test('2b. a hold with nothing playing opens the key\'s choices, named with key and speaker', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await setPlaying(page, LR.host, 'STANDBY');
      await runFor(page, 8_000);
      await expect(body(page, 4)).toContainText('Jazz24');
      await mock.resetCalls();
      await body(page, 4).hover();
      await page.mouse.down();
      await runFor(page, 800);
      await expect(tile(page, 4)).toHaveClass(/long-press/);
      await expect(panel(page)).toBeHidden();
      await runFor(page, 400);
      await expect(panel(page)).toBeVisible();
      await page.mouse.up();
      await expect(panel(page)).toHaveAttribute('aria-labelledby', 'ui2KeyEditTitle');
      await expect(panel(page).locator('#ui2KeyEditTitle')).toHaveText(en('ui2.radio.keyEditTitle', { n: 4, speaker: LR.friendlyName }));
      await expect(panel(page).locator('.ui2-key-edit-now')).toHaveText(en('ui2.radio.keyEditNow', { name: 'Jazz24' }));
      // Nothing plays, so there is nothing to save: finding a station comes
      // first and has the keyboard; the key's own rename and clear follow.
      expect(await ids(page)).toEqual(['find', 'favorites', 'rename', 'clear']);
      await expect(choice(page, 'find')).toBeFocused();
      await expect(choice(page, 'find')).toHaveClass(/u-btn--primary/);
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

    test('10. a long touch opens the choices on its context menu, never saves, and lifting the finger does not play', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await mock.resetCalls();
      await fire(page, 4, [['touchstart']]);
      // Chromium reports a long touch as a context menu at about 500 ms,
      // before the hold itself would save: the menu ends the press, so a
      // long touch opens the choices even while something plays.
      await runFor(page, 550);
      await fire(page, 4, [['contextmenu', { button: 2 }]]);
      await expect(panel(page)).toBeVisible();
      await runFor(page, 1_500);
      await fire(page, 4, [['touchend']]);
      await runFor(page, 1_500);
      expect(await mock.count('PlaySlot')).toBe(0);
      expect(await mock.count('SetPreset')).toBe(0);
      await expect(panel(page).locator('#ui2KeyEditTitle')).toHaveText(en('ui2.radio.keyEditTitle', { n: 4, speaker: LR.friendlyName }));
      await expect(tile(page, 4)).not.toHaveClass(/long-press/);
    });

    test('11. macOS Ctrl+click opens the choices, does not save and does not play', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await mock.resetCalls();
      // WKWebView sends a left mousedown with Ctrl, then the context menu,
      // then the mouseup.
      await fire(page, 2, [['mousedown', { ctrlKey: true }], ['contextmenu', { ctrlKey: true }], ['mouseup', { ctrlKey: true }]]);
      await runFor(page, 1_500);
      await expect(panel(page)).toBeVisible();
      expect(await mock.count('PlaySlot')).toBe(0);
      expect(await mock.count('SetPreset')).toBe(0);
    });

    test('12. a press the grid is rebuilt under does not save or open the choices later', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await mock.resetCalls();
      await body(page, 4).hover();
      await page.mouse.down();
      await runFor(page, 200);
      // The grid is drawn again while the key is held (here by the row tab;
      // the status poll does the same on a cover or a station change), so
      // the release lands on the new grid.
      await page.evaluate(() => document.getElementById('presetRowTab2').click());
      await page.mouse.up();
      await runFor(page, 1_500);
      await expect(panel(page)).toBeHidden();
      expect(await mock.count('PlaySlot')).toBe(0);
      // Living Room plays: without the guard the old tile's hold would save.
      expect(await mock.count('SetPreset')).toBe(0);
    });

    test('13. a repaint by the status poll keeps the panel and the keyboard on its choice', async ({ app }) => {
      const { page } = app;
      await settle(page, 9_000);
      await setPlaying(page, LR.host, 'STANDBY');
      await runFor(page, 8_000);
      await body(page, 3).focus();
      await page.keyboard.press('Shift+Enter');
      await expect(panel(page)).toBeVisible();
      // Nothing plays: no save offered.
      expect(await ids(page)).toEqual(['find', 'favorites', 'rename', 'clear']);
      await choice(page, 'find').focus();
      // Playback starts: the poll redraws the panel with save first, and the
      // keyboard stays on Find a station instead of moving onto the save.
      await setPlaying(page, LR.host, 'PLAY_STATE');
      // A speaker in standby is read every 15 s (main.js STATUS_IDLE_MS).
      await runFor(page, 16_000);
      await expect.poll(() => ids(page)).toEqual(['save', 'find', 'favorites', 'rename', 'clear']);
      await expect(panel(page)).toBeVisible();
      await expect(choice(page, 'find')).toBeFocused();
      // It stops again: the save goes, the keyboard stays where it was.
      await choice(page, 'rename').focus();
      await setPlaying(page, LR.host, 'STANDBY');
      await runFor(page, 8_000);
      await expect.poll(() => ids(page)).toEqual(['find', 'favorites', 'rename', 'clear']);
      await expect(choice(page, 'rename')).toBeFocused();
    });

    for (const kind of ['native', 'lost']) {
      test.describe(`the speaker's ${kind} key`, () => {
        test.use({ scenario: key6Scenario(KEY6[kind]) });

        const title = kind === 'native'
          ? '14. native: a hold fills the bar and opens the choices, never saves over the speaker\'s own key, and does not recall'
          : '14. lost: a hold fills the bar and saves what plays onto the key, and does not recall';
        test(title, async ({ app, mock }) => {
          const { page } = app;
          await settle(page, 9_000);
          await expect(body(page, 6)).toContainText(KEY6[kind].name);
          await mock.resetCalls();
          await body(page, 6).hover();
          await page.mouse.down();
          await runFor(page, 400);
          await expect(tile(page, 6)).toHaveClass(/long-press/);
          expect(await barWidth(page, 6)).toBe(100);
          await runFor(page, 800);
          if (kind === 'native') {
            await expect(panel(page)).toBeVisible();
          } else {
            await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 6, times: 1 });
          }
          await page.mouse.up();
          await runFor(page, 1_500);
          if (kind === 'native') {
            expect(await ids(page)).toEqual(['find', 'favorites']);
            expect(await mock.count('SetPreset')).toBe(0);
            await expect(keyLine(page, 6)).toHaveText('');
          } else {
            await expect(panel(page)).toBeHidden();
            await expect(keyLine(page, 6)).not.toHaveText('');
          }
          expect(await mock.count('PlaySlot')).toBe(0);
          expect(await mock.count('RecallBoxPreset')).toBe(0);
        });
      });
    }
  });

  test('15. choosing another speaker closes the choices', async ({ app }) => {
    const { page } = app;
    await body(page, 3).focus();
    await page.keyboard.press('Shift+Enter');
    await expect(panel(page)).toBeVisible();
    await pick(page, ROSTER.bedroom.host).click();
    await expect(pick(page, ROSTER.bedroom.host)).toHaveAttribute('aria-current', 'true');
    await expect(panel(page)).toBeHidden();
    await pick(page, LR.host).click();
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
    await expect(panel(page)).toBeHidden();
  });

  test('16. Choose a favorite opens the key editor on that key with the favourites, also after a search there', async ({ app, mock }) => {
    const { page } = app;
    // A favourite, starred on Radio.
    await openTopList(page);
    await row(page, JAZZ.name).locator('[data-act="fav"]').click();
    // A search in the editor first, from key 1's "Find a station".
    await body(page, 1).focus();
    await page.keyboard.press('Shift+Enter');
    await choice(page, 'find').click();
    await onKeysTab(page, 1);
    await expect(page.locator('#kaeRadioQ')).toBeFocused();
    await page.keyboard.type('kexp');
    await page.keyboard.press('Enter');
    await expect(editor(page).getByRole('heading', { name: KE('radioHits', { q: 'kexp' }) })).toBeVisible();
    // Back on Radio, key 3 (NPR News): its favourites, not the old hits.
    await page.locator('#ui2Tabs .ui2-nav-item[data-view="box"]').click();
    await expect(body(page, 3)).toContainText('NPR News');
    await body(page, 3).focus();
    await page.keyboard.press('Shift+Enter');
    await choice(page, 'favorites').click();
    await expect(panel(page)).toBeHidden();
    await onKeysTab(page, 3);
    await expect(page.locator('#kaeHint')).toHaveText(KE('hintReplace', { n: 3, current: 'NPR News' }));
    await expect(editor(page).getByRole('heading', { name: en('ui2.radio.favorites') })).toBeVisible();
    await expect(page.locator('#kaeRadioQ')).toHaveValue('');
    const put = editor(page).getByRole('button', { name: KE('putName', { what: JAZZ.name, n: 3, name: LR.friendlyName }) });
    await expect(put).toBeFocused();
    await mock.resetCalls();
    await page.keyboard.press('Enter');
    await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 3 && c.args[3] === JAZZ.name, times: 1 });
    await expect(page.locator('#kaeResult')).toHaveText(en('ui2.radio.saved', { n: 3, speaker: LR.friendlyName }));
  });

  test('17. a key of the second row: the key editor opens on that key of that row', async ({ app }) => {
    const { page } = app;
    await page.locator('#presetRowTab2').click();
    await expect(tile(page, 8)).toHaveClass(/empty/);
    await tile(page, 8).click();
    await expect(panel(page)).toBeVisible();
    await choice(page, 'find').click();
    // Row one is full, so the editor would have chosen key 1 on its own.
    await onKeysTab(page, 8);
    await expect(page.locator('#kaeHint')).toHaveText(KE('hintEmpty', { n: en('preset.row2.num', { n: 2 }) }));
    await expect(page.locator('#kaeRadioQ')).toBeFocused();
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
    // Living Room plays, and a right click still only opens the choices.
    await body(page, 1).click({ button: 'right' });
    await expect(panel(page).locator('#ui2KeyEditTitle')).toHaveText(en('ui2.radio.keyEditTitle', { n: 1, speaker: LR.friendlyName }));
    expect(await ids(page)).toEqual(['save', 'find', 'favorites', 'rename', 'clear']);
    await expect(choice(page, 'save')).toBeFocused();
    expect(await mock.count('PlaySlot')).toBe(0);
    expect(await mock.count('SetPreset')).toBe(0);
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
      await expect(panel(page).locator('.ui2-key-edit-note')).toHaveText(en('ui2.radio.keyEditNoteEditor', { n: 5, speaker: LR.friendlyName }));
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

    test('8. Find a station opens the key editor on that key with the station search, and only it writes the key', async ({ app, mock }) => {
      const { page } = app;
      await tile(page, 5).click();
      await choice(page, 'find').click();
      await expect(panel(page)).toBeHidden();
      await onKeysTab(page, 5);
      await expect(page.locator('#kaeRadioQ')).toBeFocused();
      await page.keyboard.type('kexp');
      await page.keyboard.press('Enter');
      await mock.resetCalls();
      await editor(page).getByRole('button', { name: KE('putName', { what: 'KEXP', n: 5, name: LR.friendlyName }) }).click();
      await mock.expectCalled('SetPreset', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 5 && c.args[3] === 'KEXP', times: 1 });
      await expect(page.locator('#kaeResult')).toHaveText(en('ui2.radio.saved', { n: 5, speaker: LR.friendlyName }));
      await expect(editorKey(page, 5)).toContainText('KEXP');
      // Radio's own Put on key was never aimed: it starts on the first key.
      await page.locator('#ui2Tabs .ui2-nav-item[data-view="box"]').click();
      await expect(tile(page, 5).locator('.name')).toHaveText('KEXP');
      await openTopList(page);
      await row(page, JAZZ.name).locator('[data-act="key"]').click();
      await expect(page.locator('#pickModal')).toBeVisible();
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
