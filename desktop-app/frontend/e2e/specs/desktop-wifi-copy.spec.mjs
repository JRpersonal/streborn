// STR keeps its own copy of a speaker's Wi-Fi by itself (wificopy.js). A
// speaker without a copy (wlanCredsMissing) used to get a banner with a "Save
// Wi-Fi" button that read like a required action and restarted the speaker.
// Now the app stores the copy store-only on its own, and only when it cannot
// does that speaker's Network settings carry a quiet note with a button.
//
//   E2E_PORT=4191 npx playwright test specs/desktop-wifi-copy.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { settle } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const KITCHEN = ROSTER.kitchen;

const nav = (page, view) => page.locator(`#ui2Tabs .ui2-nav-item[data-view="${view}"]`);
const listCard = (page, host) => page.locator(`#view-speakers li.spl-item[data-host="${host}"]`);
const triggered = (host, trigger) => (c) => c.args[0] === host && c.args[2] === trigger;

async function openNetworkTab(page, host, name) {
  await nav(page, 'speakers').click();
  await page.locator(`#view-speakers .spl-open[data-host="${host}"]`).click();
  await expect(page.locator('#ui2SpkPageName')).toHaveText(name);
  await page.locator('#ui2SpkTab-network').click();
  await expect(page.locator('#ui2SpkTab-network')).toHaveAttribute('aria-selected', 'true');
}

test.describe('classic layout, a speaker without a copy', () => {
  test.use({ fakeClock: true, scenario: { boxPatches: { livingRoom: { wlanCredsMissing: true } } } });

  test('shows no banner and keeps the copy by itself', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await mock.expectCalled('KeepSpeakerWiFiCopy', { pred: triggered(LR.host, 'seen') });
    await expect(page.locator('#boxIssueBanner')).toBeHidden();
    await expect(page.locator('#boxIssueWifiBtn')).toHaveCount(0);
  });
});

test.describe('UI 2.0, the copy was kept', () => {
  test.use({ ui2: true, fakeClock: true, scenario: { boxPatches: { livingRoom: { wlanCredsMissing: true } } } });

  test('no attention item, no note, and asked once per refresh window', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await mock.expectCalled('KeepSpeakerWiFiCopy', { pred: triggered(LR.host, 'seen'), times: 1 });
    await nav(page, 'speakers').click();
    await expect(listCard(page, LR.host).locator('.spl-attn')).toHaveCount(0);
    await expect(page.locator(`#ui2SpkLists .ui2-spk-row [data-chip="attention"]`)).toHaveCount(0);
    await openNetworkTab(page, LR.host, LR.friendlyName);
    await expect(page.locator('#wlanCopyNote')).toBeHidden();
    // More refreshes do not ask again.
    await settle(page, 20_000);
    expect(await mock.count('KeepSpeakerWiFiCopy', onHost(LR.host))).toBe(1);
  });
});

test.describe('UI 2.0, the copy cannot be kept automatically', () => {
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: {
      boxPatches: { livingRoom: { wlanCredsMissing: true } },
      bindings: {
        KeepSpeakerWiFiCopy: { value: { stored: false, reason: 'no-password', note: true } },
        TryWiFiPassword: { value: '' },
      },
    },
  });

  test('the Network tab carries a quiet note; its form saves the copy store-only', async ({ app, mock }) => {
    const { page, api } = app;
    await settle(page, 9_000);
    await mock.expectCalled('KeepSpeakerWiFiCopy', { pred: triggered(LR.host, 'seen') });
    // Not a banner and not an attention item.
    await nav(page, 'speakers').click();
    await expect(listCard(page, LR.host).locator('.spl-attn')).toHaveCount(0);
    await openNetworkTab(page, LR.host, LR.friendlyName);
    const note = page.locator('#wlanCopyNote');
    await expect(note).toBeVisible();
    await expect(note).toContainText(en('settingsView.wlanCopyNote'));
    // The button tries once more, then opens the form on the copy job.
    await note.getByRole('button', { name: en('settingsView.wlanCopySaveBtn') }).click();
    await mock.expectCalled('KeepSpeakerWiFiCopy', { pred: triggered(LR.host, 'button') });
    await expect(page.locator('#wlanSwitchForm')).toBeVisible();
    await expect(page.locator('#wlanStoreHint')).toBeVisible();
    await expect(page.locator('#boxWlanSSID')).toHaveValue('HomeWiFi');
    const save = page.locator('#boxWlanSave');
    await expect(save).toHaveText(en('settingsView.wlanCopySaveBtn'));
    await page.locator('#boxWlanPass').fill('correct-horse-battery');
    await save.click();
    await mock.expectCalled('StoreSpeakerWiFiCopy', { pred: onHost(LR.host) });
    const call = await mock.last('StoreSpeakerWiFiCopy', onHost(LR.host));
    expect(call.args.slice(2)).toEqual(['HomeWiFi', 'correct-horse-battery']);
    // Store-only: no switch request, no confirmation, and the note is gone.
    expect(api.calls.filter((c) => c.method === 'PUT' && new URL(c.url).pathname === '/api/box/wlan')).toHaveLength(0);
    await expect(page.locator('#warnModal')).toBeHidden();
    await expect(note).toBeHidden();
    await expect(page.locator('#wlanSwitchForm')).toBeHidden();
  });
});

test.describe('install', () => {
  test.use({
    ui2: true,
    scenario: { boxes: ['livingRoom', 'bedroom', 'kitchen'], installFlow: true },
  });

  test('keeps a copy of the speaker\'s Wi-Fi at the end of the install', async ({ app, mock }) => {
    const { page } = app;
    const W = (key, vars) => en(`ui2.setup.wizard.${key}`, vars);
    const foot = (act) => page.locator(`#w2Foot .w2-act[data-act="${act}"]`);
    await nav(page, 'speakers').click();
    await page.locator('#ui2SpkSubnav .spa-sub[data-route="add"]').click();
    await page.locator(`#w2FindList input.w2-radio[value="${KITCHEN.host}"]`).check();
    await foot('next').click();
    await expect(foot('install')).not.toHaveAttribute('aria-disabled', 'true');
    await foot('install').click();
    await page.locator('#setupHeroInstall').click();
    await mock.expectCalled('InstallSTROnBox', { pred: onHost(KITCHEN.host), times: 1 });
    await expect(page.locator('#w2Title')).toHaveText(W('title.done', { name: KITCHEN.friendlyName }));
    await mock.expectCalled('KeepSpeakerWiFiCopy', { pred: triggered(KITCHEN.host, 'install'), times: 1 });
  });
});
