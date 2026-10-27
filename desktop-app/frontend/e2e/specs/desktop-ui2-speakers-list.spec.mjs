// The UI 2.0 Speakers list (#1287, phase 3, src/shell2/speakers/list.js):
// every speaker as one card with its name, what it is and ONE status pill in
// words; the stereo pair as one card; the actions that belong at list level
// (open, update, set up STR, search again for an offline speaker) each naming
// its speaker; the footer to Groups, update every speaker and Add speaker; and
// the list at XL text in the smallest window.
//
// The network is the phase 3 fixture (testkit/demo-data.mjs SPEAKERS3): ten
// speakers, one in every state.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-ui2-speakers-list.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';
import { speakersScenario } from '../../testkit/scenario.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { KILL_ANIM } from '../helpers/a11y.mjs';
import { minSize, noHorizontalOverflow } from '../helpers/layout.mjs';

const R = ROSTER;
const card = (page, host) => page.locator(`#view-speakers .spl-item[data-host="${host}"]`);
const pill = (page, host) => card(page, host).locator('.u-pill');
const act = (page, what, host) => page.locator(`#view-speakers .spl-act[data-act="${what}"]${host ? `[data-host="${host}"]` : ''}`);

async function openList(page) {
  await page.locator('#ui2Sidebar .ui2-nav-item[data-view="speakers"]').click();
  await expect(page.locator('#ui2MainInner > #view-speakers')).toBeVisible();
  // The zone poll has answered once the stereo pair is one card.
  await expect(page.locator('#view-speakers .spl-item')).toHaveCount(9);
}

test.describe('UI 2.0 Speakers list', () => {
  test.use({ ui2: true, scenario: speakersScenario() });

  test('1. every speaker says its state in words, one pill each', async ({ app }) => {
    const { page } = app;
    await openList(page);
    const expected = [
      [R.livingRoom, 'Playing'],
      [R.oldAgent, 'Update ready'],
      [R.nursery, 'Starting up'],
      [R.office, 'Offline'],
      [R.terraceLeft, 'Stereo pair'],
      [R.dining, 'Fixed group'],
      [R.hall, 'Fixed group'],
      [R.kitchen, 'Ready for STR'],
    ];
    for (const [b, words] of expected) {
      await expect(pill(page, b.host), b.friendlyName).toHaveCount(1);
      await expect(pill(page, b.host), b.friendlyName).toHaveText(words);
    }
    // The dot only repeats the words.
    await expect(page.locator('#view-speakers .u-pill-dot:not([aria-hidden="true"])')).toHaveCount(0);
    // The pair is one card, named as in the sidebar (its halves, as the app
    // keeps no name of its own for it here), with what the halves are.
    await expect(card(page, R.terraceRight.host)).toHaveCount(0);
    await expect(card(page, R.terraceLeft.host).locator('.spl-name')).toHaveText('Terrace left + Terrace right');
    await expect(card(page, R.terraceLeft.host).locator('.spl-meta').first()).toHaveText('SoundTouch 10');
    // What plays, the version, the fixed group and the update.
    await expect(card(page, R.livingRoom.host).locator('.spl-now')).toHaveText(/^Plays /);
    await expect(card(page, R.livingRoom.host).locator('.spl-meta').first()).toContainText('STR v1.0.19');
    await expect(card(page, R.dining.host)).toContainText('Main speaker of a fixed group with Hall');
    await expect(card(page, R.hall.host)).toContainText('In the fixed group of Dining room');
    await expect(card(page, R.oldAgent.host)).toContainText('STR v1.0.10 installed, v1.0.19 is ready.');
    await expect(card(page, R.kitchen.host)).toContainText('Bose original');
    await expect(card(page, R.nursery.host)).toContainText('STR is starting on this speaker.');
    // The speaker you control is marked, by aria-current too.
    await expect(page.locator('#view-speakers .spl-open[aria-current="true"]')).toHaveCount(1);
    await expect(card(page, R.livingRoom.host).locator('.spl-open')).toHaveAttribute('aria-current', 'true');
  });

  test('2. standby shows on the speaker you control, as in the sidebar', async ({ app }) => {
    const { page } = app;
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${R.bedroom.host}"]`).click();
    await openList(page);
    await expect(pill(page, R.bedroom.host)).toHaveText('Standby');
    await expect(card(page, R.bedroom.host).locator('.spl-open')).toHaveAttribute('aria-current', 'true');
  });

  test('3. Update on a speaker opens its page and starts the update there', async ({ app, mock }) => {
    const { page } = app;
    await openList(page);
    const btn = act(page, 'update', R.oldAgent.host);
    await expect(btn).toHaveAccessibleName('Update STR on Hallway');
    await expect(act(page, 'update')).toHaveCount(1);
    await btn.click();
    await expect(page.locator('#ui2MainInner > #view-settings')).toBeVisible();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(R.oldAgent.friendlyName);
    await mock.expectCalled('RunSpeakerUpdate', { pred: onHost(R.oldAgent.host), times: 1 });
    expect(await mock.count('RunSpeakerUpdate', (c) => c.args[0] !== R.oldAgent.host)).toBe(0);
  });

  test('4. a Bose original opens the setup, from its card and from Set up STR', async ({ app }) => {
    const { page } = app;
    await openList(page);
    const btn = act(page, 'setup', R.kitchen.host);
    await expect(btn).toHaveAccessibleName('Set up STR on Kitchen');
    await btn.click();
    await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
    await openList(page);
    await card(page, R.kitchen.host).locator('.spl-open').click();
    await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
  });

  test('5. an offline speaker explains itself, and Search again reports what it found', async ({ app, mock }) => {
    const { page } = app;
    await openList(page);
    const off = card(page, R.office.host);
    await expect(off).toContainText('Not reachable, last seen 10 minutes ago.');
    await expect(off).toContainText('Check that it has power');
    await expect(off.locator('.spl-name')).toHaveCSS('color', await off.locator('.spl-meta').first().evaluate((e) => getComputedStyle(e).color));
    const before = await mock.count('DiscoverBoxes');
    await act(page, 'search', R.office.host).click();
    await expect.poll(() => mock.count('DiscoverBoxes')).toBeGreaterThan(before);
    // The mock never brings it back: the card says so, in its own place.
    await expect(off.locator('.spl-note-result')).toHaveText('Office is still not reachable. Unplug it for ten seconds, wait two minutes, then search again.');
    await expect(act(page, 'search', R.office.host)).toBeFocused();
    // An offline speaker still opens its page.
    await off.locator('.spl-open').click();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(R.office.friendlyName);
  });

  test('6. the footer leads to Groups, every update and Add speaker', async ({ app }) => {
    const { page } = app;
    await openList(page);
    await expect(act(page, 'updateAll')).toHaveText('1 waiting');
    await expect(act(page, 'updateAll')).toHaveAccessibleName('Update every speaker, 1 waiting');
    await act(page, 'groups').click();
    await expect(page.locator('#ui2MainInner > #view-multiroom')).toBeVisible();
    await openList(page);
    await act(page, 'add').click();
    await expect(page.locator('#ui2MainInner > #view-setup')).toBeVisible();
  });

  test('7. a card opens the speaker; the keyboard reaches its action right after it', async ({ app }) => {
    const { page } = app;
    await openList(page);
    const open = card(page, R.oldAgent.host).locator('.spl-open');
    await open.focus();
    await page.keyboard.press('Tab');
    await expect(act(page, 'update', R.oldAgent.host)).toBeFocused();
    // Then the next card (the list keeps the app's own speaker order).
    await page.keyboard.press('Tab');
    expect(await page.evaluate(() => {
      const a = document.activeElement;
      return a && a.classList.contains('spl-open') ? a.dataset.host : '';
    })).not.toMatch(/^$|^192\.0\.2\.54$/);
    await page.keyboard.press('Shift+Tab');
    await page.keyboard.press('Shift+Tab');
    await expect(open).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.locator('#ui2SpkPageName')).toHaveText(R.oldAgent.friendlyName);
  });
});

test.describe('UI 2.0 Speakers list, XL text in the smallest window', () => {
  test.use({ ui2: true, scale: '3', viewport: { width: 1000, height: 600 }, scenario: speakersScenario() });

  test('8. every card fits, nothing scrolls sideways, every target is 40px', async ({ app }) => {
    const { page } = app;
    await page.addStyleTag({ content: KILL_ANIM });
    await openList(page);
    await noHorizontalOverflow(page);
    const over = await page.locator('#view-speakers .spl-item').evaluateAll((els) => {
      const pane = document.getElementById('view-speakers').getBoundingClientRect();
      return els.map((e) => e.getBoundingClientRect())
        .filter((r) => r.left < pane.left - 1 || r.right > pane.right + 1).length;
    });
    expect(over).toBe(0);
    await minSize(page.locator('#view-speakers .spl-open, #view-speakers .spl-act'));
    // The last card and the footer are reachable by scrolling.
    await act(page, 'add').scrollIntoViewIfNeeded();
    await expect(act(page, 'add')).toBeInViewport();
  });
});
