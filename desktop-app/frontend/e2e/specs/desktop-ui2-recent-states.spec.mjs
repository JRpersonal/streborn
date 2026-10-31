// Recently played in the UI 2.0 preview, its states and honest feedback
// (#1286 phase 5, docs/UI-2.0.md): a speaker whose history did not answer is
// named above the list with Try again, the list on its way is a status line,
// the empty history leads to Radio, Library and Spotify, and a card says
// "Playing" only once the speaker reports it. The last block checks the
// current layout.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-recent-states.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;

const radio = (ts, key, name) => ({ ts, source: 'radio', cardKey: key, cardName: name, cardArt: '', cardURL: `http://example.com/${key}`, track: '' });
const RING = [radio('2026-10-23T16:00:00Z', 'jazz', 'Jazz Example'), radio('2026-10-23T17:40:00Z', 'rock', 'Rock Example')];

const nav = (page, view) => page.locator(`#ui2Sidebar .ui2-nav-item[data-view="${view}"]`);
const card = (page, name) => page.locator('#recentList .recent-card').filter({ has: page.locator('.rc-name', { hasText: name }) });

async function openRecent(page) {
  await nav(page, 'recent').click();
  await expect(page.locator('#ui2MainInner > #view-recent')).toBeVisible();
}

// waitForCardLine runs the page clock until the card's line says something
// final: the radio play watches its stream for six seconds, then the card
// asks the speaker every two seconds for up to 26 seconds.
async function waitForCardLine(page, c, re, step = 1000) {
  await expect.poll(async () => {
    await runFor(page, step);
    return (await c.locator('.rc-err').textContent()) || '';
  }, { timeout: 60_000 }).toMatch(re);
}

test.describe('UI 2.0 Recently played states', () => {
  test.use({ ui2: true });

  test.describe('a speaker that does not answer', () => {
    test.use({ scenario: { bindings: { RecentPlayed: { value: RING, byHost: { [BR.host]: { error: 'speaker did not answer' } } } } } });

    test('1. is named above the list, and Try again asks again', async ({ app, mock }) => {
      const { page } = app;
      await openRecent(page);
      await page.locator('#recentScopeAll').click();
      const note = page.locator('#recentList .rc-unanswered');
      await expect(note).toHaveAttribute('role', 'status');
      await expect(note).toContainText(`${BR.friendlyName} did not answer, so their history is missing here.`);
      // The cards of the speakers that answered are still there.
      await expect(card(page, 'Rock Example').first()).toBeVisible();

      await mock.set('RecentPlayed', { value: RING });
      await mock.resetCalls();
      const retry = page.locator('#recentUnansweredRetry');
      await expect(retry).toHaveText(en('ui2.feedback.tryAgain'));
      await retry.click();
      await mock.expectCalled('RecentPlayed', { pred: onHost(BR.host) });
      await expect(note).toHaveCount(0);
    });
  });

  test.describe('the list on its way', () => {
    test.use({ scenario: { bindings: { RecentPlayed: { value: RING, delayMs: 2500 } } } });

    test('2. is a status line', async ({ app }) => {
      const { page } = app;
      await openRecent(page);
      const loading = page.locator('#recentList .lib2-loading');
      await expect(loading).toHaveAttribute('role', 'status');
      await expect(loading).toHaveText(en('recent.loading'));
      await expect(card(page, 'Rock Example')).toBeVisible({ timeout: 10_000 });
    });
  });

  test.describe('nothing played yet', () => {
    test.use({ scenario: { bindings: { RecentPlayed: { value: [] } } } });

    test('3. leads to Radio, Library and Spotify', async ({ app }) => {
      const { page } = app;
      await openRecent(page);
      const acts = page.locator('#recentList .rc-empty2 .rc-empty-acts button');
      await expect(acts).toHaveText([en('ui2.feedback.openRadio'), en('ui2.feedback.openLibrary'), en('ui2.feedback.openSpotify')]);
      await acts.nth(1).click();
      await expect(page.locator('#ui2MainInner > #view-library')).toBeVisible();
      await openRecent(page);
      await page.locator('#recentList .rc-empty-acts button').nth(2).click();
      await expect(page.locator('#ui2MainInner > #view-spotify')).toBeVisible();
      await openRecent(page);
      await page.locator('#recentList .rc-empty-acts button').nth(0).click();
      await expect(page.locator('#ui2MainInner > #view-box')).toBeVisible();
    });
  });

  test.describe('playing a card', () => {
    test.use({ fakeClock: true, scenario: { playing: { livingRoom: 'STANDBY' }, bindings: { RecentPlayed: { value: RING } } } });

    test('4. says "Playing on" only once the speaker reports it', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await openRecent(page);
      const c = card(page, 'Jazz Example');
      await mock.resetCalls();
      await c.locator('.rc-play').click();
      await expect(c.locator('.rc-err')).toHaveText(en('ui2.radio.starting', { speaker: LR.friendlyName }));
      await expect(c.locator('.rc-err')).toHaveClass(/rc-err--busy/);
      await mock.expectCalled('PlayURL', { pred: onHost(LR.host) });
      await waitForCardLine(page, c, /^Playing on /);
      await expect(c.locator('.rc-err')).toHaveText(en('ui2.radio.playing', { speaker: LR.friendlyName }));
      await expect(c.locator('.rc-err')).toHaveClass(/rc-err--ok/);
      await expect(page.locator('#toast')).not.toHaveClass(/show/);
    });
  });

  test.describe('a card the speaker never starts', () => {
    // The speaker takes the URL and stays in standby.
    test.use({ fakeClock: true, scenario: { playing: { livingRoom: 'STANDBY' }, bindings: { RecentPlayed: { value: RING }, PlayURL: { value: null } } } });

    test('5. says "Sent", never "Playing"', async ({ app }) => {
      test.slow();
      const { page } = app;
      await settle(page, 9_000);
      await openRecent(page);
      const c = card(page, 'Jazz Example');
      await c.locator('.rc-play').click();
      await expect(c.locator('.rc-err')).toHaveText(en('ui2.radio.starting', { speaker: LR.friendlyName }));
      await waitForCardLine(page, c, /^Sent to /, 3000);
      await expect(c.locator('.rc-err')).toHaveText(en('ui2.feedback.sentNotStarted', { speaker: LR.friendlyName }));
      await expect(c.locator('.rc-err')).toHaveClass(/rc-err--warn/);
    });
  });
});

test.describe('current layout', () => {
  test.use({ scenario: { bindings: { RecentPlayed: { value: RING, byHost: { [BR.host]: { error: 'speaker did not answer' } } } } } });

  test('6. draws no unanswered note, no state buttons and no card line', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="recent"]').first().click();
    await expect(page.locator('#recentList .recent-card').first()).toBeVisible();
    await expect(page.locator('#recentList .rc-unanswered')).toHaveCount(0);
    await expect(page.locator('#recentList .rc-err')).toHaveCount(0);
    await expect(page.locator('#recentList .lib2-loading')).toHaveCount(0);
  });
});
