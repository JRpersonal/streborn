// desktop-long-titles: a key whose station name runs over many lines stops
// after two lines with "…" instead of stretching the key tile (#1783), in
// the classic layout and in the UI 2.0 preview, and the full name stays the
// tile name's title.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-long-titles.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { PRESETS, RECENT, STATIONS } from '../../testkit/demo-data.mjs';
import { en } from '../helpers/i18n.mjs';

// E2E_SHOTS=<dir> also saves screenshots of a cut-off and an opened title there.
const SHOTS = process.env.E2E_SHOTS || '';

const LONG = '#TOP 100 - DJ CHARTS & REMIX RADIO @ TikTok PROMO Charts, Electronic Music, EDM, House, Deep House, '
  + 'Dance Music, Techno & Hypertechno, Top40, Latin Charts, Reggaeton, Urban, Moombahton, HipHop, Funk, Club & Party Radio';
const UNBROKEN = 'X'.repeat(160);

// lines is how many lines the element shows, against its own line height,
// and whether its text was cut (more text than room).
function lines(el) {
  const cs = getComputedStyle(el);
  const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.2;
  const box = el.getBoundingClientRect();
  return { shown: Math.round(box.height / lh), cut: el.scrollHeight > el.clientHeight + 1, right: box.right };
}

for (const ui2 of [false, true]) {
  test.describe(ui2 ? 'UI 2.0' : 'classic layout', () => {
    test.use({
      ui2,
      scenario: { presets: { 'demobox.local': [{ ...PRESETS[0], name: LONG }, { ...PRESETS[1], name: UNBROKEN }, ...PRESETS.slice(2)] } },
    });

    test('a long key name takes two lines with "…" and keeps the full name as its title', async ({ app }) => {
      const { page } = app;
      const tiles = page.locator('#presets .preset');
      const long = tiles.nth(0).locator('.name');
      const unbroken = tiles.nth(1).locator('.name');
      await expect(long).toHaveAttribute('title', LONG);
      await expect(unbroken).toHaveAttribute('title', UNBROKEN);
      for (const name of [long, unbroken]) {
        const m = await name.evaluate(lines);
        expect(m.shown).toBeLessThanOrEqual(2);
        expect(m.cut).toBe(true);
        // The unbroken name breaks inside its tile, it does not run out sideways.
        const tileRight = await name.evaluate((el) => el.closest('.preset').getBoundingClientRect().right);
        expect(m.right).toBeLessThanOrEqual(tileRight + 1);
      }
      // A short name is not touched.
      const short = await tiles.nth(2).locator('.name').evaluate(lines);
      expect(short.shown).toBe(1);
      expect(short.cut).toBe(false);
    });

    // #1811: a click on a cut-off title opens it, but never where the press
    // belongs to a control. The whole key tile plays its key.
    test('a click on a key tile name still presses the key and opens nothing', async ({ app, mock }) => {
      const { page } = app;
      const name = page.locator('#presets .preset').nth(0).locator('.name');
      await expect(name).toHaveAttribute('title', LONG);
      await mock.resetCalls();
      await name.click();
      await expect(name).not.toHaveClass(/title-open/);
      // No arrow either: the tile does not open, so it must not promise to.
      await expect(name).not.toHaveClass(/title-cut/);
      expect((await name.evaluate(lines)).shown).toBeLessThanOrEqual(2);
      if (!ui2) await mock.expectCalled('PlaySlot', { times: 1 });
    });
  });
}

// #1811: in the lists, a click on a cut-off title opens it to its full
// length and a second click closes it. Only the text reacts, so the row's
// buttons keep their action, and a title that fits does nothing.
const cut = (el) => el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1;
// arrow describes the mark at the end of a title: its ::after box against the
// title's own box, and which way it points.
function arrow(el) {
  const cs = getComputedStyle(el, '::after');
  if (cs.content === 'none') return { content: 'none' };
  const box = el.getBoundingClientRect();
  const rtl = getComputedStyle(el).direction === 'rtl';
  const size = parseFloat(cs.width);
  const fromEnd = parseFloat(rtl ? cs.left : cs.right);
  const bottom = parseFloat(cs.bottom);
  // matrix(a, b, ...): rotate(45deg) has b > 0 (down), rotate(-135deg) b < 0.
  const b = parseFloat(cs.transform.replace('matrix(', '').split(',')[1]);
  return {
    content: cs.content,
    down: b > 0,
    rtl,
    // Wholly inside the title, in the room kept free for it.
    inside: fromEnd >= 0 && bottom >= 0 && bottom + size <= box.height,
    atEnd: fromEnd + size <= parseFloat(getComputedStyle(el)[rtl ? 'paddingLeft' : 'paddingRight']),
  };
}

const LONG_STATION = { ...STATIONS[0], name: LONG, stationuuid: 'long1' };

test.describe('classic layout: a cut-off title in the station results', () => {
  test.use({
    ui2: false,
    scenario: { bindings: {
      RadioSearch: { value: [LONG_STATION, ...STATIONS.slice(1)] },
      RadioSearchDetailed: { value: { stations: [LONG_STATION, ...STATIONS.slice(1)], relaxed: false } },
    } },
  });

  test('opens on a click on the text, closes on the next, and the row keeps its buttons', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('#searchQ').fill('charts');
    await page.locator('#searchBtn').click();
    const rows = page.locator('#searchResults .result-row');
    const row = rows.filter({ hasText: '#TOP 100' });
    const title = row.locator('.result-name-text');
    await expect(title).toHaveText(LONG);
    expect(await title.evaluate(cut)).toBe(true);
    const before = (await title.boundingBox()).height;
    // A cut-off title says that it opens: a small arrow at its end, pointing
    // down, drawn as a shape and not as a glyph. A title that fits has none,
    // and the arrow does not make its row any taller than the others.
    const short = rows.filter({ hasText: STATIONS[1].name }).locator('.result-name-text');
    await expect(title).toHaveClass(/title-cut/);
    await expect(short).not.toHaveClass(/title-cut/);
    expect(await title.evaluate(arrow)).toMatchObject({ content: '""', down: true, inside: true, atEnd: true });
    expect((await short.evaluate(arrow)).content).toBe('none');
    expect(before).toBe((await short.boundingBox()).height);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/desktop-classic-results-title-cut-off.png` });

    await mock.resetCalls();
    await title.click();
    await expect(title).toHaveClass(/title-open/);
    expect(await title.evaluate(cut)).toBe(false);
    expect((await title.boundingBox()).height).toBeGreaterThan(before);
    // Reading a title starts nothing.
    expect(await mock.count('PlayURL')).toBe(0);
    // Open, the arrow points up.
    expect(await title.evaluate(arrow)).toMatchObject({ content: '""', down: false, inside: true, atEnd: true });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/desktop-classic-results-title-open.png` });

    // The row's own action is where it was.
    await row.locator('.play-now').click();
    await mock.expectCalled('PlayURL', { times: 1 });
    await expect(row.locator('.play-now')).toHaveAttribute('aria-label', en('search.playNow'));

    await title.click();
    await expect(title).not.toHaveClass(/title-open/);
    await expect(title).toHaveClass(/title-cut/);
    expect(await title.evaluate(cut)).toBe(true);
    expect((await title.evaluate(arrow)).down).toBe(true);

    // A title that fits is left alone, and so is the rest of a row.
    await short.click();
    await expect(short).not.toHaveClass(/title-open/);
    await row.locator('.result-meta').click();
    await expect(title).not.toHaveClass(/title-open/);
  });

  test('the arrow follows the room: it comes when a title no longer fits and goes when it fits again', async ({ app }) => {
    const { page } = app;
    await page.locator('#searchQ').fill('charts');
    await page.locator('#searchBtn').click();
    const noise = [];
    page.on('console', (m) => { if (/ResizeObserver loop/.test(m.text())) noise.push(m.text()); });
    await expect(page.locator('#searchResults .result-row')).toHaveCount(STATIONS.length);
    // By position: the test rewrites this row's name further down.
    const short = page.locator('#searchResults .result-row').nth(1).locator('.result-name-text');
    await expect(short).toHaveText(STATIONS[1].name);
    await expect(short).not.toHaveClass(/title-cut/);
    // The window gets narrower (here: the title's own line does).
    await short.evaluate((el) => { el.parentElement.style.maxWidth = '40px'; });
    await expect(short).toHaveClass(/title-cut/);
    await short.evaluate((el) => { el.parentElement.style.maxWidth = ''; });
    await expect(short).not.toHaveClass(/title-cut/);
    // A name written into a row that stays is measured again.
    await short.evaluate((el, long) => { el.textContent = long; }, LONG);
    await expect(short).toHaveClass(/title-cut/);
    // And a short one again: the arrow goes.
    await short.evaluate((el) => { el.textContent = 'Jazz'; });
    await expect(short).not.toHaveClass(/title-cut/);
    // Following the size never trips the browser's observer-loop guard.
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    expect(noise).toEqual([]);
  });

  test('in a right-to-left language the arrow sits at the left end', async ({ app }) => {
    const { page } = app;
    await page.locator('#searchQ').fill('charts');
    await page.locator('#searchBtn').click();
    const title = page.locator('#searchResults .result-row').filter({ hasText: '#TOP 100' }).locator('.result-name-text');
    await expect(title).toHaveClass(/title-cut/);
    await page.evaluate(() => { document.documentElement.dir = 'rtl'; });
    expect(await title.evaluate(arrow)).toMatchObject({ rtl: true, inside: true, atEnd: true });
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/desktop-classic-results-title-cut-off-rtl.png` });
  });

  test('an opened title is open again when the list is drawn again', async ({ app }) => {
    const { page } = app;
    await page.locator('#searchQ').fill('charts');
    await page.locator('#searchBtn').click();
    const title = page.locator('#searchResults .result-row').filter({ hasText: '#TOP 100' }).locator('.result-name-text');
    await title.click();
    await expect(title).toHaveClass(/title-open/);
    await page.locator('#searchBtn').click();
    await expect(page.locator('#searchResults .result-row')).toHaveCount(STATIONS.length);
    await expect(title).toHaveClass(/title-open/);
  });
});

test.describe('classic layout: a cut-off title in Recently played', () => {
  test.use({
    ui2: false,
    scenario: { bindings: { RecentPlayed: { value: RECENT.map((e) => (e.cardKey === 'r:jazz24' ? { ...e, cardName: LONG, track: `${LONG} - ${e.track}` } : e)) } } },
  });

  test('opens the card name and a track line, each on its own', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="recent"]').click();
    const card = page.locator('#recentList .recent-card').filter({ hasText: '#TOP 100' });
    const name = card.locator('.rc-name');
    await expect(name).toBeVisible();
    expect(await name.evaluate(cut)).toBe(true);
    await expect(name).toHaveClass(/title-cut/);
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/desktop-classic-recent-title-cut-off.png` });
    await name.click();
    await expect(name).toHaveClass(/title-open/);
    expect(await name.evaluate(cut)).toBe(false);
    const track = card.locator('.rc-tr-main').first();
    if (await track.count()) {
      await expect(track).not.toHaveClass(/title-open/);
      if (await track.evaluate(cut)) {
        await track.click();
        await expect(track).toHaveClass(/title-open/);
      }
    }
    if (SHOTS) await page.screenshot({ path: `${SHOTS}/desktop-classic-recent-title-open.png` });
  });
});
