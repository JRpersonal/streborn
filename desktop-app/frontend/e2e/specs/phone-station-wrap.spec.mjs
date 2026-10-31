// phone-station-wrap: a search result row on the phone remote's Browse tab
// keeps its station name readable when the three buttons beside it (Play,
// Save to key, star) do not fit. Before, the buttons kept their width and the
// name shrank toward zero, one letter per line, in French ("Enregistrer sur
// touche") and on a 360px phone in German. The buttons now sit in one group
// that moves, as a whole, onto its own right-aligned line beneath the name
// (same failure mode as the header pill, #1336). The group is never split up,
// and English on a 360 to 412px phone keeps everything on one line.

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

// Per row: the name's width against its 5em floor, whether the buttons share
// one line, whether that line is the name's, and whether the group stays
// inside the row and ends at its right edge.
function measureRows() {
  const overlap = (a, b) => a.top < b.bottom && a.bottom > b.top;
  return [...document.querySelectorAll('#results .station')].map((row) => {
    const name = row.querySelector('.sname');
    const n = name.getBoundingClientRect();
    const r = row.getBoundingClientRect();
    const btns = [...row.querySelectorAll('.btn')].map((b) => b.getBoundingClientRect());
    const right = Math.max(...btns.map((b) => b.right));
    return {
      text: (name.firstChild ? name.firstChild.textContent : '').trim().slice(0, 40),
      width: n.width,
      floor: parseFloat(getComputedStyle(name).fontSize) * 5 - 1,
      count: btns.length,
      together: btns.every((b) => overlap(b, btns[0])),
      besideName: btns.every((b) => overlap(b, n)),
      below: btns.every((b) => b.top >= n.bottom),
      inside: right <= r.right + 1,
      flushRight: Math.abs(right - r.right) <= 1,
    };
  });
}

async function search(page, scale) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  if (scale !== 1) await page.evaluate((s) => { localStorage.setItem('a11yScale', String(s)); window.a11yApply(); }, scale);
  await page.locator('#tabBrowse').click();
  await expect(page.locator('#panelBrowse')).toBeVisible();
  await page.locator('#q').fill('rock');
  await page.locator('#btnSearch').click();
  await expect(page.locator('#results .station')).toHaveCount(PHONE.STATIONS.length);
}

// expect: 'beside' = one line with the name, 'below' = the whole group on its
// own line beneath the name, 'fits' = only no sideways overflow (the last
// resort at the largest text size on the narrowest phone, where even the
// group alone is wider than the row).
const CASES = [
  { lang: 'en', width: 360, scale: 1, expect: 'beside' },
  { lang: 'en', width: 375, scale: 1, expect: 'beside' },
  { lang: 'en', width: 390, scale: 1, expect: 'beside' },
  { lang: 'en', width: 412, scale: 1, expect: 'beside' },
  { lang: 'en', width: 320, scale: 1, expect: 'below' },
  { lang: 'fr', width: 412, scale: 1, expect: 'below' },
  { lang: 'fr', width: 360, scale: 1, expect: 'below' },
  { lang: 'fr', width: 320, scale: 1, expect: 'below' },
  { lang: 'de', width: 412, scale: 1, expect: 'below' },
  { lang: 'de', width: 360, scale: 1, expect: 'below' },
  { lang: 'de', width: 320, scale: 1, expect: 'below' },
  { lang: 'en', width: 390, scale: 3, expect: 'below' },
  { lang: 'fr', width: 390, scale: 3, expect: 'below' },
  { lang: 'de', width: 360, scale: 3, expect: 'below' },
  { lang: 'fr', width: 320, scale: 3, expect: 'fits' },
  { lang: 'de', width: 320, scale: 3, expect: 'fits' },
];

for (const c of CASES) {
  test.describe(`${c.lang} at ${c.width}px, text scale ${c.scale}`, () => {
    test.use({ phoneLang: c.lang, viewport: { width: c.width, height: 844 } });

    test(`the name stays readable and the buttons stay ${c.expect === 'fits' ? 'on screen' : (c.expect === 'beside' ? 'beside it' : 'together beneath it')}`, async ({ phone }) => {
      const { page } = phone;
      await search(page, c.scale);
      for (const r of await page.evaluate(measureRows)) {
        const what = `"${r.text}"`;
        expect(r.count, `${what} has Play, Save to key and the star`).toBe(3);
        expect(r.width, `${what} is ${Math.round(r.width)}px wide`).toBeGreaterThanOrEqual(r.floor);
        expect(r.inside, `${what}: a button sticks out of the row`).toBe(true);
        if (c.expect === 'fits') continue;
        expect(r.together, `${what}: the buttons got split over two lines`).toBe(true);
        if (c.expect === 'beside') {
          expect(r.besideName, `${what}: the buttons left the name's line`).toBe(true);
        } else {
          expect(r.below, `${what}: the button group is not on its own line`).toBe(true);
          expect(r.flushRight, `${what}: the button group is not right-aligned`).toBe(true);
        }
      }
      const w = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(w, 'the Browse tab scrolls sideways').toBeLessThanOrEqual(c.width);
    });
  });
}
