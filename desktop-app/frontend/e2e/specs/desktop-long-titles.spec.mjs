// desktop-long-titles: a key whose station name runs over many lines stops
// after two lines with "…" instead of stretching the key tile (#1783), in
// the classic layout and in the UI 2.0 preview, and the full name stays the
// tile name's title.
//
//   E2E_PORT=4183 npx playwright test specs/desktop-long-titles.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { PRESETS } from '../../testkit/demo-data.mjs';

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
  });
}
