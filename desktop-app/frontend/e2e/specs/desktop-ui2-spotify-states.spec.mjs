// The UI 2.0 preview's Spotify view, its states and in-place feedback
// (#1286 phase 5, docs/UI-2.0.md): an account row that did not answer leads
// to its speaker's page; the quality rows tell a speaker that is not
// reachable apart from one that needs an update, and report a change in the
// row; the login copy reports under its button and says why it cannot run
// with fewer than two speakers. The last block checks the current layout.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-spotify-states.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const OFFICE = ROSTER.office;
const HALL = ROSTER.oldAgent;

const QUALITY_OK = { ok: true, bitrate: 160, autoSupported: true, choice: 0, reason: 'free-default', pending: false };
const NET = {
  boxes: ['livingRoom', 'bedroom', 'office', 'oldAgent', 'kitchen'],
  bindings: {
    // Living Room answers; Bedroom does not answer at all; the Hallway agent
    // is too old for the setting; the Office is offline.
    SpotifyQuality: {
      value: { ok: false },
      byHost: { [LR.host]: { value: QUALITY_OK } },
    },
    BoxAgentVersion: { byHost: { [BR.host]: { error: 'dial tcp: i/o timeout' } } },
    SpotifyNowPlaying: { byHost: { [BR.host]: { error: 'dial tcp: i/o timeout' } } },
  },
};

async function openSpotify(page) {
  await page.locator('#ui2Sidebar .ui2-nav-item[data-view="spotify"]').click();
  await expect(page.locator('#ui2MainInner > #view-spotify')).toBeVisible();
}

const qrow = (page, host) => page.locator(`#spotifyQualityList .sp2-q-row[data-host="${host}"]`);

test.describe('UI 2.0 Spotify states', () => {
  test.use({ ui2: true, scenario: NET });

  test('1. a speaker that is not reachable is told apart from one that needs an update', async ({ app }) => {
    const { page } = app;
    await openSpotify(page);
    await expect(qrow(page, LR.host)).toHaveAttribute('data-state', 'ok');
    await expect(qrow(page, LR.host).locator('select')).toHaveAttribute('aria-label', `${en('spotify.qualityTitle')}: ${LR.friendlyName}`);
    await expect(qrow(page, BR.host)).toHaveAttribute('data-state', 'unreachable');
    await expect(qrow(page, BR.host)).toContainText(en('ui2.feedback.qualityUnreachable', { name: BR.friendlyName }));
    await expect(qrow(page, BR.host).locator('button')).toHaveCount(0);
    await expect(qrow(page, OFFICE.host)).toHaveAttribute('data-state', 'unreachable');
    await expect(qrow(page, HALL.host)).toHaveAttribute('data-state', 'update');
    await expect(qrow(page, HALL.host)).toContainText(en('ui2.feedback.qualityNeedsUpdate'));
    const update = qrow(page, HALL.host).locator('.sp2-q-update');
    await expect(update).toHaveText(en('ui2.feedback.updateSpeaker', { name: HALL.friendlyName }));
    // Update leads to the speaker's own page.
    await update.click();
    await expect(page.locator('#ui2MainInner > #view-spotify')).toBeHidden();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(HALL.friendlyName);
  });

  test('2. a quality change reports in its row, "set" only when the speaker applied it', async ({ app, mock }) => {
    const { page } = app;
    await openSpotify(page);
    const sel = qrow(page, LR.host).locator('select');
    const status = qrow(page, LR.host).locator('.sp2-q-status');
    await expect(status).toHaveAttribute('role', 'status');

    await mock.set('SetSpotifyQuality', { delayMs: 600, value: { ok: true, applied: true, bitrate: 320, reason: 'chosen', choice: 320, autoSupported: true } });
    await sel.focus();
    await sel.selectOption('320');
    await expect(status).toHaveText(en('ui2.feedback.qualitySetting', { name: LR.friendlyName, choice: '320 kbps' }));
    await expect(status).toHaveText(en('ui2.feedback.qualitySet', { name: LR.friendlyName, choice: '320 kbps' }));
    await expect(status).toHaveText(`${LR.friendlyName}: 320 kbps set.`);
    await expect(status).toHaveClass(/is-ok/);
    await mock.expectCalled('SetSpotifyQuality', { pred: (c) => onHost(LR.host)(c) && c.args[2] === 320 });
    // The selector keeps the keyboard; the list is not redrawn.
    await expect(sel).toBeFocused();

    await mock.set('SetSpotifyQuality', { value: { ok: true, applied: false, bitrate: 320, reason: 'chosen', choice: 0, autoSupported: true } });
    await sel.selectOption('0');
    await expect(status).toHaveText(en('ui2.feedback.qualityPending', { name: LR.friendlyName, choice: en('spotify.qualityAuto') }));
    await expect(status).toHaveClass(/is-warn/);

    await mock.set('SetSpotifyQuality', { value: { ok: false } });
    await sel.selectOption('160');
    await expect(status).toHaveText(en('ui2.feedback.qualityFailed', { name: LR.friendlyName, reason: en('spotify.qualityFailed') }));
    await expect(page.locator('#errorModal')).toBeHidden();
    await expect(page.locator('#toast')).not.toHaveClass(/show/);
  });

  test('3. the login copy reports under its button', async ({ app, mock }) => {
    const { page } = app;
    await openSpotify(page);
    await mock.set('SyncSpotifyLogin', { value: { synced: [BR.host, HALL.host], failed: {}, source: LR.friendlyName } });
    const btn = page.locator('#spotifySyncBtn');
    await expect(btn).not.toHaveAttribute('aria-disabled', 'true');
    await btn.click();
    const status = page.locator('#spotifySyncStatus');
    await expect(status).toHaveAttribute('role', 'status');
    await expect(status).toHaveText(en('spotify.syncDoneFrom', { n: 2, source: LR.friendlyName }));
    await expect(status).toHaveClass(/is-ok/);
    await expect(page.locator('#toast')).not.toHaveClass(/show/);

    await mock.set('SyncSpotifyLogin', { error: 'no speaker is logged into Spotify yet' });
    await btn.click();
    await expect(status).toHaveText(en('spotify.syncNone'));
    await expect(page.locator('#errorModal')).toBeHidden();
  });

  test('4. an account row that did not answer offers its speaker\'s page, not settings elsewhere', async ({ app }) => {
    const { page } = app;
    await openSpotify(page);
    const row = page.locator(`#sp2Accounts .sp2-acct[data-host="${BR.host}"]`);
    await expect(row).toHaveAttribute('data-state', 'unknown');
    await expect(row.locator('.sp2-acct-line')).toHaveText(en('ui2.feedback.acctUnknown', { name: BR.friendlyName }));
    await expect(row).not.toContainText('Speaker settings');
    const open = row.locator('.sp2-acct-open');
    await expect(open).toHaveText(en('ui2.feedback.openSpeaker', { name: BR.friendlyName }));
    await open.click();
    await expect(page.locator('#ui2MainInner > #view-spotify')).toBeHidden();
    await expect(page.locator('#ui2SpkPageName')).toHaveText(BR.friendlyName);
  });

  test.describe('with fewer than two speakers', () => {
    test.use({ scenario: { boxes: ['livingRoom', 'kitchen'], bindings: { SpotifyQuality: { value: QUALITY_OK } } } });

    test('5. the copy button says why it cannot run, and does nothing', async ({ app, mock }) => {
      const { page } = app;
      await openSpotify(page);
      const btn = page.locator('#spotifySyncBtn');
      await expect(btn).toHaveAttribute('aria-disabled', 'true');
      // aria-disabled, not disabled: it keeps the keyboard.
      expect(await btn.evaluate((el) => el.disabled)).toBe(false);
      const reason = page.locator('#spotifySyncStatus');
      await expect(reason).toBeVisible();
      await expect(reason).toHaveText(en('spotify.syncNeedTwo'));
      await expect(btn).toHaveAttribute('aria-describedby', 'spotifySyncStatus');
      await mock.resetCalls();
      // Playwright will not click an aria-disabled button; Enter does.
      await btn.focus();
      await page.keyboard.press('Enter');
      expect(await mock.count('SyncSpotifyLogin')).toBe(0);
      await expect(page.locator('#toast')).not.toHaveClass(/show/);
    });
  });
});

test.describe('current layout', () => {
  test.use({ scenario: NET });

  test('6. keeps its quality rows, with no UI 2.0 row or result lines', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="spotify"]').first().click();
    await expect(page.locator('#spotifyQualityList .quality-row').first()).toBeVisible();
    await expect(page.locator('#spotifyQualityList .sp2-q-row')).toHaveCount(0);
    await expect(page.locator('#spotifyQualityList')).toContainText(en('spotify.qualityUnavailable'));
    await expect(page.locator('#spotifySyncBtn')).not.toHaveAttribute('aria-disabled', 'true');
    await expect(page.locator('#spotifySyncStatus')).toHaveCount(0);
  });
});
