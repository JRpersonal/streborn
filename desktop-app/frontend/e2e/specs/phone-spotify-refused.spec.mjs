// The phone remote says it too when Spotify refuses the speaker's account on
// (STR) (#1671): a short notice under the now-playing line, from the
// /spotify/info answer the page reads anyway, in both layouts.
//
//   E2E_PORT=4184 npx playwright test specs/phone-spotify-refused.spec.mjs
//   SHOOT_DIR=<dir> ... saves a picture of the now-playing card.

import { test, expect } from '../fixtures.mjs';

const STR_SPOTIFY = { name: 'Discover Weekly', loc: 'http://192.0.2.50:8888/spotify/stream.ogg', key: '' };
const INFO = {
  track: 'Morning Light', artist: 'The Demo Band', account: 'a1b2c3d4e5f6a7b8c9d0e1f2a3',
  accountNames: { a1b2c3d4e5f6a7b8c9d0e1f2a3: 'Anna' }, audioKeyRefused: false,
  accountRefused: true, accountRefusedName: 'Anna',
};
const NAMED = "Spotify won't stream the account Anna on (STR). The speaker's own Spotify entry, the one without (STR), still plays it.";

const notice = (page) => page.locator('#statusCard #spRefused');

test.describe('phone: Spotify refused notice', () => {
  test.use({ phonePlaying: STR_SPOTIFY, phoneOverrides: { '/spotify/info': { body: INFO } } });

  test('shows under the now-playing line while STR Spotify plays the refused account', async ({ phone }) => {
    const { page } = phone;
    await expect(notice(page)).toBeVisible();
    await expect(notice(page)).toHaveText(NAMED);
    if (process.env.SHOOT_DIR) {
      await page.locator('#statusCard').screenshot({ path: `${process.env.SHOOT_DIR}/spotify-refused-phone.png`, animations: 'disabled' });
    }
  });

  test('the UI 2.0 preview shows it in the same place', async ({ phone }) => {
    const { page } = phone;
    await page.addInitScript(() => { try { localStorage.setItem('str.ui2Preview', '1'); } catch (e) { /* none */ } });
    await page.reload();
    await expect(page.locator('html')).toHaveClass(/(^|\s)ui2(\s|$)/);
    await expect(notice(page)).toBeVisible();
    await expect(notice(page)).toHaveText(NAMED);
    if (process.env.SHOOT_DIR) {
      await page.locator('#statusCard').screenshot({ path: `${process.env.SHOOT_DIR}/spotify-refused-phone-ui2.png`, animations: 'disabled' });
    }
  });
});

test.describe('phone: stopped on the stream', () => {
  // The engine gave up on refused keys and the speaker sits stopped on STR's
  // Spotify stream: the song read does not run then, the notice still does.
  const STOPPED = '<?xml version="1.0" encoding="UTF-8"?><nowPlaying deviceID="A1B2C3D4E5F6" source="UPNP">'
    + `<ContentItem source="UPNP" location="${STR_SPOTIFY.loc}" isPresetable="true"><itemName>Discover Weekly</itemName></ContentItem>`
    + '<playStatus>STOP_STATE</playStatus></nowPlaying>';
  test.use({ phoneOverrides: { '/api/status': { body: STOPPED, contentType: 'application/xml' }, '/spotify/info': { body: INFO } } });

  test('still says it, from one read per change', async ({ phone }) => {
    const { page, handler } = phone;
    await expect(notice(page)).toHaveText(NAMED);
    // The status poll runs every five seconds; the stopped speaker is asked
    // about Spotify once, not on every poll.
    await page.waitForTimeout(11_000);
    const reads = handler.calls.filter((c) => new URL(c.url).pathname === '/spotify/info').length;
    expect(reads).toBe(1);
  });
});

test.describe('phone: German', () => {
  test.use({ phoneLang: 'de', phonePlaying: STR_SPOTIFY, phoneOverrides: { '/spotify/info': { body: { ...INFO, accountRefusedName: '' } } } });

  test('without a name it speaks about this account', async ({ phone }) => {
    await expect(notice(phone.page)).toHaveText('Spotify sperrt dieses Konto für (STR). Der Spotify-Eintrag des Lautsprechers ohne (STR) spielt es weiterhin.');
  });
});

test.describe('phone: nothing to say', () => {
  test.use({ phonePlaying: STR_SPOTIFY, phoneOverrides: { '/spotify/info': { body: { ...INFO, accountRefused: false, accountRefusedName: '' } } } });

  test('an account that plays gets no notice', async ({ phone }) => {
    const { page } = phone;
    await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
    await expect(page.locator('#statusCard')).toContainText('Morning Light');
    await expect(notice(page)).toBeHidden();
  });
});

test.describe('phone: radio', () => {
  test.use({ phoneOverrides: { '/spotify/info': { body: INFO } } });

  test('radio is not Spotify: no notice', async ({ phone }) => {
    const { page } = phone;
    await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
    await expect(page.locator('#statusCard')).toContainText('Best Of Rock.FM');
    await expect(notice(page)).toBeHidden();
  });
});
