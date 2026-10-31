// phone-ui2-switch: the phone remote's own UI 2.0 preview switch, a Layout
// row in the "Aa" menu stored under localStorage str.ui2Preview. Off by
// default, a tap on the preview reloads into it (and back), and a phone that
// cannot keep the choice works as before and never sees the row.

import { test, expect } from '../fixtures.mjs';

async function bootDone(page) {
  await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
}

async function openMenu(page) {
  await page.locator('#a11yTrigger').click();
  await expect(page.locator('#a11yMenu')).toBeVisible();
}

test('is off by default and the Layout row shows Current pressed', async ({ phone }) => {
  const { page } = phone;
  await bootDone(page);
  await expect(page.locator('html')).not.toHaveClass(/(^|\s)ui2(\s|$)/);
  expect(await page.evaluate(() => localStorage.getItem('str.ui2Preview'))).toBeNull();
  await openMenu(page);
  await expect(page.locator('#a11yLayoutGroup')).toBeVisible();
  await expect(page.locator('#a11yLayoutLabel')).toHaveText('Layout');
  await expect(page.locator('#a11yMenu button[data-layout="classic"]')).toHaveText('Current');
  await expect(page.locator('#a11yMenu button[data-layout="classic"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('#a11yMenu button[data-layout="ui2"]')).toHaveText('UI 2.0 preview');
  await expect(page.locator('#a11yMenu button[data-layout="ui2"]')).toHaveAttribute('aria-pressed', 'false');
  expect(phone.errors).toEqual([]);
});

test('a tap on the preview reloads into it, and Current goes back', async ({ phone }) => {
  const { page } = phone;
  await bootDone(page);
  await openMenu(page);
  await Promise.all([
    page.waitForEvent('load'),
    page.locator('#a11yMenu button[data-layout="ui2"]').click(),
  ]);
  await expect(page.locator('html')).toHaveClass(/(^|\s)ui2(\s|$)/);
  expect(await page.evaluate(() => localStorage.getItem('str.ui2Preview'))).toBe('1');
  await bootDone(page);
  await openMenu(page);
  await expect(page.locator('#a11yMenu button[data-layout="ui2"]')).toHaveAttribute('aria-pressed', 'true');
  await Promise.all([
    page.waitForEvent('load'),
    page.locator('#a11yMenu button[data-layout="classic"]').click(),
  ]);
  await expect(page.locator('html')).not.toHaveClass(/(^|\s)ui2(\s|$)/);
  expect(await page.evaluate(() => localStorage.getItem('str.ui2Preview'))).toBe('0');
  expect(phone.errors).toEqual([]);
});

test('tapping the layout already on only closes the menu', async ({ phone }) => {
  const { page } = phone;
  await bootDone(page);
  await openMenu(page);
  // A reload would take this marker with it; the handler closes and returns.
  await page.evaluate(() => { window.__stillHere = true; });
  await page.locator('#a11yMenu button[data-layout="classic"]').click();
  await expect(page.locator('#a11yMenu')).toBeHidden();
  expect(await page.evaluate(() => window.__stillHere)).toBe(true);
  expect(await page.evaluate(() => localStorage.getItem('str.ui2Preview'))).toBeNull();
});

test.describe('in German', () => {
  test.use({ locale: 'de-DE' });

  test('the Layout row carries the desktop wording', async ({ phone }) => {
    const { page } = phone;
    await bootDone(page);
    await openMenu(page);
    await expect(page.locator('#a11yLayoutLabel')).toHaveText('Layout');
    await expect(page.locator('#a11yMenu button[data-layout="classic"]')).toHaveText('Aktuell');
    await expect(page.locator('#a11yMenu button[data-layout="ui2"]')).toHaveText('Vorschau UI 2.0');
  });
});

test('without storage the page works on the current layout and hides the row', async ({ phone }) => {
  const { page } = phone;
  await bootDone(page);
  await page.addInitScript(() => {
    Object.defineProperty(window, 'localStorage', {
      configurable: true,
      get() { throw new DOMException('The operation is insecure.', 'SecurityError'); },
    });
  });
  await page.reload();
  await bootDone(page);
  await expect(page.locator('html')).not.toHaveClass(/(^|\s)ui2(\s|$)/);
  await openMenu(page);
  await expect(page.locator('#a11yLayoutGroup')).toBeHidden();
  // The other two rows still work.
  await page.locator('#a11yMenu button[data-theme="light"]').click();
  await expect(page.locator('html')).toHaveClass(/a11y-light/);
  expect(phone.errors).toEqual([]);
});

test('a page opened from the preview list as #ui2 keeps the preview and drops the hash', async ({ phone }) => {
  const { page } = phone;
  await bootDone(page);
  await page.goto(page.url().split('#')[0] + '#ui2');
  await page.reload();
  await expect(page.locator('html')).toHaveClass(/(^|\s)ui2(\s|$)/);
  expect(new URL(page.url()).hash).toBe('');
  expect(await page.evaluate(() => localStorage.getItem('str.ui2Preview'))).toBe('1');
  expect(phone.errors).toEqual([]);
});
