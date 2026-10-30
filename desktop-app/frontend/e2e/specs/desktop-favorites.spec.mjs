// desktop-favorites: the Favorites list of the classic "Listen to music"
// screen (discussion #1521). A library playlist or album starred from Recently
// played shows neither a "- kbit/s" placeholder nor a red "not reachable" dot,
// a radio-browser station keeps its real check dot (red included) and its
// bitrate when one is known, and the Top list / Favorites buttons show which
// list is open; a second click on the pressed button closes that list again.
//
//   E2E_PORT=4190 npx playwright test specs/desktop-favorites.spec.mjs

import { test, expect } from '../fixtures.mjs';

// The shapes favorites.js stores: a library item starred from Recently played
// carries no lastcheckok at all, a radio-browser station carries its 0/1.
const FAVORITES = [
  { stationuuid: 'lib:demo-playlist', name: 'Road Trip Playlist', url: 'http://192.0.2.60:8200/playlist', bitrate: 0, starredAt: 1 },
  { stationuuid: '9617a958-0601-11e8-ae97-52543be04c81', name: 'Unrated Radio', url: 'http://example.com/unrated', bitrate: 0, countrycode: 'DE', country: 'Germany', lastcheckok: 1, starredAt: 2 },
  { stationuuid: 'lib:demo-album', name: 'Demo Album', url: 'http://192.0.2.60:8200/album', bitrate: 0, starredAt: 3 },
  { stationuuid: '0a1b2c3d-0601-11e8-ae97-52543be04c81', name: 'Broken Radio', url: 'http://example.com/broken', bitrate: 128, lastcheckok: 0, starredAt: 4 },
];

test.use({ ui2: false, scenario: { seed: { extraStorage: { 'str.favStations': JSON.stringify(FAVORITES) } } } });

const row = (page, name) => page.locator('#searchResults .result-row').filter({ hasText: name });

test('favourites: no "kbit/s" and no dot on library items, real dots on stations', async ({ app }) => {
  const { page } = app;
  await page.locator('#favModeBtn').click();
  await expect(page.locator('#searchResults .result-row')).toHaveCount(FAVORITES.length);

  for (const name of ['Road Trip Playlist', 'Demo Album']) {
    await expect(row(page, name).locator('.result-meta')).not.toContainText('kbit/s');
    await expect(row(page, name).locator('.result-online-dot')).toHaveCount(0);
  }
  // A station with no known bitrate: the cell is left out, the check dot stays.
  await expect(row(page, 'Unrated Radio').locator('.result-meta')).not.toContainText('kbit/s');
  await expect(row(page, 'Unrated Radio').locator('.result-online-dot.ok')).toHaveCount(1);
  // A station radio-browser found broken still says so.
  await expect(row(page, 'Broken Radio').locator('.result-meta')).toContainText('128 kbit/s');
  await expect(row(page, 'Broken Radio').locator('.result-online-dot.bad')).toHaveCount(1);
  await expect(page.locator('#searchResults')).not.toContainText('- kbit/s');
});

test('the Favorites and Top list buttons show which list is open', async ({ app }) => {
  const { page } = app;
  const fav = page.locator('#favModeBtn');
  const top = page.locator('#topBtn');
  // Nothing shown yet: nothing pressed, not even the default Top mode.
  await expect(fav).toHaveAttribute('aria-pressed', 'false');
  await expect(top).toHaveAttribute('aria-pressed', 'false');

  await fav.click();
  await expect(page.locator('#searchResults .result-row')).toHaveCount(FAVORITES.length);
  await expect(fav).toHaveAttribute('aria-pressed', 'true');
  await expect(fav).toHaveClass(/\bmode-pressed\b/);
  await expect(top).toHaveAttribute('aria-pressed', 'false');

  await top.click();
  await expect(top).toHaveAttribute('aria-pressed', 'true');
  await expect(fav).toHaveAttribute('aria-pressed', 'false');
  await expect(fav).not.toHaveClass(/\bmode-pressed\b/);

  // A name search is no list button: both released.
  await page.locator('#searchQ').fill('rock');
  await page.locator('#searchBtn').click();
  await expect(top).toHaveAttribute('aria-pressed', 'false');
  await expect(fav).toHaveAttribute('aria-pressed', 'false');
  await expect(page.locator('#searchBtn')).not.toHaveAttribute('aria-pressed', /.*/);
});

test('a second click on the pressed list button closes its list (#1521)', async ({ app }) => {
  const { page } = app;
  const fav = page.locator('#favModeBtn');
  const top = page.locator('#topBtn');
  const rows = page.locator('#searchResults .result-row');

  // Open Favorites, close it with a second click.
  await fav.click();
  await expect(rows).toHaveCount(FAVORITES.length);
  await expect(fav).toHaveAttribute('aria-pressed', 'true');
  await fav.click();
  await expect(fav).toHaveAttribute('aria-pressed', 'false');
  await expect(fav).not.toHaveClass(/\bmode-pressed\b/);
  await expect(page.locator('#searchResults')).toBeEmpty();
  await expect(page.locator('#searchCount')).toBeHidden();

  // The Top list toggles the same way.
  await top.click();
  await expect(top).toHaveAttribute('aria-pressed', 'true');
  await expect(rows.first()).toBeVisible();
  await top.click();
  await expect(top).toHaveAttribute('aria-pressed', 'false');
  await expect(page.locator('#searchResults')).toBeEmpty();
  await expect(page.locator('#loadMoreRow')).toBeHidden();

  // With one list open, another list's button switches to that list.
  await top.click();
  await expect(top).toHaveAttribute('aria-pressed', 'true');
  await expect(rows.first()).toBeVisible();
  await fav.click();
  await expect(fav).toHaveAttribute('aria-pressed', 'true');
  await expect(top).toHaveAttribute('aria-pressed', 'false');
  await expect(rows).toHaveCount(FAVORITES.length);

  // Keyboard: Enter on the focused pressed button closes it, Space reopens it.
  await fav.focus();
  await page.keyboard.press('Enter');
  await expect(fav).toHaveAttribute('aria-pressed', 'false');
  await expect(page.locator('#searchResults')).toBeEmpty();
  await page.keyboard.press('Space');
  await expect(fav).toHaveAttribute('aria-pressed', 'true');
  await expect(rows).toHaveCount(FAVORITES.length);

  // A closed list stays closed when a filter only re-renders the panel.
  await fav.click();
  await expect(page.locator('#searchResults')).toBeEmpty();
  await page.locator('#searchOnlyBose').click();
  await expect(page.locator('#searchResults')).toBeEmpty();

  // A name search after a closed list still shows its results.
  await page.locator('#searchQ').fill('rock');
  await page.locator('#searchBtn').click();
  await expect(rows.first()).toBeVisible();
});
