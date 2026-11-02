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

// Save, Cancel and Delete sit side by side too (they stacked for the same
// reason as the days); where three no longer fit, Delete takes a row of its
// own under the other two. Every word stays inside its button.
for (const [w, scale, lang, rows] of [[390, 1, 'en', 1], [390, 1, 'de', 1], [320, 1, 'de', 2], [390, 3, 'de', 2], [320, 3, 'de', 2]]) {
  test.describe(`buttons ${lang} at ${w}px, text scale ${scale}`, () => {
    test.use({ phoneLang: lang });
    test(`Save, Cancel, Delete in ${rows} row(s)`, async ({ phone }) => {
      const { page } = phone;
      await page.setViewportSize({ width: w, height: 844 });
      if (scale !== 1) await page.evaluate((s) => { localStorage.setItem('a11yScale', String(s)); window.a11yApply(); }, scale);
      await openEditor(page);
      const btns = page.locator('#alarmBtns button');
      await expect(btns).toHaveCount(3);
      const boxes = await btns.evaluateAll((els) => els.map((e) => {
        const r = e.getBoundingClientRect();
        return { y: Math.round(r.top), w: r.width, h: r.height, right: r.right, fits: e.scrollWidth <= e.clientWidth + 1 };
      }));
      expect(new Set(boxes.map((b) => b.y)).size).toBe(rows);
      for (const b of boxes) {
        expect(b.w).toBeGreaterThanOrEqual(40);
        expect(b.h).toBeGreaterThanOrEqual(40);
        expect(b.right).toBeLessThanOrEqual(w);
        expect(b.fits).toBe(true);
      }
      await page.locator('#btnAlarmCancel').click();
      await expect(page.locator('#alarmBtns')).toHaveCount(0);
    });
  });
}

// The badge on the alarm and sleep-timer cards names a feature the Bose app
// never had. It came in English and German only, so a Lithuanian page (and
// ten others) read "NEW WITH STR" in English.
test.describe('STR badge in Lithuanian', () => {
  test.use({ phoneLang: 'lt' });
  test('is Lithuanian, and every language has its own', async ({ phone }) => {
    const { page } = phone;
    await expect(page.locator('#lblAlarmBadge')).toHaveText('Nauja su STR');
    await expect(page.locator('#lblSleepBadge')).toHaveText('Nauja su STR');
    const d = await page.evaluate(() => window.I18N2);
    expect(Object.keys(d).length).toBe(12);
    for (const lang of Object.keys(d)) {
      expect(d[lang].strOnly, lang).toBeTruthy();
      if (lang !== 'en') expect(d[lang].strOnly, lang).not.toBe(d.en.strOnly);
    }
  });
});

// Records every PUT /api/alarms the page sends and answers it with the body,
// the way the agent answers with the saved document.
async function recordAlarmSaves(page) {
  const saves = [];
  await page.route('**/api/alarms', async (route) => {
    const req = route.request();
    if (req.method() !== 'PUT') return route.fallback();
    const body = JSON.parse(req.postData() || '{}');
    saves.push(body);
    return route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ ...body, status: { clockTrusted: true, zoneResolved: true, maxSkipDates: 60 } }),
    });
  });
  return saves;
}

// Switch-off at a time of day (discussion #1364): picking it shows the time
// picker, the save carries offAt, and picking minutes again drops it.
test('switch-off at a set time is saved as offAt, and dropped again', async ({ phone }) => {
  const { page } = phone;
  const saves = await recordAlarmSaves(page);
  await openEditor(page);
  await expect(page.locator('#alarmOffAtRow')).toBeHidden();
  await page.locator('#alarmAutoOff').selectOption('at');
  await expect(page.locator('#alarmOffAtRow')).toBeVisible();
  await page.locator('#alarmOffAt').fill('20:00');
  await page.locator('#btnAlarmSave').click();
  await expect.poll(() => saves.length).toBe(1);
  expect(saves[0].alarms[0].offAt).toBe('20:00');
  expect(saves[0].alarms[0].autoOff).toBe(0);

  // The saved answer re-opens on the time of day.
  await openEditor(page);
  await expect(page.locator('#alarmAutoOff')).toHaveValue('at');
  await expect(page.locator('#alarmOffAt')).toHaveValue('20:00');
  await page.locator('#alarmAutoOff').selectOption('60');
  await expect(page.locator('#alarmOffAtRow')).toBeHidden();
  await page.locator('#btnAlarmSave').click();
  await expect.poll(() => saves.length).toBe(2);
  expect(saves[1].alarms[0].offAt).toBeUndefined();
  expect(saves[1].alarms[0].autoOff).toBe(60);
});

// Days without an alarm (discussion #1491): each day is a chip with its
// remove cross on the same line, adding and removing save at once, and the
// list goes along sorted.
for (const w of [390, 320]) {
  test(`skip dates: add and remove at ${w}px`, async ({ phone }) => {
    const { page } = phone;
    await page.setViewportSize({ width: w, height: 844 });
    const saves = await recordAlarmSaves(page);
    await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
    const chips = page.locator('#alarmSkipList > span');
    await expect(chips).toHaveCount(3);
    for (const box of await chips.evaluateAll((els) => els.map((e) => {
      const r = e.getBoundingClientRect();
      const x = e.querySelector('button').getBoundingClientRect();
      return { right: r.right, sameLine: Math.abs((x.top + x.bottom) / 2 - (r.top + r.bottom) / 2) < 4 };
    }))) {
      expect(box.sameLine).toBe(true);
      expect(box.right).toBeLessThanOrEqual(w);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

    await page.locator('#alarmSkipDate').fill('2026-12-31');
    await page.locator('#btnAlarmSkipAdd').click();
    await expect.poll(() => saves.length).toBe(1);
    expect(saves[0].skipDates).toEqual(['2026-12-24', '2026-12-25', '2026-12-31', '2027-01-01']);
    await expect(chips).toHaveCount(4);

    await page.locator('#alarmSkipList [data-skip-del="0"]').click();
    await expect.poll(() => saves.length).toBe(2);
    expect(saves[1].skipDates).toEqual(['2026-12-25', '2026-12-31', '2027-01-01']);
    await expect(chips).toHaveCount(3);
  });
}
