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
import { DEMO, assertPortFree, FRONTEND, agentRoutes, buildFrontend, installDesktopMock, shotScenario, startServer, stopServer, waitForServer } from './demo.mjs';

const OUT = path.resolve(FRONTEND, '../../docs/screenshots');         // repo/docs/screenshots
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
const ALL_VIEWS = ['app-library', 'app-listen', 'app-search', 'app-recent', 'app-settings1', 'app-settings2', 'app-settings3', 'app-install', 'app-stick-step1', 'app-stick-step2', 'app-logs', 'app-snooze'];
// The UI 2.0 preview (#1285), switched on through its localStorage key: one
// shot per theme and one at the largest text size. Not part of a full run,
// since the preview is opt-in; SHOOT_VIEWS=ui2 shoots all four.
const UI2_VIEWS = ['app-ui2-dark', 'app-ui2-light', 'app-ui2-contrast', 'app-ui2-xl'];
const VIEWS = (process.env.SHOOT_VIEWS && process.env.SHOOT_VIEWS.trim())
  ? process.env.SHOOT_VIEWS.split(',').map((s) => s.trim()).filter(Boolean).flatMap((v) => (v === 'ui2' ? UI2_VIEWS : [v]))
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
    if (view === 'app-ui2-dark') {
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
  } else if (view === 'app-snooze') {
    // The remote key map with AUX selected and its built-in action set to
    // snooze. The section lives in a collapsed group, so open them all.
    await click('.tab-btn[data-view="settings"]');
    await page.waitForSelector('#view-settings:not(.hidden) #webhookSection', { state: 'attached', timeout: 9000 });
    await page.evaluate(() => {
      document.querySelectorAll('#view-settings .settings-group').forEach((d) => { d.open = true; });
      document.getElementById('webhookSection').open = true;
    });
    await page.waitForTimeout(800);
    await page.evaluate(() => { const t = document.getElementById('webhookTarget'); t.value = 'aux'; t.dispatchEvent(new Event('change')); });
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

// ---- main ------------------------------------------------------------------
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
      if (view.startsWith('app-ui2')) await page.setViewportSize({ width: 1240, height: 740 });
      const scenario = shotScenario(view, lang);
      await page.addInitScript(installDesktopMock, { scenario, demo: DEMO });
      await page.route('**/api/**', agentRoutes(scenario));
      try {
        await page.goto(BASE, { waitUntil: 'domcontentloaded' });
        await drive(page, view);
        const out = path.join(OUT, lang, view + '.png');
        // app-logs keeps only the lower part of the window: the footer is
        // what matters, and a full-height shot would dwarf it.
        const clip = view === 'app-logs' ? { x: 0, y: 780 - 300, width: 1100, height: 300 } : undefined;
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
