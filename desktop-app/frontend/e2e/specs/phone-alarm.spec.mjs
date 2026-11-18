// phone-alarm: the alarm editor on the phone remote. The seven weekday
// buttons sit side by side in one row (they stacked one per line from the day
// the alarm clock arrived), each a 40px target at least, and wrap to two rows
// where seven would not fit: a 320px phone, the extra-large text setting.

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

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
  // This mock agent has no revisions, like one from before #1918: no rev goes along.
  expect(saves[0].rev).toBeUndefined();
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

// fakeAlarmAgent answers /api/alarms the way the agent does since discussion
// #1918: GET carries a rev; a PUT at a stale rev answers 409 with the current
// document; a PUT at the current rev, or without one, is stored and bumps it.
async function fakeAlarmAgent(page, doc) {
  const st = { doc: structuredClone(doc), puts: [], gets: 0 };
  await page.route('**/api/alarms', async (route) => {
    const req = route.request();
    const answer = (status) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(st.doc) });
    if (req.method() === 'GET') { st.gets++; return answer(200); }
    if (req.method() !== 'PUT') return route.fallback();
    const body = JSON.parse(req.postData() || '{}');
    st.puts.push(body);
    if (body.rev !== undefined && body.rev !== st.doc.rev) return answer(409);
    st.doc = { ...st.doc, zone: body.zone, alarms: body.alarms, skipDates: body.skipDates, rev: st.doc.rev + 1 };
    return answer(200);
  });
  return st;
}

// The other app (the STR Remote iOS app in #1918) switches alarm a1 off.
function switchedOffElsewhere(st) {
  st.doc = { ...st.doc, rev: st.doc.rev + 1, alarms: st.doc.alarms.map((a) => (a.id === 'a1' ? { ...a, enabled: false } : a)) };
}

// The page re-reads at most every two seconds; a test does not wait that out.
async function pastRereadGuard(page) {
  await page.evaluate(() => { window.alarmReadAt = 0; });
}

test('the Play tab shows an alarm switched off from another app (#1918)', async ({ phone }) => {
  const { page } = phone;
  const st = await fakeAlarmAgent(page, { ...PHONE.ALARMS, rev: 1 });
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  const a1 = page.locator('#alarmList [data-alarm-on="0"]');
  await expect(a1).toBeChecked();
  switchedOffElsewhere(st);
  await page.locator('#tabBrowse').click();
  await pastRereadGuard(page);
  await page.locator('#tabPlay').click();
  await expect(a1).not.toBeChecked();
  expect(st.puts).toEqual([]);
});

test('a window brought back to the front re-reads the alarms (#1918)', async ({ phone }) => {
  const { page } = phone;
  const st = await fakeAlarmAgent(page, { ...PHONE.ALARMS, rev: 1 });
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  const a1 = page.locator('#alarmList [data-alarm-on="0"]');
  await expect(a1).toBeChecked();
  switchedOffElsewhere(st);
  await pastRereadGuard(page);
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await expect(a1).not.toBeChecked();
  expect(st.puts).toEqual([]);
});

// The worse half of #1918: a page still showing a1 on saved its whole stale
// copy and switched it back on. Now the speaker answers 409, and the page puts
// its own one change (a2 on) on top of the current document instead.
test('a save from a stale page keeps the other app\'s change (#1918)', async ({ phone }) => {
  const { page } = phone;
  const st = await fakeAlarmAgent(page, { ...PHONE.ALARMS, rev: 1 });
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  await pastRereadGuard(page);
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  await expect.poll(() => page.evaluate(() => window.alarmDoc.rev)).toBe(1);
  switchedOffElsewhere(st); // the page does not hear of it
  await page.locator('#alarmList [data-alarm-on="1"]').check();
  await expect.poll(() => st.puts.length).toBe(2);
  expect(st.puts[0].rev).toBe(1);
  expect(st.puts[1].rev).toBe(2);
  const sent = Object.fromEntries(st.puts[1].alarms.map((a) => [a.id, a.enabled]));
  expect(sent).toEqual({ a1: false, a2: true });
  expect(st.doc.rev).toBe(3);
  await expect(page.locator('#alarmList [data-alarm-on="0"]')).not.toBeChecked();
  await expect(page.locator('#alarmList [data-alarm-on="1"]')).toBeChecked();
  const fail = await page.evaluate(() => window.T.alarmFail);
  expect(await page.locator('#alarmSum').textContent()).not.toContain(fail);
});

// #1952: the list printed a fixed 24h "06:45" while the editor's native time
// picker read "6:45 AM". The list and the "Next:" line now follow the
// browser's own 12h/24h convention, the one the picker follows too; "Next:"
// drops the seconds and stays in the speaker's zone (the zone named under the
// card) on a phone set to another one.
for (const [locale, timezoneId, time] of [
  ['en-US', 'Europe/Berlin', '6:45 AM'],
  ['en-US', 'America/New_York', '6:45 AM'],
  ['de-DE', 'Europe/Berlin', '06:45'],
]) {
  test.describe(`alarm times in ${locale}, phone in ${timezoneId}`, () => {
    test.use({ locale, timezoneId });
    test('the list and Next: use the browser\'s time format (#1952)', async ({ phone }) => {
      const { page } = phone;
      await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
      await expect(page.locator('#alarmList [data-alarm-edit="0"] strong')).toHaveText(time);
      const sum = await page.locator('#alarmSum').textContent();
      expect(sum).toContain(time);
      expect(sum).not.toMatch(/\d:\d\d:\d\d/);
    });
  });
}

// #1952: a station name that runs to a paragraph stops after two lines in the
// alarm list and in the editor, with the whole name in the title; the editor's
// time sits at the page's own input size instead of 1.4 times it.
test.describe('alarm on a long station name', () => {
  const NAME = '# TOP 100 DJ CHARTS - DJ REMIX & CHARTS RADIO @ TikTok Charts, Electronic Music, EDM, House, Deep House, '
    + 'Dance Music, Techno & Hypertechno, Top40, Latin Charts, Reggaeton, Urban, HipHop, Club & Party Radio - & LIVE DJ SETS';
  test.use({ phoneOverrides: { '/api/presets': { body: PHONE.PRESETS.map((p) => (p.slot === 3 ? { ...p, name: NAME } : p)) } } });

  function lines(el) {
    const cs = getComputedStyle(el);
    const lh = parseFloat(cs.lineHeight) || parseFloat(cs.fontSize) * 1.2;
    return { shown: Math.round(el.getBoundingClientRect().height / lh), cut: el.scrollHeight > el.clientHeight + 1 };
  }

  for (const w of [390, 320]) {
    test(`stops after two lines at ${w}px (#1952)`, async ({ phone }) => {
      const { page } = phone;
      await page.setViewportSize({ width: w, height: 844 });
      await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
      const row = page.locator('#alarmList [data-alarm-edit="0"]');
      await expect(row).toHaveAttribute('title', /LIVE DJ SETS$/);
      const listed = await row.locator('.clamp2').evaluate(lines);
      expect(listed.shown).toBeLessThanOrEqual(2);
      expect(listed.cut).toBe(true);
      // The switch next to it stays on screen.
      const box = await page.locator('#alarmList [data-alarm-on="0"]').boundingBox();
      expect(box.x + box.width).toBeLessThanOrEqual(w);
      // The short name of the second alarm is not touched.
      expect((await page.locator('#alarmList [data-alarm-edit="1"] .clamp2').evaluate(lines)).cut).toBe(false);

      await openEditor(page);
      const line = page.locator('#alarmPresetLine');
      await expect(line).toHaveAttribute('title', /LIVE DJ SETS$/);
      const ed = await line.evaluate(lines);
      expect(ed.shown).toBeLessThanOrEqual(2);
      expect(ed.cut).toBe(true);
      expect(await page.locator('#alarmTime').evaluate((e) => getComputedStyle(e).fontSize)).toBe('16px');
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    });
  }
});
