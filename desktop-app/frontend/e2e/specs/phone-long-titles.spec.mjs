// phone-long-titles: on the phone remote a station name that runs over many
// lines stops after two lines with "…" (#1783): in the now-playing card and
// on a key tile, with the full name as the element's title.
//
//   E2E_PORT=4184 npx playwright test specs/phone-long-titles.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const LONG = '#TOP 100 - DJ CHARTS &amp; REMIX RADIO @ TikTok PROMO Charts, Electronic Music, EDM, House, Deep House, '
  + 'Dance Music, Techno &amp; Hypertechno, Top40, Latin Charts, Reggaeton, Urban, Moombahton, HipHop, Funk, Club &amp; Party Radio';
const SHOWN = LONG.replace(/&amp;/g, '&');

function lines(el) {
  const cs = getComputedStyle(el);
  const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.2;
  return { shown: Math.round(el.getBoundingClientRect().height / lh), cut: el.scrollHeight > el.clientHeight + 1 };
}

test.use({
  phonePlaying: { name: LONG, loc: 'http://example.com/long', key: '1' },
  phoneOverrides: { '/api/presets': { body: [{ ...PHONE.PRESETS[0], name: SHOWN }, ...PHONE.PRESETS.slice(1)] } },
});

test('the now-playing title and a key tile stop after two lines and keep the full name as title', async ({ phone }) => {
  const { page } = phone;
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  const now = page.locator('#status .now');
  await expect(now).toHaveAttribute('title', SHOWN);
  const tile = page.locator('#presets .preset').first().locator('.name');
  await expect(tile).toHaveAttribute('title', SHOWN);
  for (const el of [now, tile]) {
    const m = await el.evaluate(lines);
    expect(m.shown).toBeLessThanOrEqual(2);
    expect(m.cut).toBe(true);
  }
  // A short key name is not touched.
  const short = await page.locator('#presets .preset').nth(2).locator('.name').evaluate(lines);
  expect(short.shown).toBe(1);
  expect(short.cut).toBe(false);
});
