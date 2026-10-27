// desktop-keyboard: everything a keyboard user does in the UI 2.0 shell.
//
// The popovers (More, Levels): Enter opens and focuses inside, Escape and the
// close button give focus back to the trigger, Tab past the last control and
// a click on the main pane close them. The sidebar speaker list is one roving
// list (pinned group and the others as one sequence), the arrows reach a
// row's "+" and back (mirrored in a right-to-left language), and a live
// repaint of the list keeps the keyboard on the same speaker. The player's
// volume and the group volume answer the arrow keys with one write in flight
// and the final level sent. The Aa menu closes on Escape in both layouts.
// The classic layout's "Assign to a key" picker is a keyboard dialog too.

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';

const LIVING = 'demobox.local';
const BEDROOM = '192.0.2.52';
const BEDROOM_ID = 'B7C8D9E0F1A2';
const LIVING_ID = 'A1B2C3D4E5F6';

// Living room leads a group that Bedroom follows, both playing: the player
// shows the group volume and Levels.
const GROUP = {
  master: LIVING_ID,
  senderIP: LIVING,
  members: [{ deviceID: LIVING_ID, ip: LIVING }, { deviceID: BEDROOM_ID, ip: BEDROOM }],
};
const GROUPED = {
  playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' },
  zones: { livingRoom: GROUP, bedroom: GROUP },
};
const SLOW_VOLUME = { bindings: { SetBoxVolume: { value: null, delayMs: 50 } } };

const focusedInfo = (page) => page.evaluate(() => {
  const el = document.activeElement;
  return el ? { id: el.id, cls: el.className, host: el.dataset ? el.dataset.host || '' : '', old: !!el.__old } : null;
});

// maxOpenPerHost is the most SetBoxVolume calls in flight at once for one
// speaker, from the recorded start times and the override's fixed delay.
function maxOpenPerHost(calls, delayMs) {
  let most = 0;
  const byHost = {};
  for (const c of calls) (byHost[c.args[0]] ||= []).push(c.t);
  for (const ts of Object.values(byHost)) {
    ts.sort((a, b) => a - b);
    for (let i = 0; i < ts.length; i++) {
      let n = 1;
      for (let j = i + 1; j < ts.length && ts[j] - ts[i] < delayMs; j++) n++;
      most = Math.max(most, n);
    }
  }
  return most;
}

test.describe('UI 2.0 popovers', () => {
  test.use({ ui2: true, fakeClock: true, scenario: GROUPED });

  test('More: Enter opens and focuses inside, Escape closes back onto the trigger', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    const btn = page.locator('#ui2MoreBtn');
    const pop = page.locator('#ui2MorePop');
    await btn.focus();
    await page.keyboard.press('Enter');
    await expect(pop).toBeVisible();
    await expect(btn).toHaveAttribute('aria-expanded', 'true');
    expect(await pop.evaluate((p) => p.contains(document.activeElement))).toBe(true);
    await page.keyboard.press('Escape');
    await expect(pop).toBeHidden();
    await expect(btn).toHaveAttribute('aria-expanded', 'false');
    await expect(btn).toBeFocused();
  });

  test('Levels: Enter focuses the first speaker slider, Escape returns to the trigger', async ({ app }) => {
    const { page } = app;
    await settle(page, 20_000);
    const btn = page.locator('#ui2LevelsBtn');
    const pop = page.locator('#ui2LevelsPop');
    await expect(btn).toBeVisible();
    await btn.focus();
    await page.keyboard.press('Enter');
    await expect(pop).toBeVisible();
    const first = pop.locator('input[type=range][id^="memberVol-"]').first();
    await expect(first).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(pop).toBeHidden();
    await expect(btn).toHaveAttribute('aria-expanded', 'false');
    await expect(btn).toBeFocused();
  });

  for (const which of ['More', 'Levels']) {
    test(`${which}: the close button returns focus to the trigger`, async ({ app }) => {
      const { page } = app;
      await settle(page, 20_000);
      const btn = page.locator(`#ui2${which}Btn`);
      const pop = page.locator(`#ui2${which}Pop`);
      await btn.focus();
      await page.keyboard.press('Enter');
      await expect(pop).toBeVisible();
      await pop.locator('.u-pop-close').click();
      await expect(pop).toBeHidden();
      await expect(btn).toHaveAttribute('aria-expanded', 'false');
      await expect(btn).toBeFocused();
    });
  }

  // Levels is the last thing in the document: Tab past its last slider leaves
  // the page (focusout with no relatedTarget, which popover.js ignores), so
  // the Tab keydown on the last control is what closes it.
  for (const which of ['More', 'Levels']) {
    test(`${which}: Tab past the last control closes it`, async ({ app }) => {
      const { page } = app;
      await settle(page, 20_000);
      const btn = page.locator(`#ui2${which}Btn`);
      const pop = page.locator(`#ui2${which}Pop`);
      await btn.focus();
      await page.keyboard.press('Enter');
      await expect(pop).toBeVisible();
      // Tab until focus leaves the panel; the panel must be closed by then.
      let inside = true;
      for (let i = 0; i < 40 && inside; i++) {
        await page.keyboard.press('Tab');
        inside = await pop.evaluate((p) => p.contains(document.activeElement));
      }
      expect(inside, 'Tab left the panel within 40 presses').toBe(false);
      await expect(pop).toBeHidden();
      await expect(btn).toHaveAttribute('aria-expanded', 'false');
    });
  }

  for (const which of ['More', 'Levels']) {
    test(`${which}: a click on the main pane closes it without moving focus to the trigger`, async ({ app }) => {
      const { page } = app;
      await settle(page, 20_000);
      const btn = page.locator(`#ui2${which}Btn`);
      const pop = page.locator(`#ui2${which}Pop`);
      await btn.focus();
      await page.keyboard.press('Enter');
      await expect(pop).toBeVisible();
      await page.locator('#ui2Main').click({ position: { x: 5, y: 5 } });
      await expect(pop).toBeHidden();
      await expect(btn).toHaveAttribute('aria-expanded', 'false');
      await expect(btn).not.toBeFocused();
    });
  }
});

test.describe('UI 2.0 sidebar speaker list', () => {
  test.use({ ui2: true, fakeClock: true });

  test('Up, Down, Home and End rove across the pinned and the other rows as one list', async ({ app }) => {
    const { page } = app;
    await settle(page, 20_000);
    const picks = page.locator('#ui2SpkLists .ui2-spk-pick');
    await expect(page.locator('#ui2SpkLists .ui2-spk-pinned .ui2-spk-pick')).toHaveCount(1);
    const n = await picks.count();
    expect(n).toBe(4);
    const hosts = await picks.evaluateAll((els) => els.map((e) => e.dataset.host));
    // Only one pick takes Tab.
    expect(await picks.evaluateAll((els) => els.filter((e) => e.tabIndex === 0).length)).toBe(1);

    await picks.nth(0).focus();
    await page.keyboard.press('ArrowUp'); // clamps at the top
    await expect(picks.nth(0)).toBeFocused();
    for (let i = 1; i < n; i++) {
      await page.keyboard.press('ArrowDown'); // the first step crosses from pinned into the others
      await expect(page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${hosts[i]}"]`)).toBeFocused();
    }
    await page.keyboard.press('ArrowDown'); // clamps at the bottom
    await expect(picks.nth(n - 1)).toBeFocused();
    await page.keyboard.press('Home');
    await expect(picks.nth(0)).toBeFocused();
    await page.keyboard.press('End');
    await expect(picks.nth(n - 1)).toBeFocused();
    await page.keyboard.press('ArrowUp');
    await expect(picks.nth(n - 2)).toBeFocused();
    // The roving tabindex followed.
    expect(await picks.evaluateAll((els) => els.map((e) => e.tabIndex))).toEqual(
      hosts.map((h, i) => (i === n - 2 ? 0 : -1)),
    );
  });

  test('ArrowRight reaches the row\'s "+", ArrowLeft returns to the row', async ({ app }) => {
    const { page } = app;
    await settle(page, 20_000);
    const pick = page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BEDROOM}"]`);
    const act = page.locator(`#ui2SpkLists .ui2-spk-act[data-host="${BEDROOM}"]`);
    await expect(act).toBeVisible();
    await pick.focus();
    await page.keyboard.press('ArrowRight');
    await expect(act).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(pick).toBeFocused();
  });

  test('live repaint keeps focus on the same speaker', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    const pick = page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BEDROOM}"]`);
    await pick.focus();
    await page.evaluate(() => {
      document.querySelectorAll('#ui2SpkLists .ui2-spk-row, #ui2SpkLists .ui2-spk-pick').forEach((el) => { el.__old = 1; });
      // Bedroom starts playing on its own: the next every-speaker poll changes
      // its row, so the list really repaints.
      window.__strMock.state.playing['192.0.2.52'] = 'PLAY_STATE';
    });
    await runFor(page, 30_000);
    // Repaint proof: the rows are new nodes.
    expect(await page.evaluate(() => [...document.querySelectorAll('#ui2SpkLists .ui2-spk-row')].some((el) => el.__old))).toBe(false);
    const f = await focusedInfo(page);
    expect(f).toMatchObject({ host: BEDROOM, old: false });
    expect(f.cls).toContain('ui2-spk-pick');
    await expect(pick).toBeFocused();
  });
});

test.describe('UI 2.0 sidebar speaker list, right to left', () => {
  test.use({ ui2: true, fakeClock: true, appLocale: 'ar' });

  test('ArrowLeft reaches the row\'s "+", ArrowRight returns to the row', async ({ app }) => {
    const { page } = app;
    await settle(page, 20_000);
    await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
    const pick = page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BEDROOM}"]`);
    const act = page.locator(`#ui2SpkLists .ui2-spk-act[data-host="${BEDROOM}"]`);
    await expect(act).toBeVisible();
    await pick.focus();
    await page.keyboard.press('ArrowRight'); // the wrong way in rtl: stays
    await expect(pick).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(act).toBeFocused();
    await page.keyboard.press('ArrowRight');
    await expect(pick).toBeFocused();
  });
});

test.describe('UI 2.0 player volume', () => {
  test.use({ ui2: true, fakeClock: true, scenario: SLOW_VOLUME });

  test('three ArrowRight presses: +3, one write in flight, the last write is the final level', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    const vol = page.locator('#musicVolume');
    await expect(vol).toBeVisible();
    const v = Number(await vol.inputValue());
    await mock.resetCalls();
    await vol.focus();
    for (let i = 0; i < 3; i++) await page.keyboard.press('ArrowRight');
    await runFor(page, 1_000);
    await expect(vol).toHaveValue(String(v + 3));
    const last = await mock.last('SetBoxVolume', onHost(LIVING));
    expect(last && last.args[2]).toBe(v + 3);
    expect(await mock.maxOpen('SetBoxVolume')).toBeLessThanOrEqual(1);
  });

  for (const [key, want] of [['End', () => 100], ['Home', () => 0], ['PageUp', (v) => Math.min(100, v + 10)]]) {
    test(`${key} on the player slider sends the final level once settled`, async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      const vol = page.locator('#musicVolume');
      await expect(vol).toBeVisible();
      const v = Number(await vol.inputValue());
      await mock.resetCalls();
      await vol.focus();
      await page.keyboard.press(key);
      await runFor(page, 1_000);
      await expect(vol).toHaveValue(String(want(v)));
      const last = await mock.last('SetBoxVolume', onHost(LIVING));
      expect(last && last.args[2]).toBe(want(v));
      expect(await mock.maxOpen('SetBoxVolume')).toBeLessThanOrEqual(1);
    });
  }
});

test.describe('UI 2.0 grouped player volume', () => {
  test.use({ ui2: true, fakeClock: true, scenario: { ...GROUPED, ...SLOW_VOLUME } });

  test('three ArrowRight presses on the group volume: +3 for the group, one write per speaker in flight', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 20_000);
    const vol = page.locator('#ui2Player #groupVolume');
    await expect(vol).toBeVisible();
    const v = Number(await vol.inputValue());
    await mock.resetCalls();
    await vol.focus();
    for (let i = 0; i < 3; i++) await page.keyboard.press('ArrowRight');
    await runFor(page, 1_000);
    await expect(vol).toHaveValue(String(v + 3));
    // The group slider sits at the loudest member, the leader here: it moves by
    // exactly the offset.
    const last = await mock.last('SetBoxVolume', onHost(LIVING));
    expect(last && last.args[2]).toBe(v + 3);
    const calls = await mock.calls('SetBoxVolume');
    expect(maxOpenPerHost(calls, 50)).toBeLessThanOrEqual(1);
  });
});

for (const ui2 of [false, true]) {
  test.describe(`Aa menu, ${ui2 ? 'UI 2.0' : 'classic'} layout`, () => {
    test.use({ ui2 });

    test('Escape closes it', async ({ app }) => {
      const { page } = app;
      const trigger = page.locator('#a11yTrigger');
      const menu = page.locator('#a11yMenu');
      await trigger.click();
      await expect(menu).toBeVisible();
      await expect(trigger).toHaveAttribute('aria-expanded', 'true');
      await page.keyboard.press('Escape');
      await expect(menu).toBeHidden();
      await expect(trigger).toHaveAttribute('aria-expanded', 'false');
    });
  });
}

test.describe('classic layout key picker', () => {
  test.use({ ui2: false });

  test('"Assign to a key" is a dialog for the keyboard: focus inside, Tab stays, Escape returns focus', async ({ app, mock }) => {
    const { page } = app;
    await mock.resetCalls();
    await page.locator('#topBtn').click();
    const open = page.locator('#searchResults .result-row').first().locator('.pick');
    await open.focus();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('dialog', { name: 'Assign station to a key' })).toBeVisible();
    const slots = page.locator('#pickGrid .pick-slot');
    await expect(slots.first()).toBeFocused();
    // Tab stays inside: six keys and Cancel, then back to key 1.
    for (let i = 0; i < 7; i++) await page.keyboard.press('Tab');
    await expect(slots.first()).toBeFocused();
    await page.keyboard.press('Shift+Tab');
    await expect(page.locator('#pickCancel')).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(page.locator('#pickModal')).toBeHidden();
    await expect(open).toBeFocused();
    await mock.expectCalled('SetPreset', { times: 0 });
  });
});
