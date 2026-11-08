// desktop-favorites-order: moving the starred stations (#1519) and starring a
// Pandora station (#1663), in the classic layout and in UI 2.0.
//
// A move changes the stored order and pushes it to the speakers with the time
// of the move; a Pandora station gets a star in the Pandora list, lands in
// Favourites with its station reference and plays through the Pandora call.
//
//   E2E_PORT=4191 npx playwright test specs/desktop-favorites-order.spec.mjs

import { test, expect } from '../fixtures.mjs';

const FAVORITES = [
  { stationuuid: 'u-1', name: 'First Radio', url: 'http://example.com/first', bitrate: 128, starredAt: 1 },
  { stationuuid: 'u-2', name: 'Second Radio', url: 'http://example.com/second', bitrate: 128, starredAt: 2 },
  { stationuuid: 'u-3', name: 'Third Radio', url: 'http://example.com/third', bitrate: 128, starredAt: 3 },
];

const PANDORA_READY = {
  standIn: { services: [{ type: 'PANDORA', mode: 'us', registered: true }] },
  boxSources: { PANDORA: { hasAccount: 'true', status: 'READY' } },
};
const PANDORA_STATIONS = {
  status: 'READY',
  stations: [
    { name: 'Jazz Radio', location: '111', type: 'stationurl', art: 'https://img.example.com/111.jpg', presetable: true },
    { name: 'Rock Radio', location: '222', type: 'stationurl', presetable: true },
  ],
};

const SEED = { seed: { extraStorage: { 'str.favStations': JSON.stringify(FAVORITES) } } };

const storedIds = (page) => page.evaluate(() => JSON.parse(localStorage.getItem('str.favStations') || '[]').map((s) => s.stationuuid));

test.describe('classic layout', () => {
  test.use({ ui2: false, scenario: { ...SEED, bindings: { USServicesStatus: { value: PANDORA_READY }, PandoraStations: { value: PANDORA_STATIONS } } } });

  test('moves a favourite up and down and pushes the new order', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#favModeBtn').click();
    const names = page.locator('#searchResults .result-row .result-name-text');
    await expect(names).toHaveText(['First Radio', 'Second Radio', 'Third Radio']);

    // The first row cannot go up, the last cannot go down.
    const first = page.locator('#searchResults .result-row').nth(0);
    const last = page.locator('#searchResults .result-row').nth(2);
    await expect(first.locator('.fav-move[data-dir="-1"]')).toBeDisabled();
    await expect(last.locator('.fav-move[data-dir="1"]')).toBeDisabled();

    await mock.resetCalls();
    await last.locator('.fav-move[data-dir="-1"]').click();
    await expect(names).toHaveText(['First Radio', 'Third Radio', 'Second Radio']);
    expect(await storedIds(page)).toEqual(['u-1', 'u-3', 'u-2']);
    // The keyboard stays on the moved row's button.
    await expect(page.locator('#searchResults .result-row').nth(1).locator('.fav-move[data-dir="-1"]')).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(names).toHaveText(['Third Radio', 'First Radio', 'Second Radio']);

    await mock.expectCalled('PushFavoritesAt');
    const push = await mock.last('PushFavoritesAt');
    expect(JSON.parse(push.args[2]).map((s) => s.stationuuid)).toEqual(['u-3', 'u-1', 'u-2']);
    expect(push.args[3]).toBeGreaterThan(0);
  });

  test('has no move buttons outside Favorites', async ({ app }) => {
    const { page } = app;
    await page.locator('#topBtn').click();
    await expect(page.locator('#searchResults .result-row').first()).toBeVisible();
    await expect(page.locator('#searchResults .fav-move')).toHaveCount(0);
  });

  test('stars a Pandora station, which then plays from Favourites', async ({ app, mock }) => {
    const { page } = app;
    const pandora = page.locator('#pandoraModeBtn');
    await expect(pandora).toBeVisible();
    await pandora.click();
    const jazz = page.locator('#searchResults .result-row').filter({ hasText: 'Jazz Radio' });
    const star = jazz.locator('.pandora-fav');
    await expect(star).toHaveAttribute('aria-pressed', 'false');
    await star.click();
    await expect(star).toHaveAttribute('aria-pressed', 'true');
    const stored = await page.evaluate(() => JSON.parse(localStorage.getItem('str.favStations')).pop());
    expect(stored).toMatchObject({ stationuuid: 'pandora:111', name: 'Jazz Radio', source: 'pandora', location: '111', itemType: 'stationurl', favicon: 'https://img.example.com/111.jpg' });

    await page.locator('#favModeBtn').click();
    const fav = page.locator('#searchResults .result-row').filter({ hasText: 'Jazz Radio' });
    await expect(fav.locator('.result-meta')).toHaveText('Pandora');
    await expect(fav.locator('.result-online-dot')).toHaveCount(0);
    await mock.resetCalls();
    await fav.locator('.play-now').click();
    await mock.expectCalled('PlayPandoraStation', { times: 1 });
    const call = await mock.last('PlayPandoraStation');
    expect(call.args.slice(2)).toEqual(['111', 'Jazz Radio', 'stationurl']);
    expect(await mock.count('PlayURL')).toBe(0);
  });
});

test.describe('UI 2.0', () => {
  test.use({ ui2: true, scenario: SEED });

  test('the Favorites tab moves a row up and down', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.ui2-radio-tab[data-tab="favorites"]').click();
    const names = page.locator('#searchResults .ui2-st .ui2-st-name');
    await expect(names).toHaveText(['First Radio', 'Second Radio', 'Third Radio']);
    const row = (n) => page.locator('#searchResults .ui2-st').nth(n);
    await expect(row(0).locator('[data-act="move"][data-dir="-1"]')).toHaveAttribute('aria-disabled', 'true');
    await expect(row(0).locator('[data-act="move"][data-dir="1"]')).toHaveAttribute('aria-label', 'Move First Radio down');

    await mock.resetCalls();
    await row(0).locator('[data-act="move"][data-dir="1"]').click();
    await expect(names).toHaveText(['Second Radio', 'First Radio', 'Third Radio']);
    await expect(row(1).locator('[data-act="move"][data-dir="1"]')).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(names).toHaveText(['Second Radio', 'Third Radio', 'First Radio']);
    // At the bottom the keyboard moves to the button that can still act.
    await expect(row(2).locator('[data-act="move"][data-dir="-1"]')).toBeFocused();
    expect(await storedIds(page)).toEqual(['u-2', 'u-3', 'u-1']);
    await mock.expectCalled('PushFavoritesAt');

    // Discover has no move buttons.
    await page.locator('.ui2-radio-tab[data-tab="discover"]').click();
    await expect(page.locator('#searchResults .ui2-st').first()).toBeVisible();
    await expect(page.locator('#searchResults [data-act="move"]')).toHaveCount(0);
  });
});

// #1763: an account on the speaker that lists Pandora UNAVAILABLE. The entry
// used to vanish without a word; it stays, greyed, and says why when pressed.
test.describe('classic layout, Pandora unavailable on the speaker', () => {
  const PANDORA_UNAVAILABLE = {
    standIn: { services: [{ type: 'PANDORA', mode: 'us', registered: true }] },
    boxSources: { PANDORA: { hasAccount: 'true', status: 'UNAVAILABLE' } },
  };
  test.use({ ui2: false, scenario: { ...SEED, bindings: { USServicesStatus: { value: PANDORA_UNAVAILABLE }, PandoraStations: { value: PANDORA_STATIONS } } } });

  test('the Pandora entry stays visible with a hint and does not open the list', async ({ app, mock }) => {
    const { page } = app;
    const pandora = page.locator('#pandoraModeBtn');
    await expect(pandora).toBeVisible();
    await expect(pandora).toHaveAttribute('aria-disabled', 'true');
    await expect(pandora).toHaveAttribute('title', 'Pandora is not available on the speaker right now');
    await mock.resetCalls();
    // aria-disabled, not disabled: a press still lands and says why.
    await pandora.click({ force: true });
    await expect(page.locator('#toast')).toHaveText('Pandora is not available on the speaker right now');
    await expect(page.locator('#searchResults .pandora-play')).toHaveCount(0);
    expect(await mock.count('PandoraStations')).toBe(0);
  });
});
