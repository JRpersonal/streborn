// UI 2.0 Speakers > Groups (#1287, src/shell2/speakers/groups.js): stereo
// pairs and fixed groups as cards that name their speakers, a confirmation
// that names the pair or group before anything is taken apart, "done" only
// once the speakers' own zone reports show it, errors in the card they belong
// to, every control of the classic Multi-room tab in a fixed place, and the
// classic layout unchanged.
//
// The network is the phase 3 fixture (testkit/demo-data.mjs SPEAKERS3): the
// stereo pair "Terrace" and the stored fixed group Dining room + Hall. The
// mock's group bindings change nothing on their own, so a test plays the
// speakers' side by writing the zone documents (setZone) the 5 s poll reads.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-ui2-speakers-groups.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { KILL_ANIM, scan } from '../helpers/a11y.mjs';
import { noHorizontalOverflow } from '../helpers/layout.mjs';
import { ROSTER, SPEAKERS3 } from '../../testkit/demo-data.mjs';
import { mergeScenario, speakersScenario } from '../../testkit/scenario.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const TL = ROSTER.terraceLeft;
const TR = ROSTER.terraceRight;
const DINING = ROSTER.dining;
const HALL = ROSTER.hall;
const BASELINE = 'baselines/a11y-ui2-groups.json';
// The poll runs every 5 s; a confirmation can take two rounds.
const POLL = { timeout: 15_000 };

const LR_BR_GROUP = {
  master: LR.deviceID,
  senderIP: LR.host,
  members: [{ deviceID: LR.deviceID, ip: LR.host }, { deviceID: BR.deviceID, ip: BR.host }],
};
const TERRACE = SPEAKERS3.zones.terraceLeft;

// Every control of the classic Multi-room tab that is not "play along now",
// by its id, and where the page keeps it.
const PLACES = {
  '#zoneRefresh': '.ui2-grp-tools',
  '#zoneCurrent': '[data-sec="now"]',
  '#zoneUngroup': '[data-sec="now"]',
  '#stereoLeft': '#ui2GrpPairEditor',
  '#stereoRight': '#ui2GrpPairEditor',
  '#stereoName': '#ui2GrpPairEditor',
  '#stereoNameSave': '#ui2GrpPairEditor',
  '#stereoCreate': '#ui2GrpPairEditor',
  '#stereoDissolve': '#ui2GrpPairEditor',
  '#stereoResult': '[data-sec="pairs"]',
  '#zonePermanent': '#ui2GrpFixedEditor',
  '#zoneCreate': '#ui2GrpFixedEditor',
  '#zoneResult': '.ui2-grp',
  '#gkRemote': '[data-sec="keys"]',
  '#gkName': '[data-sec="keys"]',
  '#gkSave': '[data-sec="keys"]',
  '#gkKeyMap': '[data-sec="keys"]',
  '#gkResult': '[data-sec="keys"]',
};

const sec = (page, s) => page.locator(`#view-multiroom .ui2-grp-sec[data-sec="${s}"]`);
const card = (page, kind, key) => page.locator(`#view-multiroom .ui2-grp-card[data-kind="${kind}"][data-key="${key}"]`);
const dialog = (page) => page.locator('#ui2GrpConfirm');
// The frame x of the pair and of the stored group, named for what they take apart.
const undoBtn = (page) => card(page, 'pair', TL.deviceID).getByRole('button', { name: 'Undo Terrace' });
const deleteBtn = (page) => card(page, 'stored', DINING.deviceID).getByRole('button', { name: `Delete the group ${DINING.friendlyName}` });

async function openGroups(page) {
  await page.locator('#ui2Sidebar .ui2-nav-item[data-view="speakers"]').click();
  await page.locator('#ui2SpkSubnav .spa-sub[data-route="groups"]').click();
  await expect(page.locator('#ui2MainInner > #view-multiroom')).toBeVisible();
  await expect(page.locator('#view-multiroom > .ui2-grp')).toBeVisible();
}

// setZone is the speaker's side: what GetZoneState answers from now on.
const setZone = (page, host, doc) => page.evaluate(([h, d]) => { window.__strMock.state.zones[h] = d; }, [host, doc]);

test.describe('UI 2.0 Speakers > Groups', () => {
  test.use({ ui2: true, scenario: speakersScenario() });

  test('1. the page names every pair and group with its speakers, and says where "play along" lives', async ({ app }) => {
    const { page } = app;
    await openGroups(page);
    await expect(sec(page, 'now').locator('.ui2-grp-lead')).toContainText('+ on a speaker adds it');
    await expect(page.locator('#view-multiroom .ui2-grp-h')).toHaveText(['Playing together now', 'Stereo pairs', 'Fixed groups', /^Group keys/]);
    const pair = card(page, 'pair', TL.deviceID);
    await expect(pair.locator('.ui2-grp-card-title')).toHaveText('Terrace');
    await expect(pair.locator('.ui2-grp-members li')).toHaveText([`Left: ${TL.friendlyName}`, `Right: ${TR.friendlyName}`]);
    await expect(pair.getByRole('button', { name: 'Change Terrace' })).toBeVisible();
    await expect(pair.getByRole('button', { name: 'Undo Terrace' })).toBeVisible();
    const fixed = card(page, 'stored', DINING.deviceID);
    await expect(fixed).toBeVisible();
    await expect(sec(page, 'fixed').locator('.ui2-grp-card')).toHaveCount(1);
    await expect(fixed.locator('.ui2-grp-card-title')).toHaveText(DINING.friendlyName);
    await expect(fixed.locator('.u-pill')).toHaveText(['Fixed group', 'Saved, not playing']);
    await expect(fixed.locator('.ui2-grp-members li')).toHaveCount(2);
    await expect(fixed.getByRole('button', { name: `Take ${HALL.friendlyName} out of this group` })).toBeVisible();
    await expect(fixed.getByRole('button', { name: `Delete the group ${DINING.friendlyName}` })).toBeVisible();
    // Nothing plays together in this network: the section says so, no card.
    await expect(sec(page, 'now').locator('.ui2-grp-card')).toHaveCount(0);
    await expect(page.locator('#zoneCurrent')).toHaveText('No group right now.');
    // With no group playing there is nothing to ungroup (test 10 has the button back).
    await expect(page.locator('#zoneUngroup')).toBeHidden();
    // The editors name what they would do.
    await expect(page.locator('#ui2GrpPairEditorTitle')).toHaveText('Change Terrace');
    await expect(page.locator('#stereoCreate')).toHaveText(`Pair ${TL.friendlyName} (left) and ${TR.friendlyName} (right)`);
    await expect(page.locator('#ui2GrpFixedEditorTitle')).toHaveText('New fixed group');
    await expect(page.locator('#zonePermanent')).toBeChecked();
    // The status region is there, empty, before the first action speaks into it.
    await expect(page.locator('#ui2GrpLive[role="status"]')).toHaveText('');
    expect(await app.page.evaluate(() => window.__strMock.pageErrors)).toEqual([]);
  });

  test('2. nothing of the classic Multi-room tab is missing: every control has its place', async ({ app }) => {
    const { page } = app;
    await openGroups(page);
    for (const [id, place] of Object.entries(PLACES)) {
      await expect(page.locator(`#view-multiroom ${place} ${id}`), `${id} in ${place}`).toHaveCount(1);
    }
    // The zone cards (main speaker, members, update first) are in the editor,
    // the stale-pair fix and the notes above, nothing stayed behind outside the page.
    await expect(page.locator('#ui2GrpFixedEditor .zone-card')).toHaveCount(await page.locator('#view-multiroom .zone-card').count());
    await expect(page.locator('#ui2GrpFixedEditor .zone-update-badge')).toHaveCount(1);
    await expect(page.locator('#ui2GrpFixedEditor .zone-makemain').first()).toBeVisible();
    await expect(page.locator('#view-multiroom > :not(.ui2-grp)')).toHaveCount(0);
    // Play along now stays in the sidebar: "+" on an idle speaker, "-" on a member.
    await expect(page.locator('#ui2SpkLists .ui2-spk-act').first()).toBeVisible();
  });

  test('3. undoing a pair asks first, naming it; Cancel sends nothing; done only once the speakers report it', async ({ app, mock }) => {
    const { page } = app;
    await openGroups(page);
    const undo = undoBtn(page);
    await undo.click();
    await expect(dialog(page)).toBeVisible();
    await expect(dialog(page).locator('#ui2GrpConfirmTitle')).toHaveText('Undo the stereo pair Terrace?');
    await expect(dialog(page).locator('#ui2GrpConfirmBody')).toHaveText(`${TL.friendlyName} and ${TR.friendlyName} then play on their own again.`);
    await expect(page.locator('#ui2GrpConfirmNo')).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog(page)).toBeHidden();
    await expect(undo).toBeFocused();
    await undoBtn(page).click();
    await page.locator('#ui2GrpConfirmNo').click();
    expect(await mock.count('DissolveStereoPair')).toBe(0);

    await undoBtn(page).click();
    await dialog(page).getByRole('button', { name: 'Undo Terrace' }).click();
    await mock.expectCalled('DissolveStereoPair', { pred: onHost(TL.host) });
    await mock.expectCalled('DissolveStereoPair', { pred: onHost(TR.host) });
    const status = page.locator('#ui2GrpStatus-pairs');
    await expect(status).toHaveText(`Sent. Waiting for ${TL.friendlyName}, ${TR.friendlyName} to confirm.`);
    // The classic green note would say "undone" before the speakers did.
    await expect(page.locator('#stereoResult .setup-ok')).toBeHidden();
    await setZone(page, TL.host, {});
    await setZone(page, TR.host, {});
    await expect(status).toHaveText(`Terrace is undone. ${TL.friendlyName}, ${TR.friendlyName} play on their own again.`, POLL);
    await expect(card(page, 'pair', TL.deviceID)).toHaveCount(0);
    await expect(sec(page, 'pairs').locator('.ui2-grp-empty')).toHaveText('No stereo pair yet.');
    await expect(page.locator('#ui2GrpLive')).toHaveText(/Terrace is undone/);
  });

  test('4. a refused undo shows its error in the pair card', async ({ app, mock }) => {
    const { page } = app;
    await mock.set('DissolveStereoPair', { error: 'speaker busy' });
    await openGroups(page);
    await undoBtn(page).click();
    await dialog(page).getByRole('button', { name: 'Undo Terrace' }).click();
    const err = card(page, 'pair', TL.deviceID).locator('#stereoResult .setup-err');
    await expect(err).toHaveText('Could not change the group: speaker busy');
    await expect(page.locator('#ui2GrpStatus-pairs')).toBeHidden();
  });

  test('5. Change loads a pair into the editor; a new pair names its speakers on its button', async ({ app }) => {
    const { page } = app;
    await openGroups(page);
    await page.getByRole('button', { name: 'Change Terrace' }).click();
    await expect(page.locator('#stereoName')).toBeFocused();
    await expect(page.locator('#stereoLeft')).toHaveValue(TL.deviceID);
    // A new pair: Bedroom left, Terrace right... the editor names it.
    await page.locator('#stereoLeft').selectOption(BR.deviceID);
    await expect(page.locator('#ui2GrpPairEditorTitle')).toHaveText('New stereo pair');
    await expect(page.locator('#stereoCreate')).toHaveText(`Pair ${BR.friendlyName} (left) and ${TR.friendlyName} (right)`);
  });

  test.describe('with one half of the pair offline', () => {
    test.use({ scenario: mergeScenario(speakersScenario(), { boxPatches: { terraceRight: { offline: true } }, zones: { terraceRight: null } }) });

    test('5b. undoing the pair says the offline half still holds its side, not a plain done', async ({ app, mock }) => {
      const { page } = app;
      await openGroups(page);
      await undoBtn(page).click();
      await dialog(page).getByRole('button', { name: 'Undo Terrace' }).click();
      await mock.expectCalled('DissolveStereoPair', { pred: onHost(TL.host) });
      expect(await mock.count('DissolveStereoPair', onHost(TR.host))).toBe(0);
      const status = page.locator('#ui2GrpStatus-pairs');
      await expect(status).toHaveText(`Sent. Waiting for ${TL.friendlyName} to confirm.`);
      await setZone(page, TL.host, {});
      await expect(status).toHaveText(`Terrace is undone on ${TL.friendlyName}. ${TR.friendlyName} is not reachable and still holds its side of the pair. Once it is back, this page offers to clear it.`, POLL);
      await expect(status).toHaveClass(/\bis-warn\b/);
    });
  });

  test.describe('without a pair', () => {
    test.use({ scenario: mergeScenario(speakersScenario(), { zones: { terraceLeft: null, terraceRight: null } }) });

    test('6. creating a pair sends it once and says done when both halves report the pair', async ({ app, mock }) => {
      const { page } = app;
      await openGroups(page);
      await expect(sec(page, 'pairs').locator('.ui2-grp-empty')).toHaveText('No stereo pair yet.');
      await page.locator('#stereoLeft').selectOption(TL.deviceID);
      await page.locator('#stereoRight').selectOption(TR.deviceID);
      await page.locator('#stereoName').fill('Patio');
      const create = page.getByRole('button', { name: `Pair ${TL.friendlyName} (left) and ${TR.friendlyName} (right)` });
      await create.click();
      await mock.expectCalled('FormZone', { pred: (c) => c.args[0] === TL.host && c.args[2].stereo === true, times: 1 });
      const status = page.locator('#ui2GrpStatus-pairs');
      await expect(status).toHaveText(`Sent. Waiting for ${TL.friendlyName}, ${TR.friendlyName} to confirm.`);
      await setZone(page, TL.host, TERRACE);
      await setZone(page, TR.host, TERRACE);
      await expect(status).toHaveText(`Patio is set up: ${TL.friendlyName} plays left, ${TR.friendlyName} plays right.`, POLL);
      await expect(card(page, 'pair', TL.deviceID)).toBeVisible();
    });
  });

  test('7. a fixed group: delete and take out ask first, naming the group; done once the main speaker forgets it', async ({ app, mock }) => {
    const { page } = app;
    await openGroups(page);
    await page.getByRole('button', { name: `Take ${HALL.friendlyName} out of this group` }).click();
    await expect(dialog(page).locator('#ui2GrpConfirmTitle')).toHaveText(`Take ${HALL.friendlyName} out of the group ${DINING.friendlyName}?`);
    await page.locator('#ui2GrpConfirmNo').click();
    expect(await mock.count('RemoveGroupMember')).toBe(0);

    await deleteBtn(page).click();
    await expect(dialog(page).locator('#ui2GrpConfirmTitle')).toHaveText(`Delete the fixed group ${DINING.friendlyName}?`);
    await expect(dialog(page).locator('#ui2GrpConfirmBody')).toContainText(HALL.friendlyName);
    await dialog(page).getByRole('button', { name: `Delete the group ${DINING.friendlyName}` }).click();
    await mock.expectCalled('ForgetPermanentGroup', { pred: onHost(DINING.host), times: 1 });
    const status = page.locator('#ui2GrpStatus-fixed');
    await expect(status).toHaveText(`Sent. Waiting for ${DINING.friendlyName} to confirm.`);
    // The keyboard waits on the section heading, not on a button about to go.
    await expect(page.locator('#ui2GrpH-fixed')).toBeFocused();
    await setZone(page, DINING.host, {});
    await expect(status).toHaveText(`The fixed group ${DINING.friendlyName} is deleted. It no longer forms by itself.`, POLL);
    await expect(sec(page, 'fixed').locator('.ui2-grp-empty')).toHaveText('No fixed group yet.');
    // The card is gone; the focus did not fall to the top of the document.
    await expect(page.locator('#ui2GrpH-fixed')).toBeFocused();
  });

  test('7b. by keyboard alone: after a confirmed undo the focus stays on the section, never on <body>', async ({ app }) => {
    const { page } = app;
    await openGroups(page);
    await undoBtn(page).focus();
    await page.keyboard.press('Enter');
    await expect(page.locator('#ui2GrpConfirmNo')).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(page.locator('#ui2GrpConfirmYes')).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.locator('#ui2GrpH-pairs')).toBeFocused();
    await setZone(page, TL.host, {});
    await setZone(page, TR.host, {});
    await expect(card(page, 'pair', TL.deviceID)).toHaveCount(0, POLL);
    await expect(page.locator('#ui2GrpH-pairs')).toBeFocused();
  });

  test('8. a new fixed group names its speakers on the button and is done when the main speaker stores it', async ({ app, mock }) => {
    const { page } = app;
    await openGroups(page);
    // Nothing plays: the main speaker (the star) defaults to the stored
    // group's, Dining room.
    await expect(page.locator(`#ui2GrpFixedEditor .zone-card.master[data-id="${DINING.deviceID}"]`)).toBeVisible();
    const include = page.getByRole('button', { name: `Include ${BR.friendlyName}` });
    await expect(include).toHaveAttribute('aria-pressed', 'false');
    await include.click();
    await expect(page.getByRole('button', { name: `Include ${BR.friendlyName}` })).toHaveAttribute('aria-pressed', 'true');
    await expect(page.getByRole('button', { name: `Include ${BR.friendlyName}` })).toBeFocused();
    const create = page.locator('#zoneCreate');
    await expect(create).toHaveText(`Save ${DINING.friendlyName} with ${BR.friendlyName} as a fixed group`);
    // Unticking "keep permanently" makes it a group for now, and says so.
    await page.locator('#zonePermanent').uncheck();
    await expect(page.locator('#ui2GrpFixedEditorTitle')).toHaveText('New group');
    await expect(create).toHaveText(`Group ${DINING.friendlyName} with ${BR.friendlyName} now`);
    await page.locator('#zonePermanent').check();
    await create.click();
    await mock.expectCalled('FormZone', { pred: (c) => c.args[0] === DINING.host && c.args[2].permanent === true, times: 1 });
    const status = page.locator('#ui2GrpStatus-fixed');
    await expect(status).toHaveText(`Sent. Waiting for ${DINING.friendlyName}, ${BR.friendlyName} to confirm.`);
    await setZone(page, DINING.host, {
      members: [],
      permanent: true,
      remembered: [
        { deviceID: HALL.deviceID, ip: HALL.host, name: HALL.friendlyName },
        { deviceID: BR.deviceID, ip: BR.host, name: BR.friendlyName },
      ],
    });
    await expect(status).toHaveText(`The fixed group ${DINING.friendlyName} is saved. It forms by itself the next time ${DINING.friendlyName} plays.`, POLL);
    await expect(card(page, 'stored', DINING.deviceID).locator('.ui2-grp-members li')).toHaveCount(3);
  });

  test.describe('on the page clock', () => {
    test.use({ fakeClock: true });

    test('9. the speakers not confirming is said, not hidden', async ({ app, mock }) => {
      const { page } = app;
      await page.clock.runFor(3_000);
      await openGroups(page);
      await page.clock.runFor(1_000);
      await deleteBtn(page).click();
      await dialog(page).getByRole('button', { name: `Delete the group ${DINING.friendlyName}` }).click();
      await mock.expectCalled('ForgetPermanentGroup');
      await page.clock.runFor(1_000);
      await expect(page.locator('#ui2GrpStatus-fixed')).toHaveText(`Sent. Waiting for ${DINING.friendlyName} to confirm.`);
      await page.clock.runFor(26_000);
      const status = page.locator('#ui2GrpStatus-fixed');
      await expect(status).toHaveText(`${DINING.friendlyName} did not confirm it yet. "Refresh" shows what the speakers report now.`);
      await expect(status).toHaveClass(/\bis-warn\b/);
    });
  });

  test.describe('with a group playing', () => {
    test.use({ scenario: mergeScenario(speakersScenario(), { zones: { livingRoom: LR_BR_GROUP, bedroom: LR_BR_GROUP }, playing: { bedroom: 'PLAY_STATE' } }) });

    test('10. the group that plays now has its card; dissolving it asks first and names everyone in it', async ({ app, mock }) => {
      const { page } = app;
      await openGroups(page);
      const now = card(page, 'zone', LR.deviceID);
      await expect(sec(page, 'now').locator('.ui2-grp-card')).toHaveCount(1);
      await expect(now.locator('.ui2-grp-card-title')).toHaveText(LR.friendlyName);
      await expect(now.locator('.u-pill')).toHaveText('Playing together');
      await expect(page.locator('#zoneCurrent')).toContainText(`${LR.friendlyName} + ${BR.friendlyName}`);
      await page.locator('#zoneUngroup').click();
      await expect(dialog(page).locator('#ui2GrpConfirmTitle')).toHaveText(`Dissolve the group ${LR.friendlyName}?`);
      await expect(dialog(page).locator('#ui2GrpConfirmBody')).toHaveText(`${LR.friendlyName}, ${BR.friendlyName} stop playing together and play on their own again.`);
      await dialog(page).getByRole('button', { name: `Dissolve the group ${LR.friendlyName}` }).click();
      await mock.expectCalled('DissolveZone', { pred: onHost(LR.host), times: 1 });
      await expect(page.locator('#ui2GrpStatus-now')).toHaveText(`Sent. Waiting for ${LR.friendlyName}, ${BR.friendlyName} to confirm.`);
      await setZone(page, LR.host, {});
      await setZone(page, BR.host, {});
      await expect(page.locator('#ui2GrpStatus-now')).toHaveText(`The group ${LR.friendlyName} is dissolved. ${LR.friendlyName}, ${BR.friendlyName} play on their own again.`, POLL);
    });
  });

  test('11. the 5 s repaint keeps the keyboard focus where it was', async ({ app }) => {
    const { page } = app;
    await openGroups(page);
    const change = page.getByRole('button', { name: 'Change Terrace' });
    await change.focus();
    // Mark the page the user is on; the repaint builds a new one.
    await page.locator('#view-multiroom > .ui2-grp').evaluate((el) => { el.dataset.e2eOld = '1'; });
    await expect(page.locator('#view-multiroom > .ui2-grp[data-e2e-old]')).toHaveCount(0, POLL);
    await expect(page.getByRole('button', { name: 'Change Terrace' })).toBeFocused();
  });

  for (const scale of ['1', '3']) {
    test.describe(`text size ${scale} at the smallest window`, () => {
      test.use({ scale, viewport: { width: 1000, height: 600 } });

      test(`12. scale ${scale}: no sideways scroll, every page target at least 40 px high`, async ({ app }) => {
        const { page } = app;
        await openGroups(page);
        await page.addStyleTag({ content: KILL_ANIM });
        await noHorizontalOverflow(page);
        const main = await page.locator('#ui2Main').evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }));
        expect(main.scroll).toBeLessThanOrEqual(main.client + 1);
        const small = await page.locator('#view-multiroom .ui2-grp').evaluate((root) => [...root.querySelectorAll('button, select, input[type="text"]')]
          .filter((e) => e.offsetParent !== null)
          .map((e) => ({ id: e.id || e.className, h: e.getBoundingClientRect().height }))
          .filter((x) => x.h < 39.5));
        expect(small).toEqual([]);
      });
    });
  }

  for (const theme of ['dark', 'light', 'contrast']) {
    test.describe(`${theme} theme`, () => {
      test.use({ theme });

      test(`13. ${theme}: the Groups page against its axe baseline`, async ({ app }) => {
        const { page } = app;
        await openGroups(page);
        await page.addStyleTag({ content: KILL_ANIM });
        await scan(page, `${theme}|ui2|groups`, BASELINE, { include: '#view-multiroom' });
        await undoBtn(page).click();
        await expect(dialog(page)).toBeVisible();
        await scan(page, `${theme}|ui2|groups-confirm`, BASELINE, { include: '#ui2GrpConfirm' });
      });
    });
  }
});

test.describe('classic layout, phase 3 network', () => {
  test.use({ ui2: false, scenario: speakersScenario() });

  test('14. Multi-room is unchanged: no Groups page, the frame x undoes the pair at once', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="multiroom"]').click();
    await expect(page.locator('#view-multiroom #zoneCreate')).toBeVisible();
    await expect(page.locator('#view-multiroom .ui2-grp, #ui2GrpConfirm')).toHaveCount(0);
    await expect(page.locator('#view-multiroom > .zone-intro')).toBeVisible();
    await expect(page.locator('#zoneCreate')).toHaveText('Create group');
    // The pair picker opens on the live pair, so its button changes that pair.
    await expect(page.locator('#stereoCreate')).toHaveText('Change stereo pair');
    await expect(page.locator('#view-multiroom .zone-card[role="button"]').first()).toBeVisible();
    await page.locator('#view-multiroom .box-group-x[data-dissolve-kind="pair"]').click();
    await mock.expectCalled('DissolveStereoPair');
    await expect(page.locator('#ui2GrpConfirm')).toHaveCount(0);
  });
});
