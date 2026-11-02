// The display settings in the classic layout: every note sits under the row it
// explains and reads as a paragraph of its own (#1677). The clock note used to
// trail the track notes on the same line, below the preview picture, where it
// read as a label that had lost its control.

import { test, expect } from '../fixtures.mjs';

const PANEL = {
  GetDisplayTrack: { value: { enabled: true, onPanel: true, supported: true } },
  GetDisplayClock: { value: { enabled: true, supported: true } },
};

test.describe('classic layout: display settings notes', () => {
  test.use({ ui2: false, scenario: { bindings: PANEL } });

  test('each note follows its own row and stands on its own line', async ({ app }) => {
    const { page } = app;
    await page.locator('.tab-btn[data-view="settings"]').click();
    await page.evaluate(() => document.querySelectorAll('#settingsBody details').forEach((d) => { d.open = true; }));
    const sec = page.locator('#displayTrackSection');
    await expect(sec.locator('#displayClockRow')).toBeVisible();
    await expect(sec.locator('#displayClockHelp')).toBeVisible();
    await expect(sec.locator('#displayTrackHelpPanel')).toBeVisible();

    const layout = await sec.evaluate((el) => {
      const kids = [...el.children].filter((c) => getComputedStyle(c).display !== 'none').map((c) => c.id || c.tagName);
      const help = el.querySelector('#displayClockHelp');
      return { kids, display: getComputedStyle(help).display };
    });
    // The track note right under the on/off row, the clock note right under
    // the clock row, the picture after both.
    const at = (id) => layout.kids.indexOf(id);
    expect(at('displayTrackHelpPanel')).toBe(at('displayTrackModeRow') - 1);
    expect(at('displayClockHelp')).toBe(at('displayClockRow') + 1);
    expect(at('displayPreview')).toBeGreaterThan(at('displayClockHelp'));
    expect(layout.display).toBe('block');

    if (process.env.SHOOT_DIR) {
      await sec.screenshot({ path: `${process.env.SHOOT_DIR}/display-notes-classic.png`, animations: 'disabled' });
    }
  });
});
