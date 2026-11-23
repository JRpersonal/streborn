// Shuffle and repeat in the player while a Spotify playlist plays (#2058): the
// music folder's own two buttons, asking the speaker's engine for the step a
// remote key bound to shuffle or repeat does. Classic layout and UI 2.0 (where
// the buttons sit under More, as for a folder).
//
//   E2E_PORT=4184 npx playwright test specs/desktop-spotify-playmode.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;

const NOW = {
  bitrate: 160, track: 'Song A', artist: 'Artist A', cover: '', context: 'spotify:playlist:37i9dQZF1DXcBWIGoYBM5M',
  account: '', premiumRequired: false, audioKeyRefused: false, accountRefused: false,
  canRecall: true, product: 'premium',
};

// playStrSpotify makes the mock speaker play from STR's own Spotify engine.
async function playStrSpotify(page, host) {
  await page.evaluate(([h, it]) => {
    window.__strMock.state.playing[h] = 'PLAY_STATE';
    window.__strMock.state.item[h] = it;
  }, [host, { source: 'UPNP', location: `http://${host}:8888/spotify/stream.ogg`, name: 'Discover Weekly', art: '' }]);
}

const shuffleBtn = (page) => page.locator('#queueShuffleBtn');
const repeatBtn = (page) => page.locator('#queueRepeatBtn');

// openButtons makes the two buttons reachable: in UI 2.0 they live under More.
async function openButtons(page, ui2) {
  if (!ui2) return;
  if ((await page.locator('#ui2MoreBtn').getAttribute('aria-expanded')) !== 'true') {
    await page.locator('#ui2MoreBtn').click();
  }
}

for (const ui2 of [false, true]) {
  const layout = ui2 ? 'UI 2.0' : 'classic';
  test.describe(`Spotify shuffle and repeat, ${layout} layout`, () => {
    test.use({ ui2, fakeClock: true });

    test('the folder buttons show the engine state and step it', async ({ app, mock }) => {
      const { page } = app;
      await mock.set('SpotifyNowPlaying', { value: { ...NOW, playModes: { shuffle: false, repeat: 'off' } } });
      await settle(page, 9_000);
      await playStrSpotify(page, LR.host);
      await runFor(page, 8_000);
      await openButtons(page, ui2);

      await expect(shuffleBtn(page)).toBeVisible();
      await expect(repeatBtn(page)).toBeVisible();
      // Spotify skips with the track buttons beside Play: the folder's own
      // skip and position stay out of it.
      await expect(page.locator('#queuePrevBtn')).toBeHidden();
      await expect(page.locator('#queueNextBtn')).toBeHidden();
      await expect(shuffleBtn(page)).not.toHaveClass(/\bactive\b/);
      await expect(repeatBtn(page)).not.toHaveClass(/\bactive\b/);

      await mock.set('SpotifyPlayMode', { value: { ok: true, shuffle: true, repeat: 'off' } });
      await shuffleBtn(page).click();
      await mock.expectCalled('SpotifyPlayMode', { pred: (c) => c.args[0] === LR.host && c.args[2] === 'shuffle', times: 1 });
      await expect(shuffleBtn(page)).toHaveClass(/\bactive\b/);
      // No folder call went out for it.
      expect(await mock.count('QueueShuffle')).toBe(0);

      await mock.set('SpotifyPlayMode', { sequence: [
        { value: { ok: true, shuffle: true, repeat: 'context' } },
        { value: { ok: true, shuffle: true, repeat: 'track' } },
      ] });
      await repeatBtn(page).click();
      await expect(repeatBtn(page)).toHaveClass(/\bactive\b/);
      await expect(repeatBtn(page)).not.toContainText('¹');
      await repeatBtn(page).click();
      await expect(repeatBtn(page)).toContainText('¹');
      await mock.expectCalled('SpotifyPlayMode', { pred: (c) => c.args[2] === 'repeat', times: 2 });
      expect(await mock.count('QueueRepeat')).toBe(0);
    });

    test('a change made elsewhere shows with the next now-playing refresh', async ({ app, mock }) => {
      const { page } = app;
      await mock.set('SpotifyNowPlaying', { value: { ...NOW, playModes: { shuffle: false, repeat: 'off' } } });
      await settle(page, 9_000);
      await playStrSpotify(page, LR.host);
      await runFor(page, 8_000);
      await openButtons(page, ui2);
      await expect(shuffleBtn(page)).not.toHaveClass(/\bactive\b/);

      await mock.set('SpotifyNowPlaying', { value: { ...NOW, playModes: { shuffle: true, repeat: 'context' } } });
      await runFor(page, 10_000);
      await expect(shuffleBtn(page)).toHaveClass(/\bactive\b/);
      await expect(repeatBtn(page)).toHaveClass(/\bactive\b/);
    });

    test('an agent older than the buttons gets none', async ({ app, mock }) => {
      const { page } = app;
      await mock.set('SpotifyNowPlaying', { value: NOW });
      await settle(page, 9_000);
      await playStrSpotify(page, LR.host);
      await runFor(page, 8_000);
      await openButtons(page, ui2);
      await expect(page.locator('#queueControls')).toBeHidden();
    });

    test('an agent that cannot do the press loses the buttons', async ({ app, mock }) => {
      const { page } = app;
      await mock.set('SpotifyNowPlaying', { value: { ...NOW, playModes: { shuffle: false, repeat: 'off' } } });
      await mock.set('SpotifyPlayMode', { error: 'spotify_playmode_unsupported' });
      await settle(page, 9_000);
      await playStrSpotify(page, LR.host);
      await runFor(page, 8_000);
      await openButtons(page, ui2);
      await shuffleBtn(page).click();
      await expect(page.locator('#queueControls')).toBeHidden();
      // Not an error: nothing to tell the user about.
      await expect(page.locator('.toast', { hasText: 'spotify_playmode' })).toHaveCount(0);
      if (ui2) await expect(page.locator('#ui2PlayerStatus')).toHaveText('');
      await runFor(page, 8_000);
      await expect(page.locator('#queueControls')).toBeHidden();
    });

    test('radio has no Spotify buttons', async ({ app, mock }) => {
      const { page } = app;
      await mock.set('SpotifyNowPlaying', { value: { ...NOW, playModes: { shuffle: true, repeat: 'off' } } });
      await settle(page, 9_000);
      await openButtons(page, ui2);
      await expect(page.locator('#queueControls')).toBeHidden();
    });
  });
}
