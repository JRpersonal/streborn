// The UI 2.0 preview's Spotify view (#1286, docs/UI-2.0.md phase 2): the
// playing playlist onto a key of the selected speaker, and the Spotify login
// of every speaker. The classic layout must not show either card.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-spotify.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { scan, KILL_ANIM } from '../helpers/a11y.mjs';
import { en } from '../helpers/i18n.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const BR = ROSTER.bedroom;
const OFFICE = ROSTER.office;
const HALL = ROSTER.oldAgent;

const PLAYLIST = 'spotify:playlist:37i9dQZF1DXcBWIGoYBM5M';
const ANNA_ID = 'a1b2c3d4e5f6a7b8c9d0e1f2a3';

// playSpotify makes the mock speaker play a playlist on its own Spotify entry,
// the way the speaker reports it: source SPOTIFY, a container location that
// carries the URI.
async function playSpotify(page, host, name = 'Discover Weekly') {
  const location = `/playback/container/${Buffer.from(PLAYLIST).toString('base64')}`;
  await page.evaluate(([h, it]) => {
    window.__strMock.state.playing[h] = 'PLAY_STATE';
    window.__strMock.state.item[h] = it;
  }, [host, { source: 'SPOTIFY', location, name, art: '' }]);
}

async function openSpotify(page) {
  await page.locator('#ui2Sidebar .ui2-nav-item[data-view="spotify"]').click();
  await expect(page.locator('#ui2MainInner > #view-spotify')).toBeVisible();
}

// The status poll and the view's own follow-up both run on the page clock.
async function tick(page, ms = 6_000) {
  await runFor(page, ms);
}

const keysCard = (page) => page.locator('#sp2Keys');
const key = (page, n) => page.locator(`#sp2Keys .sp2-key[data-slot="${n}"]`);

test.describe('UI 2.0 Spotify', () => {
  test.use({ ui2: true, fakeClock: true });

  test('1. nothing from Spotify playing: says so, names the speaker and explains how', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    await openSpotify(page);
    const card = keysCard(page);
    await expect(card.locator('#sp2KeysTitle')).toHaveText(en('ui2.spotify.keysTitle', { name: LR.friendlyName }));
    await expect(card).toContainText(en('ui2.spotify.notPlaying', { name: LR.friendlyName }));
    await expect(card).toContainText(en('ui2.spotify.notPlayingHow', { name: LR.friendlyName }));
    await expect(card.locator('.sp2-key')).toHaveCount(0);
    await expect(card.locator('#sp2Save')).toHaveCount(0);
    // Every function of the view keeps its place below the cards.
    await expect(page.locator('#spotifySyncBtn')).toBeVisible();
    await expect(page.locator('#spotifyQualityList')).toBeAttached();
  });

  test('2. a playlist goes on the chosen key, done only once the speaker stored it', async ({ app, mock }) => {
    const { page } = app;
    // The model stores the key, two seconds (page clock) after the call.
    await mock.set('SaveSpotifyPreset', { delayMs: 2_000 });
    await settle(page, 9_000);
    await playSpotify(page, LR.host);
    await tick(page);
    await openSpotify(page);
    await tick(page, 2_000);

    const card = keysCard(page);
    await expect(card.locator('.sp2-now')).toContainText(en('ui2.spotify.playingOn', { name: LR.friendlyName, title: 'Discover Weekly' }));
    await expect(card.locator('.sp2-now')).toContainText(en('ui2.spotify.kind.playlist'));
    await expect(card.locator('.sp2-key')).toHaveCount(6);
    const save = card.locator('#sp2Save');
    await expect(save).toBeDisabled();
    await expect(save).toHaveText(en('ui2.spotify.saveBtnNoKey'));

    await key(page, 4).click();
    await expect(key(page, 4)).toHaveAttribute('aria-pressed', 'true');
    await expect(save).toBeEnabled();
    await expect(save).toHaveText(en('ui2.spotify.saveBtn', { n: 4, name: LR.friendlyName }));
    await expect(card.locator('#sp2Hint')).toHaveText(en('ui2.spotify.replaces', { current: 'Jazz24' }));

    await mock.resetCalls();
    await save.click();
    // Sent is not arrived: while the speaker has not answered, the card says
    // it is saving and nothing claims done.
    await expect(card.locator('#sp2Save')).toHaveText(en('ui2.spotify.saving', { name: LR.friendlyName }));
    await expect(card.locator('#sp2Result')).not.toContainText(en('ui2.spotify.saved', { n: 4, name: LR.friendlyName, title: 'Discover Weekly' }));
    await mock.expectCalled('SaveSpotifyPreset', { pred: (c) => c.args[0] === LR.host && c.args[2] === 4 && c.args[4] === PLAYLIST, times: 1 });

    await tick(page, 3_000);
    await expect(card.locator('#sp2Result')).toContainText(en('ui2.spotify.saved', { n: 4, name: LR.friendlyName, title: 'Discover Weekly' }));
    await expect(key(page, 4)).toContainText('Discover Weekly');
    await expect(key(page, 4)).toContainText(en('ui2.spotify.keyHere'));
    await expect(card.locator('#sp2Save')).toBeDisabled();
    // The window-wide toast is not how this view answers.
    await expect(page.locator('.toast', { hasText: en('preset.savedToKey', { n: 4, name: 'Discover Weekly' }) })).toHaveCount(0);
  });

  test('3. a refused save is reported in the card, naming the speaker', async ({ app, mock }) => {
    const { page } = app;
    await mock.set('SaveSpotifyPreset', { error: 'status 500 speaker busy' });
    await settle(page, 9_000);
    await playSpotify(page, LR.host);
    await tick(page);
    await openSpotify(page);
    await tick(page, 2_000);
    await key(page, 2).click();
    await keysCard(page).locator('#sp2Save').click();
    const result = keysCard(page).locator('#sp2Result');
    await expect(result).toHaveAttribute('role', 'alert');
    await expect(result).toContainText(en('ui2.spotify.saveFailed', { name: LR.friendlyName, err: '' }).replace(/:\s*$/, ''));
    await expect(result).toContainText('speaker busy');
    await expect(key(page, 2)).toContainText('Best Of Rock.FM');
  });

  test('4. a key that already holds the playlist cannot take it twice', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 9_000);
    await page.evaluate(([h, uri]) => {
      const m = window.__strMock;
      m.state.presets[h] = [{ slot: 3, name: 'Discover Weekly', type: 'spotify', uri, stream_url: '' }];
    }, [LR.host, PLAYLIST]);
    await playSpotify(page, LR.host);
    await tick(page);
    await openSpotify(page);
    await tick(page, 2_000);
    await expect(key(page, 3)).toContainText(en('ui2.spotify.keyHere'));
    await key(page, 3).click();
    await expect(keysCard(page).locator('#sp2Hint')).toHaveText(en('ui2.spotify.alreadyOnKey', { n: 3, name: LR.friendlyName }));
    await expect(keysCard(page).locator('#sp2Save')).toBeDisabled();
    await expect.poll(() => mock.count('SaveSpotifyPreset')).toBe(0);
  });

  test('5. the keys follow the speaker picked in the sidebar', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    await playSpotify(page, LR.host);
    await tick(page);
    await openSpotify(page);
    await tick(page, 2_000);
    await expect(keysCard(page).locator('.sp2-key')).toHaveCount(6);
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR.host}"]`).click();
    await tick(page);
    await expect(keysCard(page).locator('#sp2KeysTitle')).toHaveText(en('ui2.spotify.keysTitle', { name: BR.friendlyName }));
    await expect(keysCard(page)).toContainText(en('ui2.spotify.notPlaying', { name: BR.friendlyName }));
  });

  test.describe('accounts', () => {
    test.use({
      scenario: {
        boxes: ['livingRoom', 'bedroom', 'office', 'kitchen', 'oldAgent'],
        bindings: {
          SpotifyNowPlaying: {
            byHost: {
              [LR.host]: { value: { account: ANNA_ID, canRecall: true, product: 'premium', accountNames: { [ANNA_ID]: 'Anna', bbbbbbbbbbbbbbbbbbbbbb: 'Ben' } } },
              [BR.host]: { value: { account: '', canRecall: false, product: '' } },
            },
            // Anything else answers like an agent older than the login fields.
            value: { bitrate: 0, track: '', artist: '', cover: '', context: '', account: '', premiumRequired: false, audioKeyRefused: false, product: '' },
          },
        },
      },
    });

    test('6. every speaker with its login, by name, never by id', async ({ app, mock }) => {
      const { page } = app;
      await settle(page, 9_000);
      await openSpotify(page);
      await tick(page, 1_000);
      const acct = page.locator('#sp2Accounts');
      const row = (host) => acct.locator(`.sp2-acct[data-host="${host}"]`);
      await expect(row(LR.host)).toHaveAttribute('data-state', 'in');
      await expect(row(LR.host)).toContainText(en('ui2.spotify.acct.inAs', { account: 'Anna' }));
      await expect(row(LR.host)).toContainText(en('ui2.spotify.acct.premium'));
      await expect(row(LR.host)).toContainText(en('ui2.spotify.acct.others', { accounts: 'Ben' }));
      await expect(row(BR.host)).toHaveAttribute('data-state', 'none');
      await expect(row(BR.host)).toContainText(en('ui2.spotify.acct.none', { name: BR.friendlyName }));
      await expect(row(OFFICE.host)).toHaveAttribute('data-state', 'offline');
      await expect(row(HALL.host)).toHaveAttribute('data-state', 'unknown');
      // A Bose original has no STR login to list.
      await expect(acct.locator('.sp2-acct')).toHaveCount(4);
      await expect(acct).not.toContainText(ANNA_ID);
      // The offline speaker was not asked.
      expect(await mock.count('SpotifyNowPlaying', onHost(OFFICE.host))).toBe(0);

      // Check again asks again, and the keyboard stays on the button.
      await mock.resetCalls();
      await acct.locator('#sp2AcctRefresh').focus();
      await page.keyboard.press('Enter');
      await mock.expectCalled('SpotifyNowPlaying', { pred: onHost(LR.host) });
      await expect(acct.locator('#sp2AcctRefresh')).toBeFocused();
    });
  });

  test('7. axe finds nothing in the cards while a key is chosen', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    await playSpotify(page, LR.host);
    await tick(page);
    await openSpotify(page);
    await tick(page, 2_000);
    await key(page, 5).click();
    await page.addStyleTag({ content: KILL_ANIM });
    // axe schedules its batches with setTimeout: step the paused clock while
    // it runs, as desktop-a11y does.
    let done = false;
    const run = scan(page, 'dark|ui2|spotify-cards', 'baselines/a11y-desktop.json', { include: '#sp2Root' })
      .finally(() => { done = true; });
    const ticking = (async () => {
      while (!done) await page.clock.runFor(4).catch(() => {});
    })();
    await Promise.all([run, ticking]);
  });

  test('8. the keys work from the keyboard', async ({ app }) => {
    const { page } = app;
    await settle(page, 9_000);
    await playSpotify(page, LR.host);
    await tick(page);
    await openSpotify(page);
    await tick(page, 2_000);
    await key(page, 1).focus();
    await page.keyboard.press('Tab');
    await expect(key(page, 2)).toBeFocused();
    await page.keyboard.press('Space');
    await expect(key(page, 2)).toHaveAttribute('aria-pressed', 'true');
    // The repaint keeps the keyboard on the key that was pressed.
    await expect(key(page, 2)).toBeFocused();
    const box = await key(page, 2).boundingBox();
    expect(box.height).toBeGreaterThanOrEqual(40);
  });
});

test.describe('classic layout', () => {
  test('9. the Spotify view has no UI 2.0 cards', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="spotify"]').click();
    await expect(page.locator('#view-spotify .spotify-page')).toBeVisible();
    await expect(page.locator('#sp2Root')).toHaveCount(0);
    await expect(page.locator('#spotifySyncBtn')).toBeVisible();
  });
});
