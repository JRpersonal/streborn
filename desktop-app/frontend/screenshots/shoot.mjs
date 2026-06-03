// Automated per-language screenshot harness for the STR desktop app.
//
// The desktop UI is a plain Vite frontend that talks to the Go backend via
// window.go.main.App.* and window.runtime. Here we render that frontend in a
// real (headless) Chromium with those bindings MOCKED to return deterministic
// demo data, set the UI language via the localStorage "locale" key, drive each
// screen, and capture it. No speaker, no Wails, no network needed.
//
// Output: docs/screenshots/<lang>/<view>.png for every language x view.
//
// Run: npm run screenshots   (builds the frontend, then shoots)

import { chromium } from 'playwright';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const FRONTEND = path.resolve(__dirname, '..');                       // desktop-app/frontend
const OUT = path.resolve(FRONTEND, '../../docs/screenshots');         // repo/docs/screenshots
const PORT = 4178;
const BASE = `http://127.0.0.1:${PORT}/`;

const LANGS = ['en', 'de', 'fr', 'es', 'ja', 'uk', 'nl', 'pl'];
const VIEWS = ['app-library', 'app-listen', 'app-search', 'app-settings1', 'app-settings2', 'app-settings3', 'app-stick-step1', 'app-stick-step2'];

// ---- demo data -------------------------------------------------------------
// A tiny inline SVG cover so screenshots have artwork without any network.
const cover = (letter, bg) =>
  'data:image/svg+xml;utf8,' +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="160" height="160"><rect width="160" height="160" rx="14" fill="${bg}"/><text x="80" y="104" font-family="Segoe UI,Arial,sans-serif" font-size="78" font-weight="700" fill="#ffffff" text-anchor="middle">${letter}</text></svg>`,
  );

const BOX = {
  name: 'Living Room',
  host: 'demobox.local',
  port: 8888,
  deviceID: 'A1B2C3D4E5F6',
  friendlyName: 'Living Room',
  model: 'SoundTouch 20',
  version: 'v0.6.18',
  build: '2026-06-03',
  serialNumber: '',
  kind: 'STR',
  portVerified: true,
};

const PRESETS = [
  { slot: 1, name: 'BBC Radio 1', stream_url: 'http://demobox.local:8888/stream/1', type: 'radio', art: cover('B', '#c4302b'), bitrate: 128 },
  { slot: 2, name: 'Best Of Rock.FM', stream_url: 'http://demobox.local:8888/stream/2', type: 'radio', art: cover('R', '#1b2636'), bitrate: 256 },
  { slot: 3, name: 'NPR News', stream_url: 'http://demobox.local:8888/stream/3', type: 'radio', art: cover('N', '#2a6f97'), bitrate: 128 },
  { slot: 4, name: 'Jazz24', stream_url: 'http://demobox.local:8888/stream/4', type: 'radio', art: cover('J', '#7b2cbf'), bitrate: 320 },
  { slot: 5, name: 'Radio Paradise', stream_url: 'http://demobox.local:8888/stream/5', type: 'radio', art: cover('P', '#e07a00'), bitrate: 320 },
  { slot: 6, name: 'FIP', stream_url: 'http://demobox.local:8888/stream/6', type: 'radio', art: cover('F', '#d62246'), bitrate: 192 },
];

const SETTINGS = {
  info: { deviceID: 'A1B2C3D4E5F6', name: 'Living Room', type: 'SoundTouch 20' },
  volume: { actual: 32, muted: false },
  bass: { actual: 0, default: 0, min: -9, max: 9, available: true },
  network: { interfaces: [{ type: 'WIFI', state: 'NETWORK_WIFI_CONNECTED', ssid: 'HomeWiFi', ipAddress: '192.0.2.50', signal: 'GOOD', frequencyKHz: 2412000 }] },
  sources: [{ source: 'AUX', status: 'READY' }, { source: 'BLUETOOTH', status: 'READY' }],
};

const STATUS_PLAYING =
  '<?xml version="1.0" encoding="UTF-8"?>' +
  '<nowPlaying deviceID="A1B2C3D4E5F6" source="INTERNET_RADIO">' +
  '<ContentItem source="INTERNET_RADIO" location="http://demobox.local:8888/stream/2" isPresetable="true">' +
  '<itemName>Best Of Rock.FM</itemName></ContentItem>' +
  '<art artImageStatus="IMAGE_PRESENT">' + cover('R', '#1b2636') + '</art>' +
  '<playStatus>PLAY_STATE</playStatus></nowPlaying>';

const STATUS_IDLE =
  '<?xml version="1.0" encoding="UTF-8"?>' +
  '<nowPlaying deviceID="A1B2C3D4E5F6" source="STANDBY">' +
  '<ContentItem source="STANDBY"></ContentItem><playStatus></playStatus></nowPlaying>';

const DRIVES = [
  { path: 'E:\\', label: 'USB DISK', totalBytes: 31043616768, freeBytes: 30500000000, filesystem: 'FAT32', removable: true, hasStick: false, description: 'Verbatim STORE N GO (29 GB)' },
];

const WIFI = [
  { ssid: 'HomeWiFi', hasPass: true, source: 'system' },
  { ssid: 'Guest-Net', hasPass: false, source: 'system' },
];

const APPINFO = {
  version: 'v0.6.18',
  build: '2026-06-03',
  author: 'Jens',
  githubUrl: 'https://github.com/JRpersonal/streborn',
  websiteUrl: 'https://st-reborn.de',
  donateUrl: 'https://github.com/sponsors/JRpersonal',
  donateSlogan: 'Support STR',
  updateManifestUrl: '',
};

const STATIONS = [
  { name: 'Best Of Rock.FM', url: 'http://example.com/rock', url_resolved: 'http://example.com/rock', stationuuid: 's1', countrycode: 'DE', bitrate: 256, votes: 4210, clicktrend: 12, clickcount: 90000, codec: 'MP3', tags: 'rock,alternative', favicon: cover('R', '#1b2636'), lastcheckok: 1 },
  { name: 'BBC Radio 1', url: 'http://example.com/bbc1', url_resolved: 'http://example.com/bbc1', stationuuid: 's2', countrycode: 'GB', bitrate: 128, votes: 3890, clicktrend: 8, clickcount: 120000, codec: 'MP3', tags: 'pop,charts', favicon: cover('B', '#c4302b'), lastcheckok: 1 },
  { name: 'Radio Paradise', url: 'http://example.com/rp', url_resolved: 'http://example.com/rp', stationuuid: 's3', countrycode: 'US', bitrate: 320, votes: 3550, clicktrend: 20, clickcount: 80000, codec: 'MP3', tags: 'eclectic,rock', favicon: cover('P', '#e07a00'), lastcheckok: 1 },
  { name: 'Jazz24', url: 'http://example.com/jazz24', url_resolved: 'http://example.com/jazz24', stationuuid: 's4', countrycode: 'US', bitrate: 320, votes: 2980, clicktrend: 5, clickcount: 60000, codec: 'AAC', tags: 'jazz', favicon: cover('J', '#7b2cbf'), lastcheckok: 1 },
  { name: 'FIP', url: 'http://example.com/fip', url_resolved: 'http://example.com/fip', stationuuid: 's5', countrycode: 'FR', bitrate: 192, votes: 2710, clicktrend: 9, clickcount: 55000, codec: 'MP3', tags: 'eclectic', favicon: cover('F', '#d62246'), lastcheckok: 1 },
  { name: 'NPR News', url: 'http://example.com/npr', url_resolved: 'http://example.com/npr', stationuuid: 's6', countrycode: 'US', bitrate: 128, votes: 2540, clicktrend: 3, clickcount: 70000, codec: 'MP3', tags: 'news,talk', favicon: cover('N', '#2a6f97'), lastcheckok: 1 },
  { name: 'KEXP', url: 'http://example.com/kexp', url_resolved: 'http://example.com/kexp', stationuuid: 's7', countrycode: 'US', bitrate: 256, votes: 2300, clicktrend: 7, clickcount: 50000, codec: 'MP3', tags: 'indie,alternative', favicon: cover('K', '#0a9396'), lastcheckok: 1 },
  { name: 'Classic FM', url: 'http://example.com/classic', url_resolved: 'http://example.com/classic', stationuuid: 's8', countrycode: 'GB', bitrate: 128, votes: 2100, clicktrend: 4, clickcount: 48000, codec: 'MP3', tags: 'classical', favicon: cover('C', '#5f0f40'), lastcheckok: 1 },
];

const LANGUAGES = [
  { name: 'english', stationcount: 12000 },
  { name: 'german', stationcount: 4200 },
  { name: 'french', stationcount: 2600 },
  { name: 'spanish', stationcount: 3100 },
  { name: 'japanese', stationcount: 900 },
  { name: 'ukrainian', stationcount: 300 },
  { name: 'dutch', stationcount: 700 },
];

const MEDIA_SERVERS = [
  { udn: 'uuid:demo-mediaserver', friendlyName: 'Living Room NAS', manufacturer: 'Demo', modelName: 'Media Server', iconURL: '', address: '192.0.2.60' },
];
const LIBRARY_PAGE = {
  containers: [
    { id: 'c1', title: 'Music', childCount: 6 },
    { id: 'c2', title: 'Pictures', childCount: 2 },
    { id: 'c3', title: 'Videos', childCount: 2 },
    { id: 'c4', title: 'Internet Radio', childCount: 0 },
    { id: 'c5', title: 'Podcasts', childCount: 0 },
    { id: 'c6', title: 'File Index', childCount: 1 },
  ],
  items: [],
  totalMatches: 6,
  returned: 6,
};

const DEMO = { box: BOX, presets: PRESETS, settings: SETTINGS, statusPlaying: STATUS_PLAYING, statusIdle: STATUS_IDLE, drives: DRIVES, wifi: WIFI, appInfo: APPINFO, stations: STATIONS, languages: LANGUAGES, mediaServers: MEDIA_SERVERS, libraryPage: LIBRARY_PAGE };

// ---- the in-page mock (runs before the app's own scripts) ------------------
function installMocks({ locale, view, demo }) {
  try {
    localStorage.setItem('locale', locale);
    localStorage.setItem('cachedBoxes', JSON.stringify([demo.box]));
    localStorage.setItem('lastBoxDeviceID', demo.box.deviceID);
    localStorage.setItem('searchCountry', '');
  } catch (e) {}

  const P = (v) => Promise.resolve(v);
  // app-listen shows the lively now-playing state (matches the README hero);
  // every other view boots idle/standby.
  const status = view === 'app-listen' ? demo.statusPlaying : demo.statusIdle;

  const App = {
    AppInfo: () => P(demo.appInfo),
    AppVersion: () => P(demo.appInfo.version),
    CheckAppUpdate: () => P({}),
    DiscoverBoxes: () => P([demo.box]),
    BoxSettings: () => P(demo.settings),
    BoxAgentVersion: () => P({ version: demo.appInfo.version, build: demo.appInfo.build }),
    GetPresets: () => P(demo.presets),
    Status: () => P(status),
    StreamBitrate: () => P(256),
    ListDrives: () => P(demo.drives),
    StickVersion: () => P(''),
    StickConfigs: () => P({ wlanSSID: '', wlanPass: '', region: '', name: '', locale: '' }),
    ListWiFiProfiles: () => P(demo.wifi),
    TryWiFiPassword: () => P(''),
    CurrentWiFi: () => P('HomeWiFi'),
    SuggestBoxLanguage: () => P(3),
    GetBoxLanguage: () => P('3'),
    GetClockDisplay: () => P('true'),
    GetClockFormat24: () => P(true),
    GetAirplayOpt: () => P({ enabled: true }),
    ListMediaServers: () => P(demo.mediaServers),
    BrowseLibrary: () => P(demo.libraryPage),
    PlayURL: () => P(undefined),
    SetAppLocale: () => P(undefined),
    LogClientError: () => P(undefined),
  };
  // Any unmocked binding resolves to null so nothing throws.
  window.go = { main: { App: new Proxy(App, { get(t, p) { return p in t ? t[p] : () => Promise.resolve(null); } }) } };

  const rt = {
    EventsOn: () => () => {}, EventsOnce: () => () => {}, EventsOff: () => {}, EventsEmit: () => {},
    LogPrint: () => {}, LogTrace: () => {}, LogDebug: () => {}, LogInfo: () => {}, LogWarning: () => {}, LogError: () => {}, LogFatal: () => {},
    BrowserOpenURL: () => {}, WindowReload: () => {}, WindowReloadApp: () => {}, Quit: () => {},
    WindowSetTitle: () => {}, WindowShow: () => {}, WindowHide: () => {},
    ClipboardSetText: () => Promise.resolve(true), ClipboardGetText: () => Promise.resolve(''),
    Environment: () => Promise.resolve({ buildType: 'production', platform: 'windows', arch: 'amd64' }),
  };
  window.runtime = new Proxy(rt, { get(t, p) { return p in t ? t[p] : () => {}; } });
}

// ---- mock the agent HTTP endpoints the frontend fetch()es ------------------
async function routeAgent(route) {
  const url = route.request().url();
  const json = (body) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  if (url.includes('/api/radio/search') || url.includes('/api/radio/top')) return json(DEMO.stations);
  if (url.includes('/api/radio/languages')) return json(DEMO.languages);
  if (url.includes('/api/radio/tags')) return json([]);
  if (url.includes('/api/region')) return json({ countryCode: 'US', country: 'United States' });
  if (url.includes('/api/agent/version')) return json({ version: DEMO.appInfo.version, build: DEMO.appInfo.build });
  if (url.includes('/api/box/settings')) return json(DEMO.settings);
  if (url.includes('/api/stick/status')) return json({});
  if (url.includes('/api/debug/state')) return json({});
  return json([]); // safe default for any other /api/* call
}

// ---- per-view driving ------------------------------------------------------
async function drive(page, view) {
  const click = async (sel) => { const el = await page.$(sel); if (el) await el.click(); };
  const scrollY = (y) => page.evaluate((yy) => window.scrollTo(0, yy), y);
  const scrollIntoView = (sel) => page.evaluate((s) => { const e = document.querySelector(s); if (e) e.scrollIntoView({ block: 'start' }); }, sel);
  if (view === 'app-listen') {
    await page.waitForSelector('#presets .preset-body, #presets .preset-hint', { timeout: 9000 });
    await scrollY(0);
  } else if (view === 'app-search') {
    await page.waitForSelector('#topBtn', { timeout: 9000 });
    await click('#topBtn');
    await page.waitForSelector('#searchResults .result-row', { timeout: 9000 });
    await scrollIntoView('#searchQ');
  } else if (view === 'app-library') {
    await click('.tab-btn[data-view="library"]');
    await page.waitForSelector('#view-library:not(.hidden) .library-row-folder', { timeout: 9000 });
    await scrollY(0);
  } else if (view.startsWith('app-settings')) {
    await click('.tab-btn[data-view="settings"]');
    await page.waitForSelector('#view-settings:not(.hidden) .settings-section', { timeout: 9000 });
    // settings1/2/3 are three scroll positions through the long settings form.
    if (view === 'app-settings1') await scrollY(0);
    else if (view === 'app-settings2') await scrollY(640);
    else await scrollY(100000); // app-settings3: bottom (Status / Actions / Speaker info)
  } else if (view === 'app-stick-step1') {
    await click('.tab-btn[data-view="setup"]');
    await page.waitForSelector('#view-setup:not(.hidden) #drivesList .drive-row', { timeout: 9000 });
    await scrollY(0);
  } else if (view === 'app-stick-step2') {
    await click('.tab-btn[data-view="setup"]');
    await page.waitForSelector('#drivesList .drive-row', { timeout: 9000 });
    await click('#drivesList .drive-row');
    await page.waitForSelector('#nameSection:not(.hidden)', { timeout: 9000 });
    await scrollIntoView('#nameSection');
  }
  await page.waitForTimeout(450); // let transitions / art settle
}

// ---- build + vite preview server -------------------------------------------
// We build/serve with the parent frontend's own vite (resolved via npx in
// cwd=FRONTEND), so this harness's only dependency is playwright. That keeps
// playwright out of the app's package.json and therefore out of every CI /
// wails build (it would otherwise download ~130 MB of browsers on install).
function buildFrontend() {
  const bin = process.platform === 'win32' ? 'npx.cmd' : 'npx';
  return new Promise((resolve, reject) => {
    const p = spawn(bin, ['vite', 'build'], { cwd: FRONTEND, stdio: 'ignore', shell: process.platform === 'win32' });
    p.on('exit', (code) => (code === 0 ? resolve() : reject(new Error('vite build exited ' + code))));
    p.on('error', reject);
  });
}

function startServer() {
  const bin = process.platform === 'win32' ? 'npx.cmd' : 'npx';
  const srv = spawn(bin, ['vite', 'preview', '--host', '127.0.0.1', '--port', String(PORT), '--strictPort'], { cwd: FRONTEND, stdio: 'ignore', shell: process.platform === 'win32' });
  return srv;
}
async function waitForServer() {
  for (let i = 0; i < 60; i++) {
    try { const r = await fetch(BASE); if (r.ok) return; } catch (e) {}
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error('vite preview did not come up on ' + BASE);
}

// ---- main ------------------------------------------------------------------
await buildFrontend();
const server = startServer();
let browser;
try {
  await waitForServer();
  browser = await chromium.launch({ headless: true });
  let ok = 0, fail = 0;
  for (const lang of LANGS) {
    await mkdir(path.join(OUT, lang), { recursive: true });
    const ctx = await browser.newContext({ viewport: { width: 1100, height: 780 }, deviceScaleFactor: 2 });
    for (const view of VIEWS) {
      const page = await ctx.newPage();
      page.on('pageerror', (e) => console.warn(`  [${lang}/${view}] pageerror:`, e.message));
      await page.addInitScript(installMocks, { locale: lang, view, demo: DEMO });
      await page.route('**/api/**', routeAgent);
      try {
        await page.goto(BASE, { waitUntil: 'domcontentloaded' });
        await drive(page, view);
        const out = path.join(OUT, lang, view + '.png');
        await page.screenshot({ path: out });
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
  server.kill();
}
