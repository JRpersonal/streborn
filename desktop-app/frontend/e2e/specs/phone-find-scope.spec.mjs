// phone-find-scope: the Find tab of the phone remote while the music library
// is still being searched, and the "Search in" picker that chooses what a
// search covers (discussion #1710). Station hits came back at once and the
// library ones 15 to 20 s later with nothing on screen in between, so people
// left before their music turned up. The library section now says it is
// still searching, and the picker under the search field (album, artist and
// track since #1325) also offers stations only and the music library only.

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const SONG = { title: 'Morning Light', artist: 'The Demo Band', album: 'Demo Album', server: 'Living Room NAS',
  url: 'http://192.0.2.60:9790/music/morning-light.flac', mime: 'audio/flac' };

// holdLibrary answers the library search only when release() is called, and
// records every request it got.
async function holdLibrary(page, answer) {
  const seen = [];
  let release;
  const gate = new Promise((r) => { release = r; });
  await page.route('**/api/library/search**', async (route) => {
    seen.push(new URL(route.request().url()));
    await gate;
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(answer) });
  });
  return { seen, release: () => release() };
}

async function openFind(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  await page.locator('#tabBrowse').click();
  await expect(page.locator('#panelBrowse')).toBeVisible();
  // The picker shows once the server list said a music server is known.
  await expect(page.locator('#libField')).toBeVisible();
}

const radioCalls = (handler) => handler.calls.filter((c) => /radio-browser\.info/.test(c.url)).length;

test.describe('Find, a music server known', () => {
  test('says the library is still being searched, then shows its hits', async ({ phone }) => {
    const { page } = phone;
    const lib = await holdLibrary(page, { registered: 1, results: [SONG] });
    await openFind(page);
    await expect(page.locator('#libField')).toHaveValue('any');
    await page.locator('#q').fill('light');
    await page.locator('#btnSearch').click();
    await expect(page.locator('#results .station')).toHaveCount(PHONE.STATIONS.length);
    await expect(page.locator('#lblLibSec')).toHaveText('From your music library');
    await expect(page.locator('#libMsg')).toHaveText('Searching your music library…');
    lib.release();
    await expect(page.locator('#libResults .station')).toHaveCount(1);
    await expect(page.locator('#libResults .station')).toContainText('Morning Light');
    await expect(page.locator('#libMsg')).toBeHidden();
  });

  test('a miss replaces the searching line with the nothing-found note', async ({ phone }) => {
    const { page } = phone;
    const lib = await holdLibrary(page, { registered: 1, results: [] });
    await openFind(page);
    await page.locator('#q').fill('nothing');
    await page.locator('#btnSearch').click();
    await expect(page.locator('#libMsg')).toHaveText('Searching your music library…');
    lib.release();
    await expect(page.locator('#libMsg')).toHaveText('Nothing found on your media servers.');
  });

  test('stations only skips the library, and the choice is kept', async ({ phone }) => {
    const { page, handler } = phone;
    const lib = await holdLibrary(page, { registered: 1, results: [SONG] });
    lib.release();
    await openFind(page);
    await page.locator('#libField').selectOption('stations');
    await page.locator('#q').fill('rock');
    await page.locator('#btnSearch').click();
    await expect(page.locator('#results .station')).toHaveCount(PHONE.STATIONS.length);
    expect(lib.seen.length, 'the library was searched').toBe(0);
    await expect(page.locator('#lblLibSec')).toBeHidden();
    expect(radioCalls(handler)).toBeGreaterThan(0);
    await page.reload();
    await openFind(page);
    await expect(page.locator('#libField')).toHaveValue('stations');
  });

  test('the library by album skips the stations', async ({ phone }) => {
    const { page, handler } = phone;
    const lib = await holdLibrary(page, { registered: 1, results: [SONG] });
    lib.release();
    await openFind(page);
    await page.locator('#libField').selectOption('album');
    await page.locator('#q').fill('demo');
    await page.locator('#btnSearch').click();
    await expect(page.locator('#libResults .station')).toHaveCount(1);
    expect(lib.seen.at(-1).searchParams.get('field')).toBe('album');
    expect(radioCalls(handler), 'the station directory was searched').toBe(0);
    await expect(page.locator('#results .station')).toHaveCount(0);
  });
});

test.describe('Find in German at 320px', () => {
  test.use({ phoneLang: 'de', viewport: { width: 320, height: 700 } });

  test('the picker reads German and the tab does not scroll sideways', async ({ phone }) => {
    const { page } = phone;
    const lib = await holdLibrary(page, { registered: 1, results: [] });
    await openFind(page);
    await expect(page.locator('#lblLibIn')).toHaveText('Suchen in');
    await expect(page.locator('#libField option[value="stations"]')).toHaveText('Nur Sender');
    await page.locator('#q').fill('jazz');
    await page.locator('#btnSearch').click();
    await expect(page.locator('#libMsg')).toHaveText('Deine Musikbibliothek wird durchsucht…');
    const w = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(w, 'the Find tab scrolls sideways').toBeLessThanOrEqual(320);
    lib.release();
  });
});

test.describe('Find, no music server', () => {
  test.use({ phoneOverrides: { '/api/library/servers': { body: [] } } });

  test('has no picker and no searching line', async ({ phone }) => {
    const { page } = phone;
    const lib = await holdLibrary(page, { registered: 0, results: [] });
    await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
    await page.locator('#tabBrowse').click();
    await page.locator('#q').fill('rock');
    await page.locator('#btnSearch').click();
    await expect(page.locator('#results .station')).toHaveCount(PHONE.STATIONS.length);
    await expect(page.locator('#libFieldRow')).toBeHidden();
    await expect(page.locator('#libMsg')).toBeHidden();
    lib.release();
  });
});
