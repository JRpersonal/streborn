// phone-favorites-order: the phone remote's favourites move up and down
// (#1519), and its Pandora list shows each station's cover (#1409) and a star
// that keeps the station in Favourites (#1663), where it plays through the
// Pandora call.
//
//   E2E_PORT=4194 npx playwright test specs/phone-favorites-order.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const FAVS = [
  ...PHONE.FAVORITES,
  { stationuuid: 'pandora:111', name: 'Jazz Radio', source: 'pandora', location: '111', itemType: 'stationurl' },
];
const PANDORA = {
  status: 'READY',
  stations: [
    { name: 'Jazz Radio', location: '111', type: 'stationurl', art: `${PHONE.ART_HOST}/jazz.svg` },
    { name: 'Rock Radio', location: '222', type: 'stationurl' },
  ],
};

test.use({
  phoneOverrides: {
    '/api/favorites': { status: 200, body: FAVS },
    '/api/favorites/order': { status: 200, body: { ok: true, count: 3, at: 1 } },
    '/api/pandora/stations': { status: 200, body: PANDORA },
    '/api/pandora/play': { status: 200, body: { ok: true } },
  },
});

async function openBrowse(page) {
  await expect(page.locator('#presets .preset')).toHaveCount(6);
  await page.locator('#tabBrowse').click();
  await expect(page.locator('#panelBrowse')).toBeVisible();
  await expect(page.locator('#favResults .fav-row')).toHaveCount(FAVS.length);
}

test('a favourite moves up and down, sent to the speaker as the new order', async ({ phone }) => {
  const { page } = phone;
  await openBrowse(page);
  const rows = page.locator('#favResults .fav-row');
  // The first cannot go up, the last cannot go down.
  await expect(rows.nth(0).locator('.fav-move[data-dir="-1"]')).toBeDisabled();
  await expect(rows.nth(2).locator('.fav-move[data-dir="1"]')).toBeDisabled();
  await expect(rows.nth(1).locator('.fav-move[data-dir="-1"]')).toHaveAttribute('aria-label', 'Move up: Classic FM');

  const sent = page.waitForRequest((r) => r.url().endsWith('/api/favorites/order') && r.method() === 'POST');
  await rows.nth(1).locator('.fav-move[data-dir="-1"]').click();
  const req = await sent;
  expect(JSON.parse(req.postData())).toEqual({ ids: ['s8', 's7', 'pandora:111'] });
});

test('the Pandora list shows covers and stars, and a Pandora favourite plays through Pandora', async ({ phone }) => {
  const { page } = phone;
  await openBrowse(page);
  const pandora = page.locator('#pandoraResults .station');
  await expect(pandora).toHaveCount(2);
  // The cover the speaker listed, drawn like a key's cover; none listed, the
  // letter tile the keys use (#1799), so the row never stands without a picture.
  await expect(pandora.nth(0).locator('img.art')).toHaveAttribute('src', `${PHONE.ART_HOST}/jazz.svg`);
  await expect(pandora.nth(1).locator('img.art')).toHaveCount(1);
  await expect(pandora.nth(1).locator('img.art')).toHaveAttribute('src', /^data:image\//);
  // Jazz Radio is a favourite already, Rock Radio is not.
  await expect(pandora.nth(0).locator('.fav-star')).toHaveAttribute('title', 'Remove from favourites');
  await expect(pandora.nth(1).locator('.fav-star')).toHaveAttribute('title', 'Add to favourites');

  const starred = page.waitForRequest((r) => r.url().endsWith('/api/favorites') && r.method() === 'POST');
  await pandora.nth(1).locator('.fav-star').click();
  expect(JSON.parse((await starred).postData())).toEqual({
    source: 'pandora', name: 'Rock Radio', location: '222', itemType: 'stationurl', favicon: '',
  });

  const played = page.waitForRequest((r) => r.url().endsWith('/api/pandora/play') && r.method() === 'POST');
  await page.locator('#favResults .fav-row').nth(2).locator('button').first().click();
  expect(JSON.parse((await played).postData())).toEqual({ location: '111', name: 'Jazz Radio', type: 'stationurl' });
});
