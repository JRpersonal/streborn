// The phone remote's library browser: a folder of folders plays through the
// speaker (#1682) and the list can be ordered A to Z (#1257).
//
// An artist folder with two album folders and no tracks of its own. The
// server lists "Night Lines" before "_Live in Demo City".
//
//   E2E_PORT=4184 npx playwright test specs/phone-library-subfolders.spec.mjs
//   E2E_SHOTS=<dir> also saves screenshots of the new controls there.

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const SHOTS = process.env.E2E_SHOTS || '';
const SRV = PHONE.LIB_SERVERS[0];

const FOLDERS = {
  '0': { folders: [{ id: 'art', title: 'Ada Demo Trio', childCount: 2 }], tracks: [] },
  art: {
    folders: [
      { id: 'night', title: 'Night Lines', childCount: 2, class: 'object.container.album.musicAlbum' },
      { id: 'live', title: '_Live in Demo City', childCount: 2, class: 'object.container.album.musicAlbum' },
    ],
    tracks: [],
  },
  night: {
    folders: [],
    tracks: [
      { title: 'Late Train', artist: 'Ada Demo Trio', album: 'Night Lines', url: 'http://192.0.2.60:8200/31.flac', mime: 'audio/flac', trackNumber: 1 },
      { title: 'Blue Hour', artist: 'Ada Demo Trio', album: 'Night Lines', url: 'http://192.0.2.60:8200/32.flac', mime: 'audio/flac', trackNumber: 2 },
    ],
  },
};

async function routeLibrary(page, playReply) {
  const plays = [];
  await page.route('**/api/library/browse*', (route) => {
    const id = new URL(route.request().url()).searchParams.get('id') || '0';
    const f = FOLDERS[id] || { folders: [], tracks: [] };
    const body = { server: SRV.friendlyName, ...f, total: f.folders.length + f.tracks.length, start: 0, count: f.folders.length + f.tracks.length };
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  });
  await page.route('**/api/library/play-folder', (route) => {
    plays.push(JSON.parse(route.request().postData() || '{}'));
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(playReply) });
  });
  return plays;
}

async function openArtist(page) {
  await page.locator('#tabBrowse').click();
  await expect(page.locator('#panelBrowse')).toBeVisible();
  // The one registered server (named by its id: the list carries no name).
  await page.locator('#browseSrv button').first().click();
  await expect(page.locator('#browseList .station', { hasText: 'Ada Demo Trio' })).toBeVisible();
  // The server root has no folder play: its first level is Music, Videos.
  await expect(page.locator('#btnBrowseTree')).toHaveCount(0);
  await page.locator('#browseList .station', { hasText: 'Ada Demo Trio' }).locator('button').click();
  await expect(page.locator('#browseList .station .sname', { hasText: 'Night Lines' })).toBeVisible();
}

const names = (page) => page.locator('#browseList .station .sname').evaluateAll((els) => els.map((e) => e.firstChild.nodeValue));

test.describe('phone library browser', () => {
  test('1. a folder of folders plays through the speaker, in the order on screen', async ({ phone }) => {
    const { page } = phone;
    const plays = await routeLibrary(page, { status: 'ok', tracks: 4, folders: 3, subfolders: true, cut: false, cutDepth: false, maxDepth: 3 });
    await openArtist(page);
    await expect(page.locator('#btnBrowseTree')).toBeVisible();
    await expect(page.locator('#browseList .sum')).toContainText('levels down');
    await expect(page.locator('#browseSort')).toHaveValue('server');
    expect(await names(page)).toEqual(['Night Lines', '_Live in Demo City']);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/phone-library-artist-folder.png` });

    await page.locator('#browseSort').selectOption('az');
    expect(await names(page)).toEqual(['_Live in Demo City', 'Night Lines']);
    expect(await page.evaluate(() => localStorage.getItem('strLibSort'))).toBe('az');
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/phone-library-sort-a-to-z.png` });

    await page.locator('#btnBrowseTree').click();
    await expect.poll(() => plays.length).toBe(1);
    expect(plays[0]).toMatchObject({ udn: SRV.udn, id: 'art', name: 'Ada Demo Trio', sort: 'az' });
    // A whole tree moves on to Play like any folder start.
    await expect(page.locator('#panelPlay')).toBeVisible();
  });

  test('2. says when the speaker had to leave part of a big tree out', async ({ phone }) => {
    const { page } = phone;
    await routeLibrary(page, { status: 'ok', tracks: 1000, folders: 120, subfolders: true, cut: true, cutDepth: true, maxDepth: 3 });
    await openArtist(page);
    await page.locator('#btnBrowseTree').click();
    await expect(page.locator('#browseTreeMsg')).toHaveText(
      'This folder is very big, so only the first 1000 tracks are included. Folders more than 3 levels down are left out.');
    await expect(page.locator('#panelBrowse')).toBeVisible();
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/phone-library-tree-cut-note.png` });
  });

  test('3. an album keeps its track order under A to Z', async ({ phone }) => {
    const { page } = phone;
    await routeLibrary(page, { status: 'ok' });
    await openArtist(page);
    await page.locator('#browseSort').selectOption('az');
    await page.locator('#browseList .station', { hasText: 'Night Lines' }).locator('button').click();
    await expect(page.locator('#browseList .station .sname', { hasText: 'Late Train' })).toBeVisible();
    expect(await names(page)).toEqual(['Late Train', 'Blue Hour']);
    // A folder with tracks keeps the ordinary folder play.
    await expect(page.locator('#btnBrowseTree')).toHaveCount(0);
  });
});
