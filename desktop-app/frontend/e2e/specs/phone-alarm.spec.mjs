// phone-alarm: the alarm editor on the phone remote. The seven weekday
// buttons sit side by side in one row (they stacked one per line from the day
// the alarm clock arrived), each a 40px target at least, and wrap to two rows
// where seven would not fit: a 320px phone, the extra-large text setting.

import { test, expect } from '../fixtures.mjs';

async function openEditor(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  await page.locator('#alarmList [data-alarm-edit="0"]').click();
  await expect(page.locator('#alarmDays button')).toHaveCount(7);
}

// The weekday buttons' boxes, and how many distinct rows they sit on.
async function dayBoxes(page) {
  const boxes = await page.locator('#alarmDays button').evaluateAll((els) => els.map((e) => {
    const r = e.getBoundingClientRect();
    return { x: r.left, y: Math.round(r.top), w: r.width, h: r.height, right: r.right };
  }));
  return { boxes, rows: new Set(boxes.map((b) => b.y)).size };
}

for (const [w, scale, rows] of [[390, 1, 1], [320, 1, 2], [390, 3, 2]]) {
  test(`weekdays: ${rows} row(s) at ${w}px, text scale ${scale}`, async ({ phone }) => {
    const { page } = phone;
    await page.setViewportSize({ width: w, height: 844 });
    if (scale !== 1) await page.evaluate((s) => { localStorage.setItem('a11yScale', String(s)); window.a11yApply(); }, scale);
    await openEditor(page);
    const { boxes, rows: got } = await dayBoxes(page);
    expect(got).toBe(rows);
    for (const b of boxes) {
      expect(b.w).toBeGreaterThanOrEqual(40);
      expect(b.h).toBeGreaterThanOrEqual(40);
      expect(b.right).toBeLessThanOrEqual(w);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    // A tap still toggles the day it lands on.
    const sat = page.locator('#alarmDays [data-alarm-day="6"]');
    await expect(sat).not.toHaveClass(/armed/);
    await sat.click();
    await expect(sat).toHaveClass(/armed/);
  });
}
