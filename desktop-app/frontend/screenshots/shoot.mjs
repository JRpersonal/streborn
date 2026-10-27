// Automated per-language screenshot harness for the STR desktop app.
//
// The desktop UI is a plain Vite frontend that talks to the Go backend via
// window.go.main.App.* and window.runtime. Here we render that frontend in a
// real (headless) Chromium with those bindings MOCKED to return deterministic
// demo data, set the UI language via the localStorage "locale" key, drive each
// screen, and capture it. No speaker, no Wails, no network needed.
//
// Each view's mocked world is a scenario (demo.mjs shotScenario) on the shared
// testkit (../testkit/), the same mock backend the Playwright suite uses.
//
// Output: docs/screenshots/<lang>/<view>.png for every language x view.
//
// Run: npm run shoot   (builds the frontend, then shoots)

import { chromium } from 'playwright';
import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import { DEMO, assertPortFree, FRONTEND, agentRoutes, buildFrontend, installDesktopMock, shotDemo, shotScenario, startServer, stopServer, waitForServer } from './demo.mjs';

// SHOOT_OUT sends a run elsewhere, e.g. review shots that are not published.
const OUT = process.env.SHOOT_OUT ? path.resolve(process.env.SHOOT_OUT) : path.resolve(FRONTEND, '../../docs/screenshots');
// SHOOT_PORT moves the preview server, for a second checkout shooting at the same time.
const PORT = Number(process.env.SHOOT_PORT) || 4178;
const BASE = `http://127.0.0.1:${PORT}/`;

// All shipped UI locales. Override with SHOOT_LANGS=ar,de (comma-separated) to
// re-shoot only some languages without regenerating the whole set.
const ALL_LANGS = ['en', 'de', 'fr', 'es', 'ja', 'uk', 'nl', 'pl', 'lt', 'lv', 'tr', 'ar', 'zh-Hant'];
const LANGS = (process.env.SHOOT_LANGS && process.env.SHOOT_LANGS.trim())
  ? process.env.SHOOT_LANGS.split(',').map((s) => s.trim()).filter(Boolean)
  : ALL_LANGS;
// app-logs is a cropped shot of the settings tail with the "Save diagnostic
// logs" footer link highlighted; the website FAQ uses it to show where the
// log lives. Override with SHOOT_VIEWS=app-logs to re-shoot a subset.
const ALL_VIEWS = ['app-library', 'app-listen', 'app-search', 'app-recent', 'app-settings1', 'app-settings2', 'app-settings3', 'app-install', 'app-stick-step1', 'app-stick-step2', 'app-logs', 'app-snooze', 'app-basskey'];
// The UI 2.0 preview (#1285), switched on through its localStorage key: one
// shot per theme and one at the largest text size. Not part of a full run,
// since the preview is opt-in; SHOOT_VIEWS=ui2 shoots all four.
const UI2_VIEWS = ['app-ui2-dark', 'app-ui2-light', 'app-ui2-contrast', 'app-ui2-xl'];
// The phase 2 views of the preview (#1286), each in the dark and the light
// theme: SHOOT_VIEWS=ui2p2 shoots all ten.
const UI2P2_VIEWS = ['radio', 'radiorows', 'library', 'recent', 'spotify'].flatMap((v) => [`app-ui2p2-${v}-dark`, `app-ui2p2-${v}-light`]);
// Phase 3, the Speakers area (#1287): the list, the page of one speaker on
// each of its five tabs, Groups with the stereo pair and the fixed group, and
// a confirmation before something is taken apart (restart on the speaker
// page, undoing the pair on Groups). Each in the dark and the light theme, at
// the smallest window (min, 1000 x 700) and a wide one (wide, 1600 x 1000):
// SHOOT_VIEWS=ui2p3 shoots all forty. A name without the width
// (app-ui2p3-sound-dark) shoots the app's default window, 1240 x 740.
const UI2P3_SCREENS = ['list', 'overview', 'sound', 'network', 'keys', 'maintenance', 'groups', 'confirm', 'confirmpair'];
const UI2P3_VIEWS = UI2P3_SCREENS.flatMap((v) => ['min', 'wide'].flatMap((w) => [`app-ui2p3-${v}-${w}-dark`, `app-ui2p3-${v}-${w}-light`]));
const UI2P3_SIZES = { min: { width: 1000, height: 700 }, wide: { width: 1600, height: 1000 } };
// The same four views in the classic layout (preview off), on the same
// simulated network and window, to compare a phase 2 view with what it
// replaces: SHOOT_VIEWS=classicp2.
const CLASSICP2_VIEWS = ['radio', 'library', 'recent', 'spotify'].map((v) => `app-classic-${v}`);
const VIEWS = (process.env.SHOOT_VIEWS && process.env.SHOOT_VIEWS.trim())
  ? process.env.SHOOT_VIEWS.split(',').map((s) => s.trim()).filter(Boolean)
    .flatMap((v) => (v === 'ui2' ? UI2_VIEWS : v === 'ui2p2' ? UI2P2_VIEWS : v === 'ui2p3' ? UI2P3_VIEWS : v === 'classicp2' ? CLASSICP2_VIEWS : [v]))
  : ALL_VIEWS;

// ---- per-view driving ------------------------------------------------------
async function drive(page, view) {
  const click = async (sel) => { const el = await page.$(sel); if (el) await el.click(); };
  const scrollY = (y) => page.evaluate((yy) => window.scrollTo(0, yy), y);
  const scrollIntoView = (sel) => page.evaluate((s) => { const e = document.querySelector(s); if (e) e.scrollIntoView({ block: 'start' }); }, sel);
  if (view === 'app-listen') {
    await page.waitForSelector('#presets .preset-body, #presets .preset-hint', { timeout: 9000 });
    await scrollY(0);
  } else if (view.startsWith('app-ui2')) {
    // The preview shell on the simulated network of demo.mjs: the sidebar
    // lists every speaker, the group of two pinned on top. Wait for the "+"
    // marks, which need the first every-speaker poll, then one shot each:
    // the list scrolled (dark), Levels open over Library (light), More over
    // Recently played (contrast), the largest text size (xl).
    await page.waitForSelector('html.ui2 .ui2-shell', { timeout: 9000 });
    await page.waitForSelector('#presets .preset-body, #presets .preset-hint', { timeout: 9000 });
    await page.waitForSelector('#ui2SpkLists .ui2-spk-pinned .ui2-spk-pick', { timeout: 9000 });
    await page.waitForSelector('#ui2SpkLists .ui2-spk-act[data-act="add"]', { timeout: 20000 });
    if (view.startsWith('app-ui2p2-')) {
      await driveUi2Phase2(page, view.split('-')[2]);
    } else if (view.startsWith('app-ui2p3-')) {
      await driveUi2Phase3(page, view.split('-')[2]);
    } else if (view === 'app-ui2-dark') {
      // Scrolled to the offline speaker, so the shot shows it greyed with
      // "Offline" and the Bose original's "Ready for STR" below it.
      await page.evaluate(() => {
        const s = document.querySelector('.ui2-spk-scroll');
        const off = s && s.querySelector('.ui2-spk-row.is-offline');
        if (off) s.scrollTop += off.getBoundingClientRect().top - s.getBoundingClientRect().top;
      });
    } else if (view === 'app-ui2-light') {
      await click('.ui2-nav-item[data-view="library"]');
      await page.waitForSelector('#view-library:not(.hidden) .library-row-folder', { timeout: 9000 });
      await page.waitForSelector('#ui2LevelsBtn:not([hidden])', { state: 'visible', timeout: 9000 });
      await click('#ui2LevelsBtn');
      await page.waitForSelector('#ui2LevelsPop:not([hidden])', { timeout: 9000 });
    } else if (view === 'app-ui2-contrast') {
      await click('.ui2-nav-item[data-view="recent"]');
      await page.waitForSelector('#view-recent:not(.hidden) .recent-card', { timeout: 9000 });
      await click('#ui2MoreBtn');
      await page.waitForSelector('#ui2MorePop:not([hidden])', { timeout: 9000 });
    }
  } else if (view.startsWith('app-classic-')) {
    await driveClassicPhase2(page, view.slice('app-classic-'.length));
  } else if (view === 'app-search') {
    await page.waitForSelector('#topBtn', { timeout: 9000 });
    await click('#topBtn');
    await page.waitForSelector('#searchResults .result-row', { timeout: 9000 });
    await scrollIntoView('#searchQ');
  } else if (view === 'app-recent') {
    await click('.tab-btn[data-view="recent"]');
    await page.waitForSelector('#view-recent:not(.hidden) .recent-card', { timeout: 9000 });
    await scrollY(0);
  } else if (view === 'app-library') {
    await click('.tab-btn[data-view="library"]');
    await page.waitForSelector('#view-library:not(.hidden) .library-row-folder', { timeout: 9000 });
    await scrollY(0);
  } else if (view.startsWith('app-settings')) {
    await click('.tab-btn[data-view="settings"]');
    await page.waitForSelector('#view-settings:not(.hidden) .settings-section', { timeout: 9000 });
    // The settings list is now folded into collapsible groups (Basics open,
    // the rest collapsed), so the three shots show: 1) the tidy overview with
    // the phone-control card, 2) a couple of groups expanded, 3) everything
    // expanded scrolled to the advanced/actions tail.
    await page.waitForSelector('#view-settings .settings-group', { timeout: 9000 });
    if (view === 'app-settings1') {
      await scrollY(0);
    } else if (view === 'app-settings2') {
      await page.evaluate(() => {
        const g = document.querySelectorAll('#view-settings .settings-group');
        [g[1], g[2]].forEach((d) => { if (d) d.open = true; }); // Sound & display + Network
        if (g[1]) g[1].scrollIntoView({ block: 'start' });       // scroll the expanded groups into view
      });
      await page.waitForTimeout(250);
    } else {
      await page.evaluate(() => {
        document.querySelectorAll('#view-settings .settings-group').forEach((d) => { d.open = true; });
      });
      await page.waitForTimeout(250);
      await scrollY(100000);
    }
  } else if (view === 'app-snooze' || view === 'app-basskey') {
    // The remote key map with AUX selected and its built-in action set to
    // snooze (app-snooze), or thumbs up selected with "Bass up" on it
    // (app-basskey). The section lives in a collapsed group, so open them all.
    const keyId = view === 'app-snooze' ? 'aux' : 'thumbsUp';
    await click('.tab-btn[data-view="settings"]');
    await page.waitForSelector('#view-settings:not(.hidden) #webhookSection', { state: 'attached', timeout: 9000 });
    await page.evaluate(() => {
      document.querySelectorAll('#view-settings .settings-group').forEach((d) => { d.open = true; });
      document.getElementById('webhookSection').open = true;
    });
    await page.waitForTimeout(800);
    await page.evaluate((k) => { const t = document.getElementById('webhookTarget'); t.value = k; t.dispatchEvent(new Event('change')); }, keyId);
    await page.waitForTimeout(400);
    await scrollIntoView('#webhookSection');
    await page.waitForTimeout(300);
  } else if (view === 'app-logs') {
    // Settings tail with the footer link that saves the diagnostic logs,
    // ringed so the eye lands on it. Groups stay collapsed: the point is
    // the footer, not the settings.
    await click('.tab-btn[data-view="settings"]');
    await page.waitForSelector('#view-settings:not(.hidden) .settings-section', { timeout: 9000 });
    await page.waitForSelector('#footerSaveLogs', { timeout: 9000 });
    await scrollY(100000);
    await page.evaluate(() => {
      const a = document.getElementById('footerSaveLogs');
      a.style.outline = '3px solid #f59e0b';
      a.style.outlineOffset = '6px';
      a.style.borderRadius = '4px';
      a.style.fontWeight = '700';
    });
  } else if (view.startsWith('app-install') || view.startsWith('app-stick')) {
    // "Set up speaker": the network install is the default path (a card for
    // the stock speaker that was found), the USB stick is the collapsed
    // fallback below it.
    await click('.tab-btn[data-view="setup"]');
    await page.waitForSelector('#view-setup:not(.hidden) .setup-target-card[data-kind="stock"]', { timeout: 9000 });
    await click('.setup-target-card[data-kind="stock"]');
    await page.waitForSelector('#setupPrimaryAction .setup-hero[data-kind="stock"]', { timeout: 9000 });
    if (view === 'app-install') {
      await scrollY(0);
    } else {
      await page.evaluate(() => { const d = document.getElementById('setupStickDetails'); if (d) d.open = true; });
      await page.waitForSelector('#drivesList .drive-row', { state: 'visible', timeout: 9000 });
      if (view === 'app-stick-step1') {
        await scrollIntoView('#setupStickDetails');
      } else {
        await click('#drivesList .drive-row');
        await page.waitForSelector('#nameSection:not(.hidden)', { timeout: 9000 });
        await scrollIntoView('#nameSection');
      }
    }
  }
  await page.waitForTimeout(450); // let transitions / art settle
}

// driveUi2Phase2 opens one phase 2 view of the preview in its telling state:
// Radio on Discover with the top list, Library one folder down, Recently
// played under its day headings, Spotify with a playlist playing on the
// speaker you control.
async function driveUi2Phase2(page, which) {
  const open = (v) => page.click(`#ui2Sidebar .ui2-nav-item[data-view="${v}"]`);
  if (which === 'radio' || which === 'radiorows') {
    await open('box');
    await page.waitForSelector('#ui2KeysTitle', { timeout: 9000 });
    await page.waitForSelector('#searchResults .ui2-st', { timeout: 15000 });
    if (which === 'radio') return page.evaluate(() => { document.getElementById('ui2Main').scrollTop = 0; });
    // The station rows, the first one with its details open.
    await page.click('#searchResults .ui2-st [data-act="details"]');
    await page.waitForTimeout(300);
    return page.evaluate(() => document.getElementById('searchResults').scrollIntoView({ block: 'start' }));
  } else if (which === 'library') {
    await open('library');
    // The context keeps localStorage between shots, so the second theme's
    // Library comes back where the first one left it (the last place opens).
    await page.waitForSelector('#view-library .lib2-folder, #view-library .lib2-track', { timeout: 9000 });
    if (await page.$('#view-library .lib2-track')) return;
    await page.locator('#view-library .lib2-folder', { hasText: 'Music' }).first().click();
    await page.waitForSelector('#view-library .lib2-folder >> text=Evening Sessions', { timeout: 9000 });
    await page.locator('#view-library .lib2-folder', { hasText: 'Evening Sessions' }).first().click();
    await page.waitForSelector('#view-library .lib2-track', { timeout: 9000 });
  } else if (which === 'recent') {
    await open('recent');
    await page.waitForSelector('#recentList .rc-day-head', { timeout: 9000 });
  } else if (which === 'spotify') {
    const host = DEMO.ui2Net.find((b) => b.friendlyName === 'Living Room').host;
    const uri = 'spotify:playlist:37i9dQZF1DXcBWIGoYBM5M';
    await page.evaluate(([h, it]) => {
      window.__strMock.state.playing[h] = 'PLAY_STATE';
      window.__strMock.state.item[h] = it;
    }, [host, { source: 'SPOTIFY', location: `/playback/container/${Buffer.from(uri).toString('base64')}`, name: 'Discover Weekly', art: '' }]);
    await open('spotify');
    await page.waitForSelector('#sp2Keys .sp2-key', { timeout: 20000 });
  }
  await page.evaluate(() => { const m = document.getElementById('ui2Main'); if (m) m.scrollTop = 0; });
}

// driveUi2Phase3 opens one screen of the Speakers area: the list, the page
// of the speaker you control (its row in the list) on one of its five tabs,
// Groups, or a confirmation (restart on Maintenance, undoing the stereo pair
// on Groups), left open for the shot.
async function driveUi2Phase3(page, which) {
  await page.click('#ui2Sidebar .ui2-nav-item[data-view="speakers"]');
  await page.waitForSelector('#view-speakers .spl-open', { timeout: 9000 });
  const top = () => page.evaluate(() => { const m = document.getElementById('ui2Main'); if (m) m.scrollTop = 0; });
  if (which === 'list') {
    await page.waitForTimeout(600);
    return top();
  }
  if (which === 'groups' || which === 'confirmpair') {
    await page.click('#ui2SpkSubnav .spa-sub[data-route="groups"]');
    await page.waitForSelector('#view-multiroom > .ui2-grp .ui2-grp-card[data-kind="pair"]', { timeout: 15000 });
    await page.waitForTimeout(600);
    await top();
    if (which === 'confirmpair') {
      await page.locator('#view-multiroom .ui2-grp-card[data-kind="pair"]').getByRole('button', { name: /./ }).last().click();
      await page.waitForSelector('#ui2GrpConfirm', { state: 'visible', timeout: 9000 });
      await page.waitForTimeout(300);
    }
    return;
  }
  await page.click('#view-speakers .spl-open[aria-current="true"]');
  await page.waitForSelector('#ui2SpkPanels .sps-sec .settings-section', { timeout: 9000 });
  const tab = which === 'confirm' ? 'maintenance' : which;
  await page.click(`#ui2SpkTab-${tab}`);
  await page.waitForTimeout(600);
  await top();
  if (which === 'confirm') {
    await page.click('#boxRebootBtn');
    await page.waitForSelector('#warnModal', { state: 'visible', timeout: 9000 });
    await page.waitForTimeout(300);
  }
}

// ---- main ------------------------------------------------------------------
// driveClassicPhase2 opens one of the four phase 2 views in the classic
// layout, with the same content the preview shots show.
async function driveClassicPhase2(page, which) {
  const tab = (v) => page.click(`.tab-btn[data-view="${v}"]`);
  await page.waitForSelector('#presets .preset-body, #presets .preset-hint', { timeout: 9000 });
  if (which === 'radio') {
    await page.waitForSelector('#topBtn', { timeout: 9000 });
    await page.click('#topBtn');
    await page.waitForSelector('#searchResults .result-row', { timeout: 9000 });
  } else if (which === 'library') {
    await tab('library');
    await page.waitForSelector('#view-library:not(.hidden) .library-row-folder', { timeout: 9000 });
  } else if (which === 'recent') {
    await tab('recent');
    await page.waitForSelector('#view-recent:not(.hidden) .recent-card', { timeout: 9000 });
  } else if (which === 'spotify') {
    await tab('spotify');
    await page.waitForSelector('#view-spotify:not(.hidden) .spotify-page', { timeout: 9000 });
  }
  await page.evaluate(() => window.scrollTo(0, 0));
}

await assertPortFree(BASE);
await buildFrontend(PORT);
const server = startServer(PORT);
let browser;
try {
  await waitForServer(BASE, server);
  browser = await chromium.launch({ headless: true });
  let ok = 0, fail = 0;
  for (const lang of LANGS) {
    await mkdir(path.join(OUT, lang), { recursive: true });
    const ctx = await browser.newContext({ viewport: { width: 1100, height: 780 }, deviceScaleFactor: 2, timezoneId: 'Europe/Berlin' });
    for (const view of VIEWS) {
      const page = await ctx.newPage();
      page.on('pageerror', (e) => console.warn(`  [${lang}/${view}] pageerror:`, e.message));
      // SHOOT_DEBUG=1 forwards the page's console, for a shot that will not come up.
      if (process.env.SHOOT_DEBUG) page.on('console', (m) => console.log(`  [${lang}/${view}] console.${m.type()}:`, m.text()));
      // The UI 2.0 shots use the app's own default window (main.go, 1240 x 740),
      // the size the sidebar has to fit.
      const p3size = view.startsWith('app-ui2p3-') ? UI2P3_SIZES[view.split('-')[3]] : null;
      if (p3size) await page.setViewportSize(p3size);
      else if (view.startsWith('app-ui2') || view.startsWith('app-classic-')) await page.setViewportSize({ width: 1240, height: 740 });
      const scenario = shotScenario(view, lang);
      await page.addInitScript(installDesktopMock, { scenario, demo: shotDemo(view) });
      await page.route('**/api/**', agentRoutes(scenario));
      try {
        await page.goto(BASE, { waitUntil: 'domcontentloaded' });
        await drive(page, view);
        const out = path.join(OUT, lang, view + '.png');
        // app-logs keeps only the lower part of the window: the footer is
        // what matters, and a full-height shot would dwarf it.
        const clip = view === 'app-logs' ? { x: 0, y: 780 - 300, width: 1100, height: 300 } : undefined;
        // SHOOT_TALL=1 (review shots) grows the window until the main pane
        // no longer scrolls, so one picture shows a whole long page.
        if (process.env.SHOOT_TALL && view.startsWith('app-ui2') && !/-confirm/.test(view)) {
          const extra = await page.evaluate(() => { const m = document.getElementById('ui2Main'); return m ? m.scrollHeight - m.clientHeight : 0; });
          if (extra > 0) {
            const vp = page.viewportSize();
            await page.setViewportSize({ width: vp.width, height: Math.min(vp.height + extra, 6000) });
            await page.waitForTimeout(400);
          }
        }
        await page.screenshot({ path: out, clip });
        console.log(`ok  ${lang}/${view}`);
        ok++;
      } catch (e) {
        console.error(`FAIL ${lang}/${view}: ${e.message}`);
        fail++;
      }
      await page.close();
    }
    await ctx.close();
  }
  console.log(`\nDone: ${ok} ok, ${fail} failed -> ${OUT}`);
  if (fail) process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  stopServer(server);
}
