// The UI 2.0 setup wizard, Speakers > Add speaker (#1288, phase 4): the frame
// (stepper, Back, leaving mid-install), Find, Install and Confirm.
//
// The wizard moves the classic setup's own nodes into its steps and reads its
// screens; these tests drive it the way a user does and check that the step
// says what the speaker did. The scenario's installFlow (testkit) makes the
// Bose original Kitchen behave like a real one: no agent until the install
// went through, then discovery lists it as STR and its agent answers.
//
// The Check step (check.js) reads the Bose firmware; the mock's speakers are
// on 27.0.6, so the check passes on its own and Install unlocks in the
// footer. passCheck presses it.
//
//   E2E_PORT=4190 npx playwright test specs/desktop-ui2-setup.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER, SETUP_CHECK, STOCK_BOX } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const KITCHEN = ROSTER.kitchen;
const LADDER_PLAN = SETUP_CHECK.ladderPlan;
// A second Bose original that is switched off.
const ATTIC = { ...STOCK_BOX, name: 'Attic', friendlyName: 'Attic', host: '192.0.2.58', deviceID: 'A1A2A3A4A5A6', offline: true };

const W = (key, vars) => en(`ui2.setup.wizard.${key}`, vars);
const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);
const sub = (page, route) => page.locator(`#ui2SpkSubnav .spa-sub[data-route="${route}"]`);
const radio = (page, value) => page.locator(`#w2FindList input.w2-radio[value="${value}"]`);
const step = (page, name) => page.locator(`#w2Steps .w2-step[data-step="${name}"]`);
const foot = (page, act) => page.locator(`#w2Foot .w2-act[data-act="${act}"]`);
const title = (page) => page.locator('#w2Title');

test.use({
  ui2: true,
  scenario: {
    boxes: ['livingRoom', 'bedroom', 'kitchen', ATTIC],
    installFlow: true,
  },
});

async function openWizard(page) {
  await nav(page, 'speakers').click();
  await sub(page, 'add').click();
  await expect(page.locator('#ui2MainInner > #view-setup #ui2SetupWizard')).toBeVisible();
  await expect(title(page)).toHaveText(W('title.find'));
}

async function stepState(page, name, state) {
  await expect(step(page, name).locator('.w2-step-state')).toHaveText(W(`state.${state}`));
}

async function chooseKitchen(page) {
  await radio(page, KITCHEN.host).check();
  await expect(foot(page, 'next')).toHaveText(W('next', { name: KITCHEN.friendlyName }));
  await expect(foot(page, 'next')).not.toHaveAttribute('aria-disabled', 'true');
}

// passCheck goes through the Check step: the speaker on 27.0.6 passes, the
// step reads Done and Install unlocks in the footer.
async function passCheck(page) {
  const go = foot(page, 'install');
  await expect(go).toHaveText(W('check.toInstall', { name: KITCHEN.friendlyName }));
  await expect(go).not.toHaveAttribute('aria-disabled', 'true');
  await stepState(page, 'check', 'done');
  await go.click();
  await expect(page.locator('#w2PanelInstall')).toBeVisible();
}

async function toInstall(page) {
  await openWizard(page);
  await chooseKitchen(page);
  await foot(page, 'next').click();
  await expect(title(page)).toHaveText(W('title.check', { name: KITCHEN.friendlyName }));
  await stepState(page, 'find', 'done');
  await expect(step(page, 'check')).toHaveAttribute('aria-current', 'step');
  await passCheck(page);
  await expect(title(page)).toHaveText(W('title.install', { name: KITCHEN.friendlyName }));
  await expect(step(page, 'install')).toHaveAttribute('aria-current', 'step');
  const hero = page.locator('#w2PanelInstall #setupPrimaryAction .setup-hero');
  await expect(hero).toHaveAttribute('data-kind', 'stock');
  await expect(hero).toHaveAttribute('data-host', KITCHEN.host);
}

test.describe('UI 2.0 setup wizard', () => {
  test('1. Find offers the Bose originals, shows STR speakers as such, and names the speaker', async ({ app }) => {
    const { page } = app;
    await openWizard(page);
    // Four steps, the first current, every state in words.
    await expect(page.locator('#w2Steps .w2-step')).toHaveCount(4);
    await expect(step(page, 'find')).toHaveAttribute('aria-current', 'step');
    await stepState(page, 'find', 'current');
    for (const s of ['check', 'install', 'confirm']) await stepState(page, s, 'waiting');
    // The classic cards are not on screen; the wizard's list is.
    await expect(page.locator('#view-setup .setup-target-card').first()).toBeHidden();
    await expect(radio(page, KITCHEN.host)).toBeEnabled();
    await expect(radio(page, ATTIC.host)).toBeDisabled();
    await expect(page.locator(`#w2FindList .w2-row[data-host="${ATTIC.host}"]`)).toContainText(W('find.offlineNote', { name: 'Attic' }));
    await expect(radio(page, LR.host)).toHaveCount(0);
    await expect(radio(page, BR.host)).toHaveCount(0);
    // Nothing chosen yet: Next says why it waits.
    await expect(foot(page, 'next')).toHaveAttribute('aria-disabled', 'true');
    await expect(page.locator('#w2NextHint')).toHaveText(W('find.chooseHint'));
    await expect(foot(page, 'next')).toHaveAttribute('aria-describedby', 'w2NextHint');
    // aria-disabled keeps it in the tab order with its reason; a press does nothing.
    await foot(page, 'next').click({ force: true });
    await expect(title(page)).toHaveText(W('title.find'));
    await chooseKitchen(page);
    await expect(page.locator('.setup-target-card[data-kind="stock"][data-host="192.0.2.51"]')).toHaveAttribute('aria-checked', 'true');
    // A speaker on STR: shown as such, with the way to its page.
    await page.locator('#w2OnStr > summary').click();
    const open = page.getByRole('button', { name: W('find.openLabel', { name: BR.friendlyName }) });
    await expect(open).toBeVisible();
    await open.click();
    await expect(page.locator('#ui2MainInner > #view-settings')).toBeVisible();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(BR.friendlyName);
  });

  test('2. a speaker not on the network yet gets the Wi-Fi onboarding and the stick route', async ({ app, mock }) => {
    const { page } = app;
    await openWizard(page);
    await radio(page, '__factory').check();
    await expect(page.locator('#w2Onboard .setup-target-factory-help').first()).toBeVisible();
    await expect(page.locator('#w2Onboard #boseAppAndroidBtn')).toBeVisible();
    await expect(page.locator('#w2Onboard #setupAPPushPanel')).toBeAttached();
    await mock.expectCalled('ProbeSetupAP');
    await expect(page.locator('#w2Foot')).toContainText(W('find.factoryHint'));
    await foot(page, 'stick').click();
    await expect(title(page)).toHaveText(W('title.install', { name: W('newSpeaker') }));
    await stepState(page, 'check', 'skipped');
    await expect(page.locator('#w2PanelInstall #setupStickDetails')).toHaveAttribute('open', '');
    await expect(page.locator('#w2PanelInstall #drivesList')).toBeVisible();
    await expect(page.locator('#w2InstallIntro')).toContainText(W('install.introStick', { name: W('newSpeaker') }));
    // Back leads to Find, the choice kept.
    await foot(page, 'back').click();
    await expect(title(page)).toHaveText(W('title.find'));
    await expect(radio(page, '__factory')).toBeChecked();
  });

  test('3. install, then Confirm: done only when the agent answers, and the speaker page opens', async ({ app, mock }) => {
    const { page } = app;
    await toInstall(page);
    await stepState(page, 'check', 'done');
    await expect(page.locator('#w2InstallIntro')).toContainText(W('install.introNetwork', { name: KITCHEN.friendlyName }));
    // Before the install the Bose original has no agent.
    await mock.resetCalls();
    await page.locator('#setupHeroInstall').click();
    await mock.expectCalled('InstallSTROnBox', { pred: onHost(KITCHEN.host), times: 1 });
    await expect(title(page)).toHaveText(W('title.done', { name: KITCHEN.friendlyName }));
    await expect(page.locator('#w2ConfirmBody')).toContainText(W('confirm.doneBody', { name: KITCHEN.friendlyName, version: 'v1.0.19' }));
    for (const s of ['find', 'check', 'install', 'confirm']) await stepState(page, s, 'done');
    // Confirm asked the agent itself, on the agent ports.
    const ask = await mock.last('BoxAgentVersion', onHost(KITCHEN.host));
    expect(ask.args).toEqual([KITCHEN.host, 0]);
    // The classic result (how to play, power-cycle advice) moved along.
    await expect(page.locator('#w2ConfirmDetails #setupResult #installGoMusic')).toBeVisible();
    await expect(foot(page, 'back')).toHaveCount(0);
    await page.getByRole('button', { name: W('confirm.openPageLabel', { name: KITCHEN.friendlyName }) }).click();
    await expect(page.locator('#ui2MainInner > #view-settings')).toBeVisible();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(KITCHEN.friendlyName);
  });

  test('4. set up another speaker starts over at Find', async ({ app }) => {
    const { page } = app;
    await toInstall(page);
    await page.locator('#setupHeroInstall').click();
    await expect(title(page)).toHaveText(W('title.done', { name: KITCHEN.friendlyName }));
    await page.getByRole('button', { name: W('confirm.another') }).click();
    await expect(title(page)).toHaveText(W('title.find'));
    await stepState(page, 'find', 'current');
    await expect(page.locator('#w2PanelInstall #setupResult')).toBeAttached();
    await expect(page.locator('#setupResult')).toBeEmpty();
    // Kitchen runs STR now: listed as such, no longer offered.
    await expect(radio(page, KITCHEN.host)).toHaveCount(0);
    await expect(page.locator(`#w2OnStr .w2-row--str[data-host="${KITCHEN.host}"]`)).toBeAttached();
  });

  test.describe('install refused: an update still finishing', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: { InstallSTROnBox: { value: { step: 'guard', code: 'update-finishing', ok: false, message: 'update finishing', log: '', firmware: '27.0.6', model: 'SoundTouch 10', moduleType: '', variant: '' } } },
      },
    });

    test('5. is said calmly in the step, and Confirm waits until the agent answers', async ({ app, mock }) => {
      const { page } = app;
      await toInstall(page);
      await page.locator('#setupHeroInstall').click();
      const note = page.locator('#w2InstallNote .w2-note--calm');
      await expect(note).toContainText(W('install.finishingHead', { name: KITCHEN.friendlyName }));
      await expect(note).toHaveAttribute('role', 'status');
      await expect(page.locator('#w2InstallNote [role="alert"]')).toHaveCount(0);
      await stepState(page, 'install', 'current');
      // The classic words are still there, in the step.
      await expect(page.locator('#w2PanelInstall #setupResult')).toContainText(en('setup.installUpdateFinishing'));
      await page.getByRole('button', { name: W('install.waitConfirmLabel', { name: KITCHEN.friendlyName }) }).click();
      await expect(title(page)).toHaveText(W('title.confirm', { name: KITCHEN.friendlyName }));
      await expect(page.locator('#w2ConfirmBody')).toContainText(W('confirm.waitingHead', { name: KITCHEN.friendlyName }));
      // Asked, not answered: not done.
      await expect.poll(() => mock.count('BoxAgentVersion', onHost(KITCHEN.host)), { timeout: 10_000 }).toBeGreaterThanOrEqual(2);
      await stepState(page, 'confirm', 'current');
      await expect(page.locator('#w2ConfirmBody .w2-done')).toHaveCount(0);
      // The update finished: the agent answers.
      await mock.set('BoxAgentVersion', { value: { version: 'v1.0.19', build: '2026-10-23', goLibrespot: 'present' } });
      await expect(title(page)).toHaveText(W('title.done', { name: KITCHEN.friendlyName }), { timeout: 10_000 });
      await stepState(page, 'confirm', 'done');
      expect(await mock.count('InstallSTROnBox')).toBe(1);
    });
  });

  test.describe('install refused: STR already running', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: {
          InstallSTROnBox: { value: { step: 'guard', code: 'str-running', ok: false, message: 'str running', log: '', firmware: '27.0.6', model: 'SoundTouch 10', moduleType: '', variant: '' } },
          BoxAgentVersion: { byHost: { '192.0.2.51': { value: { version: 'v1.0.19', build: '2026-10-23', goLibrespot: 'present' } } } },
        },
      },
    });

    test('6. is not a failure: the step says so and Confirm finds STR', async ({ app }) => {
      const { page } = app;
      await toInstall(page);
      await page.locator('#setupHeroInstall').click();
      await expect(page.locator('#w2InstallNote .w2-note--calm')).toContainText(W('install.strRunningHead', { name: KITCHEN.friendlyName }));
      await stepState(page, 'install', 'done');
      // The classic update button stays in reach.
      await expect(page.locator('#w2PanelInstall #setupUpdateAgentBtn')).toBeVisible();
      await page.getByRole('button', { name: W('install.toConfirmLabel', { name: KITCHEN.friendlyName }) }).click();
      await expect(title(page)).toHaveText(W('title.done', { name: KITCHEN.friendlyName }));
    });
  });

  test.describe('install failed', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: { InstallSTROnBox: { value: { step: 'access', code: 'install-window-closed', ok: false, message: 'the install window closed', log: '', firmware: '27.0.6', model: 'SoundTouch 10', moduleType: '', variant: '' } } },
      },
    });

    test('7. marks the step failed where it happened, opens the stick fallback, and Back works', async ({ app }) => {
      const { page } = app;
      await toInstall(page);
      await page.locator('#setupHeroInstall').click();
      const note = page.locator('#w2InstallNote .w2-note--failed');
      await expect(note).toContainText(W('install.failedHead', { name: KITCHEN.friendlyName }));
      await expect(note).toHaveAttribute('role', 'alert');
      await stepState(page, 'install', 'failed');
      await expect(page.locator('#w2PanelInstall #setupResult .setup-err')).toBeVisible();
      // The classic flow opens the USB stick for this code: the fallback, in the step.
      await expect(page.locator('#w2PanelInstall #setupStickDetails')).toHaveAttribute('open', '');
      await expect(foot(page, 'back')).not.toHaveAttribute('aria-disabled', 'true');
      await foot(page, 'back').click();
      await expect(title(page)).toHaveText(W('title.check', { name: KITCHEN.friendlyName }));
    });
  });

  test.describe('install running', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: { InstallSTROnBox: { never: true } },
      },
    });

    test('8. holds Back, warns before leaving, and the install keeps running', async ({ app, mock }) => {
      const { page } = app;
      await toInstall(page);
      await page.locator('#setupHeroInstall').click();
      await mock.expectCalled('InstallSTROnBox', { times: 1 });
      await expect(page.locator('#w2InstallNote')).toContainText(W('install.runningBody'));
      await expect(foot(page, 'back')).toHaveAttribute('aria-disabled', 'true');
      await expect(page.locator('#w2Foot')).toContainText(W('backBusy', { name: KITCHEN.friendlyName }));
      // Leaving asks first, naming the speaker.
      const modal = page.locator('#warnModal');
      await nav(page, 'box').click();
      await expect(modal).toBeVisible();
      await expect(modal.locator('#warnBody')).toContainText(W('leave.body', { name: KITCHEN.friendlyName }));
      await modal.locator('#warnCancel').click();
      await expect(modal).toBeHidden();
      await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
      // Leave anyway, then come back: the wizard is where it was.
      await nav(page, 'box').click();
      await expect(modal).toBeVisible();
      await modal.locator('#warnConfirm').click();
      await expect(page.locator('#ui2MainInner > #view-box')).toBeVisible();
      await nav(page, 'speakers').click();
      await sub(page, 'add').click();
      await expect(title(page)).toHaveText(W('title.install', { name: KITCHEN.friendlyName }));
      await expect(page.locator('#w2PanelInstall #setupResult')).toContainText(en('setup.netInstallOn', { name: KITCHEN.friendlyName }));
      expect(await mock.count('InstallSTROnBox')).toBe(1);
    });
  });

  test('9. Back from Check returns to Find with the choice kept', async ({ app }) => {
    const { page } = app;
    await openWizard(page);
    await chooseKitchen(page);
    await foot(page, 'next').click();
    await expect(step(page, 'check')).toHaveAttribute('aria-current', 'step');
    await foot(page, 'back').click();
    await expect(title(page)).toHaveText(W('title.find'));
    await expect(radio(page, KITCHEN.host)).toBeChecked();
    await stepState(page, 'check', 'waiting');
  });

  test.describe('the check in the wizard: 15.x, the one-click update', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: {
          GetBoxFirmware: { sequence: [{ value: SETUP_CHECK.routeA }, { value: SETUP_CHECK.current }] },
          GetFirmwareGuide: { value: SETUP_CHECK.guideOne },
          FlashBoseFirmware: { value: { before: '15.0.4', after: '27.0.6', via: 'page' }, delayMs: 1500 },
        },
      },
    });

    test('12. Install stays locked until Kitchen reports 27.0.6, Back is held while it updates', async ({ app, mock }) => {
      const { page } = app;
      const name = KITCHEN.friendlyName;
      await openWizard(page);
      await chooseKitchen(page);
      await foot(page, 'next').click();
      const check = page.locator('#w2CheckBody .u-check');
      await expect(check).toHaveAttribute('data-case', 'routeA');
      await expect(foot(page, 'install')).toHaveAttribute('aria-disabled', 'true');
      await expect(page.locator('#w2InstallHint')).toHaveText(W('check.lockedUpdate', { name }));
      await expect(foot(page, 'stick')).toBeVisible();
      await check.locator('[data-check="flash"]').click();
      await page.locator('#warnConfirm').click();
      await mock.expectCalled('FlashBoseFirmware', { pred: (c) => c.args[0] === KITCHEN.host, times: 1 });
      // Updating: Back is held, no stick, leaving warns.
      await expect(foot(page, 'back')).toHaveAttribute('aria-disabled', 'true');
      await expect(foot(page, 'stick')).toHaveCount(0);
      await expect(foot(page, 'install')).toHaveCount(0);
      // Read back: done, Install unlocks.
      await expect(check.locator('.u-check-callout')).toContainText(en('ui2.setup.check.updateDone', { name, version: '27.0.6' }));
      await stepState(page, 'check', 'done');
      await expect(foot(page, 'install')).not.toHaveAttribute('aria-disabled', 'true');
      await expect(page.locator('#w2InstallHint')).toHaveCount(0);
      await foot(page, 'install').click();
      await expect(title(page)).toHaveText(W('title.install', { name }));
      await stepState(page, 'check', 'done');
    });
  });

  test.describe('the check in the wizard: 7.x, a first generation SoundTouch 20', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: {
          GetBoxFirmware: { value: SETUP_CHECK.ladder },
          GetFirmwareGuide: { value: SETUP_CHECK.guideOne },
          GetFirmwareLadder: { value: LADDER_PLAN },
        },
      },
    });

    test('13. shows the stick ladder in the step; the USB stick is the way on without the update', async ({ app }) => {
      const { page } = app;
      const name = KITCHEN.friendlyName;
      await openWizard(page);
      await chooseKitchen(page);
      await foot(page, 'next').click();
      const check = page.locator('#w2CheckBody .u-check');
      await expect(check).toHaveAttribute('data-case', 'ladder');
      await expect(check.locator('.u-check-guide .fw-ladder')).toBeVisible();
      await expect(check.locator('[data-check="skip"]')).toBeHidden();
      await expect(foot(page, 'install')).toHaveAttribute('aria-disabled', 'true');
      await expect(page.locator('#w2InstallHint')).toHaveText(W('check.lockedUpdate', { name }));
      await foot(page, 'stick').click();
      await expect(title(page)).toHaveText(W('title.install', { name }));
      await stepState(page, 'check', 'skipped');
      await expect(page.locator('#w2PanelInstall #setupStickDetails')).toHaveAttribute('open', '');
    });
  });

  test.describe('the check in the wizard: 10.x, the guide\'s own Update now', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: {
          GetBoxFirmware: { sequence: [{ value: { ...SETUP_CHECK.guide, model: KITCHEN.model } }, { value: SETUP_CHECK.current }] },
          GetFirmwareGuide: { value: { ...SETUP_CHECK.guideOne, model: KITCHEN.model, current: '10.0.11' } },
          FlashBoseFirmware: { never: true },
        },
      },
    });

    test('14. an update started from the guide holds Back, the stick and leaving, like the step\'s own', async ({ app, mock }) => {
      const { page } = app;
      const name = KITCHEN.friendlyName;
      await openWizard(page);
      await chooseKitchen(page);
      await foot(page, 'next').click();
      const check = page.locator('#w2CheckBody .u-check');
      await expect(check).toHaveAttribute('data-case', 'guide');
      await expect(foot(page, 'back')).not.toHaveAttribute('aria-disabled', 'true');
      await check.locator('.u-check-fw [data-fwflash]').click();
      await page.locator('#warnConfirm').click();
      await mock.expectCalled('FlashBoseFirmware', { pred: (c) => c.args[0] === KITCHEN.host, times: 1 });
      await expect(check).toHaveAttribute('data-phase', 'updating');
      await expect(foot(page, 'back')).toHaveAttribute('aria-disabled', 'true');
      await expect(foot(page, 'stick')).toHaveCount(0);
      // Its progress lines count too.
      await mock.emit('firmware:progress', { host: KITCHEN.host, phase: 'upload', pct: 40, done: 40, total: 100 });
      await expect(check).toHaveAttribute('data-phase', 'updating');
      // Leaving asks first.
      const modal = page.locator('#warnModal');
      await nav(page, 'box').click();
      await expect(modal).toBeVisible();
      await expect(modal.locator('#warnBody')).toContainText(W('leave.bodyCheck', { name }));
      await modal.locator('#warnCancel').click();
      await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
      // The backend says done: the step reads the speaker back and passes.
      await mock.emit('firmware:progress', { host: KITCHEN.host, phase: 'done', pct: -1, done: 0, total: 0 });
      await expect(check.locator('.u-check-callout')).toContainText(en('ui2.setup.check.updateDone', { name, version: '27.0.6' }));
      await stepState(page, 'check', 'done');
      await expect(foot(page, 'back')).not.toHaveAttribute('aria-disabled', 'true');
    });
  });

  test.describe('the check in the wizard: 15.x, an update across a reload', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: {
          GetBoxFirmware: { sequence: [{ value: SETUP_CHECK.routeA }, { value: SETUP_CHECK.current }] },
          GetFirmwareGuide: { value: SETUP_CHECK.guideOne },
          FlashBoseFirmware: { never: true },
        },
      },
    });

    test('15. the layout switch asks first, and after a reload the same update is not offered again', async ({ app, mock }) => {
      const { page } = app;
      const name = KITCHEN.friendlyName;
      await openWizard(page);
      await chooseKitchen(page);
      await foot(page, 'next').click();
      const check = page.locator('#w2CheckBody .u-check');
      await expect(check).toHaveAttribute('data-case', 'routeA');
      await check.locator('[data-check="flash"]').click();
      await page.locator('#warnConfirm').click();
      await mock.expectCalled('FlashBoseFirmware', { times: 1 });
      await expect(foot(page, 'back')).toHaveAttribute('aria-disabled', 'true');
      // The screen reader hears the phase, not the whole block: the callout
      // is no live region, one short line is.
      await expect(check.locator('.u-check-callout')).not.toHaveAttribute('role', /.+/);
      await expect(check.locator('.u-check-live[role="status"]')).toHaveText(en('fwGuide.autoStarting'));
      // Aa menu > Layout: classic asks before the reload.
      await page.locator('#a11yTrigger').click();
      await page.locator('#a11yMenu button[data-ui2="0"]').click();
      const modal = page.locator('#warnModal');
      await expect(modal).toBeVisible();
      await expect(modal.locator('#warnBody')).toContainText(W('leave.bodyCheck', { name }));
      await modal.locator('#warnCancel').click();
      await expect(page.locator('#ui2SetupWizard')).toBeVisible();
      await expect(page.locator('html')).toHaveClass(/\bui2\b/);
      // A reload anyway (the Go call goes on): the check holds the wizard.
      await page.reload();
      await expect(page.locator('#ui2Sidebar')).toBeVisible();
      await openWizard(page);
      await chooseKitchen(page);
      await foot(page, 'next').click();
      const again = page.locator('#w2CheckBody .u-check');
      await expect(again).toHaveAttribute('data-case', 'routeA');
      await expect(again).toHaveAttribute('data-phase', 'updating');
      await expect(again.locator('[data-check="flash"]')).toHaveAttribute('aria-disabled', 'true');
      await expect(foot(page, 'back')).toHaveAttribute('aria-disabled', 'true');
      await expect(foot(page, 'stick')).toHaveCount(0);
      // Its end arrives on the new page: read back, done.
      await mock.emit('firmware:progress', { host: KITCHEN.host, phase: 'done', pct: -1, done: 0, total: 0 });
      await expect(again.locator('.u-check-callout')).toContainText(en('ui2.setup.check.updateDone', { name, version: '27.0.6' }));
      await stepState(page, 'check', 'done');
      expect(await mock.count('FlashBoseFirmware')).toBe(0);
    });
  });

  test.describe('the check in the wizard: a slow read', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: {
          GetBoxFirmware: { sequence: [{ value: SETUP_CHECK.routeA, delayMs: 2000 }, { value: SETUP_CHECK.current }] },
          GetFirmwareGuide: { value: SETUP_CHECK.guideOne },
          FlashBoseFirmware: { value: { before: '15.0.4', after: '27.0.6', via: 'page' }, delayMs: 1500 },
        },
      },
    });

    test('16. a footer button keeps the keyboard when the footer changes, and a locked one says why', async ({ app }) => {
      const { page } = app;
      const name = KITCHEN.friendlyName;
      await openWizard(page);
      await chooseKitchen(page);
      await expect(foot(page, 'next')).not.toHaveAttribute('aria-describedby', /.+/);
      await foot(page, 'next').click();
      const check = page.locator('#w2CheckBody .u-check');
      await expect(check).toHaveAttribute('data-case', 'loading');
      await expect(page.locator('#w2InstallHint')).toHaveText(W('check.lockedCheck', { name }));
      await foot(page, 'stick').focus();
      // The read ends, the hint changes and the footer is drawn anew.
      await expect(check).toHaveAttribute('data-case', 'routeA');
      await expect(page.locator('#w2InstallHint')).toHaveText(W('check.lockedUpdate', { name }));
      await expect(foot(page, 'stick')).toBeFocused();
      await expect(foot(page, 'install')).toHaveAttribute('aria-describedby', 'w2InstallHint');
      // Waiting on the locked Install: it unlocks under the keyboard.
      await foot(page, 'install').focus();
      await check.locator('[data-check="flash"]').click();
      await page.locator('#warnConfirm').click();
      await foot(page, 'back').focus();
      await expect(foot(page, 'back')).toHaveAttribute('aria-disabled', 'true');
      await expect(foot(page, 'back')).toBeFocused();
      await stepState(page, 'check', 'done');
      await expect(foot(page, 'back')).not.toHaveAttribute('aria-disabled', 'true');
      await expect(foot(page, 'back')).toBeFocused();
      await expect(foot(page, 'install')).not.toHaveAttribute('aria-describedby', /.+/);
    });
  });

  test.describe('Confirm without an answer', () => {
    test.use({
      fakeClock: true,
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'kitchen'],
        installFlow: true,
        bindings: { InstallSTROnBox: { value: { step: 'guard', code: 'update-finishing', ok: false, message: 'update finishing', log: '', firmware: '27.0.6', model: 'SoundTouch 10', moduleType: '', variant: '' } } },
      },
    });

    test('17. stops asking while the user is elsewhere, and after the window offers a way out', async ({ app, mock }) => {
      const { page } = app;
      const name = KITCHEN.friendlyName;
      await toInstall(page);
      await page.locator('#setupHeroInstall').click();
      await page.getByRole('button', { name: W('install.waitConfirmLabel', { name }) }).click();
      await expect(title(page)).toHaveText(W('title.confirm', { name }));
      await expect(page.locator('#w2ConfirmLive[role="status"]')).toHaveText(W('confirm.waitingHead', { name }));
      await expect(page.locator('#w2ConfirmBody [aria-busy]')).toHaveCount(0);
      await expect.poll(() => mock.count('BoxAgentVersion', onHost(KITCHEN.host))).toBeGreaterThanOrEqual(1);
      // Elsewhere: no more asking.
      await nav(page, 'box').click();
      await expect(page.locator('#ui2MainInner > #view-box')).toBeVisible();
      await page.clock.runFor(4_000);
      const asked = await mock.count('BoxAgentVersion', onHost(KITCHEN.host));
      for (let i = 0; i < 4; i++) await page.clock.runFor(4_000);
      expect(await mock.count('BoxAgentVersion', onHost(KITCHEN.host))).toBe(asked);
      // Back on Add speaker: it asks again.
      await nav(page, 'speakers').click();
      await sub(page, 'add').click();
      await expect(title(page)).toHaveText(W('title.confirm', { name }));
      await expect.poll(() => mock.count('BoxAgentVersion', onHost(KITCHEN.host))).toBeGreaterThan(asked);
      // The window runs out: failed, with the way back to the install and out.
      for (let i = 0; i < 12; i++) await page.clock.runFor(30_000);
      await expect(page.locator('#w2ConfirmBody [role="alert"]')).toContainText(W('confirm.failedHead', { name }));
      await stepState(page, 'confirm', 'failed');
      await expect(page.getByRole('button', { name: W('confirm.recheckLabel', { name }) })).toBeVisible();
      await expect(page.getByRole('button', { name: W('confirm.another') })).toBeVisible();
      await page.getByRole('button', { name: W('confirm.backInstallLabel', { name }) }).click();
      await expect(title(page)).toHaveText(W('title.install', { name }));
      await stepState(page, 'confirm', 'waiting');
      await expect(page.locator('#w2PanelInstall #setupStickDetails')).toHaveAttribute('open', '');
      await expect(page.locator('#w2PanelInstall #setupPrimaryAction .setup-hero')).toHaveAttribute('data-host', KITCHEN.host);
    });
  });

  test('10. a Bose original from the speaker list opens Find with it chosen', async ({ app }) => {
    const { page } = app;
    await nav(page, 'speakers').click();
    await page.locator(`#view-speakers .spl-open[data-host="${KITCHEN.host}"]`).click();
    await expect(page.locator('#ui2MainInner > #view-setup #ui2SetupWizard')).toBeVisible();
    await expect(radio(page, KITCHEN.host)).toBeChecked();
    await expect(foot(page, 'next')).toHaveText(W('next', { name: KITCHEN.friendlyName }));
  });
});

test.describe('classic layout', () => {
  test.use({ ui2: false });

  test('11. Set up speaker is the classic view, without the wizard', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="setup"]').click();
    await expect(page.locator('#view-setup .setup-target-card').first()).toBeVisible();
    await expect(page.locator('#view-setup > h2')).toBeVisible();
    await expect(page.locator('#ui2SetupWizard')).toHaveCount(0);
    await expect(page.locator('#view-setup')).not.toHaveClass(/\bw2-on\b/);
  });
});
