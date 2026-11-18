// desktop-update-dark-risk: a speaker whose old STR (before 1.3.0) ends the
// update with a restart of the whole speaker, on a speaker type that often
// does not come back from it (the first-generation SoundTouch 20 and 30 and
// the other coprocessor Wi-Fi speakers), is named in the update confirmation
// with what to do should it stay dark: unplug it for 5 minutes, wait 3
// minutes. The Go side decides (BoxRestartPlan): "warn" only where the app
// cannot route around that restart. The owner is told, never stopped.
//
// Covered: the classic speaker update card, the UI 2.0 Speakers list, and the
// Update all confirmation; plus the plans that must not warn.
//
//   E2E_PORT=4191 npx playwright test specs/desktop-update-dark-risk.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';

const HALL = ROSTER.oldAgent;
const WARN = { mode: 'warn', chassis: 'unknown', agentVersion: 'v1.0.10', reason: 'agent v1.0.10 reboots after an update and the chassis could not be read' };
const ROUTED = { mode: 'agent-restart', chassis: 'bco', agentVersion: 'v1.0.10', reason: 'updating over SSH and restarting only the agent' };
const NO_STICK = { '/api/stick/status': { status: 200, body: { mounted: false } } };

function classic(plan) {
  return {
    ui2: false,
    scenario: {
      boxes: ['livingRoom', 'bedroom', 'office'],
      current: 'oldAgent',
      banners: { speakerUpdate: true },
      routes: NO_STICK,
      bindings: { RunSpeakerUpdate: { never: true }, BoxRestartPlan: { value: plan } },
    },
  };
}

const modal = (page) => page.locator('#warnModal');

test.describe('classic layout, the app cannot route around the restart', () => {
  test.use(classic(WARN));

  test('the confirmation names the speaker, 5 minutes and 3 minutes, and the update goes on', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#speakerUpdateCard #sucUpdate').click();
    await expect(modal(page)).toBeVisible();
    await expect(modal(page).locator('.warn-title')).toContainText(en('update.darkRiskTitle'));
    await expect(page.locator('#warnBody')).toHaveText(en('update.darkRiskBody', { name: HALL.friendlyName }));
    await expect(page.locator('#warnBody')).toContainText('5 minutes');
    await expect(page.locator('#warnBody')).toContainText('3 minutes');
    await mock.expectCalled('BoxRestartPlan', { pred: onHost(HALL.host) });
    // Nothing has started yet.
    expect(await mock.count('RunSpeakerUpdate')).toBe(0);
    if (process.env.SHOOT_DIR) await page.locator('#warnModal .modal-content').screenshot({ path: `${process.env.SHOOT_DIR}/desktop-update-dark-risk.png` });
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('RunSpeakerUpdate', { times: 1, pred: onHost(HALL.host) });
  });

  test('cancelling it leaves the speaker untouched', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#speakerUpdateCard #sucUpdate').click();
    await expect(page.locator('#warnBody')).toHaveText(en('update.darkRiskBody', { name: HALL.friendlyName }));
    await page.locator('#warnCancel').click();
    await expect(modal(page)).toBeHidden();
    // The cancelled run returned before anything moved: the update button in
    // Speaker Settings is back to its normal label, and nothing was called.
    await expect(page.locator('#stickInfoUpdateBtn')).toBeEnabled();
    expect(await mock.count('RunSpeakerUpdate')).toBe(0);
  });
});

test.describe('classic layout, the app routes around the restart', () => {
  test.use(classic(ROUTED));

  test('no warning, the update starts', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#speakerUpdateCard #sucUpdate').click();
    await mock.expectCalled('RunSpeakerUpdate', { times: 1, pred: onHost(HALL.host) });
    await expect(modal(page)).toBeHidden();
  });
});

test.describe('classic layout, the plan cannot be read', () => {
  test.use({
    ui2: false,
    scenario: {
      boxes: ['livingRoom', 'bedroom', 'office'],
      current: 'oldAgent',
      banners: { speakerUpdate: true },
      routes: NO_STICK,
      bindings: { RunSpeakerUpdate: { never: true }, BoxRestartPlan: { error: 'speaker did not answer' } },
    },
  });

  test('no warning, the update is not blocked', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#speakerUpdateCard #sucUpdate').click();
    await mock.expectCalled('RunSpeakerUpdate', { times: 1, pred: onHost(HALL.host) });
    await expect(modal(page)).toBeHidden();
  });
});

test.describe('UI 2.0 Speakers list', () => {
  test.use({
    ui2: true,
    scenario: mergeScenario(speakersScenario(), {
      routes: NO_STICK,
      bindings: { RunSpeakerUpdate: { never: true }, BoxRestartPlan: { value: WARN } },
    }),
  });

  test('Update on the speaker asks first, then runs', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#ui2Tabs .ui2-nav-item[data-view="speakers"]').click();
    await page.locator(`#view-speakers .spl-act[data-act="update"][data-host="${HALL.host}"]`).click();
    await expect(page.locator('#warnBody')).toHaveText(en('update.darkRiskBody', { name: HALL.friendlyName }));
    expect(await mock.count('RunSpeakerUpdate')).toBe(0);
    await page.locator('#warnConfirm').click();
    await mock.expectCalled('RunSpeakerUpdate', { times: 1, pred: onHost(HALL.host) });
  });
});

test.describe('Update all', () => {
  const OLD = { version: 'v1.0.10', build: '2026-09-30' };
  test.use({
    ui2: false,
    scenario: {
      boxes: ['livingRoom', 'bedroom', 'office', 'kitchen', 'oldAgent'],
      boxPatches: { bedroom: OLD },
      routes: NO_STICK,
      bindings: {
        RunFleetUpdate: { never: true },
        // Only the Hallway speaker is at risk; Bedroom is routed around.
        BoxRestartPlan: { value: ROUTED, byHost: { [HALL.host]: { value: WARN } } },
      },
    },
  });

  test('the one confirmation carries a note for the speaker at risk only', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="settings"]').click();
    await expect(page.locator('#settingsUpdateAllBtn')).toBeVisible();
    await page.locator('#settingsUpdateAllBtn').click();
    const notes = page.locator('.warn-notes');
    await expect(notes).toBeVisible();
    await expect(notes).toContainText(en('updateAll.noteDarkRisk', { name: HALL.friendlyName }));
    await expect(notes).not.toContainText(en('updateAll.noteDarkRisk', { name: ROSTER.bedroom.friendlyName }));
    await mock.expectCalled('BoxRestartPlan', { pred: onHost(ROSTER.bedroom.host) });
    expect(await mock.count('RunFleetUpdate')).toBe(0);
  });
});
