// Layout geometry of the desktop app, in the default window (project desktop,
// 1240 x 740) and the smallest one it allows (desktop-min, 1000 x 600).
//
// Matrix: text size (1 normal, 2 large, 3 extra large) x layout (classic,
// UI 2.0 preview). For every cell:
//   - the Music view (box) and Settings never scroll sideways, neither the
//     window nor, in the shell, the main pane that scrolls in its place;
//   - in the shell the persistent player (#ui2Player) lies fully inside the
//     viewport and never overlaps the main pane (#ui2Main), solo and grouped;
//   - in the shell every control is a 40 x 40 target (shell2.css --u-target):
//     the navigation entries, More, Levels (grouped scenario only: it exists
//     only while the controlled speaker leads a group), every visible sidebar
//     row's pick button and "+"/"-" action, the mute button and the
//     title-swap label the shell moved into the player;
//   - in the shell every sidebar navigation entry takes a click: neither the
//     speaker list nor the Aa / language buttons are ever drawn over it.
//
// The sidebar lists every speaker (no room switcher, no Output popover any
// more); group volume and Levels live in the player.
//
// Every measurement runs under the fake clock, settled past the start-up
// timers, with KILL_ANIM injected so nothing is measured mid-transition.

import { test, expect } from '../fixtures.mjs';
import { settle } from '../helpers/clock.mjs';
import { KILL_ANIM } from '../helpers/a11y.mjs';
import { disjoint, inside, minSize, noHorizontalOverflow } from '../helpers/layout.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const SCALES = ['1', '2', '3'];

// Living room leads, Bedroom follows, both playing: the player shows Levels.
const GROUP = {
  master: ROSTER.livingRoom.deviceID,
  senderIP: ROSTER.livingRoom.host,
  members: [
    { deviceID: ROSTER.livingRoom.deviceID, ip: ROSTER.livingRoom.host },
    { deviceID: ROSTER.bedroom.deviceID, ip: ROSTER.bedroom.host },
  ],
};

// A proxied station (the demo plays /stream/2) with a two-part ICY title, so
// the title-swap switch is offered (titleswap.js) and its label is on screen.
const TITLE = { bindings: { StreamTitleInfo: { value: { title: 'Sultans of swing - Dire Straits', swapped: false } } } };

const SCENARIOS = {
  solo: { ...TITLE },
  grouped: {
    ...TITLE,
    playing: { livingRoom: 'PLAY_STATE', bedroom: 'PLAY_STATE' },
    zones: { livingRoom: GROUP, bedroom: GROUP },
  },
};

const LOADED = {
  box: '#view-box:not(.hidden) #presets .preset-body',
  settings: '#view-settings:not(.hidden) .settings-group',
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
}

// The shell keeps the window still and scrolls #ui2Main instead, so a page
// that only checked the document would miss a main pane that scrolls sideways.
async function mainFitsX(page) {
  const m = await page.locator('#ui2Main').evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }));
  expect(m.scroll, `#ui2Main scrolls sideways (${m.scroll} > ${m.client})`).toBeLessThanOrEqual(m.client + 1);
}

async function playerGeometry(page) {
  const viewport = page.viewportSize();
  const player = page.locator('#ui2Player');
  await expect(player).toBeVisible();
  const pBox = await player.boundingBox();
  const mBox = await page.locator('#ui2Main').boundingBox();
  expect(inside(viewport, pBox), `#ui2Player ${JSON.stringify(pBox)} leaves the ${viewport.width}x${viewport.height} viewport`).toBe(true);
  expect(mBox, '#ui2Main is rendered').not.toBeNull();
  expect(disjoint([pBox, mBox]), `#ui2Player ${JSON.stringify(pBox)} overlaps #ui2Main ${JSON.stringify(mBox)}`).toEqual([]);
}

// sidebarNavReachable: every navigation entry, scrolled into its own box,
// is the element a click at its centre lands on. At Extra large text in the
// smallest window, a group playing, the speaker list once squeezed the
// entries to nothing and the Aa / language buttons and the Office row were
// drawn over "Speaker settings" (mount.js holdSpeakerFloor).
async function sidebarNavReachable(page) {
  const bad = await page.locator('#ui2Sidebar').evaluate((side) => {
    const out = [];
    for (const it of side.querySelectorAll('.ui2-nav-item')) {
      it.scrollIntoView({ block: 'nearest' });
      const b = it.getBoundingClientRect();
      const hit = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
      if (!hit || !it.contains(hit)) out.push(`${it.dataset.view}: ${hit ? hit.className || hit.tagName : 'nothing'}`);
    }
    return out;
  });
  expect(bad, 'sidebar entries covered by something else').toEqual([]);
}

// waitForTitleSwap runs the page clock until the live-title poll
// (live.js scheduleLiveTitle, first read 1.2 s after a status poll sees a
// proxied station, then every 2 s) has fetched the two-part title and the
// player offers the swap switch.
async function waitForTitleSwap(page) {
  const swap = page.locator('#ui2Player #titleSwapRow');
  for (let i = 0; i < 30 && !(await swap.isVisible()); i++) await page.clock.runFor(2_000);
  await expect(swap).toBeVisible();
  return swap;
}

for (const scale of SCALES) {
  test.describe(`text size ${scale}, classic layout`, () => {
    test.use({ ui2: false, scale, fakeClock: true, scenario: SCENARIOS.solo });

    for (const view of ['box', 'settings']) {
      test(`${view}: no horizontal overflow`, async ({ app }) => {
        const { page } = app;
        await prepare(page);
        await openView(page, false, view);
        await noHorizontalOverflow(page);
      });
    }
  });

  for (const [scName, sc] of Object.entries(SCENARIOS)) {
    test.describe(`text size ${scale}, UI 2.0 preview, ${scName}`, () => {
      test.use({ ui2: true, scale, fakeClock: true, scenario: sc });

      for (const view of ['box', 'settings']) {
        test(`${view}: no horizontal overflow, the player inside the viewport and clear of the main pane`, async ({ app }) => {
          const { page } = app;
          await prepare(page);
          // The player at its tallest: the title-swap row is on screen too.
          await waitForTitleSwap(page);
          await openView(page, true, view);
          await expect(page.locator('html')).toHaveClass(/\bui2\b/);
          await noHorizontalOverflow(page);
          await mainFitsX(page);
          await playerGeometry(page);
          await sidebarNavReachable(page);
        });
      }

      test('every shell target is at least 40 x 40', async ({ app }) => {
        const { page } = app;
        await prepare(page);
        await openView(page, true, 'box');
        const swap = await waitForTitleSwap(page);
        // The frame has no room switcher and no Output popover any more.
        await expect(page.locator('#ui2RoomBtn')).toHaveCount(0);
        await expect(page.locator('#ui2OutputBtn')).toHaveCount(0);

        const nav = page.locator('#ui2Sidebar .ui2-nav-item');
        expect(await nav.count()).toBeGreaterThan(0);
        await minSize(nav, 40);

        await expect(page.locator('#ui2MoreBtn')).toBeVisible();
        await minSize(page.locator('#ui2MoreBtn'), 40);

        const levels = page.locator('#ui2LevelsBtn');
        if (scName === 'grouped') {
          await expect(page.locator('#ui2Player')).toHaveClass(/\bis-grouped\b/);
          await expect(levels).toBeVisible();
          await minSize(levels, 40);
        } else {
          await expect(levels).toBeHidden();
        }

        // Every speaker in the roster gets a row; "+" needs the first
        // every-speaker poll, which settle has run.
        const picks = page.locator('#ui2SpkLists .ui2-spk-pick');
        await expect(picks.first()).toBeVisible();
        expect(await picks.count()).toBeGreaterThanOrEqual(2);
        await minSize(picks, 40);
        const acts = page.locator('#ui2SpkLists .ui2-spk-act');
        await expect(acts.first()).toBeVisible();
        await minSize(acts, 40);

        const mute = page.locator('#ui2Player #volMute');
        await expect(mute).toBeVisible();
        await minSize(mute, 40);

        await minSize(swap, 40);
      });
    });
  }
}
