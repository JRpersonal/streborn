// desktop-banners: the window-wide notices on top of the app (the app update,
// the Windows Firewall block, the speaker update card, the no-internet line
// and the SSH reminder), in the classic layout and in the UI 2.0 preview, at
// the default window and at the smallest one (projects desktop, desktop-min).
//
// What makes each notice appear lives in testkit/scenario.mjs bannerOverrides.
// Two rules from the app shape the cases:
//   - The firewall banner only shows when the last discovery sweep found NO
//     speaker (firewallbanner.js, #1215), so its patch empties DiscoverBoxes.
//     It can never stand beside a notice that needs a speaker, and is checked
//     alone (and beside the app update, which needs none).
//   - An app update mutes the speaker update card ("app first",
//     speakerUpdateCardMuted), so "all at once" comes in two variants.
//
// The UI 2.0 banner region is capped (shell2.css #ui2Banners, at most 40vh and
// scrolling on its own), so a pile of notices still leaves the view usable.
//
//   E2E_PORT=4187 npx playwright test specs/desktop-banners.spec.mjs

import { test, expect } from '../fixtures.mjs';
import { settle, runFor } from '../helpers/clock.mjs';
import { KILL_ANIM } from '../helpers/a11y.mjs';
import { disjoint } from '../helpers/layout.mjs';

// Each notice: the scenario.banners key, the element, and how to find it SHOWN.
// The app update banner never goes away: with nothing to announce (or once
// dismissed) it turns into the quiet "check for updates" link, so "shown"
// means the full notice.
const BANNERS = {
  appUpdate: { id: 'appUpdateBanner', shown: '#appUpdateBanner:not(.hidden):not(.app-update-quiet)', close: '#appUpdateDismiss' },
  firewall: { id: 'firewallBanner', shown: '#firewallBanner:not(.hidden)', close: '#firewallDismiss' },
  speakerUpdate: { id: 'speakerUpdateCard', shown: '#speakerUpdateCard:not(.hidden)' },
  noInternet: { id: 'boxIssueBanner', shown: '#boxIssueBanner:not(.hidden)' },
  security: { id: 'globalSecurityBanner', shown: '#globalSecurityBanner:not(.hidden)', close: '#globalSecurityDismissBtn' },
};
const KINDS = Object.keys(BANNERS);
// The DOM order the notices keep (index.html, main.js, shell2/mount.js).
const ORDER = ['appUpdate', 'firewall', 'noInternet', 'speakerUpdate', 'security'];

// Phase 5: in the UI 2.0 preview the no-internet notice is about one speaker,
// so it moves onto that speaker (sidebar chip, list card, Overview; covered
// by desktop-ui2-banners.spec.mjs) and is no longer window-wide there.
const PER_SPEAKER_IN_UI2 = new Set(['noInternet']);
const windowWide = (layout, kinds) => (layout.ui2 ? kinds.filter((k) => !PER_SPEAKER_IN_UI2.has(k)) : kinds);

const LAYOUTS = [
  { name: 'classic layout', ui2: false, main: '#view-box' },
  { name: 'UI 2.0 preview', ui2: true, main: '#ui2Main' },
];

test.use({ fakeClock: true });

// With the firewall patch discovery finds nobody, so the classic Music tab has
// no presets to wait for (the fixture's default ready marker).
const noSpeakerReady = (layout) => (layout.ui2 ? null : '#view-box');

async function boot(page) {
  await settle(page, 9_000); // firewall check at +3 s, app update at +8 s
  await page.addStyleTag({ content: KILL_ANIM });
}

async function expectOnly(page, kinds) {
  for (const k of KINDS) {
    const loc = page.locator(BANNERS[k].shown);
    if (kinds.includes(k)) await expect(loc, `${k} shown`).toBeVisible();
    // The preview keeps a per-speaker notice in the DOM for the classic
    // layout but never shows it window-wide.
    else if (PER_SPEAKER_IN_UI2.has(k) && await page.locator('html.ui2').count()) await expect(loc, `${k} not shown`).toBeHidden();
    else await expect(loc, `${k} not shown`).toHaveCount(0);
  }
}

// Every shown notice sits where the layout puts window-wide notices, in the
// order above.
async function expectPlacement(page, ui2, kinds) {
  const ids = ORDER.filter((k) => kinds.includes(k)).map((k) => BANNERS[k].id);
  const where = await page.evaluate(([list, isUi2]) => {
    const els = list.map((id) => document.getElementById(id));
    const layout = document.querySelector('.layout');
    const region = document.getElementById('ui2Banners');
    const view = document.getElementById('view-box');
    const place = els.map((el) => {
      if (isUi2) return region && region.contains(el) ? 'region' : `outside:${el.parentElement && el.parentElement.id}`;
      if (el.id === 'globalSecurityBanner') {
        // Classic: under the tabs, inside the layout, above the views.
        return layout.contains(el) && (el.compareDocumentPosition(view) & Node.DOCUMENT_POSITION_FOLLOWING) ? 'region' : 'misplaced';
      }
      return el.parentElement === document.body && (el.compareDocumentPosition(layout) & Node.DOCUMENT_POSITION_FOLLOWING) ? 'region' : 'misplaced';
    });
    const ordered = els.every((el, i) => i === 0 || (els[i - 1].compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING));
    return { place, ordered };
  }, [ids, ui2]);
  expect(where.place, `placement of ${ids.join(', ')}`).toEqual(ids.map(() => 'region'));
  expect(where.ordered, `DOM order of ${ids.join(', ')}`).toBe(true);
}

async function boxesOf(page, kinds) {
  const out = [];
  for (const k of ORDER.filter((x) => kinds.includes(x))) out.push(await page.locator(BANNERS[k].shown).boundingBox());
  return out;
}

async function expectUsableMain(page, layout, kinds) {
  const main = page.locator(layout.main);
  await expect(main).toBeVisible();
  const mb = await main.boundingBox();
  const banners = await boxesOf(page, kinds);
  expect(disjoint(banners), 'banners overlap each other').toEqual([]);
  if (layout.ui2) {
    expect(mb.height, '#ui2Main height').toBeGreaterThanOrEqual(200);
    const region = await page.locator('#ui2Banners').boundingBox();
    expect(disjoint([region, mb]), 'banner region overlaps the main pane').toEqual([]);
    // Fix 5.2: the region is capped and scrolls on its own.
    const css = await page.locator('#ui2Banners').evaluate((el) => {
      const s = getComputedStyle(el);
      return { maxHeight: parseFloat(s.maxHeight), overflowY: s.overflowY, vh: window.innerHeight };
    });
    expect(css.maxHeight, '#ui2Banners max-height').toBeLessThanOrEqual(css.vh * 0.4 + 0.5);
    expect(['auto', 'scroll'], '#ui2Banners overflow-y').toContain(css.overflowY);
    expect(region.height).toBeLessThanOrEqual(css.vh * 0.4 + 0.5);
  } else {
    // The classic page scrolls as a whole: the view starts below the last notice.
    const lastBottom = Math.max(...banners.map((b) => b.y + b.height));
    expect(mb.y, 'view starts below the notices').toBeGreaterThanOrEqual(lastBottom - 0.5);
  }
}

const mainTop = async (page, sel) => (await page.locator(sel).boundingBox()).y;

for (const layout of LAYOUTS) {
  test.describe(layout.name, () => {
    test.use({ ui2: layout.ui2 });

    test.describe('all at once', () => {
      test.describe('app update, no internet, SSH reminder', () => {
        const kinds = ['appUpdate', 'noInternet', 'security'];
        test.use({ scenario: { banners: { appUpdate: true, noInternet: true, security: true, speakerUpdate: true } } });

        test('stack in order, apart, and leave the view usable (the speaker card waits for the app)', async ({ app }) => {
          const { page } = app;
          await boot(page);
          const shown = windowWide(layout, kinds);
          await expectOnly(page, shown);
          await expectPlacement(page, layout.ui2, shown);
          await expectUsableMain(page, layout, shown);
        });
      });

      test.describe('speaker update, no internet, SSH reminder', () => {
        const kinds = ['speakerUpdate', 'noInternet', 'security'];
        test.use({ scenario: { banners: { noInternet: true, security: true, speakerUpdate: true } } });

        test('stack in order, apart, and leave the view usable', async ({ app }) => {
          const { page } = app;
          await boot(page);
          const shown = windowWide(layout, kinds);
          await expectOnly(page, shown);
          await expectPlacement(page, layout.ui2, shown);
          await expectUsableMain(page, layout, shown);
        });
      });

      test.describe('firewall, with the app update', () => {
        const kinds = ['appUpdate', 'firewall'];
        test.use({ scenario: { banners: { appUpdate: true, firewall: true } }, ready: noSpeakerReady(layout) });

        test('stack in order, apart, and leave the view usable', async ({ app }) => {
          const { page } = app;
          await boot(page);
          const shown = windowWide(layout, kinds);
          await expectOnly(page, shown);
          await expectPlacement(page, layout.ui2, shown);
          await expectUsableMain(page, layout, shown);
        });
      });
    });

    test.describe('each alone', () => {
      for (const kind of KINDS) {
        test.describe(kind, () => {
          test.use({ scenario: { banners: { [kind]: true } }, ready: kind === 'firewall' ? noSpeakerReady(layout) : null });

          test(`only the ${kind} notice shows`, async ({ app }) => {
            const { page } = app;
            await boot(page);
            const shown = windowWide(layout, [kind]);
            await expectOnly(page, shown);
            if (shown.length) {
              await expectPlacement(page, layout.ui2, shown);
              await expectUsableMain(page, layout, shown);
            } else {
              // Preview: no window-wide notice; the speaker's row carries it.
              await expect(page.locator('#ui2Main')).toBeVisible();
              await expect(page.locator('#ui2SpkLists [data-chip="attention"]').first()).toBeVisible();
            }
            // The UI 2.0 card styling (shell2/banners.css) never reaches the
            // classic layout: its card keeps the light blue of style.css.
            if (!layout.ui2 && kind === 'speakerUpdate') {
              expect(await page.locator('#speakerUpdateCard').evaluate((el) => getComputedStyle(el).backgroundColor)).toBe('rgb(234, 242, 254)');
            }
          });
        });
      }
    });

    test.describe('dismissing', () => {
      // Each closable notice, beside the ones that stay.
      const CASES = [
        { kind: 'appUpdate', banners: { appUpdate: true, noInternet: true, security: true }, stay: ['noInternet', 'security'] },
        { kind: 'security', banners: { appUpdate: true, noInternet: true, security: true }, stay: ['appUpdate', 'noInternet'] },
        { kind: 'firewall', banners: { appUpdate: true, firewall: true }, stay: ['appUpdate'] },
      ];
      for (const c of CASES) {
        test.describe(c.kind, () => {
          test.use({ scenario: { banners: c.banners }, ready: c.banners.firewall ? noSpeakerReady(layout) : null });

          test(`closing ${c.kind} moves the view up and keeps the others`, async ({ app }) => {
            const { page } = app;
            await boot(page);
            const stay = windowWide(layout, c.stay);
            await expectOnly(page, [c.kind, ...stay]);
            const before = await mainTop(page, layout.main);
            await page.locator(BANNERS[c.kind].close).click();
            await expectOnly(page, stay);
            await expect.poll(() => mainTop(page, layout.main), { message: 'view top after dismissing' }).toBeLessThan(before - 1);
            // Stays closed through the polls that follow (status, discovery,
            // the SSH check on every refresh).
            await runFor(page, 65_000);
            await expectOnly(page, stay);
            await expectPlacement(page, layout.ui2, stay);
            await expectUsableMain(page, layout, stay);
          });
        });
      }
    });
  });
}
