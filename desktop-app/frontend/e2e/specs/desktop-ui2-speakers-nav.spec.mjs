// The UI 2.0 Speakers area, phase 3 skeleton (#1287, docs/UI-2.0.md): one
// sidebar entry for Speaker settings, Multi-room and Set up speaker; its
// places (the list, one speaker with five tabs, Groups, Add speaker); how a
// sidebar row relates to one speaker's page; and the classic layout, which
// must not change at all.
//
// The network is the phase 3 fixture (testkit/demo-data.mjs SPEAKERS3, ten
// speakers, one in every state), so the parts built on this skeleton can
// extend these checks with the states their rows and cards show.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-speakers-nav.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { ROSTER, SPEAKERS3 } from '../../testkit/demo-data.mjs';
import { speakersScenario } from '../../testkit/scenario.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const KITCHEN = ROSTER.kitchen;
const HOSTS = SPEAKERS3.boxes.map((k) => ROSTER[k].host);
// The list draws a stereo pair as one card, led by its master (the left half
// here), as the sidebar does: one row fewer than speakers.
const ROW_HOSTS = HOSTS.filter((h) => h !== ROSTER.terraceRight.host);
const TABS = ['Overview', 'Sound', 'Network', 'Keys and alarms', 'Maintenance'];

const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);
const sub = (page, route) => page.locator(`#ui2SpkSubnav .spa-sub[data-route="${route}"]`);
const listRow = (page, host) => page.locator(`#view-speakers .spl-open[data-host="${host}"]`);
const pick = (page, host) => page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${host}"]`);
const tab = (page, name) => page.locator('#ui2SpkTabs [role="tab"]', { hasText: name });

// shownViews lists the views on screen in the main pane: exactly one, always.
const shownViews = (page) => page.locator('#ui2MainInner > [id^="view-"]').evaluateAll((els) => els
  .filter((e) => !e.classList.contains('hidden')).map((e) => e.id));

async function openList(page) {
  await nav(page, 'speakers').click();
  await expect(page.locator('#ui2MainInner > #view-speakers')).toBeVisible();
  await expect(page.locator('#view-speakers .spl-open')).toHaveCount(ROW_HOSTS.length);
}

async function openSpeaker(page, host, name) {
  await openList(page);
  await listRow(page, host).click();
  await expect(page.locator('#ui2MainInner > #view-settings')).toBeVisible();
  await expect(page.locator('#ui2SpkPageName')).toHaveText(name);
}

test.describe('UI 2.0 Speakers area', () => {
  test.use({ ui2: true, scenario: speakersScenario() });

  test('1. one Speakers entry opens the list of every speaker; the sidebar list stays', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#ui2SpkArea')).toBeHidden();
    await openList(page);
    await expect(nav(page, 'speakers')).toHaveAttribute('aria-current', 'page');
    await expect(nav(page, 'speakers')).toHaveAttribute('data-views', 'speakers settings multiroom setup');
    for (const v of ['settings', 'multiroom', 'setup']) await expect(nav(page, v)).toHaveCount(0);
    await expect(page.locator('#ui2SpkArea')).toBeVisible();
    await expect(page.locator('#ui2SpkAreaTitle')).toHaveText('Speakers');
    await expect(sub(page, 'list')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#ui2SpkSubnav [aria-current]')).toHaveCount(1);
    await expect(page.locator('#ui2SpkPage')).toBeHidden();
    expect(await shownViews(page)).toEqual(['view-speakers']);
    // Every speaker of the fixture (in the app's own order), the current one marked.
    expect((await page.locator('#view-speakers .spl-open').evaluateAll((els) => els.map((e) => e.dataset.host))).sort())
      .toEqual([...ROW_HOSTS].sort());
    await expect(listRow(page, LR.host)).toHaveAttribute('aria-current', 'true');
    await expect(page.locator('#view-speakers .spl-open[aria-current]')).toHaveCount(1);
    // The sidebar speaker list is phase 1's, unchanged: still there, still
    // choosing the speaker you control.
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
    await expect(page.locator('#ui2SpkLists .ui2-spk-row').first()).toBeVisible();
  });

  test('2. Groups shows Multi-room and Add speaker the setup, under the same entry', async ({ app }) => {
    const { page } = app;
    await openList(page);
    await sub(page, 'groups').click();
    await expect(page.locator('#ui2MainInner > #view-multiroom')).toBeVisible();
    expect(await shownViews(page)).toEqual(['view-multiroom']);
    await expect(sub(page, 'groups')).toHaveAttribute('aria-current', 'page');
    await expect(nav(page, 'speakers')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#view-multiroom')).toHaveAttribute('data-ui2-route', 'groups');
    await sub(page, 'add').click();
    await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
    expect(await shownViews(page)).toEqual(['view-setup']);
    await expect(sub(page, 'add')).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('#view-setup .setup-target-card').first()).toBeVisible();
    await sub(page, 'list').click();
    await expect(page.locator('#ui2MainInner > #view-speakers')).toBeVisible();
    // Outside the area its head goes away.
    await nav(page, 'box').click();
    await expect(page.locator('#ui2SpkArea')).toBeHidden();
    await expect(nav(page, 'speakers')).not.toHaveAttribute('aria-current', 'page');
  });

  test('3. a row opens that speaker with five named tabs, and it becomes the one you control', async ({ app }) => {
    const { page } = app;
    await openSpeaker(page, BR.host, BR.friendlyName);
    expect(await shownViews(page)).toEqual(['view-settings']);
    await expect(page.locator('#ui2SpkPage')).toBeVisible();
    await expect(page.locator('#ui2SpkPageMeta')).toHaveText(`${BR.model} · ${BR.host}`);
    await expect(page.locator('#ui2SpkSubnav [aria-current]')).toHaveCount(0);
    await expect(nav(page, 'speakers')).toHaveAttribute('aria-current', 'page');
    const tablist = page.getByRole('tablist', { name: `Settings for ${BR.friendlyName}` });
    await expect(tablist).toBeVisible();
    await expect(tablist.getByRole('tab')).toHaveText(TABS);
    await expect(page.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true');
    for (const name of TABS.slice(1)) await expect(page.getByRole('tab', { name })).toHaveAttribute('aria-selected', 'false');
    await expect(page.getByRole('tabpanel', { name: 'Overview' })).toBeVisible();
    await expect(page.locator('#ui2SpkPage [role="tabpanel"]:visible')).toHaveCount(1);
    // Lock-step with the classic Settings view and the sidebar.
    await expect(page.locator('#settingsBoxSelect')).toHaveValue(BR.deviceID);
    await expect(page.locator('#boxNameCombo #boxNameInput')).toHaveValue(BR.friendlyName);
    await expect(pick(page, BR.host)).toHaveAttribute('aria-current', 'true');
  });

  test('4. the tabs work with the keyboard: arrows wrap, Home and End, one tab stop', async ({ app }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await tab(page, 'Overview').focus();
    await page.keyboard.press('ArrowRight');
    await expect(tab(page, 'Sound')).toBeFocused();
    await expect(tab(page, 'Sound')).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByRole('tabpanel', { name: 'Sound' })).toBeVisible();
    await expect(page.locator('#ui2SpkPanel-overview')).toBeHidden();
    await page.keyboard.press('End');
    await expect(tab(page, 'Maintenance')).toBeFocused();
    await page.keyboard.press('ArrowRight');
    await expect(tab(page, 'Overview')).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(tab(page, 'Maintenance')).toBeFocused();
    await page.keyboard.press('Home');
    await expect(tab(page, 'Overview')).toBeFocused();
    await expect(page.locator('#ui2SpkTabs [role="tab"][tabindex="0"]')).toHaveCount(1);
    // Tab leaves the row for the open panel.
    await page.keyboard.press('Tab');
    await expect(page.locator('#ui2SpkPanel-overview')).toBeFocused();
    // A click selects too.
    await tab(page, 'Keys and alarms').click();
    await expect(page.getByRole('tabpanel', { name: 'Keys and alarms' })).toBeVisible();
  });

  test('5. choosing another sidebar row moves the open page with it', async ({ app }) => {
    const { page } = app;
    await openSpeaker(page, LR.host, LR.friendlyName);
    await tab(page, 'Network').click();
    await pick(page, BR.host).click();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(BR.friendlyName);
    await expect(page.getByRole('tablist', { name: `Settings for ${BR.friendlyName}` })).toBeVisible();
    await expect(page.locator('#settingsBoxSelect')).toHaveValue(BR.deviceID);
    // The tab stays where it was.
    await expect(tab(page, 'Network')).toHaveAttribute('aria-selected', 'true');
    await expect(page.locator('#ui2MainInner > #view-settings')).toBeVisible();
  });

  test('6. a Bose original in the list opens Add speaker on it', async ({ app }) => {
    const { page } = app;
    await openList(page);
    await expect(listRow(page, KITCHEN.host)).toHaveAttribute('data-route', 'add');
    await listRow(page, KITCHEN.host).click();
    await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
    await expect(sub(page, 'add')).toHaveAttribute('aria-current', 'page');
    const hero = page.locator('#setupPrimaryAction .setup-hero');
    await expect(hero).toHaveAttribute('data-kind', 'stock');
    await expect(hero).toHaveAttribute('data-host', KITCHEN.host);
    // A stock speaker is never the controlled one.
    await expect(pick(page, LR.host)).toHaveAttribute('aria-current', 'true');
  });
});

test.describe('classic layout with the phase 3 network', () => {
  test.use({ ui2: false, scenario: speakersScenario() });

  test('7. is unchanged: no Speakers area, the eight tabs, each opens its view', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('html')).not.toHaveClass(/\bui2\b/);
    for (const id of ['ui2SpkArea', 'ui2SpkPage', 'view-speakers', 'ui2Shell']) await expect(page.locator(`#${id}`)).toHaveCount(0);
    const tabs = page.locator('.tab-btn[data-view]');
    expect(await tabs.evaluateAll((els) => els.map((e) => e.dataset.view)))
      .toEqual(['box', 'library', 'recent', 'settings', 'setup', 'multiroom', 'spotify', 'wishes']);
    await page.locator('.tab-btn[data-view="settings"]').click();
    await expect(page.locator('#view-settings')).toBeVisible();
    await expect(page.locator('#settingsBoxSelect option')).toHaveCount(HOSTS.length);
    await expect(page.locator('#settingsBoxSelect')).toHaveValue(LR.deviceID);
    await expect(page.locator('#view-settings .settings-group').first()).toBeVisible();
    await page.locator('.tab-btn[data-view="multiroom"]').click();
    await expect(page.locator('#view-multiroom')).toBeVisible();
    await expect(page.locator('#view-multiroom')).not.toHaveAttribute('data-ui2-route', /.*/);
    await page.locator('.tab-btn[data-view="setup"]').click();
    await expect(page.locator('#view-setup .setup-target-card').first()).toBeVisible();
    await expect(page.locator('#view-speakers')).toHaveCount(0);
  });
});
