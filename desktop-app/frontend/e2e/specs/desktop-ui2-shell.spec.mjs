// The UI 2.0 shell (#1284, #1285, docs/UI-2.0.md): the sidebar navigation,
// the sidebar speaker list (which replaced the room switcher), the views that
// must follow a speaker chosen there (regression e92872f7), the persistent
// player with its group volume and Levels (which replaced the Output
// popover), More, the nav badges and the Layout switch in the Aa menu.
//
// What screenshots/ui2-check.mjs already covers on its eleven-speaker network
// (the stereo pair as one row, the pinned group, the list scrolling on its
// own, the focus ring room) is not repeated here.
//
//   E2E_PORT=4181 npx playwright test specs/desktop-ui2-shell.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER, SETTINGS } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const OFFICE = ROSTER.office;
const KITCHEN = ROSTER.kitchen;

// Sidebar order (src/shell2/nav.js), Wishes and Help and info small at the
// bottom. Speakers stands for the list and the three classic tabs under it
// (phase 3).
const NAV_VIEWS = ['box', 'library', 'recent', 'spotify', 'speakers', 'wishes', 'help'];
// Every view, the three reached through the Speakers area included.
const ALL_VIEWS = ['box', 'library', 'recent', 'spotify', 'speakers', 'settings', 'multiroom', 'setup', 'wishes', 'help'];
// The sidebar entry marked current on each view.
const ENTRY = { settings: 'speakers', multiroom: 'speakers', setup: 'speakers' };

// The group document GetZoneState answers on both halves of a Living Room +
// Bedroom group (the same shape as demo-data.mjs UI2_GROUP).
const LR_BR_GROUP = {
  master: LR.deviceID,
  senderIP: LR.host,
  members: [{ deviceID: LR.deviceID, ip: LR.host }, { deviceID: BR.deviceID, ip: BR.host }],
};

const pick = (page, host) => page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${host}"]`);
const row = (page, host) => page.locator('#ui2SpkLists .ui2-spk-row')
  .filter({ has: page.locator(`.ui2-spk-pick[data-host="${host}"]`) });
const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);

// openView opens a view the way a user does: its sidebar entry, or for the
// three under Speakers the area's own places (the speaker you control for
// Speaker settings).
async function openView(page, view) {
  if (view === 'settings') {
    await nav(page, 'speakers').click();
    await page.locator(`#view-speakers .spl-open[data-host="${LR.host}"]`).click();
  } else if (view === 'multiroom' || view === 'setup') {
    await nav(page, 'speakers').click();
    await page.locator(`#ui2SpkSubnav .spa-sub[data-route="${view === 'setup' ? 'add' : 'groups'}"]`).click();
  } else {
    await nav(page, view).click();
  }
  await expect(page.locator(`#ui2MainInner > #view-${view}`)).toBeVisible();
}

// A stock speaker asks first (pickBox, confirmWarn); confirming opens Setup.
async function pickStockAndConfirm(page, host) {
  await pick(page, host).click();
  const modal = page.locator('#warnModal');
  await expect(modal).toBeVisible();
  await expect(modal.locator('#warnBody')).toContainText(KITCHEN.friendlyName);
  await modal.locator('#warnConfirm').click();
  await expect(modal).toBeHidden();
  await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
}

// Add speaker is the setup wizard (phase 4): a Bose original picked in the
// sidebar is the one chosen in its Find step.
const findRadio = (page, value) => page.locator(`#w2FindList input.w2-radio[value="${value}"]`);
async function expectStockChosen(page, box) {
  await expect(page.locator('#ui2SetupWizard')).toBeVisible();
  await expect(findRadio(page, box.host)).toBeChecked();
  await expect(page.locator(`.setup-target-card[data-kind="stock"][data-host="${box.host}"]`)).toHaveAttribute('aria-checked', 'true');
}

// passCheck goes through the wizard's Check step to Install: the mock's
// speaker is on 27.0.6, so the check passes and Install unlocks in the footer.
async function passCheck(page) {
  const go = page.locator('#w2Foot .w2-act[data-act="install"]');
  await expect(go).not.toHaveAttribute('aria-disabled', 'true');
  await go.click();
  await expect(page.locator('#w2PanelInstall')).toBeVisible();
}

async function expectStockHero(page, box) {
  const hero = page.locator('#setupPrimaryAction .setup-hero');
  await expect(hero).toHaveAttribute('data-kind', 'stock');
  await expect(hero).toHaveAttribute('data-host', box.host);
  await expect(hero.locator('#netBoxName')).toHaveValue(box.friendlyName);
  await expect(page.locator(`.setup-target-card[data-kind="stock"][data-host="${box.host}"]`)).toHaveAttribute('aria-checked', 'true');
}

test.describe('UI 2.0 shell', () => {
  test.use({ ui2: true });

  test.describe('navigation', () => {
    test('1. the sidebar has one entry per view, Wishes and Help and info at the bottom', async ({ app }) => {
      const { page } = app;
      const items = page.locator('#ui2Sidebar .ui2-nav-item[data-view]');
      await expect(items).toHaveCount(NAV_VIEWS.length);
      expect(await items.evaluateAll((els) => els.map((e) => e.dataset.view))).toEqual(NAV_VIEWS);
      await expect(page.locator('#ui2NavWishes')).toHaveAttribute('data-view', 'wishes');
      await expect(page.locator('#ui2Sidebar .ui2-side-small #ui2NavWishes')).toHaveCount(1);
      await expect(page.locator('#ui2NavHelp')).toHaveAttribute('data-view', 'help');
      await expect(page.locator('#ui2Sidebar .ui2-side-small #ui2NavHelp')).toHaveCount(1);
    });

    test('2. every entry opens its view at the top and is the only one marked current', async ({ app }) => {
      const { page } = app;
      for (const view of ALL_VIEWS) {
        // Scroll the main pane first, so "starts at the top" is a real check.
        await page.evaluate(() => {
          const m = document.getElementById('ui2Main');
          m.scrollTop = m.scrollHeight;
        });
        await openView(page, view);
        const item = nav(page, ENTRY[view] || view);
        await expect(item).toHaveAttribute('aria-current', 'page');
        await expect(item).toHaveClass(/\bis-active\b/);
        await expect(page.locator('#ui2Sidebar .ui2-nav-item[aria-current]')).toHaveCount(1);
        await expect(page.locator('#ui2Sidebar .ui2-nav-item.is-active')).toHaveCount(1);
        // Polled: a scroll reset done in a frame callback or smoothly would
        // otherwise race this read.
        await expect.poll(() => page.locator('#ui2Main').evaluate((m) => m.scrollTop), { message: `#ui2Main scrollTop on ${view}` }).toBe(0);
        // Exactly one view is on screen.
        const shown = await page.locator('#ui2MainInner > [id^="view-"]').evaluateAll((els) => els
          .filter((e) => !e.classList.contains('hidden')).map((e) => e.id));
        expect(shown).toEqual([`view-${view}`]);
        // The classic footer (donate, world map, links) is hidden on every
        // preview view: Help and info carries all of it.
        await expect(page.locator('#appFooter'), `footer on ${view}`).toBeHidden();
      }
    });

    test('3. Tab from the top reaches the skip link, Enter moves focus to the main area', async ({ app }) => {
      const { page } = app;
      await page.evaluate(() => { if (document.activeElement) document.activeElement.blur(); });
      await page.keyboard.press('Tab');
      await expect(page.locator('#ui2Skip')).toBeFocused();
      await page.keyboard.press('Enter');
      await expect(page.locator('#ui2Main')).toBeFocused();
    });

    test('4. ArrowDown and ArrowUp move between entries and stop at both ends', async ({ app }) => {
      const { page } = app;
      await nav(page, 'box').focus();
      await page.keyboard.press('ArrowUp');
      await expect(nav(page, 'box')).toBeFocused();
      for (let i = 1; i < NAV_VIEWS.length; i++) {
        await page.keyboard.press('ArrowDown');
        await expect(nav(page, NAV_VIEWS[i])).toBeFocused();
      }
      await page.keyboard.press('ArrowDown');
      await expect(nav(page, 'help')).toBeFocused();
      await page.keyboard.press('ArrowUp');
      await expect(nav(page, 'wishes')).toBeFocused();
    });
  });

  test.describe('speaker list', () => {
    test('5. one row per speaker; the current one is marked and is the only tab stop', async ({ app }) => {
      const { page } = app;
      const rows = page.locator('#ui2SpkLists .ui2-spk-row');
      await expect(rows).toHaveCount(4);
      const hosts = await page.locator('#ui2SpkLists .ui2-spk-pick').evaluateAll((els) => els.map((e) => e.dataset.host));
      expect([...hosts].sort()).toEqual([LR.host, BR.host, OFFICE.host, KITCHEN.host].sort());
      await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
      await expect(page.locator('#ui2SpkLists .ui2-spk-pick[aria-current]')).toHaveCount(1);
      const tabStops = page.locator('#ui2SpkLists .ui2-spk-pick[tabindex="0"]');
      await expect(tabStops).toHaveCount(1);
      await expect(tabStops).toHaveAttribute('data-host', LR.host);
    });

    test('6. the offline speaker says Offline, the Bose original says Ready for STR', async ({ app }) => {
      const { page } = app;
      const office = row(page, OFFICE.host);
      await expect(office).toHaveClass(/\bis-offline\b/);
      await expect(office).toContainText(en('ui2.pill.offline'));
      await expect(row(page, KITCHEN.host)).toContainText(en('speaker.needsInstallBadge'));
      await expect(row(page, KITCHEN.host)).toHaveClass(/\bis-stock\b/);
    });

    test.describe('with the page clock', () => {
      test.use({ fakeClock: true });

      test('7. clicking Bedroom makes it the controlled speaker; the next Status goes to it', async ({ app, mock }) => {
        const { page } = app;
        await settle(page, 9_000);
        await mock.resetCalls();
        await pick(page, BR.host).click();
        await expect(pick(page, BR.host)).toHaveAttribute('aria-current', 'true');
        await expect(pick(page, LR.host)).not.toHaveAttribute('aria-current', /.*/);
        // The clock is paused: the only Status calls are the ones the click made.
        await mock.expectCalled('Status');
        const first = (await mock.calls('Status'))[0];
        expect(first.args[0]).toBe(BR.host);
        // And the regular poll keeps asking the new current speaker.
        await mock.resetCalls();
        await runFor(page, 30_000);
        expect(await mock.count('Status', onHost(BR.host))).toBeGreaterThan(0);
      });

      test('8. clicking the offline Office explains itself, searches again once, keeps Living Room', async ({ app, mock }) => {
        const { page } = app;
        await settle(page, 9_000);
        await mock.resetCalls();
        await pick(page, OFFICE.host).click();
        const toast = page.locator('#toast');
        await expect(toast).toHaveClass(/\bshow\b/);
        await expect(toast).toContainText('Offline');
        await mock.expectCalled('DiscoverBoxes', { times: 1 });
        await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
        await expect(pick(page, OFFICE.host)).not.toHaveAttribute('aria-current', /.*/);
      });

      test('10. "+" on an idle speaker adds it to the playing group', async ({ app, mock }) => {
        const { page } = app;
        // "+" needs the first every-speaker poll (play state and zones known).
        await settle(page, 30_000);
        const add = row(page, BR.host).locator('.ui2-spk-act[data-act="add"]');
        await expect(add).toBeVisible();
        await expect(add).toHaveAttribute('data-master', LR.host);
        await expect(row(page, OFFICE.host).locator('.ui2-spk-act')).toHaveCount(0);
        await expect(row(page, KITCHEN.host).locator('.ui2-spk-act')).toHaveCount(0);
        await mock.resetCalls();
        await add.click();
        // Group edits coalesce for 600 ms (views/music/group.js).
        await runFor(page, 1_000);
        await mock.expectCalled('FormZone', { times: 1 });
        const call = await mock.last('FormZone');
        expect(call.args[0]).toBe(LR.host);
        expect(JSON.stringify(call.args)).toContain(BR.host);
        // The group edit never moves the controlled speaker.
        await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
      });
    });

    test('9. clicking the Bose original asks, and confirming opens Setup on it', async ({ app }) => {
      const { page } = app;
      await pickStockAndConfirm(page, KITCHEN.host);
      await expect(nav(page, 'speakers')).toHaveAttribute('aria-current', 'page');
      await expectStockChosen(page, KITCHEN);
      // A stock speaker is never the controlled one.
      await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
    });
  });

  // Regression e92872f7: the sidebar stays reachable while Settings or Setup
  // is open, so a speaker chosen there must bring the open view along.
  test.describe('views follow the speaker picked in the sidebar', () => {
    test.describe('settings', () => {
      // The roster's Bedroom is a SoundTouch 10, which has no display and so
      // no clock section (#299); here it is a SoundTouch 20.
      test.use({ scenario: { boxPatches: { bedroom: { model: 'SoundTouch 20' } } } });

      test('12. Settings reloads for the new speaker and its buttons act on it', async ({ app, mock }) => {
        const { page } = app;
        await openView(page, 'settings');
        // GetClockDisplay is read by the Settings form only (views/settings/sound.js).
        await mock.expectCalled('GetClockDisplay', { pred: onHost(LR.host) });
        await expect(page.locator('#boxNameCombo #boxNameInput')).toHaveValue(LR.friendlyName);
        await mock.resetCalls();
        await pick(page, BR.host).click();
        await mock.expectCalled('GetClockDisplay', { pred: onHost(BR.host) });
        await expect(page.locator('#settingsBoxSelect')).toHaveValue(BR.deviceID);
        await expect(page.locator('#boxNameCombo #boxNameInput')).toHaveValue(BR.friendlyName);
        await expect(nav(page, 'speakers')).toHaveAttribute('aria-current', 'page');
        // The clock lives on the speaker page's Sound tab.
        await page.locator('#ui2SpkTab-sound').click();
        const on = page.locator('#boxClockOn');
        await expect(on).toBeVisible();
        await mock.resetCalls();
        await on.click();
        await mock.expectCalled('SetClockDisplay', { times: 1 });
        const set = await mock.last('SetClockDisplay');
        expect(set.args[0]).toBe(BR.host);
        expect(await mock.count('SetClockDisplay', onHost(LR.host))).toBe(0);
      });
    });

    test.describe('settings reconnect', () => {
      test.use({ fakeClock: true, scenario: { bindings: { BoxSettings: { byHost: { [LR.host]: { error: 'down' } } } } } });

      test('13. the reconnect timer of the previous speaker stops', async ({ app, mock }) => {
        const { page } = app;
        await openView(page, 'settings');
        await settle(page, 30_000);
        await expect(page.locator('#settingsBody .reconnect-banner')).toBeVisible();
        expect(await mock.count('BoxSettings', onHost(LR.host)), 'the reconnect timer runs').toBeGreaterThan(0);
        await pick(page, BR.host).click();
        await expect(page.locator('#settingsBoxSelect')).toHaveValue(BR.deviceID);
        await mock.resetCalls();
        await runFor(page, 120_000);
        expect(await mock.count('BoxSettings', onHost(LR.host))).toBe(0);
        await expect(page.locator('#boxNameCombo #boxNameInput')).toHaveValue(BR.friendlyName);
      });
    });

    // The roster's STR speakers say kind 'STR'; discovery (discovery.go KindSTR)
    // sends 'str', and the Setup picker lists only that, so these tests use the
    // backend's spelling.
    test.describe('setup', () => {
      const STR_KIND = { livingRoom: { kind: 'str' }, bedroom: { kind: 'str' }, office: { kind: 'str' } };
      test.use({ scenario: { boxPatches: STR_KIND } });

      // Setup draws its target picker on entry; clicking the open entry again
      // (switchView('setup')) draws it from state.setupTarget, which shows what
      // a later repaint would show, not just what is still on screen.
      const redrawSetup = (page) => page.locator('#ui2SpkSubnav .spa-sub[data-route="add"]').click();
      test('14. Add speaker follows the classic target: an STR pick chooses nothing, a factory reset stays, a stock pick replaces it', async ({ app }) => {
        const { page } = app;
        await openView(page, 'setup');
        await expect(page.locator('#ui2SetupWizard')).toBeVisible();
        // An STR speaker is never an install target (phase 4): picking one in
        // the sidebar chooses nothing in Find.
        await pick(page, BR.host).click();
        await expect(pick(page, BR.host)).toHaveAttribute('aria-current', 'true');
        await expect(page.locator('#w2FindList input.w2-radio:checked')).toHaveCount(0);
        // A chosen factory reset survives a pick in the sidebar, and a repaint.
        const factory = page.locator('.setup-target-card[data-kind="factory-reset"]');
        await findRadio(page, '__factory').check();
        await expect(factory).toHaveAttribute('aria-checked', 'true');
        await expect(page.locator('.setup-target-factory-help').first()).toBeVisible();
        await pick(page, LR.host).click();
        await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
        await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
        await expect(factory).toHaveAttribute('aria-checked', 'true');
        await expect(findRadio(page, '__factory')).toBeChecked();
        await redrawSetup(page);
        await expect(factory).toHaveAttribute('aria-checked', 'true');
        await expect(findRadio(page, '__factory')).toBeChecked();
        await expect(page.locator('.setup-target-factory-help').first()).toBeVisible();
        await expect(page.locator('#setupPrimaryAction .setup-hero')).toHaveCount(0);
        // A stock speaker replaces it (pickBox asks first).
        await pickStockAndConfirm(page, KITCHEN.host);
        await expect(factory).toHaveAttribute('aria-checked', 'false');
        await expectStockChosen(page, KITCHEN);
      });

      test.describe('running install', () => {
        test.use({ scenario: { boxPatches: STR_KIND, bindings: { InstallSTROnBox: { never: true } } } });

        test('15. a running install keeps its target', async ({ app, mock }) => {
          const { page } = app;
          await pickStockAndConfirm(page, KITCHEN.host);
          await expectStockChosen(page, KITCHEN);
          await page.locator('#w2Foot .w2-act[data-act="next"]').click();
          await passCheck(page);
          await expectStockHero(page, KITCHEN);
          await page.locator('#setupHeroInstall').click();
          await mock.expectCalled('InstallSTROnBox', { pred: onHost(KITCHEN.host), times: 1 });
          const running = en('setup.netInstallOn', { name: KITCHEN.friendlyName });
          await expect(page.locator('#setupResult')).toContainText(running);
          await pick(page, BR.host).click();
          await expect(pick(page, BR.host)).toHaveAttribute('aria-current', 'true');
          await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
          await expectStockHero(page, KITCHEN);
          await expect(page.locator('#setupHeroInstall')).toBeDisabled();
          await expect(page.locator('#setupResult')).toContainText(running);
          // And after a repaint of the picker, too.
          await redrawSetup(page);
          await expectStockHero(page, KITCHEN);
          await expect(page.locator('#setupHeroInstall')).toBeDisabled();
          await expect(page.locator('#setupResult')).toContainText(running);
          expect(await mock.count('InstallSTROnBox')).toBe(1);
        });
      });
    });
  });

  test.describe('player', () => {
    test('16. the player is on every view; in Setup only what plays stays', async ({ app }) => {
      const { page } = app;
      const player = page.locator('#ui2Player');
      for (const view of ALL_VIEWS.filter((v) => v !== 'setup')) {
        await openView(page, view);
        await expect(player).toBeVisible();
        await expect(player.locator('.ui2-player-transport')).toBeVisible();
        await expect(player.locator('.ui2-player-volume')).toBeVisible();
      }
      await openView(page, 'setup');
      await expect(player).toBeVisible();
      await expect(player.locator('.ui2-player-now')).toBeVisible();
      for (const sel of ['.ui2-player-transport', '.ui2-player-volume', '.ui2-player-groupvol', '.ui2-player-more']) {
        const el = player.locator(sel);
        await expect(el, sel).toBeHidden();
        await expect(el, sel).toHaveAttribute('hidden', '');
        await expect(el, sel).toHaveAttribute('inert', '');
      }
      await expect(player.locator('.ui2-player-levels')).toBeHidden();
      await expect(player.locator('.ui2-player-empty')).toBeHidden();
    });

    test.describe('without speakers', () => {
      test.use({ fakeClock: true, scenario: { boxes: [], current: null, seed: { cachedBoxes: false } } });

      test('17. no speaker: the player says so and shows no control', async ({ app }) => {
        const { page } = app;
        const player = page.locator('#ui2Player');
        await expect(player.locator('.ui2-player-empty')).toBeVisible();
        // The 6 s recovery sweep and the first polls run without a page error
        // (the fixture fails the test on one).
        await settle(page, 9_000);
        await expect(player.locator('.ui2-player-empty')).toBeVisible();
        for (const sel of ['.ui2-player-now', '.ui2-player-transport', '.ui2-player-volume', '.ui2-player-groupvol', '.ui2-player-more', '.ui2-player-levels']) {
          await expect(player.locator(sel), sel).toBeHidden();
        }
        await expect(page.locator('#ui2SpkLists .ui2-spk-row')).toHaveCount(0);
      });
    });

    test.describe('speaker update', () => {
      test.use({
        fakeClock: true,
        scenario: {
          current: 'oldAgent',
          banners: { speakerUpdate: true },
          bindings: { RunSpeakerUpdate: { never: true } },
          routes: { '/api/stick/status': { status: 200, body: { mounted: false } } },
        },
      });

      test('18. while a speaker update runs, transport and volume leave the player and More closes', async ({ app }) => {
        const { page } = app;
        await settle(page, 9_000);
        const card = page.locator('#speakerUpdateCard');
        await expect(card).toBeVisible();
        await page.locator('#ui2MoreBtn').click();
        await expect(page.locator('#ui2MorePop')).toBeVisible();
        await card.locator('#sucUpdate').click();
        const player = page.locator('#ui2Player');
        await expect(player.locator('.ui2-player-transport')).toBeHidden();
        await expect(player.locator('.ui2-player-volume')).toBeHidden();
        await expect(page.locator('#ui2MorePop')).toBeHidden();
        await expect(page.locator('#ui2MoreBtn')).toHaveAttribute('aria-expanded', 'false');
        await expect(player.locator('.ui2-player-now')).toBeVisible();
      });
    });

    test('19. only the track line is a live region; the clock is not announced', async ({ app }) => {
      const { page } = app;
      const live = page.locator('#ui2Player [aria-live="polite"]');
      await expect(live).toHaveCount(1);
      await expect(live).toHaveId('statusMain');
      const bar = page.locator('#ui2Player #statusBar');
      await expect(bar).toHaveCount(1);
      await expect(bar).not.toHaveAttribute('role', /.*/);
      await expect(bar).not.toHaveAttribute('aria-live', /.*/);
    });
  });

  // The group volume and the per-speaker Levels, in the player.
  test.describe('group volume and Levels', () => {
    test.describe('grouped', () => {
      test.use({
        fakeClock: true,
        scenario: { zones: { livingRoom: LR_BR_GROUP, bedroom: LR_BR_GROUP }, playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' } },
      });

      test('20. a group puts the group volume in the player and Levels lists every member', async ({ app }) => {
        const { page } = app;
        await settle(page, 9_000);
        const player = page.locator('#ui2Player');
        await expect(player).toHaveClass(/\bis-grouped\b/);
        await expect(page.locator('#ui2GroupVolSlot #groupVolume')).toHaveCount(1);
        await expect(page.locator('#ui2GroupVolSlot #groupVolume')).toBeVisible();
        const levels = page.locator('#ui2LevelsBtn');
        await expect(levels).toBeVisible();
        // Short on the button, so a long word for "more" never cuts the name;
        // the whole sentence is its name and its tooltip.
        await expect(page.locator('#ui2LevelsText')).toHaveText(`${LR.friendlyName} +1`);
        await expect(levels).toHaveAccessibleName(`${en('ui2.player.levels')}: ${en('ui2.player.outputGroup', { name: LR.friendlyName, n: 1 })}`);
        await levels.click();
        await expect(levels).toHaveAttribute('aria-expanded', 'true');
        const pop = page.locator('#ui2LevelsPop');
        await expect(pop).toBeVisible();
        const members = pop.locator('.group-member-vol');
        await expect(members).toHaveCount(2);
        await expect(members.locator('input[type="range"]')).toHaveCount(2);
        await expect(members.nth(0)).toContainText(LR.friendlyName);
        await expect(members.nth(1)).toContainText(BR.friendlyName);
        await expect(pop.locator(`input[data-host="${BR.host}"]`)).toBeVisible();
        // The group slider lives in the player, not twice.
        await expect(pop.locator('#groupVolume')).toHaveCount(0);
      });
    });

    test.describe('solo', () => {
      test.use({ fakeClock: true });

      test('21. a speaker on its own has no Levels and no group volume', async ({ app }) => {
        const { page } = app;
        await settle(page, 9_000);
        const player = page.locator('#ui2Player');
        await expect(player.locator('.ui2-player-sound')).toBeVisible();
        await expect(player.locator('.ui2-player-volume #musicVolume')).toBeVisible();
        await expect(player).not.toHaveClass(/\bis-grouped\b/);
        await expect(page.locator('#ui2LevelsBtn')).toBeHidden();
        await expect(player.locator('.ui2-player-levels')).toHaveAttribute('inert', '');
        await expect(page.locator('#ui2GroupVolSlot #groupVolume')).toHaveCount(0);
        await expect(page.locator('#ui2LevelsText')).toHaveText(en('ui2.player.outputSolo', { name: LR.friendlyName }));
      });
    });
  });

  test.describe('More', () => {
    test('24. More opens with the queue and the inputs', async ({ app }) => {
      const { page } = app;
      await page.locator('#ui2MoreBtn').click();
      await expect(page.locator('#ui2MoreBtn')).toHaveAttribute('aria-expanded', 'true');
      const pop = page.locator('#ui2MorePop');
      await expect(pop).toBeVisible();
      await expect(pop.locator('#ui2MoreBody #sourceButtons')).toBeVisible();
      await expect(pop.locator('#ui2MoreBody #queueControls')).toHaveCount(1);
    });

    // The demo settings list AUX and Bluetooth without isLocal, which the Music
    // view's input row (sourceinputs.js) reads as "no physical input"; here
    // the speaker reports them as its own sockets.
    test.describe('with inputs', () => {
      test.use({
        scenario: {
          bindings: {
            BoxSettings: {
              byHost: {
                [LR.host]: {
                  value: {
                    ...SETTINGS,
                    sources: [
                      { source: 'AUX', sourceAccount: 'AUX', status: 'READY', isLocal: true },
                      { source: 'BLUETOOTH', sourceAccount: '', status: 'READY', isLocal: true },
                    ],
                  },
                },
              },
            },
          },
        },
      });

    test('25. the first input button switches the current speaker to it', async ({ app, mock }) => {
      const { page } = app;
      await page.locator('#ui2MoreBtn').click();
      const first = page.locator('#ui2MorePop #sourceInputs .btn-source').first();
      await expect(first).toBeVisible();
      const source = await first.getAttribute('data-source');
      await mock.resetCalls();
      await first.click();
      await mock.expectCalled('SelectBoxSource', { times: 1 });
      const call = await mock.last('SelectBoxSource');
      expect(call.args.slice(0, 3)).toEqual([LR.host, LR.port, source]);
    });
    });
  });

  test.describe('badges', () => {
    test.describe('a speaker due an update', () => {
      test.use({ fakeClock: true, scenario: { banners: { speakerUpdate: true } } });

      test('26. Speakers shows the count with a spoken name', async ({ app }) => {
        const { page } = app;
        await settle(page, 9_000);
        const badge = page.locator('#ui2SpeakersBadge');
        await expect(badge).toBeVisible();
        await expect(badge).toHaveText('1');
        await expect(badge).toHaveAttribute('aria-label', en('ui2.nav.updatesCount', { n: 1 }));
      });
    });

    test.describe('everything current', () => {
      test.use({ fakeClock: true });

      test('27. no badge and no name', async ({ app }) => {
        const { page } = app;
        await settle(page, 9_000);
        const badge = page.locator('#ui2SpeakersBadge');
        await expect(badge).toBeHidden();
        await expect(badge).not.toHaveAttribute('aria-label', /.*/);
      });
    });
  });
});

// The Layout switch in the Aa menu, starting from the current layout.
test.describe('Layout switch', () => {
  test.use({ ui2: false });

  const stored = (page) => page.evaluate(() => {
    const ls = window.localStorage;
    return { ui2: ls.getItem('str.ui2Preview'), theme: ls.getItem('a11yTheme'), scale: ls.getItem('a11yScale') };
  });

  test('28. switches to the preview and back, and both survive a reload', async ({ app }) => {
    const { page } = app;
    const before = await stored(page);
    expect(before.ui2).toBe('0');
    await page.locator('#a11yTrigger').click();
    await expect(page.locator('#a11yMenu')).toBeVisible();
    await expect(page.locator('#a11yMenu [data-ui2="0"]')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('#a11yMenu [data-ui2="1"]')).toHaveAttribute('aria-pressed', 'false');

    let loaded = page.waitForEvent('load');
    await page.locator('#a11yMenu [data-ui2="1"]').click();
    await loaded;
    await expect(page.locator('html')).toHaveClass(/\bui2\b/);
    await expect(page.locator('#ui2Shell')).toBeVisible();
    expect((await stored(page)).ui2).toBe('1');

    await page.reload();
    await expect(page.locator('html')).toHaveClass(/\bui2\b/);
    await expect(page.locator('#ui2Shell')).toBeVisible();

    // In the shell the Aa menu lives in the sidebar's app settings.
    await page.locator('#ui2AppSettings #a11yTrigger').click();
    await expect(page.locator('#ui2AppSettings #a11yMenu [data-ui2="1"]')).toHaveAttribute('aria-pressed', 'true');
    loaded = page.waitForEvent('load');
    await page.locator('#ui2AppSettings #a11yMenu [data-ui2="0"]').click();
    await loaded;
    await expect(page.locator('#presets .preset-body').first()).toBeVisible();
    await expect(page.locator('html')).not.toHaveClass(/\bui2\b/);
    expect((await stored(page)).ui2).toBe('0');

    await page.reload();
    await expect(page.locator('#presets .preset-body').first()).toBeVisible();
    await expect(page.locator('html')).not.toHaveClass(/\bui2\b/);
    await expect(page.locator('#ui2Shell')).toHaveCount(0);

    const after = await stored(page);
    expect({ theme: after.theme, scale: after.scale }).toEqual({ theme: before.theme, scale: before.scale });
  });

  test.describe('on the fake clock', () => {
    test.use({ fakeClock: true });

    test('29. the layout already on screen does not reload', async ({ app }) => {
      const { page } = app;
      await page.evaluate(() => { window.__mark = 1; });
      let navigations = 0;
      page.on('framenavigated', (f) => { if (f === page.mainFrame()) navigations++; });
      await page.locator('#a11yTrigger').click();
      const current = page.locator('#a11yMenu [data-ui2="0"]');
      await current.click();
      // A reload need not start at once (a deferred timer), so run a second
      // of timers before asserting nothing happened: a reload would have
      // counted a navigation and dropped the mark.
      await settle(page, 1000);
      expect(navigations).toBe(0);
      expect(await page.evaluate(() => window.__mark)).toBe(1);
      await expect(current).toHaveAttribute('aria-pressed', 'true');
      await expect(page.locator('#a11yMenu [data-ui2="1"]')).toHaveAttribute('aria-pressed', 'false');
      expect((await stored(page)).ui2).toBe('0');
    });
  });

  test.describe('storage that throws', () => {
    // A WebView that refuses site data: every localStorage access throws.
    // A string, not a function: test.use reads a function as a fixture.
    test.use({
      preInit: `Object.defineProperty(window, 'localStorage', {
        configurable: true,
        get() { throw new DOMException('The operation is insecure.', 'SecurityError'); },
      });`,
      // No storage, no remembered speaker: with several on the network none is
      // chosen, so there are no presets to wait for. The header is enough.
      ready: '#a11yTrigger',
    });

    test('30. boots the current layout and offers no switch it cannot keep', async ({ app }) => {
      const { page } = app;
      await expect(page.locator('html')).not.toHaveClass(/\bui2\b/);
      await expect(page.locator('#a11yMenu [data-ui2]')).toHaveCount(2);
      // Fix 5.1 of the e2e design: a switch whose reload would come back
      // without the choice is drawn disabled, and the hint says why.
      await page.locator('#a11yTrigger').click();
      await expect(page.locator('#a11yMenu')).toBeVisible();
      await expect(page.locator('#a11yMenu [data-ui2="0"]')).toBeDisabled();
      await expect(page.locator('#a11yMenu [data-ui2="1"]')).toBeDisabled();
      await expect(page.locator('#a11yLayoutHint')).not.toBeEmpty();
      await page.locator('#a11yMenu [data-ui2="1"]').click({ force: true });
      await expect(page.locator('html')).not.toHaveClass(/\bui2\b/);
    });
  });
});
