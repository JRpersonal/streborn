// The clock's brightness (#1226) on the phone remote's display card: shown
// where the speaker reports a brightnessLevel, written alone when the slider
// is let go. The desktop side is desktop-clock-brightness.spec.mjs. SHOOT_DIR
// saves the screenshot.

import { test, expect } from '../fixtures.mjs';

async function shoot(locator, name) {
  if (process.env.SHOOT_DIR) {
    await locator.screenshot({ path: `${process.env.SHOOT_DIR}/${name}.png`, animations: 'disabled' });
  }
}

test.describe('phone remote', () => {
  test.use({
    phoneOverrides: {
      '/api/box/display-clock': { body: { supported: true, enabled: true } },
      '/api/box/standby-clock': {
        body: {
          supported: true, hasClock: true, brightnessSupported: true,
          stored: { enabled: true, timezone: 'Europe/Berlin', format24: true, brightness: 30 },
          firmware: { enabled: true, timezone: 'Europe/Berlin', format24: true, brightness: 30 },
        },
      },
    },
  });

  test('the display card carries the brightness and posts it alone', async ({ phone }) => {
    const { page, handler } = phone;
    await expect(page.locator('#presets .preset.has-art')).toHaveCount(6);
    await page.locator('#tabMore').click();
    const card = page.locator('#dispCard');
    await expect(card).toBeVisible();
    await expect(card.locator('#dsBrightWrap')).toBeVisible();
    await expect(card.locator('#dsBright')).toHaveValue('30');
    await expect(card.locator('#dsBrightVal')).toHaveText('30 %');
    await shoot(card, 'clock-brightness-phone');
    await card.locator('#dsBright').fill('50');
    await expect.poll(() => handler.calls.some((c) => c.method === 'POST' && /\/api\/box\/standby-clock$/.test(c.url))).toBe(true);
  });
});
