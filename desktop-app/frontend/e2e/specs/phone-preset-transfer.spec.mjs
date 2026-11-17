// phone-preset-transfer: copying this speaker's keys to other speakers from
// the phone remote (#1591, Ideas #1780). The page cannot reach another
// speaker itself, so it asks its own agent (POST /api/box/preset-copy) and
// shows the agent's answer per speaker. Covers one target, all targets, both
// rows, the replace warning, a conflict, a partial failure, a busy agent, the
// layout at 320px and the strings in every language.

import { test, expect } from '../fixtures.mjs';

const COPY = '/api/box/preset-copy';

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
  await expect(page.locator('#btnXfer')).toBeVisible();
}

// The request the page sends when Transfer is confirmed.
async function confirmAndCatch(page) {
  const req = page.waitForRequest((r) => r.method() === 'POST' && new URL(r.url()).pathname === COPY);
  await page.locator('#btnXferGo').click();
  return (await req).postDataJSON();
}

const ok = (name, ip, copied, extra = {}) => ({ name, ip, ok: true, copied, ...extra });

test.describe('one target', () => {
  test.use({
    phoneOverrides: { [COPY]: { body: { keys: 6, rows: 2, done: 1, total: 1, results: [ok('Kitchen', '192.0.2.51', 6)] } } },
  });

  test('opens from the heading, warns that it replaces keys 1 to 6, copies to the picked speaker', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await expect(page.locator('#btnXfer')).toHaveAttribute('aria-expanded', 'false');
    await expect(page.locator('#xferCard')).toBeHidden();
    await page.locator('#btnXfer').click();
    await expect(page.locator('#btnXfer')).toHaveAttribute('aria-expanded', 'true');
    await expect(page.locator('#xferCard')).toBeVisible();
    await expect(page.locator('#xferCard')).toContainText('Copy keys to another speaker');
    // Row 2 holds nothing: only keys 1 to 6 are replaced.
    await expect(page.locator('#xferScope')).toHaveText('On the speaker you choose, this replaces keys 1 to 6.');
    // Every answering speaker, then "all" (three of them).
    await expect(page.locator('#xferTarget option')).toHaveText(['Kitchen', 'Bedroom', 'Office', 'All other speakers (3)']);
    await page.locator('#xferTarget').selectOption('192.0.2.51');
    expect(await confirmAndCatch(page)).toEqual({ targets: ['192.0.2.51'] });
    await expect(page.locator('#xferMsg')).toHaveText('Kitchen took the keys (6).');
    await expect(page.locator('#xferMsg')).toHaveClass(/ok/);
    await page.locator('#btnXferClose').click();
    await expect(page.locator('#xferCard')).toBeHidden();
    await expect(page.locator('#btnXfer')).toHaveAttribute('aria-expanded', 'false');
  });

  test('Cancel sends nothing', async ({ phone }) => {
    const { page, handler } = phone;
    await bootDone(page);
    await page.locator('#btnXfer').click();
    await page.locator('#btnXferNo').click();
    await expect(page.locator('#xferCard')).toBeHidden();
    expect(handler.calls.some((c) => new URL(c.url).pathname === COPY)).toBe(false);
  });
});

test.describe('both rows', () => {
  test.use({
    phoneRow2: true,
    phoneOverrides: {
      [COPY]: {
        body: {
          keys: 10, rows: 2, done: 2, total: 2,
          results: [ok('Bedroom', '192.0.2.52', 10), ok('Kitchen', '192.0.2.51', 6, { row1Only: true, notCopied: 4 })],
        },
      },
    },
  });

  test('says both rows are replaced, sends "all" and names the one-row speaker', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#btnXfer').click();
    await expect(page.locator('#xferScope')).toHaveText('On the speaker you choose, this replaces the keys pressed once and the keys pressed twice.');
    await page.locator('#xferTarget').selectOption('__all');
    expect(await confirmAndCatch(page)).toEqual({ all: true });
    const lines = page.locator('#xferMsg p');
    await expect(lines).toHaveText([
      'All 2 speakers took the keys.',
      'Kitchen has only one row of keys, so the keys pressed twice were not copied there.',
    ]);
    await expect(page.locator('#xferMsg')).toHaveClass(/ok/);
  });
});

test.describe('all targets', () => {
  test.use({
    phoneOverrides: {
      [COPY]: {
        body: {
          keys: 6, rows: 2, done: 3, total: 3,
          results: [ok('Bedroom', '192.0.2.52', 6), ok('Kitchen', '192.0.2.51', 6), ok('Office', '192.0.2.53', 6)],
        },
      },
    },
  });

  test('one line for all of them', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#btnXfer').click();
    await page.locator('#xferTarget').selectOption('__all');
    expect(await confirmAndCatch(page)).toEqual({ all: true });
    await expect(page.locator('#xferMsg p')).toHaveText(['All 3 speakers took the keys.']);
  });
});

test.describe('conflict', () => {
  test.use({
    phoneOverrides: {
      [COPY]: {
        body: {
          keys: 6, rows: 2, done: 0, total: 1,
          results: [{ name: 'Kitchen', ip: '192.0.2.51', ok: false, copied: 0, code: 'already-on-slot', conflict: { name: 'Jazz24', slot: 9 } }],
        },
      },
    },
  });

  test('names the station and the key it already sits on', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#btnXfer').click();
    await confirmAndCatch(page);
    await expect(page.locator('#xferMsg')).toHaveText('Jazz24 is already on key 3 · 2× of Kitchen. Delete that key there, then transfer again.');
    await expect(page.locator('#xferMsg')).toHaveClass(/error/);
  });
});

test.describe('partial failure', () => {
  test.use({
    phoneOverrides: {
      [COPY]: {
        body: {
          keys: 6, rows: 2, done: 1, total: 3,
          results: [
            ok('Bedroom', '192.0.2.52', 6),
            { name: 'Kitchen', ip: '192.0.2.51', ok: false, copied: 0, code: 'unreachable', error: 'no STR agent answered' },
            { name: 'Office', ip: '192.0.2.53', ok: false, copied: 0, code: 'refused', error: 'status 500' },
          ],
        },
      },
    },
  });

  test('counts the speakers that took the keys and says why the others did not', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#btnXfer').click();
    await page.locator('#xferTarget').selectOption('__all');
    await confirmAndCatch(page);
    await expect(page.locator('#xferMsg p')).toHaveText([
      '1 of 3 speakers took the keys.',
      'Kitchen did not answer. Check that it is switched on, then transfer again.',
      'Office did not take the keys.',
    ]);
    await expect(page.locator('#xferMsg')).toHaveClass(/warn/);
  });
});

test.describe('the agent refuses the run', () => {
  test.use({ phoneOverrides: { [COPY]: { status: 409, body: { code: 'busy', error: 'a copy is already running on this speaker' } } } });

  test('a second run says one is already going', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await page.locator('#btnXfer').click();
    await confirmAndCatch(page);
    await expect(page.locator('#xferMsg')).toHaveText('A transfer is already running on this speaker. Try again in a moment.');
  });
});

test.describe('nowhere to copy to', () => {
  test.use({ phoneOverrides: { '/api/peers': { body: [] } } });
  test('no other speaker: no Transfer button', async ({ phone }) => {
    const { page, handler } = phone;
    await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
    await expect.poll(() => handler.calls.some((c) => new URL(c.url).pathname === '/api/peers')).toBe(true);
    await expect(page.locator('#btnXfer')).toBeHidden();
  });
});

test.describe('layout at 320px', () => {
  test.use({ phoneRow2: true, phoneLang: 'de' });
  test('heading, switch and card fit, the button keeps a 40px target', async ({ phone }) => {
    const { page } = phone;
    await page.setViewportSize({ width: 320, height: 640 });
    await bootDone(page);
    await page.evaluate(() => window.scrollTo(0, 0));
    for (const sel of ['#lblPresets', '#btnXfer', '#presetRowSw']) {
      const b = await page.locator(sel).boundingBox();
      expect(b.x, sel).toBeGreaterThanOrEqual(0);
      expect(b.x + b.width, sel).toBeLessThanOrEqual(320.5);
    }
    const btn = await page.locator('#btnXfer').boundingBox();
    expect(btn.height).toBeGreaterThanOrEqual(40);
    expect(btn.width).toBeGreaterThanOrEqual(40);
    // The button does not overlap the switch.
    const sw = await page.locator('#presetRowSw').boundingBox();
    expect(btn.x + btn.width).toBeLessThanOrEqual(sw.x + 0.5);
    // The label's text stays left of the tools.
    const textRight = await page.evaluate(() => {
      const l = document.getElementById('lblPresets');
      return l.getBoundingClientRect().right - parseFloat(getComputedStyle(l).paddingRight);
    });
    expect(textRight).toBeLessThanOrEqual(btn.x + 0.5);
    // Only the icon shows at this width; the word stays the button's name.
    await expect(page.locator('#btnXferLbl')).toHaveCSS('width', '1px');
    await expect(page.locator('#btnXfer')).toHaveAccessibleName('Übertragen');
    await expect(page.locator('#btnXfer')).toHaveAttribute('title', 'Tasten auf einen anderen Lautsprecher kopieren');
    await page.locator('#btnXfer').click();
    await expect(page.locator('#xferScope')).toHaveText('Auf dem Lautsprecher, den du wählst, ersetzt das die Tasten für einmal und für zweimal Drücken.');
    for (const sel of ['#xferCard', '#xferTarget', '#btnXferGo', '#btnXferNo']) {
      const b = await page.locator(sel).boundingBox();
      expect(b.x + b.width, sel).toBeLessThanOrEqual(320.5);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  });
});

test('every language carries the transfer strings, translated and without dashes', async ({ phone }) => {
  const { page } = phone;
  await bootDone(page);
  const dicts = await page.evaluate(() => window.I18N5);
  const langs = Object.keys(dicts);
  expect(langs.length).toBe(12);
  expect(langs.sort()).toEqual(Object.keys(await page.evaluate(() => window.I18N)).sort());
  const keys = Object.keys(dicts.en);
  for (const lang of langs) {
    expect(Object.keys(dicts[lang]).sort(), `${lang} keys`).toEqual(keys.slice().sort());
    for (const k of keys) {
      const v = dicts[lang][k];
      expect(v, `${lang}.${k}`).toBeTruthy();
      expect(v, `${lang}.${k} dash`).not.toMatch(/[–—]| - /);
      if (lang !== 'en') expect(v, `${lang}.${k} untranslated`).not.toBe(dicts.en[k]);
      // The placeholders of the English line, and only those.
      const ph = (s) => (s.match(/\{\w+\}/g) || []).sort().join(',');
      expect(ph(v), `${lang}.${k} placeholders`).toBe(ph(dicts.en[k]));
    }
  }
});
