// The clock's brightness (#1226) in Speaker settings, classic and UI 2.0
// (the phone remote: phone-clock-brightness.spec.mjs). The row shows only where the speaker
// reports a brightnessLevel, its note sits right under it (#1677), and a slide
// is written once, when it is let go. SHOOT_DIR saves the screenshots.

import { test, expect } from '../fixtures.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LR = ROSTER.livingRoom;

async function shoot(locator, name) {
  if (process.env.SHOOT_DIR) {
    await locator.screenshot({ path: `${process.env.SHOOT_DIR}/${name}.png`, animations: 'disabled' });
  }
}

test.describe('classic layout', () => {
  test.use({ ui2: false });

  test('the brightness row shows the speaker level and writes it when let go', async ({ app, mock }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="settings"]').click();
    await page.evaluate(() => document.querySelectorAll('#settingsBody details').forEach((d) => { d.open = true; }));
    const sec = page.locator('#clockSection');
    await expect(sec.locator('#clockBrightnessRow')).toBeVisible();
    await expect(sec.locator('#clockBrightnessHelp')).toBeVisible();
    await expect(sec.locator('#boxClockBrightness')).toHaveValue('40');
    await expect(sec.locator('#boxClockBrightnessValue')).toHaveText('40 %');
    // The note comes right after its row.
    const order = await sec.evaluate((el) => [...el.children].filter((c) => getComputedStyle(c).display !== 'none').map((c) => c.id));
    expect(order.indexOf('clockBrightnessHelp')).toBe(order.indexOf('clockBrightnessRow') + 1);
    await shoot(sec, 'clock-brightness-classic');

    await mock.resetCalls();
    await sec.locator('#boxClockBrightness').fill('20');
    await mock.expectCalled('SetClockBrightness', { times: 1 });
    const set = await mock.last('SetClockBrightness');
    expect(set.args[0]).toBe(LR.host);
    expect(set.args[2]).toBe(20);
    expect(await mock.count('SetClockDisplay')).toBe(0);
  });

  test.describe('a speaker that reports no brightness', () => {
    test.use({ scenario: { bindings: { GetClockBrightness: { value: -1 } } } });

    test('has no brightness row', async ({ app }) => {
      const { page } = app;
      await page.locator('.tab-btn[data-view="settings"]').click();
      await page.evaluate(() => document.querySelectorAll('#settingsBody details').forEach((d) => { d.open = true; }));
      await expect(page.locator('#boxClockOn')).toBeVisible();
      await expect(page.locator('#clockBrightnessRow')).toBeHidden();
      await expect(page.locator('#clockBrightnessHelp')).toBeHidden();
    });
  });
});

test.describe('UI 2.0', () => {
  test.use({ ui2: true });

  test('the brightness row sits in the clock card on the Sound tab', async ({ app }) => {
    const { page } = app;
    await page.locator('#ui2Sidebar .ui2-nav-item[data-view="speakers"]').click();
    await page.locator(`#view-speakers .spl-open[data-host="${LR.host}"]`).click();
    await page.locator('#ui2SpkTab-sound').click();
    const sec = page.locator('#clockSection');
    await expect(sec.locator('#clockBrightnessRow')).toBeVisible();
    await expect(sec.locator('#boxClockBrightness')).toHaveValue('40');
    await shoot(sec, 'clock-brightness-ui2');
  });
});
