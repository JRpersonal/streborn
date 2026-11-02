// Spotify refuses the speaker's account on (STR) (#1671): a short notice in the
// now-playing area, in both layouts, for as long as the speaker says so. It
// used to be a one-shot toast that only someone with the app open at the
// moment of the refusal ever saw.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-spotify-refused.spec.mjs
//   SHOOT_DIR=<dir> ... saves a picture of the notice in each layout.

import { test, expect } from '../fixtures.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;

const REFUSED = {
  bitrate: 0, track: '', artist: '', cover: '', context: 'spotify:playlist:37i9dQZF1DXcBWIGoYBM5M',
  account: 'a1b2c3d4e5f6a7b8c9d0e1f2a3', accountNames: { a1b2c3d4e5f6a7b8c9d0e1f2a3: 'Anna' },
  premiumRequired: false, audioKeyRefused: false, accountRefused: true, accountRefusedName: 'Anna',
  canRecall: true, product: 'premium',
};

// playStrSpotify makes the mock speaker play from STR's own Spotify engine:
// a UPnP stream from the agent's /spotify/stream.
async function playStrSpotify(page, host, playState = 'PLAY_STATE') {
  await page.evaluate(([h, ps, it]) => {
    window.__strMock.state.playing[h] = ps;
    window.__strMock.state.item[h] = it;
  }, [host, playState, { source: 'UPNP', location: `http://${host}:8888/spotify/stream.ogg`, name: 'Discover Weekly', art: '' }]);
}

async function playRadio(page, host) {
  await page.evaluate(([h]) => {
    window.__strMock.state.playing[h] = 'PLAY_STATE';
    window.__strMock.state.item[h] = { source: 'INTERNET_RADIO', location: 'http://demobox.local:8888/stream/2', name: 'Best Of Rock.FM' };
  }, [host]);
}

const notice = (page) => page.locator('#spotifyRefusedRow');
const NAMED = () => en('spotify.accountRefusedNamed', { account: 'Anna' });

for (const ui2 of [false, true]) {
  const layout = ui2 ? 'UI 2.0' : 'classic';
  test.describe(`Spotify refused notice, ${layout} layout`, () => {
    test.use({ ui2, fakeClock: true });

    test('shows while STR Spotify plays the refused account, and goes with it', async ({ app, mock }) => {
      const { page } = app;
      await mock.set('SpotifyNowPlaying', { value: REFUSED });
      await settle(page, 9_000);
      await expect(notice(page)).toBeHidden();

      await playStrSpotify(page, LR.host);
      await runFor(page, 8_000);
      await expect(notice(page)).toBeVisible();
      await expect(notice(page)).toHaveText(NAMED());
      if (ui2) await expect(page.locator('#ui2Player #spotifyRefusedRow')).toBeVisible();
      // A notice, not a toast: nothing window-wide says it a second time.
      await expect(page.locator('.toast', { hasText: 'Spotify' })).toHaveCount(0);
      if (process.env.SHOOT_DIR) {
        const name = ui2 ? 'spotify-refused-ui2.png' : 'spotify-refused-classic.png';
        const target = ui2 ? page.locator('#ui2Player') : page.locator('#boxControls');
        await target.screenshot({ path: `${process.env.SHOOT_DIR}/${name}`, animations: 'disabled' });
        await page.screenshot({ path: `${process.env.SHOOT_DIR}/${name.replace('.png', '-window.png')}`, animations: 'disabled' });
      }

      // Still there while the speaker sits stopped on the stream.
      await playStrSpotify(page, LR.host, 'STOP_STATE');
      await runFor(page, 8_000);
      await expect(notice(page)).toBeVisible();

      // Radio is not Spotify: no notice there. A stopped speaker is polled on
      // the slow cadence, so the change takes a long poll to arrive.
      await playRadio(page, LR.host);
      await runFor(page, 30_000);
      await expect(notice(page)).toBeHidden();

      // The account played (the speaker cleared its flag): the notice goes.
      await mock.set('SpotifyNowPlaying', { value: { ...REFUSED, accountRefused: false, accountRefusedName: '' } });
      await playStrSpotify(page, LR.host);
      await runFor(page, 8_000);
      await expect(notice(page)).toBeHidden();
    });

    test('an older agent: its short-lived flag feeds the same notice, without a name', async ({ app, mock }) => {
      const { page } = app;
      const old = { ...REFUSED, audioKeyRefused: true };
      delete old.accountRefused;
      delete old.accountRefusedName;
      await mock.set('SpotifyNowPlaying', { value: old });
      await settle(page, 9_000);
      await playStrSpotify(page, LR.host);
      await runFor(page, 8_000);
      await expect(notice(page)).toHaveText(en('spotify.accountRefused'));
    });
  });
}

test.describe('Spotify refused, UI 2.0 accounts card', () => {
  test.use({ ui2: true, fakeClock: true, scenario: { bindings: { SpotifyNowPlaying: { value: REFUSED } } } });

  test('the speaker\'s login row says it too', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    await page.locator('#ui2Sidebar .ui2-nav-item[data-view="spotify"]').click();
    await runFor(page, 2_000);
    const row = page.locator(`#sp2Accounts .sp2-acct[data-host="${LR.host}"]`);
    await expect(row.locator('.sp2-acct-refused')).toHaveText(en('spotify.accountRefused'));
    if (process.env.SHOOT_DIR) {
      await page.locator('#sp2Accounts').screenshot({ path: `${process.env.SHOOT_DIR}/spotify-refused-ui2-accounts.png`, animations: 'disabled' });
    }
  });
});
