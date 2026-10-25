// The harness canary: proves the shared foundation works before any real spec
// leans on it (the desktop app and the phone remote boot on the mock, a click
// reaches a binding, an emitted event reaches a listener, the fake clock
// drives the polls). Deleted once the real specs are merged.

import { test, expect } from '../fixtures.mjs';
import { onHost } from '../helpers/mockclient.mjs';
import { settle } from '../helpers/clock.mjs';

test.describe('desktop, classic layout', () => {
  test('boots on the mock and plays a preset', async ({ app, mock }) => {
    const { page } = app;
    await expect(page.locator('html')).not.toHaveClass(/\bui2\b/);
    await expect(page.locator('html')).toHaveAttribute('lang', 'en');
    await expect(page.locator('#presets .preset-body')).toHaveCount(6);
    await mock.resetCalls();
    await page.locator('#presets .preset-body').first().click();
    await mock.expectCalled('PlaySlot', { times: 1 });
    const play = await mock.last('PlaySlot');
    expect(play.args).toEqual(['demobox.local', 8888, 1]);
  });

  test('an emitted backend event reaches a registered listener', async ({ app, mock }) => {
    const { page } = app;
    await page.evaluate(() => {
      window.__got = [];
      window.runtime.EventsOnMultiple('e2e:canary', (p) => window.__got.push(p), 1);
    });
    expect(await mock.emit('e2e:canary', { n: 1 })).toBe(1);
    expect(await mock.emit('e2e:canary', { n: 2 })).toBe(0); // max 1: the listener is gone
    expect(await page.evaluate(() => window.__got)).toEqual([{ n: 1 }]);
  });
});

test.describe('desktop, UI 2.0 preview, fake clock', () => {
  test.use({ ui2: true, fakeClock: true });

  test('boots the shell and the clock drives the status poll', async ({ app, mock }) => {
    const { page } = app;
    await expect(page.locator('#ui2SpkLists .ui2-spk-pick').first()).toBeVisible();
    await settle(page, 9_000);
    await mock.resetCalls();
    await page.clock.runFor(30_000);
    expect(await mock.count('Status', onHost('demobox.local'))).toBeGreaterThan(0);
  });
});

test.describe('phone remote', () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test('boots on the mock with six presets', async ({ phone }) => {
    await expect(phone.page.locator('#presets .preset.has-art')).toHaveCount(6);
  });
});
