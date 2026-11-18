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
// E2E_SHOTS=<dir> also saves screenshots of a cut-off and an opened title there.
const SHOTS = process.env.E2E_SHOTS || '';

function lines(el) {
  const cs = getComputedStyle(el);
  const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.2;
  return { shown: Math.round(el.getBoundingClientRect().height / lh), cut: el.scrollHeight > el.clientHeight + 1 };
}

// arrowDown: which way the mark at the end of a title points. null: no mark.
// matrix(a, b, ...): rotate(45deg) has b > 0 (down), rotate(-135deg) b < 0.
function arrowDown(el) {
  const cs = getComputedStyle(el, '::after');
  if (cs.content === 'none') return null;
  return parseFloat(cs.transform.replace('matrix(', '').split(',')[1]) > 0;
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

// #1811: a tap on a cut-off title opens it to its full length, a second tap
// closes it. Only where the tap is free: a key tile plays its key.
test('a tap on the cut-off now-playing title opens it, and it stays open over the status polls', async ({ phone }) => {
  const { page } = phone;
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  const now = page.locator('#status .now');
  await expect(now).toHaveAttribute('title', SHOWN);
  expect((await now.evaluate(lines)).cut).toBe(true);
  // A cut-off title says that it opens: a small arrow at its end, down while
  // closed and up while open. It does not change the title's two lines.
  await expect(now).toHaveClass(/title-cut/);
  expect((await now.evaluate(lines)).shown).toBe(2);
  expect(await now.evaluate(arrowDown)).toBe(true);
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/phone-now-title-cut-off.png` });
  await now.click();
  await expect(now).toHaveClass(/title-open/);
  expect(await now.evaluate(arrowDown)).toBe(false);
  const open = await now.evaluate(lines);
  expect(open.cut).toBe(false);
  expect(open.shown).toBeGreaterThan(2);
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/phone-now-title-open.png` });
  // The card is written again by every poll: the title comes back open.
  await page.evaluate(() => window.refreshStatus());
  await expect(now).toHaveClass(/title-open/);
  await now.click();
  await expect(now).not.toHaveClass(/title-open/);
  await expect(now).toHaveClass(/title-cut/);
  expect((await now.evaluate(lines)).cut).toBe(true);
  await page.evaluate(() => window.refreshStatus());
  await expect(now).not.toHaveClass(/title-open/);
  // Drawn again by the poll, and marked again.
  await expect(now).toHaveClass(/title-cut/);
});

test('a tap on a key tile name plays the key and opens nothing', async ({ phone }) => {
  const { page, handler } = phone;
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  const name = page.locator('#presets .preset').first().locator('.name');
  const before = handler.calls.length;
  await name.click();
  await expect.poll(() => handler.calls.slice(before)
    .filter((c) => c.method === 'POST' && new URL(c.url).pathname.startsWith('/api/play/'))
    .map((c) => new URL(c.url).pathname)).toEqual(['/api/play/1']);
  await expect(name).not.toHaveClass(/title-open/);
  // No arrow on a key tile: it does not open, so it must not promise to.
  await expect(page.locator('#presets .title-cut')).toHaveCount(0);
});

test.describe('Recently played', () => {
  test.use({ phoneOverrides: {
    '/api/presets': { body: PHONE.PRESETS },
    '/api/recent': { body: PHONE.RECENT.map((e) => (e.cardKey === 'r:jazz24' ? { ...e, cardName: SHOWN } : e)) },
  } });

  test('a tap on a cut-off name opens it and the row keeps its Play button', async ({ phone }) => {
    const { page, handler } = phone;
    const row = page.locator('.station', { has: page.locator('.clamp2', { hasText: '#TOP 100' }) }).first();
    const name = row.locator('.clamp2');
    await name.scrollIntoViewIfNeeded();
    expect((await name.evaluate(lines)).cut).toBe(true);
    await expect(name).toHaveClass(/title-cut/);
    // A name that fits has no arrow.
    expect(await page.locator('.station .clamp2:not(.title-cut)').count()).toBeGreaterThan(0);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/phone-recent-title-cut-off.png` });
    await name.click();
    await expect(name).toHaveClass(/title-open/);
    expect((await name.evaluate(lines)).cut).toBe(false);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/phone-recent-title-open.png` });
    // Reading the name started nothing; the button next to it still plays.
    const before = handler.calls.length;
    await row.locator('button', { hasText: 'Play' }).click();
    await expect.poll(() => handler.calls.slice(before)
      .filter((c) => c.method === 'POST' && new URL(c.url).pathname === '/api/play').length).toBe(1);
  });
});
