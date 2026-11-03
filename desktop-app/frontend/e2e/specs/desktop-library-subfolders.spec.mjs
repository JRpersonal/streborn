// The Library's folder of folders (#1682) and its sort order (#1257), in both
// desktop layouts.
//
// File Index > Ada Demo Trio is an artist folder with one folder per album and
// no tracks of its own (testkit/demo-data.mjs LIBRARY_TREE c6a). "Play folder"
// and "Put folder on a key" are offered there and take every album's tracks,
// each album in its track order, the albums in the order on screen. The server
// lists "Night Lines" before "_Live in Demo City"; A to Z turns them around and
// is remembered.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-library-subfolders.spec.mjs
//   E2E_SHOTS=<dir> also saves screenshots of the new controls there.

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle } from '../helpers/clock.mjs';
import { en } from '../helpers/i18n.mjs';
import { LIBRARY_TREE, MEDIA_SERVERS, ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;
const NAS = MEDIA_SERVERS[0].friendlyName;
const NIGHT = LIBRARY_TREE.c6a2.items.map((i) => i.streamURL);
const LIVE = LIBRARY_TREE.c6a1.items.map((i) => i.streamURL);
const SHOTS = process.env.E2E_SHOTS || '';

async function shot(page, name) {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: false });
}

test.describe('UI 2.0 Library: a folder of folders and A to Z', () => {
  test.use({ ui2: true, fakeClock: true });

  const lib = (page) => page.locator('#ui2MainInner > #view-library');
  const heading = (page) => lib(page).locator('#lib2LevelTitle');
  const folder = (page, name) => lib(page).locator('.lib2-folder', { hasText: name });

  async function openArtist(page) {
    await page.locator('#ui2Tabs .ui2-nav-item[data-view="library"]').click();
    await expect(heading(page)).toHaveText(NAS);
    // The server root has no folder actions: its first level is Music, Videos.
    await expect(lib(page).locator('.lib2-play-folder')).toHaveCount(0);
    await folder(page, 'File Index').click();
    await expect(heading(page)).toHaveText('File Index');
    await folder(page, 'Ada Demo Trio').click();
    await expect(heading(page)).toHaveText('Ada Demo Trio');
  }

  test.beforeEach(async ({ app }) => {
    await settle(app.page, 2_000);
  });

  test('1. plays every album of an artist folder, each in track order', async ({ app, mock }) => {
    const { page } = app;
    await openArtist(page);
    await expect(lib(page).locator('.lib2-play-folder')).toHaveText(en('ui2.library.playFolderOn', { name: LR.friendlyName }));
    await expect(lib(page).locator('.lib2-key-folder')).toBeVisible();
    await expect(lib(page).locator('.lib2-folder-acts .lib2-hint')).toHaveText(en('library.subfoldersHint', { depth: 3 }));
    await shot(page, 'library-ui2-artist-folder-actions');

    await mock.resetCalls();
    await lib(page).locator('.lib2-play-folder').click();
    await mock.expectCalled('StartQueue', { times: 1, pred: onHost(LR.host) });
    const payload = JSON.parse((await mock.last('StartQueue')).args[2]);
    expect(payload.items.map((i) => i.url)).toEqual([...NIGHT, ...LIVE]);
    // The card names the artist folder, so Recently played replays the tree.
    expect(payload.card.key).toBe(`queue:${MEDIA_SERVERS[0].udn}:c6a`);
    expect(payload.card.name).toBe('Ada Demo Trio');
  });

  test('2. A to Z puts "_" first, plays in that order and is remembered', async ({ app, mock }) => {
    const { page } = app;
    await openArtist(page);
    const sort = lib(page).locator('#libSort');
    await expect(sort).toHaveAccessibleName(en('library.sortLabel'));
    await expect(sort).toHaveValue('server');
    await expect(lib(page).locator('.lib2-folder .lib2-name')).toHaveText(['Night Lines', '_Live in Demo City']);
    await sort.selectOption('az');
    await expect(lib(page).locator('.lib2-folder .lib2-name')).toHaveText(['_Live in Demo City', 'Night Lines']);
    await expect(lib(page).locator('#libSort')).toHaveValue('az');
    expect(await page.evaluate(() => localStorage.getItem('str.library.sort'))).toBe('az');
    await shot(page, 'library-ui2-sort-a-to-z');

    await mock.resetCalls();
    await lib(page).locator('.lib2-play-folder').click();
    await mock.expectCalled('StartQueue', { times: 1 });
    const payload = JSON.parse((await mock.last('StartQueue')).args[2]);
    expect(payload.items.map((i) => i.url)).toEqual([...LIVE, ...NIGHT]);

    // An album's tracks keep their track order under A to Z.
    await folder(page, 'Night Lines').click();
    await expect(heading(page)).toHaveText('Night Lines');
    await expect(lib(page).locator('.lib2-track .lib2-name')).toHaveText(['Late Train', 'Blue Hour']);
  });

  test('3. puts the whole artist on a key, naming the folder it came from', async ({ app, mock }) => {
    const { page } = app;
    await openArtist(page);
    await lib(page).locator('.lib2-key-folder').click();
    await expect(page.locator('#pickModal')).toBeVisible();
    await page.locator('#pickGrid .pick-slot').nth(2).click();
    await mock.expectCalled('SaveFolderPreset', { times: 1, pred: (c) => c.args[0] === LR.host && c.args[2] === 3 });
    const p = JSON.parse((await mock.last('SaveFolderPreset')).args[3]);
    expect(p.type).toBe('queue');
    expect(p.name).toBe('Ada Demo Trio');
    expect(p.items.map((i) => i.url)).toEqual([...NIGHT, ...LIVE]);
    expect(p.folder).toEqual({ server_udn: MEDIA_SERVERS[0].udn, container_id: 'c6a' });
    await expect(lib(page).locator('.lib2-folder-acts .lib2-status').first()).toHaveText(en('ui2.library.onKey', { n: 3, name: LR.friendlyName }));
  });
});

test.describe('the classic Library: a folder of folders and A to Z', () => {
  test.use({ ui2: false });

  test('4. offers the folder actions with their note and the order picker', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="library"]').click();
    const v = page.locator('#view-library');
    await expect(v.locator('#libServerSelect')).toBeVisible();
    await expect(v.locator('.lib-play-folder-btn')).toHaveCount(0);
    await v.locator('.library-row-folder', { hasText: 'File Index' }).click();
    await v.locator('.library-row-folder', { hasText: 'Ada Demo Trio' }).click();
    await expect(v.locator('.library-crumb-active')).toHaveText('Ada Demo Trio');
    await expect(v.locator('.lib-play-folder-btn')).toBeVisible();
    await expect(v.locator('.lib-save-folder-btn')).toBeVisible();
    await expect(v.locator('.library-subfolders-hint')).toHaveText(en('library.subfoldersHint', { depth: 3 }));
    await v.locator('#libSort').selectOption('az');
    await expect(v.locator('.library-row-folder .library-title')).toHaveText(['_Live in Demo City', 'Night Lines']);
    await shot(page, 'library-classic-artist-folder-a-to-z');

    await mock.resetCalls();
    await v.locator('.lib-play-folder-btn').click();
    await mock.expectCalled('StartQueue', { times: 1 });
    const payload = JSON.parse((await mock.last('StartQueue')).args[2]);
    expect(payload.items.map((i) => i.url)).toEqual([...LIVE, ...NIGHT]);
  });
});
