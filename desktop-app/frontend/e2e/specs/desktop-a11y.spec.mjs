// Accessibility scans of the desktop app against baselines/a11y-desktop.json.
//
// Matrix: theme (dark, light, contrast) x layout (classic, UI 2.0 preview) x
// view (box, library, settings with every group opened, setup). The UI 2.0
// box view also scans its two player popovers, More and Levels (Levels only
// exists while the controlled speaker plays in a group, so that scan runs on
// a grouped scenario). Scan keys: `${theme}|${ui2 ? 'ui2' : 'old'}|${view}`.
//
// Every scan runs under the fake clock, settled past the start-up timers, with
// the KILL_ANIM stylesheet injected, so a poll or a transition never changes
// the DOM half-way through axe.
//
// Refresh the baseline: E2E_UPDATE_A11Y=1 npx playwright test --workers=1 specs/desktop-a11y.spec.mjs
// then give every new entry a real note.

import { test, expect } from '../fixtures.mjs';
import { settle } from '../helpers/clock.mjs';
import { KILL_ANIM, scan } from '../helpers/a11y.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const BASELINE = 'baselines/a11y-desktop.json';
const THEMES = ['dark', 'light', 'contrast'];

// Living room leads, Bedroom follows, both playing: the player shows Levels.
const GROUP = {
  master: ROSTER.livingRoom.deviceID,
  senderIP: ROSTER.livingRoom.host,
  members: [
    { deviceID: ROSTER.livingRoom.deviceID, ip: ROSTER.livingRoom.host },
    { deviceID: ROSTER.bedroom.deviceID, ip: ROSTER.bedroom.host },
  ],
};
const GROUPED = {
  playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' },
  zones: { livingRoom: GROUP, bedroom: GROUP },
};

// What a view shows once it has loaded.
const LOADED = {
  box: '#view-box:not(.hidden) #presets .preset-body',
  library: '#view-library:not(.hidden) .library-row-folder',
  settings: '#view-settings:not(.hidden) .settings-group',
  setup: '#view-setup:not(.hidden) .setup-target-card',
};

async function prepare(page) {
  await settle(page, 9_000);
  await page.addStyleTag({ content: KILL_ANIM });
}

async function openView(page, ui2, view) {
  if (view !== 'box') {
    const tab = ui2 ? `.ui2-nav-item[data-view="${view}"]` : `.tab-btn[data-view="${view}"]`;
    await page.locator(tab).first().click();
    await page.clock.runFor(2_000);
  }
  await expect(page.locator(LOADED[view]).first()).toBeVisible();
  if (view === 'settings') {
    const groups = page.locator('#view-settings .settings-group');
    await page.evaluate(() => {
      document.querySelectorAll('#view-settings .settings-group').forEach((d) => { d.open = true; });
    });
    const n = await groups.count();
    expect(n).toBeGreaterThan(1);
    for (let i = 0; i < n; i++) await expect(groups.nth(i)).toHaveAttribute('open', '');
    await page.clock.runFor(1_000);
  }
}

// axe-core schedules its rule batches with setTimeout, which the paused page
// clock never fires, so axe.run would hang. Step the clock in 4 ms ticks while
// the scan runs (a scan moves it by well under half a second, and three runs
// in a row found the same counts every time) and record how far
// it moved as an annotation.
async function scanTicking(page, key) {
  const start = await page.evaluate(() => Date.now());
  let done = false;
  const run = scan(page, key, BASELINE).finally(() => { done = true; });
  const tick = (async () => {
    while (!done) await page.clock.runFor(4).catch(() => {});
  })();
  await Promise.all([run, tick]);
  const moved = (await page.evaluate(() => Date.now())) - start;
  test.info().annotations.push({ type: 'scan-clock-ms', description: String(moved) });
  return run;
}

async function openPopover(page, btn, pop) {
  await expect(page.locator(btn)).toBeVisible();
  await page.locator(btn).click();
  await expect(page.locator(pop)).toBeVisible();
  await expect(page.locator(btn)).toHaveAttribute('aria-expanded', 'true');
}

for (const theme of THEMES) {
  for (const ui2 of [false, true]) {
    const layout = ui2 ? 'ui2' : 'old';
    test.describe(`${theme}, ${layout}`, () => {
      test.use({ theme, ui2, fakeClock: true });

      for (const view of Object.keys(LOADED)) {
        test(`${view} view`, async ({ app }) => {
          const { page } = app;
          await prepare(page);
          await openView(page, ui2, view);
          await scanTicking(page, `${theme}|${layout}|${view}`);
        });
      }

      if (ui2) {
        test('pop-more', async ({ app }) => {
          const { page } = app;
          await prepare(page);
          await openView(page, ui2, 'box');
          await openPopover(page, '#ui2MoreBtn', '#ui2MorePop');
          await scanTicking(page, `${theme}|${layout}|pop-more`);
        });

        test.describe('grouped', () => {
          test.use({ scenario: GROUPED });

          test('pop-levels', async ({ app }) => {
            const { page } = app;
            await prepare(page);
            await openView(page, ui2, 'box');
            await openPopover(page, '#ui2LevelsBtn', '#ui2LevelsPop');
            await scanTicking(page, `${theme}|${layout}|pop-levels`);
          });
        });
      }
    });
  }
}
