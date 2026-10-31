// phone-station-wrap: a search result row on the phone remote's Browse tab
// keeps its station name readable when the three buttons beside it (Play,
// Save to key, star) do not fit. Before, the buttons kept their width and the
// name shrank toward zero, one letter per line, in French ("Enregistrer sur
// touche") and on a 360px phone in German. The row now wraps the buttons onto
// their own line beneath the name (same failure mode as the header pill, #1336).

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

// Per row: the name's width against its 8em floor, and whether the last button
// still sits on the name's line.
function measureRows() {
  return [...document.querySelectorAll('#results .station')].map((row) => {
    const name = row.querySelector('.sname');
    const btns = row.querySelectorAll('.btn');
    const n = name.getBoundingClientRect();
    const b = btns[btns.length - 1].getBoundingClientRect();
    return {
      text: name.textContent.trim().slice(0, 40),
      width: n.width,
      floor: parseFloat(getComputedStyle(name).fontSize) * 8 - 1,
      sameLine: b.top < n.bottom && b.bottom > n.top,
    };
  });
}

async function search(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  await page.locator('#tabBrowse').click();
  await expect(page.locator('#panelBrowse')).toBeVisible();
  await page.locator('#q').fill('rock');
  await page.locator('#btnSearch').click();
  await expect(page.locator('#results .station')).toHaveCount(PHONE.STATIONS.length);
}

const CASES = [
  { lang: 'fr', width: 360 },
  { lang: 'fr', width: 412 },
  { lang: 'de', width: 360 },
  { lang: 'en', width: 360 },
  { lang: 'en', width: 412 },
];

for (const { lang, width } of CASES) {
  test.describe(`${lang} at ${width}px`, () => {
    test.use({ phoneLang: lang, viewport: { width, height: 844 } });

    test('the station name never gets squeezed below its floor', async ({ phone }) => {
      const { page } = phone;
      await search(page);
      for (const r of await page.evaluate(measureRows)) {
        expect(r.width, `"${r.text}" is ${Math.round(r.width)}px wide`).toBeGreaterThanOrEqual(r.floor);
      }
      const w = await page.evaluate(() => document.documentElement.scrollWidth);
      expect(w, 'the Browse tab scrolls sideways').toBeLessThanOrEqual(width);
    });
  });
}

// The common case stays one line: English on a 412px phone keeps the buttons
// beside the name, so the wrap only kicks in where it is needed.
test.describe('en at 412px', () => {
  test.use({ phoneLang: 'en', viewport: { width: 412, height: 844 } });

  test('the buttons stay beside the name', async ({ phone }) => {
    const { page } = phone;
    await search(page);
    for (const r of await page.evaluate(measureRows)) {
      expect(r.sameLine, `"${r.text}" wrapped its buttons`).toBe(true);
    }
  });
});
