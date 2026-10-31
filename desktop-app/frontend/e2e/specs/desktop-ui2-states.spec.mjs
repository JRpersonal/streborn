// The UI 2.0 states (phase 5, src/shell2/state.js): no preview view is a dead
// end. Without speakers every place that needs one (the sidebar, the player,
// Radio, the Speakers list, the one-speaker page, Recently played, Spotify)
// says so in words and offers the next step; a failed search says it with its
// detail folded and Search again; every speaker offline is said above the
// rows; none chosen leads to the speaker list; Groups with one STR speaker
// says it needs two; the wizard's Find says what Search again found. The
// classic layout keeps its own screens.
//
//   E2E_PORT=4181 npx playwright test specs/desktop-ui2-states.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { KILL_ANIM } from '../helpers/a11y.mjs';
import { noHorizontalOverflow } from '../helpers/layout.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);
const acts = (loc) => loc.locator('[data-state-act]').evaluateAll((els) => els.map((e) => e.dataset.stateAct));
const NONE = { boxes: [], current: null, seed: { cachedBoxes: false } };

test.describe('UI 2.0 states without speakers', () => {
  test.use({ ui2: true, scenario: NONE });

  test('1. every place that needs a speaker says so and offers the next step', async ({ app }) => {
    const { page } = app;
    // The sidebar: none found (once the search is done), three real buttons.
    const side = page.locator('#ui2SpkLists .u-state');
    await expect(side).toHaveAttribute('data-phase', 'none');
    await expect(side.locator('.u-state-title')).toHaveText(en('ui2.side.empty'));
    await expect(side.locator('.u-state-title')).toHaveAttribute('tabindex', '-1');
    await expect(side.locator('.u-state-word')).toHaveText(en('ui2.state.word.none'));
    expect(await acts(side)).toEqual(['search', 'add', 'connect']);
    await expect(page.locator('#ui2SpkLists .ui2-spk-row')).toHaveCount(0);

    // The player: no controls, the words and Add speaker.
    const player = page.locator('#ui2Player');
    await expect(player.locator('.ui2-player-empty')).toBeVisible();
    await expect(player.locator('.ui2-player-state-text')).toHaveText(en('ui2.state.player.none'));
    expect(await acts(player.locator('.ui2-player-empty'))).toEqual(['add']);
    await expect(player.locator('.ui2-player-transport')).toBeHidden();

    // Radio: the slot under the title, the classic hint hidden.
    await expect(page.locator('#ui2RadioState .u-state')).toBeVisible();
    await expect(page.locator('#ui2RadioState .u-state')).toHaveAttribute('data-phase', 'none');
    await expect(page.locator('#view-box #boxHint')).toBeHidden();

    // The Speakers list: Search again, Connect by address, Add speaker.
    await nav(page, 'speakers').click();
    const list = page.locator('#view-speakers .u-state');
    await expect(list).toBeVisible();
    expect(await acts(list)).toEqual(['search', 'connect', 'add']);
    await expect(page.locator('#view-speakers .spl-cannot-see .u-connect-label')).toHaveText(en('ui2.state.addressLabel'));
    // One address field on the page: the card's Connect by address leads to it.
    await expect(page.locator('#view-speakers .u-connect')).toHaveCount(1);
    await list.locator('[data-state-act="connect"]').click();
    await expect(page.locator('#view-speakers .spl-cannot-see .u-connect-input')).toBeFocused();

    // The one-speaker page without a speaker (the classic tab, still in the
    // page, opens it): the state replaces the classic panel.
    await page.evaluate(() => document.querySelector('.tab-btn[data-view="settings"]').click());
    const pageState = page.locator('#ui2SpkPageNone .u-state');
    await expect(pageState).toBeVisible();
    expect(await acts(pageState)).toEqual(['search', 'connect', 'add']);
    await expect(page.locator('#settingsGoSetup')).toBeHidden();

    // Recently played.
    await nav(page, 'recent').click();
    const recent = page.locator('#recentList .u-state');
    await expect(recent).toBeVisible();
    expect(await acts(recent)).toEqual(['search', 'add', 'connect']);

    // Spotify: both cards, each with Add speaker.
    await nav(page, 'spotify').click();
    await expect(page.locator('#sp2Keys .u-state')).toBeVisible();
    expect(await acts(page.locator('#sp2Keys .u-state'))).toEqual(['add']);
    await expect(page.locator('#sp2Accounts .u-state')).toBeVisible();
    expect(await acts(page.locator('#sp2Accounts .u-state'))).toEqual(['add']);

    // Add speaker opens the setup wizard.
    await page.locator('#sp2Keys [data-state-act="add"]').click();
    await expect(page.locator('#ui2SetupWizard')).toBeVisible();
  });

});

// On the page clock: the busy state lasts exactly as long as the search is
// held open, however loaded the machine running the test is.
test.describe('UI 2.0 states without speakers, Search again', () => {
  test.use({ ui2: true, fakeClock: true, scenario: NONE });

  test('2. Search again says it is searching, then what it found', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    const side = page.locator('#ui2SpkLists .u-state');
    await expect(side).toHaveAttribute('data-phase', 'none');
    await mock.resetCalls();
    await mock.set('DiscoverBoxes', { value: [], delayMs: 600 });
    await side.locator('[data-state-act="search"]').click();
    const busy = page.locator('#ui2SpkLists [data-state-act="search"]');
    await expect(busy).toHaveAttribute('aria-busy', 'true');
    await expect(busy).toHaveText(en('ui2.state.searching'));
    await runFor(page, 1_000);
    await expect(page.locator('#ui2StateLive')).toHaveText(en('ui2.state.foundNone'));
    await expect(page.locator('#ui2SpkLists [data-state-act="search"]')).toHaveText(en('ui2.state.searchAgain'));
    await expect(page.locator('#ui2SpkLists [data-state-act="search"]')).not.toHaveAttribute('aria-busy', 'true');
    await mock.expectCalled('DiscoverBoxes');
  });
});

test.describe('UI 2.0 states: the search fails', () => {
  test.use({ ui2: true, scenario: { ...NONE, bindings: { DiscoverBoxes: { error: 'mdns: socket closed' } } } });

  test('3. a failed search says so, folds the detail and offers Search again', async ({ app, mock }) => {
    const { page } = app;
    const side = page.locator('#ui2SpkLists .u-state');
    await expect(side).toHaveAttribute('data-phase', 'failed');
    await expect(side).toHaveAttribute('role', 'alert');
    await expect(side.locator('.u-state-title')).toHaveText(en('ui2.state.failedTitle'));
    await expect(side.locator('.u-state-error')).toBeHidden();
    await side.locator('.u-state-detail summary').click();
    await expect(side.locator('.u-state-error')).toContainText('mdns: socket closed');
    expect(await acts(side)).toEqual(['search', 'connect']);
    await mock.resetCalls();
    await side.locator('[data-state-act="search"]').click();
    await mock.expectCalled('DiscoverBoxes');
    await expect(page.locator('#ui2StateLive')).toHaveText(en('ui2.state.searchFailed'));
    await expect(page.locator('#errorModal')).toBeHidden();
  });
});

test.describe('UI 2.0 states: every speaker offline', () => {
  test.use({
    ui2: true,
    scenario: { boxes: ['bedroom', 'office'], current: null, boxPatches: { bedroom: { offline: true, offlineSinceSec: 300 } } },
  });

  test('4. the summary sits above the rows, which stay', async ({ app }) => {
    const { page } = app;
    const summary = page.locator('#ui2SpkLists .ui2-spk-summary .u-state');
    await expect(summary).toHaveAttribute('data-phase', 'allOffline');
    await expect(summary.locator('.u-state-title')).toHaveText(en('ui2.state.allOfflineTitle'));
    await expect(page.locator('#ui2SpkLists .ui2-spk-row')).toHaveCount(2);
    const above = await page.evaluate(() => {
      const s = document.querySelector('#ui2SpkLists .ui2-spk-summary');
      const r = document.querySelector('#ui2SpkLists .ui2-spk-row');
      return !!(s.compareDocumentPosition(r) & Node.DOCUMENT_POSITION_FOLLOWING);
    });
    expect(above).toBe(true);
    // The Speakers list says the same above its cards.
    await nav(page, 'speakers').click();
    await expect(page.locator('#view-speakers .u-state[data-phase="allOffline"]')).toBeVisible();
    await expect(page.locator('#view-speakers .spl-item')).toHaveCount(2);
  });
});

test.describe('UI 2.0 states: none chosen', () => {
  test.use({ ui2: true, scenario: { current: null } });

  test('5. the player leads to the speaker list and the keyboard lands on a row', async ({ app }) => {
    const { page } = app;
    const empty = page.locator('#ui2Player .ui2-player-empty');
    await expect(empty.locator('.ui2-player-state-text')).toHaveText(en('ui2.state.noneChosenBody'));
    await expect(empty).not.toContainText(/\b(left|above)\b/i);
    const go = empty.locator('[data-state-act="list"]');
    await expect(go).toHaveText(en('ui2.state.goToList'));
    await go.click();
    const focused = page.locator('#ui2SpkLists .ui2-spk-pick:focus');
    await expect(focused).toHaveCount(1);
    await expect(focused).toHaveAttribute('tabindex', '0');
    // Radio says it too, with the same way.
    await expect(page.locator('#ui2RadioState .u-state[data-phase="noneChosen"] [data-state-act="list"]')).toBeVisible();
  });

  test('6. the one-speaker page names both ways', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#ui2SpkLists .ui2-spk-row').first()).toBeVisible();
    await page.evaluate(() => document.querySelector('.tab-btn[data-view="settings"]').click());
    const st = page.locator('#ui2SpkPageNone .u-state');
    await expect(st).toContainText(en('ui2.state.pageChoose'));
    expect(await acts(st)).toEqual(['allSpeakers', 'list']);
    await st.locator('[data-state-act="allSpeakers"]').click();
    await expect(page.locator('#ui2MainInner > #view-speakers')).toBeVisible();
  });
});

test.describe('UI 2.0 states: one STR speaker', () => {
  test.use({ ui2: true, scenario: { boxes: ['livingRoom', 'kitchen'] } });

  test('7. Groups says it needs two speakers and offers Add speaker', async ({ app }) => {
    const { page } = app;
    await nav(page, 'speakers').click();
    await page.locator('#ui2SpkSubnav .spa-sub[data-route="groups"]').click();
    const notes = page.locator('#view-multiroom .ui2-grp-two');
    await expect(notes).toHaveCount(2);
    await expect(notes.first()).toContainText(en('ui2.state.groupsNeedTwo'));
    await expect(page.locator('#ui2GrpPairEditor')).toBeHidden();
    await expect(page.locator('#ui2GrpFixedEditor')).toBeHidden();
    await expect(page.locator('#view-multiroom [data-state-act="add"]')).toHaveCount(1);
    await page.locator('#view-multiroom [data-state-act="add"]').click();
    await expect(page.locator('#ui2SetupWizard')).toBeVisible();
  });
});

test.describe('UI 2.0 states: the wizard Find', () => {
  test.use({ ui2: true, fakeClock: true, scenario: { boxes: ['livingRoom'] } });

  test('8. Search again is busy while it runs, then says what it found', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 3_000);
    await nav(page, 'speakers').click();
    await page.locator('#ui2SpkSubnav .spa-sub[data-route="add"]').click();
    await expect(page.locator('#ui2SetupWizard')).toBeVisible();
    await expect(page.locator('#w2FindList .w2-empty')).toContainText(en('ui2.setup.wizard.find.empty'));
    await mock.set('DiscoverBoxes', { value: [LR], delayMs: 2_000 });
    const search = page.locator('#w2Foot .w2-act[data-act="search"]');
    await search.click();
    await expect(search).toHaveAttribute('aria-busy', 'true');
    await expect(search).toHaveText(en('ui2.state.searching'));
    await settle(page, 3_000);
    const result = page.locator('#w2SearchResult');
    await expect(result).toHaveAttribute('role', 'status');
    await expect(result).toHaveText(en('ui2.state.wizardNone'));
    await expect(page.locator('#w2Foot .w2-act[data-act="search"]')).toHaveText(en('ui2.setup.wizard.searchAgain'));
  });
});

test.describe('UI 2.0 states: the toast', () => {
  test.use({ ui2: true });

  test('9. is a polite status in the preview', async ({ app }) => {
    const toast = app.page.locator('#toast');
    await expect(toast).toHaveAttribute('role', 'status');
    await expect(toast).toHaveAttribute('aria-live', 'polite');
  });
});

test.describe('the classic layout without speakers', () => {
  test.use({ ready: '#view-box', scenario: NONE });

  test('10. keeps its own empty card, its toast and no preview state', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#emptyIpInput')).toBeVisible();
    await expect(page.locator('.u-state')).toHaveCount(0);
    await expect(page.locator('.u-connect')).toHaveCount(0);
    await expect(page.locator('#ui2StateLive')).toHaveCount(0);
    await expect(page.locator('#toast')).not.toHaveAttribute('role', /./);
  });
});

// The sidebar rows say what happened to them (phase 5): an offline pick, and
// the "+" / "-" of a group edit, which stays pending until the zone poll
// confirms it and reports a failure in its own row with Try again.
test.describe('UI 2.0 states: the sidebar rows', () => {
  test.use({ ui2: true, fakeClock: true });

  const BR = ROSTER.bedroom;
  const OFFICE = ROSTER.office;
  const row = (page, host) => page.locator('#ui2SpkLists .ui2-spk-row')
    .filter({ has: page.locator(`.ui2-spk-pick[data-host="${host}"]`) });

  test('11. an offline pick notes it in the row, then What to try, and no toast', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await mock.resetCalls();
    await mock.set('DiscoverBoxes', { delayMs: 1_500 });
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${OFFICE.host}"]`).click();
    const note = row(page, OFFICE.host).locator('.ui2-spk-rownote');
    await expect(note).toHaveText(en('ui2.state.offlinePick', { name: 'Office', ago: '10 minutes ago' }));
    // The live region speaks a moment later (it clears first).
    await settle(page, 200);
    await expect(page.locator('#ui2SpkLive')).toHaveText(en('ui2.state.offlinePick', { name: 'Office', ago: '10 minutes ago' }));
    await expect(page.locator('#toast')).not.toHaveClass(/\bshow\b/);
    await settle(page, 2_000);
    await mock.expectCalled('DiscoverBoxes', { times: 1 });
    await expect(note).toHaveText(en('ui2.state.stillOffline'));
    const what = row(page, OFFICE.host).locator('.ui2-spk-note-act[data-note-act="whatToTry"]');
    await expect(what).toHaveText(en('ui2.state.whatToTry'));
    await expect(what).toHaveAttribute('aria-label', en('ui2.state.whatToTryLabel', { name: 'Office' }));
    await what.click();
    await settle(page, 500);
    await expect(page.locator('#ui2MainInner > #view-speakers')).toBeVisible();
    await expect(page.locator(`#view-speakers .spl-item[data-host="${OFFICE.host}"] .spl-act[data-act="search"]`)).toBeFocused();
    await expect(page.locator('#toast')).not.toHaveClass(/\bshow\b/);
  });

  test('11b. an offline pick whose speaker answers the search again says it is back, and the note goes', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    const back = { ...OFFICE, offline: false, offlineSinceSec: 0 };
    await mock.set('DiscoverBoxes', { value: [ROSTER.livingRoom, BR, back, ROSTER.kitchen], delayMs: 1_500 });
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${OFFICE.host}"]`).click();
    const note = row(page, OFFICE.host).locator('.ui2-spk-rownote');
    await expect(note).toHaveText(en('ui2.state.offlinePick', { name: 'Office', ago: '10 minutes ago' }));
    await settle(page, 2_000);
    await expect(row(page, OFFICE.host).locator('.ui2-spk-rownote')).toHaveCount(0);
    await settle(page, 200);
    await expect(page.locator('#ui2SpkLive')).toHaveText(en('ui2.state.backOnline', { name: 'Office' }));
    await expect(page.locator('#toast')).not.toHaveClass(/\bshow\b/);
  });

  test('12. a "+" that fails says so in its row, and Try again repeats it for the same group', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 30_000);
    await mock.set('FormZone', { error: 'zone refused' });
    const add = row(page, BR.host).locator('.ui2-spk-act[data-act="add"]');
    await expect(add).toHaveAttribute('data-master', ROSTER.livingRoom.host);
    await mock.resetCalls();
    await add.click();
    await runFor(page, 1_500);
    await mock.expectCalled('FormZone', { times: 1 });
    const note = row(page, BR.host).locator('.ui2-spk-rownote');
    await expect(note).toHaveAttribute('role', 'alert');
    await expect(note).toContainText('zone refused');
    await expect(page.locator('#errorModal')).toBeHidden();
    const again = row(page, BR.host).locator('.ui2-spk-note-act[data-note-act="retry"]');
    await expect(again).toHaveAttribute('data-master', ROSTER.livingRoom.host);
    await expect(again).toHaveAttribute('aria-label', en('ui2.state.tryAgainLabel', { name: 'Bedroom' }));
    await mock.set('FormZone', { value: { ok: true } });
    await again.click();
    await runFor(page, 1_500);
    await mock.expectCalled('FormZone', { times: 2 });
    const second = await mock.last('FormZone');
    expect(second.args[0]).toBe(ROSTER.livingRoom.host);
    expect(JSON.stringify(second.args)).toContain(BR.host);
  });

  test('13. a "+" stays pending until the zone poll shows it, then plays along is announced', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 30_000);
    const add = row(page, BR.host).locator('.ui2-spk-act[data-act="add"]');
    await mock.resetCalls();
    await add.click();
    await runFor(page, 1_000);
    await mock.expectCalled('FormZone', { times: 1 });
    // Accepted, not yet confirmed: no toast, still pending.
    await expect(page.locator('#toast')).not.toHaveClass(/\bshow\b/);
    await expect(row(page, BR.host).locator('.ui2-spk-act')).toHaveAttribute('aria-busy', 'true');
    // The speakers now report the group.
    const group = {
      master: ROSTER.livingRoom.deviceID,
      senderIP: ROSTER.livingRoom.host,
      members: [{ deviceID: ROSTER.livingRoom.deviceID, ip: ROSTER.livingRoom.host }, { deviceID: BR.deviceID, ip: BR.host }],
    };
    await mock.set('GetZoneState', { value: group });
    await settle(page, 12_000);
    await expect(page.locator('#ui2SpkLive')).toHaveText(en('ui2.state.playsAlong', { name: 'Bedroom' }));
    await expect(row(page, BR.host).locator('.ui2-spk-rownote')).toHaveCount(0);
  });

  test('13b. a "+" the zone poll cannot read back is never announced; after the deadline it says so with Try again', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 30_000);
    // The speakers stop answering the zone read: only group.js's optimistic
    // entry (kept by the poll for speakers that miss a round) shows the join.
    await mock.set('GetZoneState', { error: 'timeout' });
    const add = row(page, BR.host).locator('.ui2-spk-act[data-act="add"]');
    await add.click();
    await runFor(page, 1_000);
    await mock.expectCalled('FormZone', { times: 1 });
    await settle(page, 5_000);
    await expect(page.locator('#ui2SpkLive')).not.toHaveText(en('ui2.state.playsAlong', { name: 'Bedroom' }));
    await expect(row(page, BR.host).locator('.ui2-spk-act')).toHaveAttribute('aria-busy', 'true');
    await settle(page, 16_000);
    const note = row(page, BR.host).locator('.ui2-spk-rownote');
    await expect(note).toHaveAttribute('role', 'alert');
    await expect(note).toHaveText(en('ui2.state.groupNoConfirm', { name: 'Bedroom' }));
    await expect(row(page, BR.host).locator('.ui2-spk-note-act[data-note-act="retry"]')).toBeVisible();
    await expect(page.locator('#ui2SpkLive')).not.toHaveText(en('ui2.state.playsAlong', { name: 'Bedroom' }));
  });
});

// A dissolve the master did not finish is a warning, not an error with a Try
// again that would toggle the speakers once more: each waits for the zone
// poll, and one that left is said to be out.
test.describe('UI 2.0 states: a group dissolve the master does not finish', () => {
  const BR = ROSTER.bedroom;
  const LR = ROSTER.livingRoom;
  const group = {
    master: LR.deviceID,
    senderIP: LR.host,
    members: [{ deviceID: LR.deviceID, ip: LR.host }, { deviceID: BR.deviceID, ip: BR.host }],
  };
  test.use({
    ui2: true,
    fakeClock: true,
    // Living Room leads, Bedroom follows; every other speaker plays alone.
    scenario: {
      bindings: {
        GetZoneState: { byHost: { [LR.host]: { value: group }, [BR.host]: { value: group } }, value: {} },
        DissolveZone: { value: { ok: false, remaining: 1 } },
      },
    },
  });
  const row = (page, host) => page.locator('#ui2SpkLists .ui2-spk-row')
    .filter({ has: page.locator(`.ui2-spk-pick[data-host="${host}"]`) });

  test('15. warns in the row without Try again, then says out once the poll shows it', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 30_000);
    const remove = row(page, BR.host).locator('.ui2-spk-act[data-act="remove"]');
    await expect(remove).toBeVisible();
    await remove.click();
    await runFor(page, 1_500);
    await mock.expectCalled('DissolveZone', { times: 1 });
    const note = row(page, BR.host).locator('.ui2-spk-rownote');
    await expect(note).toHaveText(en('multiroom.dissolveIncompleteN', { n: 1 }));
    await expect(note).toHaveAttribute('data-tone', 'warn');
    await expect(row(page, BR.host).locator('.ui2-spk-note-act')).toHaveCount(0);
    await expect(page.locator('#errorModal')).toBeHidden();
    // Bedroom did leave after all: the next round says so.
    await mock.set('GetZoneState', { value: {} });
    await settle(page, 16_000);
    await expect(page.locator('#ui2SpkLive')).toHaveText(en('ui2.state.isOut', { name: 'Bedroom' }));
    await expect(row(page, BR.host).locator('.ui2-spk-rownote')).toHaveCount(0);
  });
});

// The sidebar's state card at Extra large text in the smallest window: the
// speaker section's floor (mount.js holdSpeakerFloor) keeps the navigation on
// screen, the card scrolls inside the section, nothing is drawn over anything.
test.describe('UI 2.0 states at Extra large in 1000 x 600', () => {
  test.use({ ui2: true, scale: '3', viewport: { width: 1000, height: 600 }, scenario: NONE });

  test('14. the empty sidebar card leaves the navigation reachable and scrolls on its own', async ({ app }) => {
    const { page } = app;
    await page.addStyleTag({ content: KILL_ANIM });
    const card = page.locator('#ui2SpkLists .u-state');
    await expect(card).toHaveAttribute('data-phase', 'none');
    await expect(card.locator('.u-state-title')).toBeVisible();
    const geo = await page.evaluate(() => {
      const r = (el) => el.getBoundingClientRect();
      const side = document.querySelector('#ui2Sidebar');
      const section = document.querySelector('#ui2Speakers');
      const nav = document.querySelector('.ui2-side-scroll .ui2-nav-item');
      const navScroll = document.querySelector('.ui2-side-scroll');
      return {
        sideBottom: r(side).bottom,
        sectionBottom: r(section).bottom,
        navTop: r(nav).top,
        navBottom: r(nav).bottom,
        navScrollTop: r(navScroll).top,
        navScrollBottom: r(navScroll).bottom,
        vh: window.innerHeight,
      };
    });
    // The section ends where the navigation starts, the first entry shows.
    expect(geo.sectionBottom).toBeLessThanOrEqual(geo.navScrollTop + 1);
    expect(geo.navScrollBottom - geo.navScrollTop).toBeGreaterThan(20);
    expect(geo.navTop).toBeLessThan(geo.vh);
    // Every button of the card can be reached: scrolled into view, it shows.
    for (const act of ['search', 'add', 'connect']) {
      const b = card.locator(`[data-state-act="${act}"]`);
      await b.scrollIntoViewIfNeeded();
      await expect(b, act).toBeInViewport();
    }
    await expect(page.locator('#ui2Sidebar .ui2-nav-item[data-view="box"]')).toBeVisible();
    await noHorizontalOverflow(page);
  });
});
