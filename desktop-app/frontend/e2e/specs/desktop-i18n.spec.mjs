// desktop-i18n: the language bundles on screen, in both layouts.
//
// 1) en shows no raw key: no bundle key, no ui2.* key and no {{ }} in the
//    visible text or in aria-label / title / placeholder.
// 2) The longest languages (and en) boot clean, set html[lang], and neither
//    the page nor the UI 2.0 frame, nav labels or speaker names clip sideways.
// 3) Arabic mirrors the UI 2.0 frame (sidebar right of main, now-playing right
//    of the transport).
// 4) Every other language runs left to right (desktop project only).
//
// Runs in the desktop and desktop-min projects (playwright.config.mjs).

import { test, expect } from '../fixtures.mjs';
import { KILL_ANIM } from '../helpers/a11y.mjs';
import { settle } from '../helpers/clock.mjs';
import { EN_KEYS, LOCALES, LONG, RTL } from '../helpers/i18n.mjs';
import { fitsX, noHorizontalOverflow } from '../helpers/layout.mjs';
import { ROSTER } from '../../testkit/demo-data.mjs';

const LAYOUTS = [
  { name: 'classic', ui2: false },
  { name: 'UI 2.0', ui2: true },
];

// Living Room leads, Bedroom follows, both playing: the UI 2.0 player shows
// Levels (#ui2LevelsBtn only appears while the controlled speaker is grouped).
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

// The classic tab bar has no wrap and no scroller: at the smallest window
// (1000 px) the Lithuanian and Ukrainian tab labels push the last tab past the
// window edge and the whole page scrolls sideways (lt 1018 px, uk 1082 px,
// the overflowing element is the last .tab-btn).
const CLASSIC_TABS_OVERFLOW = ['lt', 'uk'];

const UI2_KEY = /\bui2\.[a-z][\w.]*\b/;
// Anything shaped like a dotted key; checked against EN_KEYS in Node.
const DOTTED = /[A-Za-z][\w-]*(?:\.[\w-]+)+/g;

// The visible text plus the three label attributes of every element.
function collectText(page) {
  return page.evaluate(() => {
    const out = [document.body.innerText];
    for (const el of document.querySelectorAll('[aria-label], [title], [placeholder]')) {
      for (const a of ['aria-label', 'title', 'placeholder']) {
        const v = el.getAttribute(a);
        if (v) out.push(`${a}=${v}`);
      }
    }
    return out.join('\n');
  });
}

function rawKeyFindings(text) {
  const found = [];
  for (const tok of text.match(DOTTED) || []) {
    const bare = tok.replace(/[.-]+$/, '');
    if (EN_KEYS.has(bare)) found.push(`bundle key: ${bare}`);
  }
  const ui2 = text.match(new RegExp(UI2_KEY.source, 'g'));
  if (ui2) found.push(...ui2.map((k) => `ui2 key: ${k}`));
  for (const line of text.split('\n')) {
    if (line.includes('{{') || line.includes('}}')) found.push(`placeholder: ${line.trim().slice(0, 120)}`);
  }
  return [...new Set(found)];
}

// Opens a UI 2.0 popover, reads the page while it is open, closes it again.
async function readWithPopover(page, btn, pop) {
  await page.locator(btn).click();
  await expect(page.locator(pop)).toBeVisible();
  const text = await collectText(page);
  await page.locator(`${pop} .u-pop-close`).click();
  await expect(page.locator(pop)).toBeHidden();
  return text;
}

for (const layout of LAYOUTS) {
  test.describe(`${layout.name}, en`, () => {
    test.use({ ui2: layout.ui2, appLocale: 'en', fakeClock: true, scenario: GROUPED });

    test('shows no raw i18n key', async ({ app }) => {
      const { page } = app;
      // The first every-speaker poll learns the group, so Levels shows.
      await settle(page, 9_000);
      const texts = [];
      if (layout.ui2) {
        texts.push(await readWithPopover(page, '#ui2MoreBtn', '#ui2MorePop'));
        texts.push(await readWithPopover(page, '#ui2LevelsBtn', '#ui2LevelsPop'));
      }
      texts.push(await collectText(page));
      expect(rawKeyFindings(texts.join('\n'))).toEqual([]);
    });
  });

  for (const code of ['en', ...LONG]) {
    test.describe(`${layout.name}, ${code}`, () => {
      test.use({ ui2: layout.ui2, appLocale: code });

      test('boots clean and nothing clips sideways', async ({ app, mock }, testInfo) => {
        test.fixme(!layout.ui2 && testInfo.project.name === 'desktop-min' && CLASSIC_TABS_OVERFLOW.includes(code),
          'classic layout at the 1000 px minimum window: the tab bar overflows in lt and uk, the last .tab-btn ends at 1018 / 1082 px and the page scrolls sideways');
        const { page } = app;
        await expect(page.locator('html')).toHaveAttribute('lang', code);
        expect(await mock.pageErrors()).toEqual([]);
        await page.addStyleTag({ content: KILL_ANIM });
        await noHorizontalOverflow(page);
        if (layout.ui2) {
          await expect(page.locator('#ui2SpkLists .ui2-spk-name').first()).toBeVisible();
          await fitsX(page.locator('#ui2Sidebar, #ui2Main, #ui2Player'));
          await fitsX(page.locator('.ui2-nav-text'));
          await fitsX(page.locator('.ui2-spk-name'));
        }
      });
    });
  }
}

for (const code of RTL) {
  test.describe(`UI 2.0, ${code}, right to left`, () => {
    test.use({ ui2: true, appLocale: code });

    test('mirrors the frame', async ({ app }) => {
      const { page } = app;
      await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
      await page.addStyleTag({ content: KILL_ANIM });
      const side = await page.locator('#ui2Sidebar').boundingBox();
      const main = await page.locator('#ui2Main').boundingBox();
      expect(side, '#ui2Sidebar rendered').not.toBeNull();
      expect(main, '#ui2Main rendered').not.toBeNull();
      expect(side.x, 'sidebar sits right of main').toBeGreaterThan(main.x);
      await expect(page.locator('.ui2-player-now')).toBeVisible();
      const now = await page.locator('.ui2-player-now').boundingBox();
      const transport = await page.locator('.ui2-player-transport').boundingBox();
      expect(transport, '.ui2-player-transport rendered').not.toBeNull();
      expect(now.x, 'now playing sits right of the transport').toBeGreaterThan(transport.x);
    });
  });
}

test.describe('left to right', () => {
  for (const code of LOCALES.filter((c) => !RTL.includes(c))) {
    test.describe(code, () => {
      test.use({ ui2: true, appLocale: code });

      test(`${code} runs left to right`, async ({ app }, testInfo) => {
        test.skip(testInfo.project.name !== 'desktop', 'one viewport is enough');
        await expect(app.page.locator('html')).toHaveAttribute('dir', 'ltr');
        await expect(app.page.locator('html')).toHaveAttribute('lang', code);
      });
    });
  }
});
