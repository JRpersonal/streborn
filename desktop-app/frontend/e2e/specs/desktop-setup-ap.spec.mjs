// desktop-setup-ap: the "Set up Wi-Fi directly" panel under Factory reset, with
// a speaker found in setup mode. An empty password field asks before sending,
// because the speaker cannot join a protected network without one and then
// never shows up; Cancel sends nothing and goes back to the field, "Send
// without password" sends as before (an open network). A filled field sends
// without asking.

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { WIFI } from '../../testkit/demo-data.mjs';

const AP_BOX = { host: '192.0.2.1', port: 8090, model: 'SoundTouch Portable', name: 'Bose SoundTouch 1A2B' };
const SETUP_AP = {
  bindings: {
    ProbeSetupAP: { value: { found: true, box: AP_BOX } },
    PushWLANToBox: { value: { ok: true, step: 'done', message: '', logTail: [] } },
  },
};
const SSID = WIFI[0].ssid;

test.use({ ui2: false, scenario: SETUP_AP });

async function openPanel(page) {
  await page.locator('.tab-btn[data-view="setup"]').click();
  await page.locator('.setup-target-card[data-kind="factory-reset"]').click();
  await expect(page.locator('#apPushGo')).toBeVisible();
  await expect(page.locator('#apPushSSID')).toHaveValue(SSID);
}

test('1. an empty password asks first; Cancel sends nothing and goes back to the field', async ({ app, mock }) => {
  const { page } = app;
  await openPanel(page);
  await mock.resetCalls();
  await page.locator('#apPushGo').click();
  await expect(page.locator('#warnModal')).toBeVisible();
  await expect(page.locator('#warnModal .warn-title')).toContainText(en('setupAPPush.emptyPassTitle'));
  await expect(page.locator('#warnBody')).toHaveText(en('setupAPPush.emptyPassBody', { ssid: SSID }));
  await page.locator('#warnCancel').click();
  await expect(page.locator('#warnModal')).toBeHidden();
  await expect(page.locator('#apPushPass')).toBeFocused();
  await mock.expectCalled('PushWLANToBox', { times: 0 });
});

test('2. "Send without password" sends with an empty password', async ({ app, mock }) => {
  const { page } = app;
  await openPanel(page);
  await mock.resetCalls();
  await page.locator('#apPushGo').click();
  await expect(page.locator('#warnConfirm')).toHaveText(en('setupAPPush.emptyPassSend'));
  await page.locator('#warnConfirm').click();
  await mock.expectCalled('PushWLANToBox', { pred: (c) => c.args[0] === AP_BOX.host && c.args[1] === SSID && c.args[2] === '', times: 1 });
  await expect(page.locator('#apPushOutput')).toContainText(en('setupAPPush.successTitle'));
});

test('3. a filled password sends without asking', async ({ app, mock }) => {
  const { page } = app;
  await openPanel(page);
  await mock.resetCalls();
  await page.locator('#apPushPass').fill('correct horse');
  await page.locator('#apPushGo').click();
  await mock.expectCalled('PushWLANToBox', { pred: (c) => c.args[2] === 'correct horse', times: 1 });
  await expect(page.locator('#warnModal')).toBeHidden();
});
