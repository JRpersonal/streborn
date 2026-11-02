// phone-favorites-order: the phone remote's favourites move up and down
// (#1519), sent to the speaker as the new order.
//
//   E2E_PORT=4194 npx playwright test specs/phone-favorites-order.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const FAVS = [
  ...PHONE.FAVORITES,
  { stationuuid: 's9', name: 'Radio Three', url: 'http://example.com/three' },
];

test.use({
  phoneOverrides: {
    '/api/favorites': { status: 200, body: FAVS },
    '/api/favorites/order': { status: 200, body: { ok: true, count: 3, at: 1 } },
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
  expect(JSON.parse(req.postData())).toEqual({ ids: ['s8', 's7', 's9'] });
});
