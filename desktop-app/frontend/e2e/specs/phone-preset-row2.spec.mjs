// phone-preset-row2: the second row of keys on the phone remote (#1110). A
// hardware key pressed twice quickly plays slot 6+n; the page shows those six
// behind a "1× | 2×" switch on the heading line of the keys, only when the
// agent says presetRows "2". Covers the switch, the explainer of an empty
// row 2 and its "New" marker, a row-2 play, a Find save and an alarm on a
// row-2 key, the move offer across rows, the playing dot, the layout at phone
// widths, and an agent without the row (no switch, nothing changed).

import { test, expect } from '../fixtures.mjs';
import { PHONE } from '../../testkit/demo-data.mjs';

const ROW2_LOC = 'http://192.0.2.50:8888/stream/9';

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
}
const seg = (page, row) => page.locator(`#presetRowSw button[data-row="${row}"]`);
const tileSlots = (page) => page.locator('#presets .preset').evaluateAll((els) => els.map((e) => Number(e.dataset.slot)));

// The requests the page sent, as "METHOD /path".
const sent = (handler) => handler.calls.map((c) => c.method + ' ' + new URL(c.url).pathname);

test.describe('row switch', () => {
  test.use({ phoneRow2: true });

  test('the switch sits on the heading line and swaps the six keys', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('#presetRowSw')).toBeVisible();
    await expect(seg(page, 1)).toHaveAttribute('aria-pressed', 'true');
    await expect(seg(page, 2)).toHaveAttribute('aria-pressed', 'false');
    expect(await tileSlots(page)).toEqual([1, 2, 3, 4, 5, 6]);

    // Same line: the switch's box overlaps the label's vertical band.
    const lbl = await page.locator('#lblPresets').boundingBox();
    const sw = await page.locator('#presetRowSw').boundingBox();
    expect(sw.y).toBeLessThan(lbl.y + lbl.height);
    expect(sw.y + sw.height).toBeGreaterThan(lbl.y);
    // Each segment is a 40px target at least.
    for (const r of [1, 2]) {
      const b = await seg(page, r).boundingBox();
      expect(b.height).toBeGreaterThanOrEqual(40);
      expect(b.width).toBeGreaterThanOrEqual(40);
    }

    await seg(page, 2).click();
    await expect(seg(page, 2)).toHaveAttribute('aria-pressed', 'true');
    expect(await tileSlots(page)).toEqual([7, 8, 9, 10, 11, 12]);
    await expect(page.locator('#presets .preset').first().locator('.num')).toContainText('2× 1');
    await expect(page.locator('#presets .preset[data-slot="9"]')).toContainText('Jazz Lounge');
    await expect(page.locator('#presets .preset[data-slot="10"]')).toHaveClass(/empty/);
    // The page never shows 7..12; a screen reader hears "Key 1 twice".
    await expect(page.locator('#presets')).not.toContainText(/#(7|8|9|10|11|12)\b/);
    await expect(page.getByRole('button', { name: /^Key 1 twice, KEXP/ })).toHaveCount(1);

    await seg(page, 1).click();
    expect(await tileSlots(page)).toEqual([1, 2, 3, 4, 5, 6]);
  });

  test('a row-2 key plays its own slot', async ({ phone }) => {
    const { page, handler } = phone;
    await bootDone(page);
    await seg(page, 2).click();
    await page.locator('#presets .preset[data-slot="9"]').click();
    await expect.poll(() => sent(handler)).toContain('POST /api/play/9');
    await expect(page.locator('#presets .preset[data-slot="9"]')).toHaveClass(/active/);
  });
});

test.describe('empty row 2', () => {
  test('shows six empty keys under one line with a Find link; the New marker goes after one look', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('#row2Hint')).toBeHidden();
    await expect(seg(page, 2).locator('.rdot.new')).toHaveCount(1);
    await seg(page, 2).click();
    await expect(page.locator('#row2Hint')).toBeVisible();
    await expect(page.locator('#row2Hint')).toContainText('Press a key twice quickly to play these.');
    expect(await tileSlots(page)).toEqual([7, 8, 9, 10, 11, 12]);
    await expect(page.locator('#presets .preset.empty')).toHaveCount(6);
    await expect(page.locator('#presets .preset').first().locator('.num')).toContainText('2× 1');
    await seg(page, 1).click();
    await bootDone(page);
    await expect(page.locator('#row2Hint')).toBeHidden();
    await expect(seg(page, 2).locator('.rdot')).toHaveCount(0);
    expect(await page.evaluate(() => localStorage.getItem('str.remote.row2Seen'))).toBe('1');

    await seg(page, 2).click();
    await page.locator('#btnRow2Find').click();
    await expect(page.locator('#panelBrowse')).toBeVisible();
  });

  test('a hold on an empty row-2 key saves what plays onto it', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await seg(page, 2).click();
    const tile = page.locator('#presets .preset[data-slot="9"]');
    const box = await tile.boundingBox();
    const save = page.waitForRequest((r) => r.method() === 'POST' && new URL(r.url()).pathname === '/api/box/save-playing');
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down();
    await page.waitForTimeout(1300);
    await page.mouse.up();
    expect((await save).postDataJSON()).toEqual({ slot: 9 });
    await expect(page.locator('#holdNote')).toHaveText('Saved to key 3 · 2×');
  });

  test.describe('German at 390x844', () => {
    test.use({ phoneLang: 'de' });
    test('the hint is one line and the keys still end above the tab bar', async ({ phone }) => {
      const { page } = phone;
      await bootDone(page);
      await seg(page, 2).click();
      await expect(page.locator('#row2Hint')).toContainText('Zweimal schnell drücken spielt diese Tasten.');
      await expect(page.locator('#btnRow2Find')).toHaveText('Sender suchen');
      await page.evaluate(() => window.scrollTo(0, 0));
      const hint = await page.locator('#row2Hint').boundingBox();
      expect(hint.height).toBeLessThanOrEqual(20);
      const gridBottom = await page.evaluate(() => document.getElementById('presets').getBoundingClientRect().bottom);
      const barTop = await page.evaluate(() => document.querySelector('.tabbar').getBoundingClientRect().top);
      expect(gridBottom).toBeLessThanOrEqual(barTop);
    });
  });
});

test.describe('saving onto row 2', () => {
  test('Find saves a station onto "Key 3 · 2×"', async ({ phone }) => {
    const { page, handler } = phone;
    await bootDone(page);
    await page.locator('#tabBrowse').click();
    await page.locator('#q').fill('rock');
    await page.locator('#btnSearch').click();
    await page.locator('#results .station').first().getByRole('button', { name: /^Save to key/ }).click();
    await expect(page.locator('.slotpick button')).toHaveCount(6);
    await page.locator('#pickRowSw button[data-row="2"]').click();
    await expect(page.locator('#pickRowSw button[data-row="2"]')).toHaveAttribute('aria-pressed', 'true');
    await expect(page.locator('.slotpick button').nth(2)).toContainText('2× 3');
    const put = page.waitForRequest((r) => r.method() === 'PUT' && new URL(r.url()).pathname === '/api/presets/9');
    await page.locator('.slotpick button[data-slot="9"]').click();
    expect((await put).postDataJSON().slot).toBe(9);
    await expect(page.locator('#searchMsg')).toHaveText('Saved to key 3 · 2×');
    expect(sent(handler)).not.toContain('PUT /api/presets/3');
  });

  test.describe('move offer', () => {
    test.use({
      phoneRow2: true,
      phoneOverrides: { '/api/presets/3': { status: 409, body: { code: 'already-on-slot', slot: 8, name: 'Classic FM' } } },
    });
    test('names a row-2 key as "key 2 · 2×" and moves across rows', async ({ phone }) => {
      const { page } = phone;
      await bootDone(page);
      await page.locator('#tabBrowse').click();
      await page.locator('#q').fill('rock');
      await page.locator('#btnSearch').click();
      await page.locator('#results .station').first().getByRole('button', { name: /^Save to key/ }).click();
      await page.locator('.slotpick button[data-slot="3"]').click();
      await expect(page.locator('#results')).toContainText('is already on key 2 · 2×');
      const move = page.waitForRequest((r) => r.method() === 'POST' && new URL(r.url()).pathname === '/api/box/preset-move');
      await page.getByRole('button', { name: 'Move it to key 3' }).click();
      expect((await move).postDataJSON()).toEqual({ from: 8, to: 3 });
    });
  });
});

test.describe('alarm', () => {
  test('an alarm can wake on a row-2 key', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#alarmList [data-alarm-edit="0"]').click();
    await expect(page.locator('#alarmRowSw button[data-row="1"]')).toHaveAttribute('aria-pressed', 'true');
    await page.locator('#alarmTime').fill('07:15');
    await page.locator('#alarmRowSw button[data-row="2"]').click();
    await expect(page.locator('#alarmSlots button')).toHaveCount(6);
    await expect(page.locator('#alarmSlots button').first()).toContainText('2× 1');
    // The switch re-renders the editor; the typed time survives it.
    await expect(page.locator('#alarmTime')).toHaveValue('07:15');
    await page.locator('#alarmSlots [data-alarm-slot="9"]').click();
    await expect(page.locator('#alarmSlots [data-alarm-slot="9"]')).toHaveClass(/armed/);
    await expect(page.locator('#alarmList')).toContainText('Preset 3 · 2×');
    const put = page.waitForRequest((r) => r.method() === 'PUT' && new URL(r.url()).pathname === '/api/alarms');
    await page.locator('#btnAlarmSave').click();
    const body = (await put).postDataJSON();
    expect(body.alarms[0].slot).toBe(9);
  });

  test.describe('on a row-2 key already', () => {
    test.use({
      phoneOverrides: { '/api/alarms': { body: { ...PHONE.ALARMS, alarms: [{ ...PHONE.ALARMS.alarms[0], slot: 10 }] } } },
    });
    test('the editor opens on row 2', async ({ phone }) => {
      const { page } = phone;
      await bootDone(page);
      await expect(page.locator('#alarmList')).toContainText('Preset 4 · 2×');
      await page.locator('#alarmList [data-alarm-edit="0"]').click();
      await expect(page.locator('#alarmRowSw button[data-row="2"]')).toHaveAttribute('aria-pressed', 'true');
      await expect(page.locator('#alarmSlots [data-alarm-slot="10"]')).toHaveClass(/armed/);
    });
  });
});

test.describe('playing on row 2', () => {
  test.use({ phoneRow2: true, phonePlaying: { name: 'Jazz Lounge', loc: ROW2_LOC, key: '' } });

  test('row 1 shows a playing dot on 2× and lights no row-1 tile', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(seg(page, 2).locator('.rdot:not(.new)')).toHaveCount(1);
    await expect(seg(page, 2)).toHaveAccessibleName(/plays now/);
    // Tile 3 of row 1 is key 3, not "the third key of whatever row plays".
    await expect(page.locator('#presets .preset.active')).toHaveCount(0);
    await seg(page, 2).click();
    await expect(page.locator('#presets .preset[data-slot="9"]')).toHaveClass(/active/);
    await expect(page.locator('#presets .preset.active')).toHaveCount(1);
    await expect(seg(page, 1).locator('.rdot')).toHaveCount(0);
  });
});

test.describe('playing key named by the agent header', () => {
  test.use({ phoneRow2: true, phonePlaying: { name: 'Some Playlist', loc: 'http://192.0.2.50:8888/spotify/stream.ogg', key: '11' } });
  test('X-STR-Playing-Key 11 lights key 5 pressed twice', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(seg(page, 2).locator('.rdot')).toHaveCount(1);
    await seg(page, 2).click();
    await expect(page.locator('#presets .preset[data-slot="11"]')).toHaveClass(/active/);
  });
});

test.describe('layout', () => {
  test.use({ phoneRow2: true });
  for (const [w, h, scale] of [[390, 844, 1], [320, 640, 1], [390, 844, 3]]) {
    test(`keys stay above the fold and the heading fits at ${w}x${h}, text scale ${scale}`, async ({ phone }) => {
      const { page } = phone;
      await page.setViewportSize({ width: w, height: h });
      if (scale !== 1) {
        // The "Aa" menu's Extra large text: a zoom on body's children.
        await page.evaluate((s) => { localStorage.setItem('a11yScale', String(s)); window.a11yApply(); }, scale);
      }
      await bootDone(page);
      await page.evaluate(() => window.scrollTo(0, 0));
      for (const sel of ['#lblPresets', '#presetRowSw']) {
        const b = await page.locator(sel).boundingBox();
        expect(b.x).toBeGreaterThanOrEqual(0);
        expect(b.x + b.width).toBeLessThanOrEqual(w + 0.5);
      }
      const lbl = await page.locator('#lblPresets').boundingBox();
      const sw = await page.locator('#presetRowSw').boundingBox();
      expect(sw.y).toBeLessThan(lbl.y + lbl.height);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
      if (scale === 1) {
        // The heading line with the switch is no taller than the label alone
        // was: the keys start where they start on an agent without the row.
        const top = () => page.evaluate(() => document.getElementById('presets').getBoundingClientRect().top + window.scrollY);
        const withSwitch = await top();
        await page.evaluate(() => window.notePresetRows({ version: 'old' }));
        await expect(page.locator('#presetRowSw')).toBeHidden();
        const without = await top();
        await page.evaluate(() => window.notePresetRows({ presetRows: '2' }));
        await expect(page.locator('#presetRowSw')).toBeVisible();
        expect(Math.abs(withSwitch - without)).toBeLessThanOrEqual(1);
      }
      if (scale === 1 && w === 390) {
        // The six keys end above the tab bar, as #1371 arranged.
        const gridBottom = await page.evaluate(() => document.getElementById('presets').getBoundingClientRect().bottom);
        const barTop = await page.evaluate(() => document.querySelector('.tabbar').getBoundingClientRect().top);
        expect(gridBottom).toBeLessThanOrEqual(barTop);
      }
    });
  }
});

test('every language carries the row strings, translated and without dashes', async ({ phone }) => {
  const { page } = phone;
  await bootDone(page);
  const KEYS = ['rowSw', 'rowOnce', 'rowTwice', 'keyTwice', 'row2Empty', 'row2Find', 'rowNew', 'rowPlays'];
  const dicts = await page.evaluate(() => window.I18N2);
  const langs = Object.keys(dicts);
  expect(langs.length).toBe(12);
  for (const lang of langs) {
    for (const k of KEYS) {
      const v = dicts[lang][k];
      expect(v, `${lang}.${k}`).toBeTruthy();
      expect(v, `${lang}.${k} dash`).not.toMatch(/[–—]| - /);
      if (lang !== 'en' && k !== 'rowSw') expect(v, `${lang}.${k} untranslated`).not.toBe(dicts.en[k]);
    }
    expect(dicts[lang].keyTwice, `${lang}.keyTwice placeholder`).toContain('{n}');
  }
});

test.describe('an agent without the second row', () => {
  test.use({ phonePresetRows: false, phoneRow2: false });
  test('shows no switch and the keys exactly as before', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    // Give the version answer time to arrive; the switch must still be absent.
    await expect.poll(() => phone.handler.calls.some((c) => c.url.endsWith('/api/agent/version'))).toBe(true);
    await expect(page.locator('#presetRowSw')).toBeHidden();
    await expect(page.locator('#presetHead')).not.toHaveClass(/two/);
    await expect(page.locator('#lblPresets')).toHaveCSS('margin-top', '18px');
    expect(await tileSlots(page)).toEqual([1, 2, 3, 4, 5, 6]);
    await expect(page.locator('#presets .preset').first().locator('.num')).toHaveText('#1');
    await page.locator('#alarmList [data-alarm-edit="0"]').click();
    await expect(page.locator('#alarmRowSw')).toHaveCount(0);
    await expect(page.locator('#alarmSlots button')).toHaveCount(6);
  });
});
