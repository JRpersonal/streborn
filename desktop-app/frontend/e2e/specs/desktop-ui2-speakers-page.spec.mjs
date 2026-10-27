// The UI 2.0 page for one speaker (#1287, phase 3; shell2/speakers/speaker.js):
// the five tabs hold the classic Speaker settings, every control of it in a
// fixed tab (rule 4, "nothing disappears"), the confirmations name the
// speaker (rule 1), a speaker that does not answer explains itself in the
// open tab (rule 3), and the classic layout is unchanged.
//
// CONTROLS below is the checklist made from views/settings/*: every control
// of today's Speaker settings and the tab it lives on now. A control the
// demo speaker renders always is checked visible on its tab and hidden on
// the others; the ones that appear only in a state (an update ready, a home
// theater, SSH open, ...) are checked for their tab wherever they render.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-ui2-speakers-page.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';
import { onHost } from '../helpers/mockclient.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const OLD = ROSTER.oldAgent;
const OFFICE = ROSTER.office;
const NURSERY = ROSTER.nursery;

const TAB_NAME = { overview: 'Overview', sound: 'Sound', network: 'Network', keys: 'Keys and alarms', maintenance: 'Maintenance' };

// tab -> { shown: always on screen on that tab, placed: rendered in some
// state only, or folded inside an expert section }
const CONTROLS = {
  overview: {
    shown: ['#stickInfoBody', '#boxNameInput', '#boxNameToggle', '#boxNameSave', '#boxLangSelect', '#boxLangSave',
      '#phoneUrlCopy', '#phoneQrImg', '.sources-grid', '.sps-to-tab'],
    placed: ['#stickInfoUpdateBtn', '#settingsUpdateAllBtn', '#securityRebootBtn', '#fwUpdateBtn', '#phoneHowto', '#phoneUrlSwap'],
  },
  sound: {
    shown: ['#boxVolume', '#boxBass', '#boxBassReset', '#resumeOnPowerOn', '#resumeOnPowerOff'],
    placed: ['#boxStartVolMark', '#boxStartVolOn', '#boxBalanceSlider', '#boxBalanceCentre', '#boxLevelCentre', '#boxLevelSurround',
      '#boxClockFormat', '#boxClockOn', '#boxClockOff', '#displayTrackOn', '#displayTrackOff',
      '#displayTrackModeArtist', '#displayTrackModeTitle', '#displayTrackModeBoth', '#displayClockOn', '#displayClockOff',
      '#displayMsgOn', '#displayMsgOff', '#displaySplashOn', '#displaySplashOff', '#displayNoticesOn', '#displayNoticesOff',
      '#arcadeOpenThread', '#airplayOptOn', '#airplayOptOff',
      '#announceText', '#announceLang', '#announceTranslate', '#announceSend', '#announceVolume', '#announceCopy'],
  },
  network: {
    shown: ['#wlanSwitchToggle', '#appRegionSelect', '#appRegionSave', '#wlanStoredList', '#musicLibList'],
    placed: ['#boxWlanSelect', '#boxWlanRefresh', '#boxWlanSSID', '#boxWlanHidden', '#boxWlanPass', '#boxWlanShowPass',
      '#boxWlanForgetOthers', '#boxWlanSave', '#wlanForgetOthersBtn', '#usServicesBody'],
  },
  keys: {
    shown: ['#boxSyncPresetsBtn', '#webhookSection > summary'],
    placed: ['#webhookKeyMap', '#webhookTarget', '#webhookMode', '#keyActionSelect', '#webhookType', '#webhookUrl',
      '#webhookMethod', '#webhookBody', '#webhookMac', '#webhookUdpHost', '#webhookUdpPort', '#webhookUdpEnc',
      '#webhookUdpPayload', '#webhookOn', '#webhookOff', '#webhookTestBtn', '#webhookSaveBtn', '#webhookHaGuideBtn',
      '#copyPresetTarget', '#copyPresetBtn', '#urlPresetSlot', '#urlPresetName', '#urlPresetUrl', '#urlPresetSaveBtn',
      '#voiceGuideBtn'],
  },
  maintenance: {
    shown: ['#boxIdentifyBtn', '#boxSaveLogsBtn', '#boxEmailSupportBtn', '#boxRebootBtn', '#boxTrueFactoryResetBtn',
      '#boxRemoveSTRBtn', '#backupExportBtn', '#backupImportBtn'],
    placed: ['#boxRemoveConflictBtn', '#sshOn', '#sshOff', '#foreignList', '#boxRestoreXml', '#boxRestoreBtn'],
  },
};
// Every section on the page: the 31 of the classic view (with a second STR
// speaker, so "Copy keys" renders too) and the page's own two cards.
const ALL_SECTIONS = 33;

const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);
const tab = (page, key) => page.locator(`#ui2SpkTab-${key}`);
const pick = (page, host) => page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${host}"]`);

async function openSpeaker(page, host, name) {
  await nav(page, 'speakers').click();
  await page.locator(`#view-speakers .spl-open[data-host="${host}"]`).click();
  await expect(page.locator('#ui2SpkPageName')).toHaveText(name);
}

// tabOf is the tab whose wrapper holds the first match of sel ('' if none).
const tabOf = (page, sel) => page.locator(sel).first().evaluate((el) => {
  const w = el.closest('.sps-sec[data-ui2-tab]');
  return w ? w.dataset.ui2Tab : '';
});

test.describe('UI 2.0 one speaker page', () => {
  test.use({ ui2: true, scenario: speakersScenario() });

  test('1. every classic control has its tab, and each tab shows only its own', async ({ app }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await expect(page.locator('#ui2SpkPanels .sps-sec')).toHaveCount(5);
    await expect(page.locator('#ui2SpkPanels .sps-sec > .settings-section[data-ui2-sec]')).toHaveCount(ALL_SECTIONS);
    // The accordion groups are gone in the preview; the tabs replace them.
    await expect(page.locator('#settingsBody details.settings-group')).toHaveCount(0);
    for (const [key, { shown, placed }] of Object.entries(CONTROLS)) {
      await tab(page, key).click();
      await expect(tab(page, key)).toHaveAttribute('aria-selected', 'true');
      const panel = page.getByRole('tabpanel', { name: TAB_NAME[key] });
      await expect(panel).toBeVisible();
      for (const sel of shown) {
        await expect(panel.locator(sel).first(), `${sel} on ${key}`).toBeVisible();
        expect(await tabOf(page, sel), sel).toBe(key);
      }
      for (const sel of placed) {
        if (await page.locator(sel).count()) expect(await tabOf(page, sel), sel).toBe(key);
      }
      // The other tabs' controls are out of sight.
      for (const [other, c] of Object.entries(CONTROLS)) {
        if (other !== key) await expect(page.locator(c.shown[0]).first(), `${c.shown[0]} on ${key}`).toBeHidden();
      }
    }
  });

  test('2. the head says which speaker, its state and what it plays; the list refresh moved into it', async ({ app }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await expect(page.locator('#ui2SpkPageState .u-pill')).toHaveText('Playing');
    await expect(page.locator('#ui2SpkPageActions #settingsRefreshBtn')).toBeVisible();
    await expect(page.locator('#ui2SpkPageActions #settingsRefreshBtn')).toHaveAccessibleName('Search speaker list again');
    // The classic title and dropdown are the page head's and the sidebar's job.
    await expect(page.locator('#view-settings > h2')).toBeHidden();
    await expect(page.locator('#settingsBoxSelect')).toBeHidden();
    await openSpeaker(page, OLD.host, OLD.friendlyName);
    await expect(page.locator('#ui2SpkPageState [data-chip="update"]')).toBeVisible();
    // Update ready: the update sits in the Overview's STR card, named for the speaker.
    await expect(page.getByRole('tabpanel', { name: 'Overview' }).locator('#stickInfoUpdateBtn')).toBeVisible();
    await expect(page.locator('#stickInfoBody')).toContainText(OLD.friendlyName);
  });

  test('3. the Network card on Overview leads to the Network tab', async ({ app }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    const card = page.locator('[data-ui2-sec="netSummary"]');
    await expect(card).toBeVisible();
    await card.getByRole('button', { name: 'Network details' }).click();
    await expect(tab(page, 'network')).toHaveAttribute('aria-selected', 'true');
    await expect(tab(page, 'network')).toBeFocused();
    await expect(page.locator('#wlanSwitchToggle')).toBeVisible();
  });

  test('4. restart, factory reset and remove STR ask first and name the speaker', async ({ app, mock }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await tab(page, 'maintenance').click();
    await mock.resetCalls();
    for (const id of ['boxRebootBtn', 'boxTrueFactoryResetBtn', 'boxRemoveSTRBtn']) {
      await page.locator(`#${id}`).click();
      await expect(page.locator('#warnModal')).toBeVisible();
      await expect(page.locator('#warnModal .warn-title')).toContainText(`${LR.friendlyName}: `);
      await page.locator('#warnCancel').click();
      await expect(page.locator('#warnModal')).toBeHidden();
    }
    for (const b of ['RebootBox', 'TrueFactoryReset', 'UninstallSTR']) expect(await mock.count(b), b).toBe(0);
    // Confirmed, the restart goes to this speaker and nowhere else.
    await page.locator('#boxRebootBtn').click();
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('RebootBox', { pred: onHost(LR.host) });
  });

  test('5. the tab stays when the sidebar moves the page to another speaker', async ({ app, mock }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await tab(page, 'sound').click();
    await mock.resetCalls();
    await pick(page, BR.host).click();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(BR.friendlyName);
    await expect(tab(page, 'sound')).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#boxNameInput')).toHaveValue(BR.friendlyName);
    await expect(page.getByRole('tabpanel', { name: 'Sound' }).locator('#boxVolume')).toBeVisible();
    await expect(page.locator('#boxVolume')).toHaveValue('18');
    await page.locator('#boxBassReset').click();
    await mock.expectCalled('SetBoxBass', { pred: onHost(BR.host) });
  });

  test.describe('a speaker that does not answer', () => {
    test.use({ scenario: mergeScenario(speakersScenario(), { bindings: { BoxSettings: { byHost: { [OFFICE.host]: { error: 'down' } } } } }) });

    test('6. explains itself in the open tab, with nothing of the previous speaker left', async ({ app }) => {
      const { page } = app;
      await openSpeaker(page, LR.host, LR.friendlyName);
      await tab(page, 'network').click();
      await expect(page.locator('#wlanSwitchToggle')).toBeVisible();
      await openSpeaker(page, OFFICE.host, OFFICE.friendlyName);
      await expect(page.locator('#ui2SpkPageState .u-pill')).toHaveText('Offline');
      await expect(tab(page, 'network')).toHaveAttribute('aria-selected', 'true');
      const panel = page.getByRole('tabpanel', { name: 'Network' });
      await expect(panel.locator('#settingsBody .reconnect-banner')).toBeVisible();
      await expect(page.locator('#settingsBody .settings-section')).toHaveCount(0);
      // The same explanation on every tab: it is not a section of one.
      await tab(page, 'overview').click();
      await expect(page.getByRole('tabpanel', { name: 'Overview' }).locator('.reconnect-banner')).toBeVisible();
    });
  });

  test('7. a speaker starting up says so in its head', async ({ app }) => {
    const { page } = app;
    await openSpeaker(page, NURSERY.host, NURSERY.friendlyName);
    await expect(page.locator('#ui2SpkPageState .u-pill')).toHaveText('Starting up');
  });
});

test.describe('classic Speaker settings with the phase 3 network', () => {
  test.use({ ui2: false, scenario: speakersScenario() });

  test('8. is unchanged: the groups, the title, the dropdown, no tabs', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="settings"]').click();
    await expect(page.locator('#view-settings > h2')).toBeVisible();
    await expect(page.locator('#settingsBoxSelect')).toBeVisible();
    await expect(page.locator('.settings-box-switcher #settingsRefreshBtn')).toBeVisible();
    await expect(page.locator('#view-settings > #settingsBody > details.settings-group').first()).toBeVisible();
    for (const id of ['ui2SpkPanels', 'ui2SpkPage']) await expect(page.locator(`#${id}`)).toHaveCount(0);
    await expect(page.locator('#settingsBody .sps-sec, #settingsBody .sps-own')).toHaveCount(0);
    // The repair button is still in the Actions grid.
    await expect(page.locator('.actions-grid > #boxSyncPresetsBtn')).toHaveCount(1);
    // Restart asks with its classic title.
    const group = page.locator('#settingsBody details.settings-group').filter({ has: page.locator('#boxRebootBtn') });
    await group.locator('summary').click();
    await page.locator('#boxRebootBtn').click();
    await expect(page.locator('#warnModal .warn-title')).not.toContainText(`${LR.friendlyName}:`);
    await page.locator('#warnCancel').click();
    expect(await mock.count('RebootBox')).toBe(0);
  });
});
