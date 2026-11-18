// A stereo pair joins a multiroom group as ONE member, by its pair master, and
// never leads one. The Bose app forms the same group that way and it plays (a
// SoundTouch 30 leading a pair of SoundTouch 10s, field bundle 2026-11-15);
// a group led by a pair half never starts the audio (#792).
//
// Classic Multi-room tab: the pair is one card, named after the pair, that can
// be ticked and never be made the main speaker; a group with the pair in it
// shows the pair as one chip. UI 2.0 sidebar: the pair's one row offers "+".
//
//   E2E_PORT=4186 npx playwright test specs/desktop-multiroom-pair-in-group.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { en } from '../helpers/i18n.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { ROSTER, SPEAKERS3 } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';

const LR = ROSTER.livingRoom;
const TL = ROSTER.terraceLeft;
const TR = ROSTER.terraceRight;
const PAIR = SPEAKERS3.zones.terraceLeft.stereo;
const PAIR_UNIT = en('ui2.side.pairName', { name: PAIR.name });

// Living room leads a group with the pair in it: its zone names the pair
// master alone, the pair master reports the zone and its pair, the other half
// only its pair (the field bundle's shape).
const GROUPED = mergeScenario(speakersScenario(), {
  zones: {
    livingRoom: { master: LR.deviceID, members: [{ deviceID: TL.deviceID, ip: TL.host }] },
    terraceLeft: { master: LR.deviceID, senderIP: LR.host, members: [{ deviceID: TL.deviceID, ip: TL.host }], stereo: PAIR },
    terraceRight: { members: [], stereo: PAIR },
  },
});

const card = (page, box) => page.locator(`#view-multiroom .zone-card[data-id="${box.deviceID}"]`);

async function openMultiroom(page) {
  await page.locator('.tab-btn[data-view="multiroom"]').click();
  await expect(page.locator('#view-multiroom #zoneCreate')).toBeAttached();
}

test.describe('classic: a stereo pair in the group editor', () => {
  test.use({ ui2: false, scenario: speakersScenario() });

  test('is one card, named after the pair, that joins and cannot be main', async ({ app, mock }) => {
    const { page } = app;
    await openMultiroom(page);
    const pair = card(page, TL);
    await expect(pair).toHaveClass(/\bpair\b/);
    await expect(pair.locator('.zone-card-name')).toContainText(PAIR_UNIT);
    await expect(pair).toContainText(en('multiroom.pairJoinOnly'));
    await expect(pair.locator('.zone-makemain')).toHaveCount(0);
    // The other half has no card of its own.
    await expect(card(page, TR)).toHaveCount(0);
    // The old dead end is gone.
    await expect(page.locator('#view-multiroom')).not.toContainText('Dissolve the pair');

    // Living room leads, the pair joins: the group is sent with the pair
    // master as its one member.
    await card(page, LR).locator('.zone-makemain').click();
    await expect(card(page, LR)).toHaveClass(/\bmaster\b/);
    await pair.click();
    await expect(card(page, TL)).toHaveClass(/\bselected\b/);
    await mock.resetCalls();
    await page.locator('#zoneCreate').click();
    await mock.expectCalled('FormZone', { times: 1 });
    const call = await mock.last('FormZone');
    expect(call.args[0]).toBe(LR.host);
    expect(call.args[2].slaves).toEqual([{ deviceID: TL.deviceID, ip: TL.host }]);
    expect(JSON.stringify(call.args)).not.toContain(TR.host);
  });

  test('is never the default main speaker', async ({ app }) => {
    const { page } = app;
    await openMultiroom(page);
    await expect(card(page, TL)).not.toHaveClass(/\bmaster\b/);
    await expect(page.locator('#view-multiroom .zone-card.master')).toHaveCount(1);
  });
});

test.describe('classic: a group with a stereo pair in it', () => {
  test.use({ ui2: false, scenario: GROUPED });

  test('shows the pair as one chip in the group, and keeps the pair\'s own frame', async ({ app }) => {
    const { page } = app;
    await openMultiroom(page);
    const group = page.locator('#view-multiroom .box-group').filter({ has: page.locator('.box-group-x[data-dissolve-kind="zone"]') });
    await expect(group).toHaveCount(1);
    await expect(group.locator('.zone-frame-chip-pair')).toHaveText(PAIR_UNIT);
    await expect(group.locator('.zone-frame-chip')).toHaveCount(2);
    await expect(group).not.toContainText(TL.friendlyName);
    await expect(group).not.toContainText(TR.friendlyName);
    // The pair stays a pair that can be undone, in its own frame.
    const pairFrame = page.locator('#view-multiroom .box-group').filter({ has: page.locator('.box-group-x[data-dissolve-kind="pair"]') });
    await expect(pairFrame).toHaveCount(1);
    await expect(pairFrame).toContainText(TL.friendlyName);
    await expect(pairFrame).toContainText(TR.friendlyName);
  });
});

test.describe('UI 2.0: a stereo pair in the sidebar', () => {
  test.use({ ui2: true, fakeClock: true, scenario: speakersScenario() });

  test('its one row adds the pair to what plays, by its pair master', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 20_000);
    const row = page.locator('#ui2SpkLists .ui2-spk-row.is-pair');
    await expect(row).toHaveCount(1);
    await expect(row.locator('.ui2-spk-name')).toHaveText(PAIR_UNIT);
    const add = row.locator('.ui2-spk-act[data-act="add"]');
    await expect(add).toBeVisible();
    await expect(add).toHaveAttribute('data-master', LR.host);
    await mock.resetCalls();
    await add.click();
    await runFor(page, 10_000);
    await mock.expectCalled('FormZone', { times: 1 });
    const call = await mock.last('FormZone');
    expect(call.args[0]).toBe(LR.host);
    expect(call.args[2].slaves).toEqual([{ deviceID: TL.deviceID, ip: TL.host }]);
  });
});

async function openGroups(page) {
  await page.locator('#ui2Tabs .ui2-nav-item[data-view="speakers"]').click();
  await page.locator('#ui2SpkSubnav .spa-sub[data-route="groups"]').click();
  await expect(page.locator('#view-multiroom > .ui2-grp')).toBeVisible();
}

test.describe('UI 2.0 Speakers > Groups: a stereo pair', () => {
  test.use({ ui2: true });

  test.describe('on its own', () => {
    test.use({ scenario: speakersScenario() });

    test('is offered to a fixed group under the pair\'s name', async ({ app }) => {
      const { page } = app;
      await openGroups(page);
      const toggle = card(page, TL).locator('.ui2-grp-toggle');
      await expect(toggle).toHaveText(en('ui2.speakers.groups.include', { name: PAIR_UNIT }));
      await expect(card(page, TR)).toHaveCount(0);
    });
  });

  test.describe('in a group', () => {
    test.use({ scenario: GROUPED });

    test('stays in "Stereo pairs", and the group card lists it as one member', async ({ app }) => {
      const { page } = app;
      await openGroups(page);
      const pair = page.locator(`#view-multiroom .ui2-grp-card[data-kind="pair"][data-key="${TL.deviceID}"]`);
      await expect(pair.locator('.ui2-grp-card-title')).toHaveText(PAIR.name);
      const group = page.locator(`#view-multiroom .ui2-grp-card[data-kind="zone"][data-key="${LR.deviceID}"]`);
      await expect(group.locator('.ui2-grp-members li')).toHaveCount(2);
      await expect(group.locator('.ui2-grp-members')).toContainText(PAIR_UNIT);
    });
  });
});

test.describe('UI 2.0: a group with a stereo pair in it', () => {
  test.use({ ui2: true, fakeClock: true, scenario: GROUPED });

  test('pins the pair as one row of the group, with a "-"', async ({ app }) => {
    const { page } = app;
    await settle(page, 20_000);
    const pinned = page.locator('#ui2SpkLists .ui2-spk-pinned .ui2-spk-row');
    await expect(pinned.locator('.ui2-spk-name')).toHaveText([LR.friendlyName, PAIR_UNIT]);
    await expect(pinned.nth(1).locator('.ui2-spk-act[data-act="remove"]')).toBeVisible();
  });
});
