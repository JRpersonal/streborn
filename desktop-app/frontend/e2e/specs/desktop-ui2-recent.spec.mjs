// Recently played in the UI 2.0 preview (#1286, docs/UI-2.0.md phase 2): the
// cards grouped by day in local time, every button naming the station and the
// speaker it acts on, errors in the card, the empty state, the history
// following a speaker picked in the sidebar, and the current layout left as it
// was. Accessibility scans of the view in the three themes run against
// baselines/a11y-ui2-recent.json.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-recent.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle } from '../helpers/clock.mjs';
import { en, bundle } from '../helpers/i18n.mjs';
import { KILL_ANIM, scan } from '../helpers/a11y.mjs';
import { minSize, noHorizontalOverflow } from '../helpers/layout.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const BASELINE = 'baselines/a11y-ui2-recent.json';

// The fake clock starts at 2026-10-23T18:00:00Z, 20:00 in Berlin (the suite's
// zone, UTC+2 until the 25th). So 22:30 UTC on the 22nd is 00:30 today, and
// 21:30 UTC on the 22nd is 23:30 yesterday: the split is local midnight.
const radio = (ts, key, name, track = '') => ({
  ts, source: 'radio', cardKey: key, cardName: name, cardArt: '', cardURL: `http://example.com/${key}`, track,
});
const HISTORY = [
  radio('2026-10-23T17:40:00Z', 'rock', 'Best Of Rock.FM', 'The Demo Band - Morning Light'),
  radio('2026-10-22T22:30:00Z', 'night', 'Night Owl Radio', 'Sample Trio - After Hours'),
  radio('2026-10-22T21:30:00Z', 'jazz', 'Jazz24', 'Quartet Example - Blue Hour'),
  {
    ts: '2026-10-20T16:20:00Z', source: 'upnp', cardKey: 'u:album', cardName: 'Evening Sessions', cardArt: '',
    cardURL: 'http://192.0.2.60:8200/MediaItems/1.flac', mime: 'audio/flac', track: 'Lorem Ipsum Ensemble - Opening',
  },
  radio('2026-10-19T08:05:00Z', 'news', 'Example News', ''),
];
// The agent answers its ring oldest first; the view sorts.
const RING = [...HISTORY].reverse();

const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);
const day = (page, label) => page.locator('#recentList .rc-day').filter({ has: page.locator('.rc-day-head', { hasText: label }) });
const card = (page, name) => page.locator('#recentList .recent-card').filter({ has: page.locator('.rc-name', { hasText: name }) });

async function openRecent(page) {
  await settle(page, 9_000);
  await nav(page, 'recent').click();
  await expect(page.locator('#ui2MainInner > #view-recent')).toBeVisible();
}

test.describe('UI 2.0 Recently played', () => {
  test.use({ ui2: true, fakeClock: true, scenario: { bindings: { RecentPlayed: { value: RING } } } });

  test('1. groups the cards under Today, Yesterday and the date, split at local midnight', async ({ app }) => {
    const { page } = app;
    await openRecent(page);
    const heads = page.locator('#recentList .rc-day-head');
    await expect(heads).toHaveText([
      en('ui2.recent.today'),
      en('ui2.recent.yesterday'),
      'Tuesday, October 20',
      'Monday, October 19',
    ]);
    await expect(day(page, en('ui2.recent.today')).locator('.rc-name')).toHaveText([/^Best Of Rock\.FM/, 'Night Owl Radio']);
    await expect(day(page, en('ui2.recent.yesterday')).locator('.rc-name')).toHaveText(['Jazz24']);
    await expect(day(page, 'Tuesday, October 20').locator('.rc-name')).toHaveText(['Evening Sessions']);
    // Each day is a section named by its heading, for a screen reader's list
    // of regions and headings.
    const today = day(page, en('ui2.recent.today'));
    await expect(today).toHaveAttribute('aria-labelledby', await today.locator('.rc-day-head').getAttribute('id'));
    // The songs heard stay on the card, with their local time.
    await expect(card(page, 'Night Owl Radio').locator('.rc-tr-time')).toHaveText(/^(00|12):30/);
  });

  test('2. every button names the station and the speaker it acts on, and is a 40px target', async ({ app }) => {
    const { page } = app;
    await openRecent(page);
    const rock = card(page, 'Best Of Rock.FM');
    await expect(rock.locator('.rc-play')).toHaveAccessibleName(en('ui2.recent.playOn', { name: 'Best Of Rock.FM', speaker: LR.friendlyName }));
    await expect(rock.locator('.rc-pick')).toHaveAccessibleName(en('ui2.radio.putOnKeyName', { name: 'Best Of Rock.FM', speaker: LR.friendlyName }));
    await expect(rock.locator('.rc-pick')).toContainText(en('ui2.radio.putOnKey'));
    await expect(rock.locator('.rc-fav')).toHaveAccessibleName(en('ui2.recent.favAdd', { name: 'Best Of Rock.FM' }));
    await expect(rock.locator('.rc-del')).toHaveAccessibleName(en('ui2.recent.removeFrom', { name: 'Best Of Rock.FM', speakers: LR.friendlyName }));
    await page.addStyleTag({ content: KILL_ANIM });
    await minSize(page.locator('#view-recent .rc-actions button'));
    await minSize(page.locator('#recentClear'));
  });

  test('3. play again plays the station on the speaker it names', async ({ app, mock }) => {
    const { page } = app;
    await openRecent(page);
    await mock.resetCalls();
    await card(page, 'Jazz24').locator('.rc-play').click();
    await mock.expectCalled('PlayURL', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 'http://example.com/jazz', times: 1 });
  });

  test('4. the star says whether it is on and keeps the station a favorite', async ({ app }) => {
    const { page } = app;
    await openRecent(page);
    const star = card(page, 'Jazz24').locator('.rc-fav');
    await expect(star).toHaveAttribute('aria-pressed', 'false');
    await star.click();
    await expect(star).toHaveAttribute('aria-pressed', 'true');
    await expect(star).toHaveAccessibleName(en('ui2.recent.favRemove', { name: 'Jazz24' }));
  });

  test('5. Tab reaches the scope, Clear list and every card button in reading order with a visible ring', async ({ app }) => {
    const { page } = app;
    await openRecent(page);
    await page.addStyleTag({ content: KILL_ANIM });
    const play = card(page, 'Best Of Rock.FM').locator('.rc-play');
    await page.locator('#recentClear').focus();
    let reached = false;
    for (let i = 0; i < 6 && !reached; i++) {
      await page.keyboard.press('Tab');
      reached = await play.evaluate((el) => el === document.activeElement);
    }
    expect(reached, 'Tab from Clear list reaches the first card\'s play button').toBe(true);
    const ring = await play.evaluate((el) => getComputedStyle(el).outlineStyle);
    expect(ring).toBe('solid');
    await page.keyboard.press('Tab');
    await expect(card(page, 'Best Of Rock.FM').locator('.rc-pick')).toBeFocused();
  });

  test.describe('a failed remove', () => {
    test.use({ scenario: { bindings: { RecentPlayed: { value: RING }, DeleteRecentCard: { error: 'speaker did not answer' } } } });

    test('6. says so in its own card, not in a window-wide dialog', async ({ app }) => {
      const { page } = app;
      await openRecent(page);
      const jazz = card(page, 'Jazz24');
      await jazz.locator('.rc-del').click();
      await expect(jazz.locator('.rc-err')).toContainText('speaker did not answer');
      await expect(jazz.locator('.rc-err')).toHaveAttribute('role', 'alert');
      await expect(page.locator('#errorModal')).toBeHidden();
      await expect(card(page, 'Best Of Rock.FM').locator('.rc-err')).toBeEmpty();
      await expect(jazz.locator('.rc-del')).toBeEnabled();
    });
  });

  test.describe('a failed clear', () => {
    test.use({ scenario: { bindings: { RecentPlayed: { value: RING }, ClearRecent: { error: 'offline' } } } });

    test('7. names the speaker whose list stayed', async ({ app }) => {
      const { page } = app;
      await openRecent(page);
      await page.locator('#recentClear').click();
      await page.locator('#warnModal #warnConfirm').click();
      await expect(page.locator('#recentClearErr')).toHaveText(en('ui2.recent.clearFailed', { speakers: LR.friendlyName }));
    });
  });

  test.describe('nothing played yet', () => {
    test.use({ scenario: { bindings: { RecentPlayed: { value: [] } } } });

    test('8. explains how the list fills, for the speaker shown', async ({ app }) => {
      const { page } = app;
      await openRecent(page);
      const empty = page.locator('#recentList .rc-empty2');
      await expect(empty.locator('.rc-empty-title')).toHaveText(en('ui2.recent.emptyTitle'));
      await expect(empty.locator('.rc-empty-body')).toHaveText(en('ui2.recent.emptyOne', { speaker: LR.friendlyName }));
      await page.locator('#recentScopeAll').click();
      await expect(page.locator('#recentList .rc-empty-body')).toHaveText(en('ui2.recent.emptyAll'));
    });
  });

  test('9. follows a speaker picked in the sidebar and names it on the buttons', async ({ app, mock }) => {
    const { page } = app;
    await openRecent(page);
    await mock.resetCalls();
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR.host}"]`).click();
    await mock.expectCalled('RecentPlayed', { pred: onHost(BR.host) });
    await expect(card(page, 'Best Of Rock.FM').locator('.rc-pick'))
      .toHaveAccessibleName(en('ui2.radio.putOnKeyName', { name: 'Best Of Rock.FM', speaker: BR.friendlyName }));
    await expect(card(page, 'Best Of Rock.FM').locator('.rc-del'))
      .toHaveAccessibleName(en('ui2.recent.removeFrom', { name: 'Best Of Rock.FM', speakers: BR.friendlyName }));
  });

  test('14. removing a card by keyboard puts the focus on the card that took its place', async ({ app, mock }) => {
    const { page } = app;
    await openRecent(page);
    const del = card(page, 'Jazz24').locator('.rc-del');
    await del.focus();
    // The speaker's history without the card, as the next read returns it.
    await mock.set('RecentPlayed', { value: RING.filter((e) => e.cardKey !== 'jazz') });
    await page.keyboard.press('Enter');
    await mock.expectCalled('DeleteRecentCard', { times: 1 });
    await expect(card(page, 'Jazz24')).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => {
      const c = document.activeElement && document.activeElement.closest('.recent-card');
      return c ? c.querySelector('.rc-name').textContent : (document.activeElement && document.activeElement.tagName);
    })).toBe('Evening Sessions');
  });

  test('16. the scope is a filter in the header: the sidebar speaker, or every speaker', async ({ app, mock }) => {
    const { page } = app;
    await openRecent(page);
    const one = page.locator('#recentScopeOne');
    const all = page.locator('#recentScopeAll');
    await expect(page.locator('#recentBoxSelect')).toHaveCount(0);
    await expect(page.locator('.rc-scope2')).toHaveAccessibleName(en('ui2.recent.scopeLabel'));
    await expect(one).toHaveText(LR.friendlyName);
    await expect(one).toHaveAttribute('aria-pressed', 'true');
    await expect(all).toHaveText(en('recent.allBoxes'));
    await expect(all).toHaveAttribute('aria-pressed', 'false');
    await mock.resetCalls();
    await all.click();
    await expect(page.locator('#recentScopeAll')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('#recentScopeAll')).toBeFocused();
    await mock.expectCalled('RecentPlayed', { pred: onHost(BR.host) });
    // Back to one speaker: the one picked in the sidebar.
    await page.locator('#recentScopeOne').click();
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR.host}"]`).click();
    await expect(page.locator('#recentScopeOne')).toHaveText(BR.friendlyName);
    await expect(page.locator('#recentScopeOne')).toHaveAttribute('aria-pressed', 'true');
  });

  test.describe('a slow play that fails', () => {
    test.use({ scenario: { bindings: { RecentPlayed: { value: RING }, PlayURL: { error: 'server gone', delayMs: 35_000 } } } });

    test('15. stays busy across a refresh that shifts every card, and reports in its own card', async ({ app, mock }) => {
      const { page } = app;
      await openRecent(page);
      const evening = () => card(page, 'Evening Sessions');
      await evening().locator('.rc-play').focus();
      await mock.resetCalls();
      await page.keyboard.press('Enter');
      await expect(evening().locator('.rc-play')).toHaveAttribute('aria-disabled', 'true');
      await expect(evening().locator('.rc-play')).toBeFocused();
      // Another speaker played something meanwhile: a new card on top.
      await mock.set('RecentPlayed', { value: [...RING, radio('2026-10-23T17:55:00Z', 'fresh', 'Fresh FM', '')] });
      await page.clock.runFor(31_000);
      await expect(card(page, 'Fresh FM')).toHaveCount(1);
      // The repainted button is still busy, still has the keyboard, and a
      // second press does not start it again.
      await expect(evening().locator('.rc-play')).toHaveAttribute('aria-disabled', 'true');
      await expect(evening().locator('.rc-play')).toBeFocused();
      await page.keyboard.press('Enter');
      expect(await mock.count('PlayURL')).toBe(1);
      await page.clock.runFor(5_000);
      await expect(evening().locator('.rc-err')).toContainText('server gone');
      await expect(card(page, 'Jazz24').locator('.rc-err')).toBeEmpty();
      await expect(evening().locator('.rc-play')).not.toHaveAttribute('aria-disabled', /.*/);
      await expect(page.locator('#errorModal')).toBeHidden();
    });
  });

  test.describe('in German', () => {
    test.use({ appLocale: 'de' });

    test('10. the headings and dates are German', async ({ app }) => {
      const { page } = app;
      await openRecent(page);
      const de = bundle('de');
      await expect(page.locator('#recentList .rc-day-head')).toHaveText([
        de['ui2.recent.today'], de['ui2.recent.yesterday'], 'Dienstag, 20. Oktober', 'Montag, 19. Oktober',
      ]);
    });
  });

  test.describe('at the largest text size in the smallest window', () => {
    test.use({ scale: '3', viewport: { width: 1000, height: 600 } });

    test('11. nothing scrolls sideways and the cards fit', async ({ app }) => {
      const { page } = app;
      await openRecent(page);
      await page.addStyleTag({ content: KILL_ANIM });
      await expect(page.locator('#recentList .recent-card')).toHaveCount(HISTORY.length);
      await noHorizontalOverflow(page);
      const over = await page.locator('#view-recent, #view-recent .recent-card, #view-recent .recent-head, #view-recent .rc-name').evaluateAll((els) => els
        .filter((e) => e.scrollWidth > e.clientWidth + 1)
        .map((e) => `${e.id || e.className}: ${e.scrollWidth} > ${e.clientWidth}`));
      expect(over).toEqual([]);
      await minSize(page.locator('#view-recent .rc-actions button'));
    });
  });

  // axe schedules its work with setTimeout, which the paused clock never
  // fires: step the clock while the scan runs (desktop-a11y.spec.mjs).
  for (const theme of ['dark', 'light', 'contrast']) {
    test.describe(`${theme} theme`, () => {
      test.use({ theme });

      test(`12. axe finds nothing in the view (${theme})`, async ({ app }) => {
        const { page } = app;
        await openRecent(page);
        await page.addStyleTag({ content: KILL_ANIM });
        await expect(page.locator('#recentList .recent-card')).toHaveCount(HISTORY.length);
        let done = false;
        const run = scan(page, `${theme}|ui2|recent`, BASELINE, { include: '#view-recent' }).finally(() => { done = true; });
        const tick = (async () => { while (!done) await page.clock.runFor(4).catch(() => {}); })();
        await Promise.all([run, tick]);
      });
    });
  }
});

test.describe('current layout', () => {
  test.use({ ui2: false, fakeClock: true, scenario: { bindings: { RecentPlayed: { value: RING } } } });

  test('13. stays one flat list with its own buttons, no day headings', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    await page.locator('.tab-btn[data-view="recent"]').first().click();
    await expect(page.locator('#recentList .recent-card')).toHaveCount(HISTORY.length);
    await expect(page.locator('#recentList .rc-day')).toHaveCount(0);
    await expect(page.locator('#recentList .rc-err')).toHaveCount(0);
    await expect(page.locator('#recentList .u-btn')).toHaveCount(0);
    await expect(page.locator('#recentList .recent-card').first().locator('.rc-play')).toHaveClass(/btn-mini/);
    await expect(page.locator('#recentList .recent-card').first().locator('.rc-play')).toHaveAttribute('title', en('search.playNow'));
    await expect(page.locator('#recentClear')).toHaveClass(/btn-mini/);
  });
});
