// The UI 2.0 player's "Show in Radio / Library / Spotify" button: next to the
// player's title, it opens the tab the music that plays comes from, and for a
// music-library folder the folder itself. A source no tab holds (Bluetooth,
// AUX, TV) gets no button. The classic layout has none.
//
//   E2E_PORT=4182 npx playwright test specs/desktop-ui2-go-to-source.spec.mjs --project=desktop

import { test, expect } from '../fixtures.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { LIBRARY_TREE, MEDIA_SERVERS, ROSTER, statusXml } from '../../testkit/demo-data.mjs';

const NAS = MEDIA_SERVERS[0];
const [OPENING, BLUE] = LIBRARY_TREE.c1a.items;

const goBtn = (page) => page.locator('#ui2Player #ui2GoSource');
const nav = (page, view) => page.locator(`#ui2Tabs .ui2-nav-item[data-view="${view}"]`);
const lib = (page) => page.locator('#ui2MainInner > #view-library');
const heading = (page) => lib(page).locator('#lib2LevelTitle');
const crumbs = (page) => lib(page).locator('.lib2-crumbs > li');

// The status poll runs on the page clock: long enough for one more round of
// its slowest cadence (main.js STATUS_IDLE_MS, the one it starts with).
const POLL_MS = 16_000;

async function expectOn(page, view) {
  await expect(nav(page, view)).toHaveAttribute('aria-current', 'page');
  await expect(page.locator(`#view-${view}`)).toBeVisible();
}

test.describe('UI 2.0 go to source: a station', () => {
  test.use({ ui2: true, fakeClock: true });

  test.beforeEach(async ({ app }) => {
    await settle(app.page, 2_000);
  });

  test('1. names Radio next to the title and opens it, by mouse and by keyboard', async ({ app }) => {
    const { page } = app;
    const btn = goBtn(page);
    await expect(btn).toBeVisible();
    await expect(btn).toHaveText(en('ui2.nav.radio'));
    await expect(btn).toHaveAccessibleName(en('ui2.player.goRadio'));
    await expect(btn).toHaveAttribute('title', en('ui2.player.goRadio'));
    // It shares the row with the title line, after it.
    const line = await page.locator('#ui2Player #statusBar').boundingBox();
    const box = await btn.boundingBox();
    expect(box.x).toBeGreaterThan(line.x);
    expect(Math.abs((box.y + box.height / 2) - (line.y + line.height / 2))).toBeLessThan(box.height);
    expect(box.height).toBeGreaterThanOrEqual(40);

    await nav(page, 'recent').click();
    await expectOn(page, 'recent');
    await btn.click();
    await expectOn(page, 'box');

    await nav(page, 'spotify').click();
    await expectOn(page, 'spotify');
    await btn.focus();
    await expect(btn).toBeFocused();
    await page.keyboard.press('Enter');
    await expectOn(page, 'box');
  });

  test('2. leaves with a speaker that goes to Bluetooth and comes back with a station', async ({ app, mock }) => {
    const { page } = app;
    await expect(goBtn(page)).toBeVisible();
    await mock.set('Status', { value: statusXml({ source: 'BLUETOOTH', itemName: '', location: '' }) });
    await runFor(page, POLL_MS);
    await expect(goBtn(page)).toBeHidden();
    await mock.set('Status', { value: statusXml() });
    await runFor(page, POLL_MS);
    await expect(goBtn(page)).toBeVisible();
    await expect(goBtn(page)).toHaveText(en('ui2.nav.radio'));
  });
});

test.describe('UI 2.0 go to source: no tab for AUX', () => {
  test.use({ ui2: true, fakeClock: true, scenario: { bindings: { Status: { value: statusXml({ source: 'AUX', itemName: 'AUX', location: '' }) } } } });

  test('3. shows no button for the AUX input', async ({ app }) => {
    const { page } = app;
    await settle(page, 2_000);
    await expect(page.locator('#ui2Player #statusMain')).toContainText('AUX');
    await expect(goBtn(page)).toBeHidden();
  });
});

test.describe('UI 2.0 go to source: Spotify', () => {
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: { bindings: { Status: { value: statusXml({ source: 'SPOTIFY', itemName: 'Demo Mix', location: '/playback/container/c3BvdGlmeTpwbGF5bGlzdDpkZW1v' }) } } },
  });

  test('4. names Spotify and opens it', async ({ app }) => {
    const { page } = app;
    await settle(page, 2_000);
    const btn = goBtn(page);
    await expect(btn).toHaveText(en('nav.spotify'));
    await expect(btn).toHaveAccessibleName(en('ui2.player.goSpotify'));
    await btn.click();
    await expectOn(page, 'spotify');
  });
});

// The folder Evening Sessions plays as a queue on the current speaker, its
// second track now. albums overrides what the queue says each track's album is.
const FOLDER_STATUS = { value: statusXml({ source: 'UPNP', itemName: BLUE.title, location: BLUE.streamURL }) };
const folderQueue = (albums = [OPENING.album, BLUE.album]) => ({
  active: true,
  pos: 1,
  shuffle: false,
  repeat: 'off',
  items: [OPENING, BLUE].map((it, i) => ({ url: it.streamURL, title: it.title, artist: it.artist, album: albums[i] })),
  url: BLUE.streamURL,
  card: `queue:${NAS.udn}:c1a`,
  preset_rev: 1,
});

test.describe('UI 2.0 go to source: a folder that plays as a queue', () => {
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: { bindings: { Status: FOLDER_STATUS, GetQueue: { value: folderQueue() } } },
  });

  test('5. opens the folder it was started from, under its trail', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 2_000);
    const btn = goBtn(page);
    await expect(btn).toHaveText(en('nav.library'));
    await expect(btn).toHaveAccessibleName(en('ui2.player.goLibrary'));
    await btn.click();
    await expectOn(page, 'library');
    await expect(heading(page)).toHaveText('Evening Sessions');
    expect((await mock.last('BrowseLibrary')).args).toEqual(expect.arrayContaining([NAS.udn, 'c1a']));
    // Never walked this session: the server root, then the folder.
    await expect(crumbs(page)).toHaveText([en('ui2.library.servers'), NAS.friendlyName, 'Evening Sessions']);
    await expect(lib(page).locator('.lib2-track')).toHaveCount(3);
  });

  test('6. keeps the whole trail of a folder walked before', async ({ app }) => {
    const { page } = app;
    await settle(page, 2_000);
    await nav(page, 'library').click();
    await expect(heading(page)).toHaveText(NAS.friendlyName);
    await lib(page).locator('.lib2-folder', { hasText: 'Music' }).click();
    await lib(page).locator('.lib2-folder', { hasText: 'Evening Sessions' }).click();
    await expect(heading(page)).toHaveText('Evening Sessions');
    // Elsewhere in the Library, then elsewhere in the window.
    await lib(page).locator('.lib2-crumb', { hasText: NAS.friendlyName }).click();
    await expect(heading(page)).toHaveText(NAS.friendlyName);
    await nav(page, 'box').click();
    await expectOn(page, 'box');

    await goBtn(page).click();
    await expectOn(page, 'library');
    await expect(heading(page)).toHaveText('Evening Sessions');
    await expect(crumbs(page)).toHaveText([en('ui2.library.servers'), NAS.friendlyName, 'Music', 'Evening Sessions']);
  });
});

test.describe('UI 2.0 go to source: a folder of many albums', () => {
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: { bindings: { Status: FOLDER_STATUS, GetQueue: { value: folderQueue(['Night Lines', 'Live in Demo City']) } } },
  });

  test('7. names the folder as its server does, not after the album that plays', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 2_000);
    await goBtn(page).click();
    await expectOn(page, 'library');
    await mock.expectCalled('LibraryFolder', { pred: (c) => c.args[0] === NAS.udn && c.args[1] === 'c1a' });
    await expect(heading(page)).toHaveText('Evening Sessions');
    await expect(crumbs(page)).toHaveText([en('ui2.library.servers'), NAS.friendlyName, 'Evening Sessions']);
  });
});

test.describe('UI 2.0 go to source: pressed twice while the servers load', () => {
  // The Library was last left in another folder of the same server, and the
  // search for servers takes a while.
  const remembered = [{ id: '0', title: NAS.friendlyName }, { id: 'c6', title: 'File Index' }, { id: 'c6a', title: 'Ada Demo Trio' }];
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: {
      seed: {
        extraStorage: {
          'str.library.lastServerUDN': NAS.udn,
          'str.library.lastPath': JSON.stringify({ udn: NAS.udn, stack: remembered }),
        },
      },
      bindings: { Status: FOLDER_STATUS, GetQueue: { value: folderQueue() }, ListMediaServers: { delayMs: 3_000 } },
    },
  });

  test('8. opens the folder that plays, not the one the Library was left in', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 2_000);
    const btn = goBtn(page);
    await expect(btn).toHaveText(en('nav.library'));
    await btn.click();
    await expectOn(page, 'library');
    await btn.press('Enter');
    await runFor(page, 4_000);
    await expect(heading(page)).toHaveText('Evening Sessions');
    await runFor(page, 2_000);
    await expect(heading(page)).toHaveText('Evening Sessions');
    // One search for the servers, joined by the second press.
    expect(await mock.count('ListMediaServers')).toBe(1);
    expect((await mock.last('BrowseLibrary')).args).toEqual(expect.arrayContaining([NAS.udn, 'c1a']));
  });
});

test.describe('UI 2.0 go to source: another speaker chosen', () => {
  // The bedroom speaker has no queue to read (its reads fail) and plays a
  // file another app sent it.
  const BR = ROSTER.bedroom.host;
  test.use({
    ui2: true,
    fakeClock: true,
    scenario: {
      bindings: {
        Status: { ...FOLDER_STATUS, byHost: { [BR]: { value: statusXml({ source: 'UPNP', itemName: 'Elsewhere', location: 'http://192.0.2.70/other.mp3' }) } } },
        GetQueue: { value: folderQueue(), byHost: { [BR]: { error: 'not found' } } },
      },
    },
  });

  test('9. does not lead to the folder the speaker before it plays', async ({ app }) => {
    const { page } = app;
    await settle(page, 2_000);
    await expect(goBtn(page)).toHaveText(en('nav.library'));
    await page.locator(`#ui2SpkLists .ui2-spk-pick[data-host="${BR}"]`).click();
    await runFor(page, POLL_MS);
    await expect(page.locator('#ui2Player #statusMain')).toContainText('Elsewhere');
    await expect(goBtn(page)).toBeHidden();
  });
});

test.describe('UI 2.0 go to source: a single track from the Library', () => {
  test.use({ ui2: true, fakeClock: true });

  test('10. leads back to the folder the track was listed in', async ({ app, mock }) => {
    const { page } = app;
    await settle(page, 2_000);
    await nav(page, 'library').click();
    await lib(page).locator('.lib2-folder', { hasText: 'Music' }).click();
    await lib(page).locator('.lib2-folder', { hasText: 'Evening Sessions' }).click();
    await expect(heading(page)).toHaveText('Evening Sessions');
    await lib(page).locator('.lib2-track', { hasText: OPENING.title }).locator('.lib2-play').click();
    await mock.expectCalled('PlayURL');
    await runFor(page, 2_500);
    const btn = goBtn(page);
    await expect(btn).toHaveText(en('nav.library'));

    await nav(page, 'recent').click();
    await expectOn(page, 'recent');
    await btn.click();
    await expectOn(page, 'library');
    await expect(heading(page)).toHaveText('Evening Sessions');
    await expect(crumbs(page)).toHaveText([en('ui2.library.servers'), NAS.friendlyName, 'Music', 'Evening Sessions']);
  });
});

test.describe('the classic layout', () => {
  test('11. has no go to source button', async ({ app }) => {
    const { page } = app;
    await expect(page.locator('#statusBar')).toBeVisible();
    await expect(page.locator('#ui2GoSource')).toHaveCount(0);
  });
});
